package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	tailLines        = 60
	progressInterval = 250 * time.Millisecond
	maxFinishedJobs  = 100
)

// PreviewInfo describes one preview frame written to disk.
type PreviewInfo struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	URL        string `json:"url"`
	Step       int    `json:"step"`
	Total      int    `json:"total"`
	FrameIndex int    `json:"frameIndex"`
	FrameTotal int    `json:"frameTotal"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Count      int    `json:"count"`
}

// Job is one queued render. All fields are guarded by mu; read them through
// Summary().
type Job struct {
	mu             sync.Mutex
	ID             string
	Session        string
	Label          string
	Params         RenderParams
	Raw            map[string]any
	State          string
	Phase          string
	Stage          string
	Progress       []int
	Command        []string
	CommandDisplay string
	// task is this render reported to helmstudio, or nil when the studio runs
	// on its own. Every method on it is a no-op when it is nil.
	task          *Task
	Output        string
	Error         string
	Hint          string
	Started       float64
	Finished      float64
	Profile       []ProfileRow
	PreviewDir    string
	PreviewCount  int
	PreviewLatest *PreviewInfo
	EstimateS     float64
	EtaS          float64

	tail            []string
	cancelRequested bool
	denoiseT0       time.Time
	denoiseN0       int
	lastProgress    time.Time
	preview         *h3PreviewState
	marker          chan struct{}
}

// JobSummary is the JSON shape of a job for the UI and sidecars.
type JobSummary struct {
	ID             string   `json:"id"`
	Session        string   `json:"session"`
	Label          string   `json:"label"`
	State          string   `json:"state"`
	Phase          string   `json:"phase"`
	Stage          string   `json:"stage"`
	Progress       []int    `json:"progress"`
	Output         string   `json:"output,omitempty"`
	Error          string   `json:"error,omitempty"`
	Hint           string   `json:"hint,omitempty"`
	Started        float64  `json:"started,omitempty"`
	Finished       float64  `json:"finished,omitempty"`
	Seed           int64    `json:"seed"`
	RunMode        string   `json:"run_mode"`
	Command        []string `json:"command,omitempty"`
	CommandDisplay string   `json:"command_display,omitempty"`
	// HelmJob is this render's helmstudio task job, when it is running under
	// helmstudio. The page streams that job's log in helm-terminal; absent,
	// it has nothing to stream and says so by not being there.
	HelmJob       string         `json:"helm_job,omitempty"`
	Profile       []ProfileRow   `json:"profile"`
	PreviewCount  int            `json:"preview_count"`
	PreviewLatest *PreviewInfo   `json:"preview_latest,omitempty"`
	EstimateS     float64        `json:"estimate_s,omitempty"`
	EtaS          float64        `json:"eta_s,omitempty"`
	Params        map[string]any `json:"params,omitempty"`
}

func (j *Job) Summary(withParams bool) JobSummary {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := JobSummary{
		ID: j.ID, Session: j.Session, Label: j.Label, State: j.State, Phase: j.Phase, Stage: j.Stage,
		Progress: append([]int(nil), j.Progress...), Output: j.Output, Error: j.Error, Hint: j.Hint,
		Started: j.Started, Finished: j.Finished, Seed: j.Params.Seed, RunMode: j.Params.RunMode,
		Command: append([]string(nil), j.Command...), CommandDisplay: j.CommandDisplay,
		Profile: append([]ProfileRow{}, j.Profile...), PreviewCount: j.PreviewCount,
		EstimateS: j.EstimateS, EtaS: j.EtaS, HelmJob: j.task.JobID(),
	}
	if j.PreviewLatest != nil {
		latest := *j.PreviewLatest
		s.PreviewLatest = &latest
	}
	if withParams {
		s.Params = cloneMap(j.Raw)
	}
	return s
}

func (j *Job) set(fn func(j *Job)) {
	j.mu.Lock()
	fn(j)
	j.mu.Unlock()
}

func (j *Job) previewDir() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.PreviewDir
}

func (j *Job) state() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.State
}

type interactiveProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	done    chan struct{}
	session string
	writeMu sync.Mutex
}

func (p *interactiveProc) send(line string) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := io.WriteString(p.stdin, line+"\n")
	return err
}

func (p *interactiveProc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// Runner owns the render queue (one job at a time — one GPU), the resident
// interactive h3 process and the optional shell terminal.
type Runner struct {
	cfg    *Config
	events *Broker
	logs   *LogSink

	mu      sync.Mutex
	cond    *sync.Cond
	jobs    map[string]*Job
	order   []string
	pending []string
	current *Job
	proc    *exec.Cmd

	// imu guards the interactive fields. It is only ever held briefly —
	// never while waiting on h3 — so status checks never block on a render.
	imu            sync.Mutex
	inter          *interactiveProc
	interJob       *Job
	manualInFlight int

	shellMu   sync.Mutex
	shellProc *exec.Cmd
}

func NewRunner(cfg *Config, events *Broker) *Runner {
	r := &Runner{cfg: cfg, events: events, logs: NewLogSink(cfg), jobs: map[string]*Job{}}
	r.cond = sync.NewCond(&r.mu)
	go r.loop()
	return r
}

func randomID() string {
	buf := make([]byte, 5)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Submit queues a validated render.
func (r *Runner) Submit(raw map[string]any, p RenderParams) JobSummary {
	job := &Job{
		ID: randomID(), Session: safeStem(p.Session), Label: p.Label, Params: p, Raw: cloneMap(raw),
		State: "queued", Profile: []ProfileRow{},
	}
	job.Raw["seed"] = p.Seed
	job.Raw["session_name"] = job.Session
	r.mu.Lock()
	r.jobs[job.ID] = job
	r.order = append(r.order, job.ID)
	r.pending = append(r.pending, job.ID)
	r.pruneLocked()
	r.cond.Signal()
	r.mu.Unlock()
	r.emitQueue()
	return job.Summary(false)
}

func (r *Runner) pruneLocked() {
	finished := 0
	for i := len(r.order) - 1; i >= 0; i-- {
		job := r.jobs[r.order[i]]
		if job == nil {
			continue
		}
		switch job.state() {
		case "done", "failed", "cancelled":
			finished++
			if finished > maxFinishedJobs {
				delete(r.jobs, r.order[i])
				r.order = append(r.order[:i], r.order[i+1:]...)
			}
		}
	}
}

// Cancel removes a queued job or stops the running one.
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	job := r.jobs[id]
	if job == nil {
		r.mu.Unlock()
		return false
	}
	for i, pid := range r.pending {
		if pid == id {
			r.pending = append(r.pending[:i], r.pending[i+1:]...)
			r.mu.Unlock()
			job.set(func(j *Job) { j.State = "cancelled"; j.Finished = nowSeconds() })
			r.events.Emit("job", job.Summary(false))
			r.emitQueue()
			return true
		}
	}
	running := r.current == job
	proc := r.proc
	r.mu.Unlock()
	if !running {
		return false
	}
	job.set(func(j *Job) { j.cancelRequested = true; j.State = "cancelling" })
	r.events.Emit("job", job.Summary(false))
	if job.Params.RunMode == "interactive" {
		go r.StopInteractive()
	} else if proc != nil {
		go stopProcess(proc)
	}
	return true
}

func stopProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// QueueState lists running and queued jobs, running first.
func (r *Runner) QueueState() []JobSummary {
	r.mu.Lock()
	ids := append([]string{}, r.pending...)
	current := r.current
	r.mu.Unlock()
	out := []JobSummary{}
	if current != nil {
		out = append(out, current.Summary(false))
	}
	for _, id := range ids {
		r.mu.Lock()
		job := r.jobs[id]
		r.mu.Unlock()
		if job != nil {
			out = append(out, job.Summary(false))
		}
	}
	return out
}

func (r *Runner) emitQueue() { r.events.Emit("queue", r.QueueState()) }

// Busy reports whether a render is running or queued.
func (r *Runner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current != nil || len(r.pending) > 0
}

func (r *Runner) loop() {
	for {
		r.mu.Lock()
		for len(r.pending) == 0 {
			r.cond.Wait()
		}
		id := r.pending[0]
		r.pending = r.pending[1:]
		job := r.jobs[id]
		r.current = job
		r.mu.Unlock()
		if job != nil {
			r.execute(job)
		}
		r.mu.Lock()
		r.current = nil
		r.proc = nil
		r.mu.Unlock()
		r.emitQueue()
	}
}

func (r *Runner) execute(job *Job) {
	// Reported to helmstudio for as long as it runs. Created outside job.set:
	// it talks to the platform, and no lock of h3 studio's is held for that.
	if task := r.cfg.Platform.StartTask(job.Label); task != nil {
		job.set(func(j *Job) { j.task = task })
	}
	defer func() {
		if rec := recover(); rec != nil {
			job.set(func(j *Job) { j.State = "failed"; j.Error = fmt.Sprint(rec) })
		}
		job.set(func(j *Job) {
			if j.Finished == 0 {
				j.Finished = nowSeconds()
			}
			j.EtaS = 0
		})
		summary := job.Summary(false)
		if dir := job.previewDir(); summary.State != "done" && dir != "" {
			_ = os.RemoveAll(dir)
		}
		r.jobLog(job, fmt.Sprintf("[studio] %s: %s in %s", firstNonEmpty(summary.Label, "take"), summary.State,
			formatSeconds(summary.Finished-summary.Started)))
		// After the last line, so helmstudio's log ends where this one does.
		job.task.Finish(summary.State, summary.Error)
		if summary.State == "failed" && summary.Error != "" {
			r.jobLog(job, "!! "+summary.Error)
		}
		r.events.Emit("job", summary)
		if summary.State == "done" {
			r.events.Emit("takes", map[string]any{"session": job.Session, "name": summary.Output})
		}
	}()
	estimate, _, _ := r.cfg.Estimate(job.Session, job.Params)
	job.set(func(j *Job) {
		j.State = "running"
		j.Started = nowSeconds()
		j.EstimateS = estimate
		j.preview = newH3PreviewState()
	})
	if job.Params.PreviewOn() {
		if dir, err := r.cfg.SessionSubdir(job.Session, "previews"); err == nil {
			job.set(func(j *Job) { j.PreviewDir = filepath.Join(dir, j.ID) })
		}
	}
	r.events.Emit("job", job.Summary(false))
	r.emitQueue()
	if job.Params.RunMode == "interactive" {
		r.runInteractive(job)
	} else {
		r.runOneShot(job)
	}
}

func (r *Runner) fail(job *Job, msg string) {
	job.set(func(j *Job) {
		if j.cancelRequested {
			j.State = "cancelled"
			return
		}
		j.State = "failed"
		j.Error = msg
		j.Hint = hintFor(append(append([]string{}, j.tail...), msg))
	})
}

// jobDirs returns the session's inputs and outputs dirs and a private
// directory h3 writes the take into before it is moved into outputs/.
func (r *Runner) jobDirs(job *Job) (inputs, outputs, work string, err error) {
	dir, err := r.cfg.EnsureSession(job.Session)
	if err != nil {
		return "", "", "", err
	}
	inputs, outputs = filepath.Join(dir, "inputs"), filepath.Join(dir, "outputs")
	work = filepath.Join(outputs, ".job-"+job.ID)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", "", "", err
	}
	return inputs, outputs, work, nil
}

func (r *Runner) runOneShot(job *Job) {
	p := job.Params
	inputs, _, work, err := r.jobDirs(job)
	if err != nil {
		r.fail(job, err.Error())
		return
	}
	defer os.RemoveAll(work)
	out := filepath.Join(work, "take.mp4")
	args := BuildOneShotArgs(p, r.cfg.Model(), inputs, out)
	env := envFor(p)
	h3 := r.cfg.H3()
	job.set(func(j *Job) {
		j.Command = append([]string{h3}, args...)
		j.CommandDisplay = displayCommand(env, j.Command)
	})
	r.jobLog(job, "$ "+job.Summary(false).CommandDisplay)

	reader, writer, err := os.Pipe()
	if err != nil {
		r.fail(job, err.Error())
		return
	}
	cmd := exec.Command(h3, args...)
	cmd.Dir = r.cfg.Workdir()
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		writer.Close()
		reader.Close()
		r.fail(job, err.Error())
		return
	}
	writer.Close()
	r.mu.Lock()
	r.proc = cmd
	r.mu.Unlock()
	job.mu.Lock()
	cancelled := job.cancelRequested
	job.mu.Unlock()
	if cancelled {
		go stopProcess(cmd)
	}
	splitter := &lineSplitter{
		emit:   func(line string, overwrite bool) { r.jobEmit(job, line, overwrite) },
		commit: func(line string) { r.jobCommit(job, line) },
	}
	splitter.readFrom(reader)
	reader.Close()
	waitErr := cmd.Wait()
	code := 0
	if waitErr != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			code = exit.ExitCode()
		}
	}
	job.mu.Lock()
	cancelled = job.cancelRequested
	job.mu.Unlock()
	switch {
	case cancelled:
		job.set(func(j *Job) { j.State = "cancelled" })
	case code == 0 && FileExists(out):
		r.finishTake(job, out)
	default:
		job.mu.Lock()
		msg := lastMeaningfulLine(job.tail)
		job.mu.Unlock()
		if msg == "" {
			msg = fmt.Sprintf("h3 exited with code %d", code)
		}
		r.fail(job, msg)
	}
}

// finishTake moves a produced video into outputs/ under the take's name,
// prunes its previews and writes the sidecar.
func (r *Runner) finishTake(job *Job, produced string) {
	outputs, err := r.cfg.SessionSubdir(job.Session, "outputs")
	if err != nil {
		r.fail(job, err.Error())
		return
	}
	stem := safeStem(firstNonEmpty(job.Label, "take")) + "-" + time.Now().Format("0102-150405")
	final := uniquePath(outputs, stem, ".mp4")
	if err := os.Rename(produced, final); err != nil {
		r.fail(job, "could not move the take into outputs: "+err.Error())
		return
	}
	var previews []string
	previewRel := ""
	if dir := job.previewDir(); dir != "" && DirExists(dir) {
		previewRel = "previews/" + job.ID
		for _, name := range prunePreviews(dir) {
			previews = append(previews, previewRel+"/"+name)
		}
		if len(previews) == 0 {
			_ = os.RemoveAll(dir)
			previewRel = ""
		}
	}
	finished := nowSeconds()
	job.set(func(j *Job) {
		j.State = "done"
		j.Output = filepath.Base(final)
		j.Finished = finished
		if len(previews) == 0 {
			j.PreviewDir = ""
		}
	})
	summary := job.Summary(true)
	meta := map[string]any{}
	if data, err := jsonRoundTrip(summary); err == nil {
		meta = data
	}
	meta["duration_s"] = round2(summary.Finished - summary.Started)
	probe := r.cfg.probeMedia(final)
	meta["probe"] = probe
	if info, err := os.Stat(final); err == nil {
		meta["probe_mtime"] = float64(info.ModTime().UnixNano()) / 1e9
	}
	if len(previews) > 0 {
		meta["previews"] = previews
		meta["preview_dir"] = previewRel
	}
	_ = writeJSONAtomic(sidecarFor(final, false), meta)

	// The take is on disk and recorded here; helmstudio, if this studio runs
	// under it, gets it too. A no-op standalone, and never fatal either way.
	r.cfg.Platform.RecordTake(final, job.Label, job.Session, probe, meta)
}

// jobEmit handles a displayed line (possibly a \r progress update).
func (r *Runner) jobEmit(job *Job, line string, overwrite bool) {
	if _, kitty := kittyPayload(line); kitty {
		return
	}
	if phase, n, total, ok := parseProgress(line); ok {
		now := time.Now()
		emit := false
		job.mu.Lock()
		if phase != job.Phase {
			emit = true
		}
		job.Phase = phase
		if stage := stageFor(phase); stage != "" {
			job.Stage = stage
		}
		job.Progress = []int{n, total}
		if phase == "denoise" {
			if job.denoiseT0.IsZero() || n < job.denoiseN0 {
				job.denoiseT0, job.denoiseN0 = now, n
			} else if n > job.denoiseN0 {
				perStep := now.Sub(job.denoiseT0).Seconds() / float64(n-job.denoiseN0)
				job.EtaS = math.Round(perStep * float64(total-n))
			}
		} else if job.Stage != "denoise" {
			job.EtaS = 0
		}
		if emit || n == total || now.Sub(job.lastProgress) >= progressInterval {
			job.lastProgress = now
			emit = true
		}
		payload := map[string]any{
			"id": job.ID, "session": job.Session, "phase": job.Phase, "stage": job.Stage,
			"progress": job.Progress, "eta_s": job.EtaS,
		}
		job.mu.Unlock()
		if emit {
			r.events.Emit("progress", payload)
			job.task.Progress(n, total)
		}
	}
	r.events.Emit("log", map[string]any{"session": job.Session, "line": line, "replace": overwrite})
}

// jobCommit handles a finished line: previews, profile rows, the error tail
// and the session's terminal log.
func (r *Runner) jobCommit(job *Job, line string) {
	job.mu.Lock()
	frame, gotFrame := job.preview.feed(line)
	job.mu.Unlock()
	if gotFrame {
		r.savePreview(job, frame)
	}
	text, kitty := kittyPayload(line)
	if kitty {
		if text == "" {
			return
		}
		line = text
	}
	job.mu.Lock()
	if row, ok := parseProfile(line); ok {
		job.Profile = append(job.Profile, row)
	}
	job.tail = append(job.tail, line)
	if len(job.tail) > tailLines {
		job.tail = job.tail[len(job.tail)-tailLines:]
	}
	job.mu.Unlock()
	r.logs.Write(job.Session, line)
}

// jobLog writes a studio-generated line to the log and the UI.
func (r *Runner) jobLog(job *Job, line string) {
	job.task.Log(line)
	r.logs.Write(job.Session, line)
	r.events.Emit("log", map[string]any{"session": job.Session, "line": line, "replace": false})
}

func (r *Runner) savePreview(job *Job, frame previewFrame) {
	job.mu.Lock()
	dir := job.PreviewDir
	job.mu.Unlock()
	if dir == "" {
		return
	}
	name := previewFileName(frame)
	if err := writePreviewPNG(filepath.Join(dir, name), frame); err != nil {
		return
	}
	job.mu.Lock()
	job.PreviewCount++
	info := &PreviewInfo{
		ID: job.ID, Session: job.Session, URL: mediaURL(job.Session, "previews/"+job.ID+"/"+name),
		Step: frame.Step, Total: frame.Total, FrameIndex: frame.FrameIndex, FrameTotal: frame.FrameTotal,
		Width: frame.Width, Height: frame.Height, Count: job.PreviewCount,
	}
	job.PreviewLatest = info
	job.mu.Unlock()
	r.events.Emit("preview", info)
}

// ── interactive ─────────────────────────────────────────────────────────

// renderSentinel is an unknown REPL command sent after every prompt. h3
// answers it on stderr (unbuffered) as soon as the render returns — success
// or failure — whereas "Done -> …" goes to block-buffered stdout.
const (
	renderSentinel = "!studio-render-finished"
	sentinelReply  = "h3: unknown command; type !help"
)

// InteractiveStatus is what the UI shows in the interactive pill.
func (r *Runner) InteractiveStatus() map[string]any {
	r.imu.Lock()
	defer r.imu.Unlock()
	if r.inter == nil || !r.inter.alive() {
		return map[string]any{"loaded": false}
	}
	return map[string]any{"loaded": true, "session": r.inter.session, "pid": r.inter.cmd.Process.Pid}
}

func (r *Runner) interactiveAlive() bool {
	r.imu.Lock()
	defer r.imu.Unlock()
	return r.inter != nil && r.inter.alive()
}

// LoadInteractive starts the resident h3 REPL if it isn't running.
func (r *Runner) LoadInteractive(p RenderParams) error {
	if err := r.ensureInteractive(p); err != nil {
		return err
	}
	r.events.Emit("interactive", r.InteractiveStatus())
	return nil
}

func (r *Runner) ensureInteractive(p RenderParams) error {
	r.imu.Lock()
	if r.inter != nil && r.inter.alive() {
		r.imu.Unlock()
		return nil
	}
	r.imu.Unlock()
	session := safeStem(p.Session)
	outputs, err := r.cfg.SessionSubdir(session, "outputs")
	if err != nil {
		return err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	args := BuildInteractiveLaunchArgs(p, r.cfg.Model())
	cmd := exec.Command(r.cfg.H3(), args...)
	cmd.Dir = r.cfg.Workdir()
	// Claim a Kitty terminal regardless of this render's preview setting, so
	// later renders can turn !show on without relaunching h3.
	env := append(envFor(p), "KITTY_WINDOW_ID=1")
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		reader.Close()
		writer.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return err
	}
	writer.Close()
	proc := &interactiveProc{cmd: cmd, stdin: stdin, done: make(chan struct{}), session: session}
	r.imu.Lock()
	r.inter = proc
	r.manualInFlight = 0
	r.imu.Unlock()
	line := "$ " + displayCommand(env, append([]string{r.cfg.H3()}, args...))
	r.logs.Write(session, line)
	r.events.Emit("log", map[string]any{"session": session, "line": line, "replace": false})
	go r.readInteractive(proc, reader)
	return proc.send("!output " + outputs)
}

func (r *Runner) readInteractive(proc *interactiveProc, reader *os.File) {
	idlePreview := newH3PreviewState()
	splitter := &lineSplitter{
		emit: func(line string, overwrite bool) {
			if job := r.activeInteractiveJob(proc); job != nil {
				r.jobEmit(job, line, overwrite)
				return
			}
			if _, kitty := kittyPayload(line); !kitty {
				r.events.Emit("log", map[string]any{"session": proc.session, "line": line, "replace": overwrite})
			}
		},
		commit: func(line string) {
			isMarker := strings.TrimSpace(line) == sentinelReply
			job := r.activeInteractiveJob(proc)
			if isMarker {
				r.imu.Lock()
				switch {
				case r.manualInFlight > 0:
					r.manualInFlight--
				case job != nil:
					select {
					case job.marker <- struct{}{}:
					default:
					}
				}
				r.imu.Unlock()
				return
			}
			if job != nil {
				r.jobCommit(job, line)
				return
			}
			if _, gotFrame := idlePreview.feed(line); gotFrame {
				return // previews of manual h3> prompts aren't kept
			}
			if text, kitty := kittyPayload(line); kitty {
				if text == "" {
					return
				}
				line = text
			}
			r.logs.Write(proc.session, line)
		},
	}
	splitter.readFrom(reader)
	reader.Close()
	_ = proc.cmd.Wait()
	close(proc.done)
	r.imu.Lock()
	if r.inter == proc {
		r.inter = nil
	}
	r.imu.Unlock()
	r.logs.Write(proc.session, "[studio] interactive h3 exited")
	r.events.Emit("log", map[string]any{"session": proc.session, "line": "[studio] interactive h3 exited", "replace": false})
	r.events.Emit("interactive", r.InteractiveStatus())
}

func (r *Runner) activeInteractiveJob(proc *interactiveProc) *Job {
	r.imu.Lock()
	defer r.imu.Unlock()
	if r.inter != proc {
		return nil
	}
	return r.interJob
}

// SendInteractive forwards a manual h3> line. Prompts (and !again) get the
// completion sentinel so a later queued render can wait for them.
func (r *Runner) SendInteractive(line string) error {
	text := flattenLine(line)
	if text == "" {
		return errors.New("input is required")
	}
	if r.Busy() {
		return errors.New("a render is running or queued; wait for it to finish")
	}
	r.imu.Lock()
	proc := r.inter
	if proc == nil || !proc.alive() {
		r.imu.Unlock()
		return errors.New("load interactive h3 first")
	}
	generates := !strings.HasPrefix(text, "!") || strings.EqualFold(strings.Fields(text)[0], "!again")
	if generates {
		r.manualInFlight++
	}
	r.imu.Unlock()
	r.logs.Write(proc.session, "h3> "+text)
	r.events.Emit("log", map[string]any{"session": proc.session, "line": "h3> " + text, "replace": false})
	if err := proc.send(text); err != nil {
		return errors.New("interactive h3 has exited; load it again")
	}
	if generates {
		return proc.send(renderSentinel)
	}
	return nil
}

func (r *Runner) runInteractive(job *Job) {
	p := job.Params
	if err := r.ensureInteractive(p); err != nil {
		r.fail(job, err.Error())
		return
	}
	r.events.Emit("interactive", r.InteractiveStatus())
	inputs, outputs, work, err := r.jobDirs(job)
	if err != nil {
		r.fail(job, err.Error())
		return
	}
	defer os.RemoveAll(work)

	// Let manual h3> renders that are still in flight finish first.
	for waited := time.Duration(0); ; waited += 250 * time.Millisecond {
		r.imu.Lock()
		inFlight, proc := r.manualInFlight, r.inter
		r.imu.Unlock()
		if inFlight == 0 || proc == nil || !proc.alive() || job.cancelled() || waited > time.Hour {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	commands := BuildInteractiveCommands(p, inputs, work)
	job.set(func(j *Job) {
		j.Command = commands
		j.CommandDisplay = "h3> " + strings.Join(commands, "\nh3> ")
		j.marker = make(chan struct{}, 1)
	})
	r.imu.Lock()
	proc := r.inter
	if proc == nil || !proc.alive() {
		r.imu.Unlock()
		r.fail(job, "interactive h3 has exited; load it again")
		return
	}
	r.interJob = job
	r.imu.Unlock()
	defer func() {
		r.imu.Lock()
		if r.interJob == job {
			r.interJob = nil
		}
		r.imu.Unlock()
	}()

	for _, command := range commands {
		r.jobLog(job, "h3> "+command)
	}
	// The sentinel marks the end of this render; the trailing !output points
	// manual h3> prompts back at the session's outputs.
	for _, command := range append(commands, renderSentinel, "!output "+outputs) {
		if err := proc.send(command); err != nil {
			r.fail(job, "interactive h3 has exited; load it again")
			return
		}
	}

	timeout := time.NewTimer(time.Hour)
	defer timeout.Stop()
	select {
	case <-job.marker:
	case <-proc.done:
	case <-timeout.C:
		r.fail(job, "interactive h3 did not finish within an hour")
		return
	}
	produced := findProducedVideo(work)
	if produced != "" && !job.cancelled() {
		r.finishTake(job, produced)
		return
	}
	job.mu.Lock()
	msg := lastMeaningfulLine(job.tail)
	job.mu.Unlock()
	if !proc.alive() && msg == "" {
		msg = "interactive h3 exited during the render"
	}
	if msg == "" {
		msg = "interactive h3 did not produce an output"
	}
	r.fail(job, msg)
}

func (j *Job) cancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cancelRequested
}

// findProducedVideo returns the finished MP4 h3 wrote into a job directory.
// The sentinel reply can arrive a moment before the muxer closes the file,
// so it waits briefly for the moov box to appear.
func findProducedVideo(dir string) string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.mp4"))
		for _, match := range matches {
			if mp4Finalized(match) {
				return match
			}
		}
		if len(matches) == 0 || time.Now().After(deadline) {
			return ""
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// StopInteractive quits the resident h3 process, killing it if needed.
func (r *Runner) StopInteractive() {
	r.imu.Lock()
	proc := r.inter
	r.imu.Unlock()
	if proc == nil {
		return
	}
	proc.writeMu.Lock()
	_, _ = io.WriteString(proc.stdin, "!quit\n")
	_ = proc.stdin.Close()
	proc.writeMu.Unlock()
	select {
	case <-proc.done:
		return
	case <-time.After(5 * time.Second):
	}
	stopProcess(proc.cmd)
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
	}
}

// Shutdown stops everything the runner started.
func (r *Runner) Shutdown() {
	r.mu.Lock()
	proc := r.proc
	r.mu.Unlock()
	if proc != nil {
		stopProcess(proc)
	}
	r.StopInteractive()
	r.shellMu.Lock()
	shell := r.shellProc
	r.shellMu.Unlock()
	if shell != nil {
		stopProcess(shell)
	}
	r.logs.Close()
}

// ── shell terminal (only with --allow-shell) ────────────────────────────

func (r *Runner) RunShell(session, command string) error {
	if !r.cfg.AllowShell {
		return errors.New("the shell terminal is disabled; start h3 studio with --allow-shell to enable it")
	}
	if r.Busy() {
		return errors.New("a render is running or queued")
	}
	r.shellMu.Lock()
	defer r.shellMu.Unlock()
	if r.shellProc != nil {
		return errors.New("the terminal is already running a command")
	}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = r.cfg.Workdir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return err
	}
	writer.Close()
	r.shellProc = cmd
	emit := func(line string, running bool) {
		r.logs.Write(session, line)
		r.events.Emit("shell", map[string]any{"session": session, "line": line, "running": running})
	}
	emit("$ "+command, true)
	go func() {
		splitter := &lineSplitter{
			emit:   func(line string, overwrite bool) {},
			commit: func(line string) { emit(line, true) },
		}
		splitter.readFrom(reader)
		reader.Close()
		code := 0
		if err := cmd.Wait(); err != nil {
			code = 1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			}
		}
		r.shellMu.Lock()
		r.shellProc = nil
		r.shellMu.Unlock()
		emit(fmt.Sprintf("[exit %d]", code), false)
	}()
	return nil
}

// ── line splitting ──────────────────────────────────────────────────────

// lineSplitter splits process output on \n and \r. emit is called for every
// displayed line, with overwrite=true when a \r update replaces the previous
// line; commit is called once per line that stays visible (never for lines
// that were overwritten), in order.
type lineSplitter struct {
	emit    func(line string, overwrite bool)
	commit  func(line string)
	buf     []byte
	prevSep byte
	pending string
	hasPend bool
}

func (s *lineSplitter) readFrom(reader io.Reader) {
	chunk := make([]byte, 64*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			s.feed(chunk[:n])
		}
		if err != nil {
			break
		}
	}
	s.close()
}

func (s *lineSplitter) feed(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexAny(data, "\r\n")
		if i < 0 {
			s.buf = append(s.buf, data...)
			return
		}
		s.buf = append(s.buf, data[:i]...)
		sep := data[i]
		data = data[i+1:]
		s.flush(sep)
	}
}

func (s *lineSplitter) flush(sep byte) {
	line := strings.TrimRight(string(s.buf), " \t")
	s.buf = s.buf[:0]
	if strings.TrimSpace(line) == "" {
		if sep == '\n' && s.hasPend {
			s.commit(s.pending)
			s.pending, s.hasPend = "", false
		}
		s.prevSep = sep
		return
	}
	overwrite := s.prevSep == '\r' && s.hasPend
	if s.hasPend && !overwrite {
		s.commit(s.pending)
	}
	s.pending, s.hasPend = "", false
	s.emit(line, overwrite)
	if sep == '\r' {
		s.pending, s.hasPend = line, true
	} else {
		s.commit(line)
	}
	s.prevSep = sep
}

func (s *lineSplitter) close() {
	if strings.TrimSpace(string(s.buf)) != "" {
		s.flush('\n')
	}
	if s.hasPend {
		s.commit(s.pending)
		s.pending, s.hasPend = "", false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func formatSeconds(s float64) string {
	if s <= 0 {
		return "0s"
	}
	total := int(math.Round(s))
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	return fmt.Sprintf("%dm%02ds", total/60, total%60)
}
