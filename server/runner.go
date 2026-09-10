package server

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Job struct {
	ID       string
	Params   map[string]any
	Label    string
	State    string
	Phase    string
	Progress []int
	Log      []string
	Command  []string
	Output   *string
	Error    *string
	Started  *float64
	Finished *float64
	Profile  []map[string]any
}

func newJob(params map[string]any) *Job {
	return &Job{
		ID:       randomID(),
		Params:   cloneMap(params),
		Label:    anyToString(params["label"]),
		State:    "queued",
		Phase:    "",
		Progress: nil,
		Log:      []string{},
		Command:  []string{},
		Profile:  []map[string]any{},
	}
}

func (j *Job) Summary() map[string]any {
	var output any
	if j.Output != nil {
		output = *j.Output
	}
	var errValue any
	if j.Error != nil {
		errValue = *j.Error
	}
	var started any
	if j.Started != nil {
		started = *j.Started
	}
	var finished any
	if j.Finished != nil {
		finished = *j.Finished
	}
	log := j.Log
	if len(log) > 400 {
		log = log[len(log)-400:]
	}
	return map[string]any{
		"id":       j.ID,
		"label":    j.Label,
		"state":    j.State,
		"phase":    j.Phase,
		"progress": j.Progress,
		"output":   output,
		"error":    errValue,
		"started":  started,
		"finished": finished,
		"params":   j.Params,
		"command":  j.Command,
		"log":      log,
		"profile":  j.Profile,
	}
}

type Runner struct {
	cfg *Config

	mu        sync.Mutex
	jobs      map[string]*Job
	order     []string
	current   *Job
	proc      *exec.Cmd
	listeners []chan string

	queue chan string

	interactiveLock  sync.Mutex
	interactiveProc  *exec.Cmd
	interactiveIn    io.WriteCloser
	interactiveDone  chan struct{}
	interactiveLines chan *string

	terminalLock sync.Mutex
	terminalProc *exec.Cmd

	reloadMu      sync.Mutex
	reloadClients map[chan string]bool
}

func (r *Runner) ReloadClients() {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	for client := range r.reloadClients {
		select {
		case client <- "reload":
		default:
		}
	}
}

func (r *Runner) SubscribeReload() chan string {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	if r.reloadClients == nil {
		r.reloadClients = make(map[chan string]bool)
	}
	q := make(chan string, 10)
	r.reloadClients[q] = true
	return q
}

func (r *Runner) UnsubscribeReload(q chan string) {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	delete(r.reloadClients, q)
	close(q)
}

func NewRunner(cfg *Config) *Runner {
	r := &Runner{
		cfg:              cfg,
		jobs:             map[string]*Job{},
		order:            []string{},
		listeners:        []chan string{},
		queue:            make(chan string, 200),
		interactiveLines: make(chan *string, 1000),
	}
	go r.loop()
	return r
}

func (r *Runner) Subscribe() chan string {
	q := make(chan string, 200)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, q)
	return q
}

func (r *Runner) Unsubscribe(q chan string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, listener := range r.listeners {
		if listener == q {
			r.listeners = append(r.listeners[:i], r.listeners[i+1:]...)
			close(listener)
			break
		}
	}
}

func (r *Runner) Emit(kind string, payload any) {
	if kind == "terminal" {
		if m, ok := payload.(map[string]any); ok {
			if line := anyToString(m["line"]); line != "" {
				path := r.cfg.TerminalLog("")
				f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err == nil {
					_, _ = f.WriteString(line + "\n")
					_ = f.Close()
				}
			}
		}
	}
	data, _ := json.Marshal(map[string]any{"kind": kind, "payload": payload})
	r.mu.Lock()
	defer r.mu.Unlock()
	alive := r.listeners[:0]
	for _, listener := range r.listeners {
		select {
		case listener <- string(data):
			alive = append(alive, listener)
		default:
			close(listener)
		}
	}
	r.listeners = alive
}

func (r *Runner) Submit(params map[string]any) *Job {
	_ = os.WriteFile(r.cfg.TerminalLog(anyToString(params["session_name"])), []byte{}, 0o644)
	job := newJob(params)
	r.mu.Lock()
	r.jobs[job.ID] = job
	r.order = append(r.order, job.ID)
	r.mu.Unlock()
	r.queue <- job.ID
	r.Emit("queue", r.QueueState())
	return job
}

func (r *Runner) Cancel(jobID string) bool {
	r.mu.Lock()
	job := r.jobs[jobID]
	proc := r.proc
	if job == nil {
		r.mu.Unlock()
		return false
	}
	if job.State == "queued" {
		job.State = "cancelled"
		r.mu.Unlock()
		r.Emit("queue", r.QueueState())
		return true
	}
	if job.State == "running" && proc != nil && proc.Process != nil {
		job.State = "cancelling"
		r.mu.Unlock()
		go stopProcess(proc)
		return true
	}
	r.mu.Unlock()
	return false
}

func stopProcess(proc *exec.Cmd) {
	if proc == nil || proc.Process == nil {
		return
	}
	_ = syscall.Kill(-proc.Process.Pid, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		_, _ = proc.Process.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}
	_ = syscall.Kill(-proc.Process.Pid, syscall.SIGKILL)
}

func (r *Runner) RunTerminal(command string) bool {
	r.terminalLock.Lock()
	defer r.terminalLock.Unlock()
	r.mu.Lock()
	busy := r.current != nil
	r.mu.Unlock()
	if r.terminalProc != nil || busy {
		return false
	}
	go r.runTerminal(command)
	return true
}

func (r *Runner) LoadInteractive(params map[string]any) (bool, string) {
	r.interactiveLock.Lock()
	defer r.interactiveLock.Unlock()
	r.normalizeInteractiveStateLocked()
	if r.interactiveProc != nil {
		return false, "interactive h3 is already loaded"
	}
	name, err := saveSession(r.cfg, params)
	if err != nil {
		return false, err.Error()
	}
	params["session_name"] = name
	if _, _, err := r.cfg.ActivateSession(name); err != nil {
		return false, err.Error()
	}
	if err := r.ensureInteractiveLocked(params); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (r *Runner) SendInteractive(line string) (bool, string) {
	text := stringsTrimSpace(line)
	if text == "" {
		return false, "input is required"
	}
	r.interactiveLock.Lock()
	defer r.interactiveLock.Unlock()
	r.normalizeInteractiveStateLocked()
	r.mu.Lock()
	busy := r.current != nil
	r.mu.Unlock()
	if busy {
		return false, "interactive h3 is busy rendering"
	}
	if r.interactiveProc == nil || r.interactiveIn == nil {
		return false, "load interactive h3 first"
	}
	if err := r.interactiveSendLocked(text); err != nil {
		r.interactiveProc = nil
		r.interactiveIn = nil
		return false, "interactive h3 has exited; load it again"
	}
	return true, ""
}

func (r *Runner) runTerminal(command string) {
	r.Emit("terminal", map[string]any{"running": true})
	reader, writer, err := os.Pipe()
	if err != nil {
		r.Emit("terminal", map[string]any{"line": err.Error(), "running": false})
		return
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.cfg.Workdir
	cmd.Stdout = writer
	cmd.Stderr = writer
	r.terminalLock.Lock()
	r.terminalProc = cmd
	r.terminalLock.Unlock()
	startErr := cmd.Start()
	_ = writer.Close()
	if startErr != nil {
		_ = reader.Close()
		r.Emit("terminal", map[string]any{"line": startErr.Error(), "running": false})
		r.terminalLock.Lock()
		r.terminalProc = nil
		r.terminalLock.Unlock()
		return
	}
	buf := bufio.NewScanner(reader)
	for buf.Scan() {
		line := stringsTrimSpaceRight(buf.Text())
		if line != "" {
			r.Emit("terminal", map[string]any{"line": line, "running": true})
		}
	}
	_ = reader.Close()
	code := 0
	if err := cmd.Wait(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = 1
		}
	}
	r.Emit("terminal", map[string]any{"line": fmt.Sprintf("[exit %d]", code), "running": false})
	r.terminalLock.Lock()
	r.terminalProc = nil
	r.terminalLock.Unlock()
}

func (r *Runner) QueueState() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []map[string]any{}
	for _, id := range r.order {
		job := r.jobs[id]
		if job != nil && (job.State == "queued" || job.State == "running") {
			out = append(out, job.Summary())
		}
	}
	return out
}

func (r *Runner) History(limit int) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []map[string]any{}
	for i := len(r.order) - 1; i >= 0; i-- {
		job := r.jobs[r.order[i]]
		if job == nil {
			continue
		}
		if job.State == "done" || job.State == "failed" || job.State == "cancelled" {
			out = append(out, job.Summary())
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func (r *Runner) loop() {
	for jobID := range r.queue {
		r.mu.Lock()
		job := r.jobs[jobID]
		if job == nil || job.State == "cancelled" {
			r.mu.Unlock()
			continue
		}
		r.current = job
		r.mu.Unlock()
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					msg := fmt.Sprint(rec)
					job.State = "failed"
					job.Error = &msg
				}
				now := nowSeconds()
				job.Finished = &now
				r.mu.Lock()
				r.current = nil
				r.proc = nil
				r.mu.Unlock()
				recordTake(r.cfg, job)
				r.Emit("job", job.Summary())
				r.Emit("queue", r.QueueState())
				r.Emit("outputs", listOutputs(r.cfg))
			}()
			if anyToString(job.Params["run_mode"]) == "interactive" {
				r.runInteractive(job)
			} else {
				r.run(job)
			}
		}()
	}
}

func (r *Runner) run(job *Job) {
	p := job.Params
	inputs, outputs, err := r.cfg.SessionDirs(anyToString(p["session_name"]))
	if err != nil {
		msg := err.Error()
		job.State = "failed"
		job.Error = &msg
		return
	}
	stem := safeStem(firstString(anyToString(p["label"]), "take"))
	name := fmt.Sprintf("%s-%s.mp4", stem, time.Now().Format("0102-150405"))
	outPath := filepath.Join(outputs, name)
	cmdArgs := []string{r.cfg.H3, "--profile", "-d", r.cfg.Model, "-p", anyToString(p["prompt"])}
	if refs, ok := p["refs"].([]any); ok {
		for _, raw := range refs {
			ref, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := filepath.Join(inputs, anyToString(ref["name"]))
			switch {
			case anyToString(ref["kind"]) == "image":
				cmdArgs = append(cmdArgs, "--ref-image", path)
			case anyToString(ref["kind"]) == "audio":
				cmdArgs = append(cmdArgs, "--ref-audio", path)
			case anyToString(ref["mode"]) == "silent":
				cmdArgs = append(cmdArgs, "--ref-silent-video", path)
			case anyToString(ref["mode"]) == "replace" && anyToString(ref["pairedAudio"]) != "":
				cmdArgs = append(cmdArgs, "--ref-video-audio", path, filepath.Join(inputs, anyToString(ref["pairedAudio"])))
			default:
				cmdArgs = append(cmdArgs, "--ref-video", path)
			}
		}
	}
	if first := anyToString(p["first_frame"]); first != "" {
		cmdArgs = append(cmdArgs, "--first-frame", filepath.Join(inputs, first))
	}
	if last := anyToString(p["last_frame"]); last != "" {
		cmdArgs = append(cmdArgs, "--last-frame", filepath.Join(inputs, last))
	}
	cmdArgs = append(cmdArgs, "--width", anyToString(p["width"]), "--height", anyToString(p["height"]))
	if intFrom(p["render_width"], 0) != 0 && intFrom(p["render_height"], 0) != 0 {
		cmdArgs = append(cmdArgs, "--render-width", anyToString(p["render_width"]), "--render-height", anyToString(p["render_height"]))
	}
	cmdArgs = append(cmdArgs, "--frames", anyToString(p["frames"]), "--steps", anyToString(p["steps"]), "--layers", anyToString(p["layers"]))
	if intFrom(p["core_reuse"], 0) != 0 {
		cmdArgs = append(cmdArgs, "--core-reuse", anyToString(p["core_reuse"]))
	} else {
		cmdArgs = append(cmdArgs, "--reuse", anyToString(firstNonEmpty(p["reuse"], 1)))
	}
	if boolFrom(p["token_reduction"]) {
		cmdArgs = append(cmdArgs, "--token-reduction")
	}
	if boolFrom(p["ssd_streaming"]) {
		cmdArgs = append(cmdArgs, "--ssd-streaming")
	}
	if boolFrom(p["int8_row_fc2"]) && !boolFrom(p["ssd_streaming"]) {
		cmdArgs = append(cmdArgs, "--use-int8-row-fc2")
	}
	cmdArgs = append(cmdArgs, "--seed", anyToString(p["seed"]), "-o", outPath)
	env := os.Environ()
	if envMap, ok := p["env"].(map[string]any); ok {
		for key, value := range envMap {
			text := anyToString(value)
			if text != "" {
				env = append(env, key+"="+text)
			}
		}
	}
	job.Command = append([]string{}, cmdArgs...)
	job.State = "running"
	now := nowSeconds()
	job.Started = &now
	r.Emit("job", job.Summary())
	r.Emit("queue", r.QueueState())
	reader, writer, err := os.Pipe()
	if err != nil {
		msg := err.Error()
		job.State = "failed"
		job.Error = &msg
		return
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = r.cfg.Workdir
	cmd.Env = env
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r.mu.Lock()
	r.proc = cmd
	r.mu.Unlock()
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		msg := err.Error()
		job.State = "failed"
		job.Error = &msg
		return
	}
	_ = writer.Close()
	r.pump(job, reader)
	_ = reader.Close()
	code := 0
	if err := cmd.Wait(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = 1
		}
	}
	finished := nowSeconds()
	job.Finished = &finished
	if job.State == "cancelling" {
		job.State = "cancelled"
		return
	}
	if code == 0 && FileExists(outPath) {
		job.State = "done"
		job.Output = &name
		writeSidecar(outPath, job)
		return
	}
	job.State = "failed"
	msg := fmt.Sprintf("h3 exited with code %d", code)
	job.Error = &msg
}

func (r *Runner) ensureInteractiveLocked(p map[string]any) error {
	if r.interactiveProc != nil {
		return nil
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.Command(r.cfg.H3, "--profile", "-d", r.cfg.Model, "--width", anyToString(p["width"]), "--height", anyToString(p["height"]))
	cmd.Dir = r.cfg.Workdir
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.Env = os.Environ()
	if envMap, ok := p["env"].(map[string]any); ok {
		for key, value := range envMap {
			text := anyToString(value)
			if text != "" {
				cmd.Env = append(cmd.Env, key+"="+text)
			}
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = stdin.Close()
		return err
	}
	_ = writer.Close()
	r.interactiveProc = cmd
	r.interactiveIn = stdin
	r.interactiveDone = make(chan struct{})
	go r.readInteractive(reader, cmd, r.interactiveDone)
	_, outputs, err := r.cfg.SessionDirs(anyToString(p["session_name"]))
	if err != nil {
		return err
	}
	return r.interactiveSendLocked("!output " + outputs)
}

func (r *Runner) readInteractive(reader *os.File, cmd *exec.Cmd, done chan struct{}) {
	defer close(done)
	defer reader.Close()
	br := bufio.NewReader(reader)
	var buf []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		if b == '\n' || b == '\r' {
			line := stringsTrimSpaceRight(string(buf))
			buf = buf[:0]
			if line != "" {
				text := line
				r.interactiveLines <- &text
				r.Emit("terminal", map[string]any{"line": line, "running": true})
			}
			continue
		}
		buf = append(buf, b)
	}
	_, _ = cmd.Process.Wait()
	r.interactiveLines <- nil
}

func (r *Runner) StopInteractive() {
	r.interactiveLock.Lock()
	defer r.interactiveLock.Unlock()
	proc := r.interactiveProc
	stdin := r.interactiveIn
	done := r.interactiveDone
	r.interactiveProc = nil
	r.interactiveIn = nil
	r.interactiveDone = nil
	if proc == nil {
		return
	}
	if stdin != nil {
		_, _ = io.WriteString(stdin, "!quit\n")
		_ = stdin.Close()
	}
	if done != nil {
		select {
		case <-done:
			return
		case <-time.After(5 * time.Second):
		}
	}
	if proc.Process != nil {
		_ = proc.Process.Kill()
		_, _ = proc.Process.Wait()
	}
}

func (r *Runner) interactiveSendLocked(line string) error {
	if r.interactiveIn == nil {
		return fmt.Errorf("interactive stdin unavailable")
	}
	if _, err := io.WriteString(r.interactiveIn, line+"\n"); err != nil {
		return err
	}
	r.Emit("terminal", map[string]any{"line": "h3> " + line, "running": true})
	return nil
}

func (r *Runner) normalizeInteractiveStateLocked() {
	if r.interactiveDone == nil {
		return
	}
	select {
	case <-r.interactiveDone:
		r.interactiveProc = nil
		r.interactiveIn = nil
		r.interactiveDone = nil
	default:
	}
}

func (r *Runner) runInteractive(job *Job) {
	refs, _ := job.Params["refs"].([]any)
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if ok && anyToString(ref["kind"]) != "image" {
			job.State = "failed"
			msg := "interactive h3 mode currently supports image references only"
			job.Error = &msg
			return
		}
	}
	job.State = "running"
	now := nowSeconds()
	job.Started = &now
	r.interactiveLock.Lock()
	defer r.interactiveLock.Unlock()
	if err := r.ensureInteractiveLocked(job.Params); err != nil {
		msg := err.Error()
		job.State = "failed"
		job.Error = &msg
		return
	}
	inputs, _, err := r.cfg.SessionDirs(anyToString(job.Params["session_name"]))
	if err != nil {
		msg := err.Error()
		job.State = "failed"
		job.Error = &msg
		return
	}
	p := job.Params
	commands := []string{
		fmt.Sprintf("!size %sx%s", anyToString(p["width"]), anyToString(p["height"])),
		fmt.Sprintf("!frames %s", anyToString(p["frames"])),
		fmt.Sprintf("!steps %s", anyToString(p["steps"])),
		fmt.Sprintf("!layers %s", anyToString(p["layers"])),
		fmt.Sprintf("!reuse %s", anyToString(firstNonEmpty(p["reuse"], 1))),
		fmt.Sprintf("!seed %s", anyToString(p["seed"])),
	}
	if intFrom(p["render_width"], 0) != 0 && intFrom(p["render_height"], 0) != 0 {
		commands = append(commands, fmt.Sprintf("!render-size %sx%s", anyToString(p["render_width"]), anyToString(p["render_height"])))
	} else {
		commands = append(commands, "!render-size native")
	}
	commands = append(commands,
		fmt.Sprintf("!token-reduction %s", onOff(boolFrom(p["token_reduction"]))),
		fmt.Sprintf("!ssd-streaming %s", onOff(boolFrom(p["ssd_streaming"]))),
		fmt.Sprintf("!int8-row-fc2 %s", onOff(boolFrom(p["int8_row_fc2"]))),
		"!refs clear",
		"!first clear",
		"!last clear",
	)
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if ok {
			commands = append(commands, "!ref-image "+filepath.Join(inputs, anyToString(ref["name"])))
		}
	}
	if first := anyToString(p["first_frame"]); first != "" {
		commands = append(commands, "!first "+filepath.Join(inputs, first))
	}
	if last := anyToString(p["last_frame"]); last != "" {
		commands = append(commands, "!last "+filepath.Join(inputs, last))
	}
	r.Emit("job", job.Summary())
	for _, command := range commands {
		if err := r.interactiveSendLocked(command); err != nil {
			msg := "interactive h3 has exited; load it again"
			job.State = "failed"
			job.Error = &msg
			return
		}
	}
	prompt := anyToString(p["prompt"])
	if err := r.interactiveSendLocked(prompt); err != nil {
		msg := "interactive h3 has exited; load it again"
		job.State = "failed"
		job.Error = &msg
		return
	}
	job.Command = append(append([]string{}, commands...), prompt)
	doneRe := regexp.MustCompile(`Done -> (.+?) \[`)
	deadline := time.Now().Add(time.Hour)
	var output string
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		select {
		case line := <-r.interactiveLines:
			if line == nil {
				goto finish
			}
			if m := doneRe.FindStringSubmatch(*line); len(m) == 2 {
				output = stringsTrimSpace(m[1])
				goto finish
			}
			if len(*line) >= 4 && (*line)[:4] == "h3: " {
				msg := *line
				job.Error = &msg
				goto finish
			}
		case <-time.After(minDuration(remaining, 500*time.Millisecond)):
		}
	}
finish:
	finished := nowSeconds()
	job.Finished = &finished
	if output != "" && FileExists(output) {
		name := filepath.Base(output)
		job.State = "done"
		job.Output = &name
		writeSidecar(output, job)
		return
	}
	job.State = "failed"
	if job.Error == nil {
		msg := "interactive h3 did not produce an output"
		job.Error = &msg
	}
}

func (r *Runner) pump(job *Job, reader *os.File) {
	br := bufio.NewReader(reader)
	var buf []byte
	profRe := regexp.MustCompile(`^h3 profile:\s+(.*?)\s{2,}(\S.*?)\s+wall=\s*([\d.]+)s`)
	progRe := regexp.MustCompile(`^(.*?)\s{2,}(\d+)/(\d+)\s*$`)
	for {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		if b == '\n' || b == '\r' {
			line := stringsTrimSpaceRight(string(buf))
			buf = buf[:0]
			if line == "" {
				continue
			}
			job.Log = append(job.Log, line)
			if len(job.Log) > 400 {
				job.Log = job.Log[100:]
			}
			if m := progRe.FindStringSubmatch(line); len(m) == 4 {
				job.Phase = stringsTrimSpace(m[1])
				job.Progress = []int{intFrom(m[2], 0), intFrom(m[3], 0)}
			} else if len(line) >= 11 && line[:11] == "h3 profile:" {
				if m := profRe.FindStringSubmatch(line); len(m) == 4 {
					wall, _ := strconv.ParseFloat(m[3], 64)
					job.Profile = append(job.Profile, map[string]any{
						"component": stringsTrimSpace(m[1]),
						"stage":     stringsTrimSpace(m[2]),
						"wall":      wall,
					})
				}
			}
			r.Emit("job", job.Summary())
			r.Emit("terminal", map[string]any{"line": line, "running": true})
			continue
		}
		buf = append(buf, b)
	}
}

func randomID() string {
	buf := make([]byte, 5)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func stringsTrimSpaceRight(s string) string {
	for len(s) > 0 {
		last := s[len(s)-1]
		if last != ' ' && last != '\t' && last != '\n' && last != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
