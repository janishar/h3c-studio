package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	helm "github.com/janishar/helmstudio/packages/helm-runtime-sdk/go"
)

const maxJSONBody = 4 << 20

type App struct {
	cfg    *Config
	runner *Runner
	events *Broker
	mux    *http.ServeMux
}

func NewApp(cfg *Config, runner *Runner, events *Broker) *App {
	a := &App{cfg: cfg, runner: runner, events: events, mux: http.NewServeMux()}
	a.routes()
	return a
}

// ServeHTTP applies the request guard before routing.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if status, msg := a.guard(r); status != 0 {
		// "message" as well as "error": a refusal on the /helm/ proxy is read
		// by helmstudio's SDK, which takes "error" for a code and shows
		// "message". With only "error" it has nothing to say and its caller
		// falls back to "that change was not made".
		writeJSON(w, status, map[string]any{"error": msg, "message": msg})
		return
	}
	a.mux.ServeHTTP(w, r)
}

// guard blocks DNS rebinding (Host must name this server) and cross-site
// request forgery (state-changing requests must come from this origin and,
// for JSON endpoints, carry a JSON content type that forces a CORS preflight).
func (a *App) guard(r *http.Request) (int, string) {
	if !a.cfg.hostAllowed(r.Host) {
		return http.StatusForbidden, "unrecognized Host header; start h3 studio with --allow-host " + hostOnly(r.Host) + " to allow it"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return 0, ""
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || origin == "null" || !strings.EqualFold(u.Host, r.Host) {
			return http.StatusForbidden, "cross-origin request refused"
		}
	} else if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return http.StatusForbidden, "cross-site request refused"
	}
	if r.URL.Path == "/api/upload" {
		if r.Header.Get("X-Filename") == "" {
			return http.StatusBadRequest, "X-Filename header is required"
		}
		return 0, ""
	}
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	// helmstudio's SDK writes in the content types its API declares, which are
	// not all application/json: a PATCH is a merge patch, and a write with no
	// body (:cancel, DELETE) sends no content type at all. Both still force the
	// CORS preflight this check is here for — no cross-site form can send a
	// +json body, and none can send no content type — so the proxy takes them.
	if strings.HasPrefix(r.URL.Path, helm.ProxyPrefix) &&
		(strings.HasSuffix(mediaType, "+json") || (mediaType == "" && r.ContentLength == 0)) {
		return 0, ""
	}
	if mediaType != "application/json" {
		return http.StatusUnsupportedMediaType, "Content-Type must be application/json"
	}
	return 0, ""
}

func hostOnly(hostport string) string {
	if i := strings.LastIndex(hostport, ":"); i > 0 && !strings.HasSuffix(hostport, "]") {
		return hostport[:i]
	}
	return hostport
}

func (a *App) routes() {
	m := a.mux
	m.HandleFunc("GET /{$}", a.index)
	m.HandleFunc("GET /static/{path...}", a.static)
	// helmstudio's same-origin proxy: helm-css, the launcher's theme stream and
	// this studio's hue for the page, with no token in it. With nothing behind
	// it, it answers 404 and the page keeps its vendored helm-css and its own
	// theme switch.
	m.Handle(helm.ProxyPrefix, helm.Proxy(helm.ProxyFromEnv()))

	m.HandleFunc("GET /api/config", a.config)
	m.HandleFunc("GET /api/model", a.modelInfo)
	m.HandleFunc("GET /api/sessions", a.sessions)
	m.HandleFunc("GET /api/session", a.sessionSettings)
	m.HandleFunc("GET /api/inputs", a.inputs)
	m.HandleFunc("GET /api/takes", a.takes)
	m.HandleFunc("GET /api/timeline", a.timeline)
	m.HandleFunc("GET /api/timeline/browse", a.timelineBrowse)
	m.HandleFunc("GET /api/queue", a.queue)
	m.HandleFunc("GET /api/events", a.sse)

	m.HandleFunc("GET /media/{session}/{path...}", a.media)
	m.HandleFunc("GET /thumb/{session}/{path...}", a.thumb)
	m.HandleFunc("GET /sfile/{path...}", a.sessionsFile)
	m.HandleFunc("GET /sthumb/{path...}", a.sessionsThumb)

	m.HandleFunc("POST /api/upload", a.upload)
	m.HandleFunc("POST /api/render", a.render)
	m.HandleFunc("POST /api/command", a.command)
	m.HandleFunc("POST /api/estimate", a.estimate)
	m.HandleFunc("POST /api/cancel", a.cancel)
	m.HandleFunc("POST /api/interactive/load", a.interactiveLoad)
	m.HandleFunc("POST /api/interactive/unload", a.interactiveUnload)
	m.HandleFunc("POST /api/interactive/input", a.interactiveInput)
	m.HandleFunc("POST /api/shell", a.shell)
	m.HandleFunc("POST /api/model", a.setModel)
	m.HandleFunc("POST /api/h3", a.setH3)
	m.HandleFunc("POST /api/session/activate", a.sessionActivate)
	m.HandleFunc("POST /api/session/save", a.sessionSave)
	m.HandleFunc("POST /api/session/delete", a.sessionDelete)
	m.HandleFunc("POST /api/session/duplicate", a.sessionDuplicate)
	m.HandleFunc("POST /api/frame", a.frame)
	m.HandleFunc("POST /api/audio", a.audio)
	m.HandleFunc("POST /api/use-video", a.useVideo)
	m.HandleFunc("POST /api/trim", a.trim)
	m.HandleFunc("POST /api/timeline/render", a.timelineRender)
	m.HandleFunc("POST /api/delete", a.deleteMedia)
	m.HandleFunc("POST /api/take/star", a.starTake)

	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	})
}

// ── plumbing ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, obj any) {
	data, err := json.Marshal(obj)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"encoding failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeError(w http.ResponseWriter, status int, err error) {
	if errors.Is(err, os.ErrNotExist) {
		status = http.StatusNotFound
		err = errors.New("file not found")
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return false
	}
	return true
}

func sessionParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("session"))
}

func (a *App) sessionOrLast(name string) string {
	if strings.TrimSpace(name) == "" {
		return a.cfg.LastSession()
	}
	return safeStem(name)
}

// ── static ──────────────────────────────────────────────────────────────

func (a *App) index(w http.ResponseWriter, r *http.Request) { a.serveStatic(w, r, "index.html") }

func (a *App) static(w http.ResponseWriter, r *http.Request) {
	a.serveStatic(w, r, r.PathValue("path"))
}

func (a *App) serveStatic(w http.ResponseWriter, r *http.Request, name string) {
	name = path.Clean("/" + name)[1:]
	data, err := fs.ReadFile(a.cfg.Static, name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", ctype)
	if a.cfg.Dev {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// serveMediaFile streams a file with Range, HEAD and If-Modified-Since support.
func serveMediaFile(w http.ResponseWriter, r *http.Request, abs string, download bool) {
	f, err := os.Open(abs)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if download {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(abs)}))
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
}

func (a *App) media(w http.ResponseWriter, r *http.Request) {
	abs, err := a.cfg.MediaPath(r.PathValue("session"), r.PathValue("path"))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	serveMediaFile(w, r, abs, r.URL.Query().Get("download") == "1")
}

func (a *App) thumb(w http.ResponseWriter, r *http.Request) {
	abs, err := a.cfg.MediaPath(r.PathValue("session"), r.PathValue("path"))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	a.serveThumb(w, r, abs)
}

func (a *App) sessionsFile(w http.ResponseWriter, r *http.Request) {
	abs, err := a.cfg.ResolveSessionsPath(r.PathValue("path"))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	serveMediaFile(w, r, abs, false)
}

func (a *App) sessionsThumb(w http.ResponseWriter, r *http.Request) {
	abs, err := a.cfg.ResolveSessionsPath(r.PathValue("path"))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
		return
	}
	a.serveThumb(w, r, abs)
}

func (a *App) serveThumb(w http.ResponseWriter, r *http.Request, abs string) {
	if !videoExts[strings.ToLower(filepath.Ext(abs))] {
		serveMediaFile(w, r, abs, false)
		return
	}
	thumb, err := a.cfg.Thumbnail(abs)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	serveMediaFile(w, r, thumb, false)
}

// ── read APIs ───────────────────────────────────────────────────────────

func (a *App) config(w http.ResponseWriter, r *http.Request) {
	info := a.cfg.InspectModel(false)
	writeJSON(w, http.StatusOK, map[string]any{
		"h3":           a.cfg.H3(),
		"model":        a.cfg.Model(),
		"model_info":   info,
		"sessions_dir": a.cfg.Sessions,
		"session":      a.cfg.LastSession(),
		"sessions":     a.cfg.ListSessions(),
		"legal_frames": legalFrames,
		"max_pixels":   maxPixels,
		"allow_shell":  a.cfg.AllowShell,
		"dev":          a.cfg.Dev,
		"ffmpeg":       a.cfg.FFmpeg != "",
		"ffprobe":      a.cfg.FFprobe != "",
		"interactive":  a.runner.InteractiveStatus(),
	})
}

func (a *App) modelInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.cfg.InspectModel(r.URL.Query().Get("refresh") == "1"))
}

func (a *App) sessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sessions": a.cfg.ListSessions(), "active": a.cfg.LastSession()})
}

func (a *App) sessionSettings(w http.ResponseWriter, r *http.Request) {
	name := sessionParam(r)
	dir, err := a.cfg.SessionDir(name)
	if err != nil || !DirExists(dir) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	data := readJSONObject(filepath.Join(dir, "setting.json"))
	delete(data, "takes")
	data["session_name"] = filepath.Base(dir)
	writeJSON(w, http.StatusOK, data)
}

func (a *App) inputs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.cfg.ListInputs(a.sessionOrLast(sessionParam(r))))
}

func (a *App) takes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.cfg.ListTakes(a.sessionOrLast(sessionParam(r)), 200))
}

// timeline is what the TIMELINE panel lists: this session's combined videos,
// and the sequences helmstudio holds for this studio. They are two different
// things under one word — a file h3 rendered here, and an edit the platform
// keeps — so each carries its source and the panel offers each only the
// actions that can work on it.
func (a *App) timeline(w http.ResponseWriter, r *http.Request) {
	items := a.cfg.ListTimeline(a.sessionOrLast(sessionParam(r)))
	writeJSON(w, http.StatusOK, append(items, a.cfg.Platform.Sequences(r.Context())...))
}

func (a *App) timelineBrowse(w http.ResponseWriter, r *http.Request) {
	listing, err := a.cfg.BrowseTimeline(a.sessionOrLast(sessionParam(r)), r.URL.Query().Get("path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (a *App) queue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"queue": a.runner.QueueState()})
}

func (a *App) sse(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	session := a.sessionOrLast(sessionParam(r))
	ch := a.events.Subscribe()
	defer a.events.Unsubscribe(ch)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	hello, _ := json.Marshal(map[string]any{"kind": "hello", "payload": map[string]any{
		"queue":        a.runner.QueueState(),
		"interactive":  a.runner.InteractiveStatus(),
		"terminal_log": a.runner.logs.Tail(session, 64<<10),
	}})
	fmt.Fprintf(w, "data: %s\n\n", hello)
	flusher.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ── render APIs ─────────────────────────────────────────────────────────

// parseRender decodes and validates one render request object.
func (a *App) parseRender(raw map[string]any) (RenderParams, []string) {
	p, err := decodeParams(raw)
	if err != nil {
		return p, []string{err.Error()}
	}
	if strings.TrimSpace(p.Session) == "" {
		return p, []string{"session_name is required"}
	}
	p.Session = safeStem(p.Session)
	exists := func(name string) bool {
		abs, err := a.cfg.InputPath(p.Session, name)
		return err == nil && FileExists(abs)
	}
	return p, p.Validate(a.cfg.InspectModel(false).Caps(), exists)
}

func (a *App) render(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	requests := []map[string]any{body}
	if list, ok := body["jobs"].([]any); ok {
		requests = requests[:0]
		for _, item := range list {
			if obj, ok := item.(map[string]any); ok {
				requests = append(requests, obj)
			}
		}
	}
	if len(requests) == 0 || len(requests) > 16 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"errors": []string{"send between 1 and 16 jobs"}})
		return
	}
	params := make([]RenderParams, len(requests))
	for i, raw := range requests {
		p, errs := a.parseRender(raw)
		if len(errs) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errors": errs})
			return
		}
		params[i] = p
	}
	jobs := make([]JobSummary, 0, len(requests))
	for i, raw := range requests {
		delete(raw, "jobs")
		jobs = append(jobs, a.runner.Submit(raw, params[i]))
	}
	_ = a.cfg.SetLastSession(params[0].Session)
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (a *App) command(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !decodeBody(w, r, &raw) {
		return
	}
	p, errs := a.parseRender(raw)
	inputs := "inputs"
	if dir, err := a.cfg.SessionDir(p.Session); err == nil {
		inputs = filepath.Join(dir, "inputs")
	}
	resp := map[string]any{"errors": errs}
	if p.RunMode == "interactive" {
		commands := BuildInteractiveCommands(p, inputs, "<session outputs>")
		resp["display"] = "h3> " + strings.Join(commands, "\nh3> ")
		resp["launch"] = displayCommand(envFor(p), append([]string{a.cfg.H3()}, BuildInteractiveLaunchArgs(p, a.cfg.Model())...))
	} else {
		argv := append([]string{a.cfg.H3()}, BuildOneShotArgs(p, a.cfg.Model(), inputs, "<session outputs>/take.mp4")...)
		resp["argv"] = argv
		resp["display"] = displayCommand(envFor(p), argv)
	}
	seconds, samples, exact := a.cfg.Estimate(p.Session, p)
	resp["estimate"] = map[string]any{"seconds": seconds, "samples": samples, "exact": exact}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) estimate(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !decodeBody(w, r, &raw) {
		return
	}
	p, _ := a.parseRender(raw)
	seconds, samples, exact := a.cfg.Estimate(p.Session, p)
	writeJSON(w, http.StatusOK, map[string]any{"seconds": seconds, "samples": samples, "exact": exact})
}

func (a *App) cancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": a.runner.Cancel(body.ID)})
}

func (a *App) interactiveLoad(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if !decodeBody(w, r, &raw) {
		return
	}
	p, err := decodeParams(raw)
	if err == nil && strings.TrimSpace(p.Session) == "" {
		err = errors.New("session_name is required")
	}
	if err == nil {
		if extra := checkExtraArgs(p.ExtraArgs); extra != nil {
			err = extra
		} else if !a.cfg.InspectModel(false).HasFL2VA {
			err = errors.New("the model directory has no FL2VA/ pipeline")
		}
	}
	if err == nil {
		p.Env = allowedEnv(p.Env)
		err = a.runner.LoadInteractive(p)
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, a.runner.InteractiveStatus())
}

func allowedEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range env {
		if envKeyRE.MatchString(key) && len(value) <= 256 && !strings.ContainsAny(value, "\x00\n\r") {
			out[key] = value
		}
	}
	return out
}

func (a *App) interactiveUnload(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	if a.runner.Busy() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "stop the running render first"})
		return
	}
	a.runner.StopInteractive()
	a.events.Emit("interactive", a.runner.InteractiveStatus())
	writeJSON(w, http.StatusOK, a.runner.InteractiveStatus())
}

func (a *App) interactiveInput(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Line string `json:"line"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := a.runner.SendInteractive(body.Line); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true})
}

func (a *App) shell(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string `json:"session"`
		Command string `json:"command"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !a.cfg.AllowShell {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "the shell terminal is disabled; start h3 studio with --allow-shell"})
		return
	}
	if strings.TrimSpace(body.Command) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "command is required"})
		return
	}
	if err := a.runner.RunShell(a.sessionOrLast(body.Session), body.Command); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (a *App) busyH3() bool { return a.runner.Busy() || a.runner.interactiveAlive() }

func (a *App) setModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	model, err := filepath.Abs(expandHome(strings.TrimSpace(body.Model)))
	if err != nil || !DirExists(model) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "model directory does not exist"})
		return
	}
	if !DirExists(filepath.Join(model, "FL2VA")) && !DirExists(filepath.Join(model, "Ref2VA")) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "that directory has no FL2VA/ or Ref2VA/ pipeline"})
		return
	}
	if a.busyH3() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "stop the active h3 process before changing the model"})
		return
	}
	a.cfg.SetModel(model)
	writeJSON(w, http.StatusOK, a.cfg.InspectModel(true))
}

func (a *App) setH3(w http.ResponseWriter, r *http.Request) {
	var body struct {
		H3 string `json:"h3"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !a.cfg.AllowShell {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "changing the h3 binary from the browser needs --allow-shell; restart with --h3 instead"})
		return
	}
	h3, err := filepath.Abs(expandHome(strings.TrimSpace(body.H3)))
	info, statErr := os.Stat(h3)
	if err != nil || statErr != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "h3 executable does not exist or is not executable"})
		return
	}
	if a.busyH3() {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "stop the active h3 process before changing the binary"})
		return
	}
	a.cfg.SetH3(h3)
	writeJSON(w, http.StatusOK, a.cfg.InspectModel(true))
}

// ── sessions ────────────────────────────────────────────────────────────

type sessionBody struct {
	Session  string         `json:"session"`
	As       string         `json:"as"`
	Settings map[string]any `json:"settings"`
}

func (a *App) sessionActivate(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if !decodeBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Session) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "enter a session name"})
		return
	}
	dir, err := a.cfg.EnsureSession(body.Session)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	name := filepath.Base(dir)
	_ = a.cfg.SetLastSession(name)
	settings := readJSONObject(filepath.Join(dir, "setting.json"))
	delete(settings, "takes")
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "settings": settings, "sessions": a.cfg.ListSessions()})
}

func (a *App) sessionSave(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if !decodeBody(w, r, &body) {
		return
	}
	dir, err := a.cfg.EnsureSession(body.Session)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	settings := body.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	delete(settings, "takes")
	settings["session_name"] = filepath.Base(dir)
	unlock := a.cfg.LockSession(filepath.Base(dir))
	err = writeJSONAtomic(filepath.Join(dir, "setting.json"), settings)
	unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) sessionBusy(name string) bool {
	for _, job := range a.runner.QueueState() {
		if job.Session == name {
			return true
		}
	}
	return false
}

func (a *App) sessionDelete(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if !decodeBody(w, r, &body) {
		return
	}
	dir, err := a.cfg.SessionDir(body.Session)
	if err != nil || !DirExists(dir) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	name := filepath.Base(dir)
	if a.sessionBusy(name) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this session has a running or queued render"})
		return
	}
	unlock := a.cfg.LockSession(name)
	err = os.RemoveAll(dir)
	unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	remaining := a.cfg.ListSessions()
	next := "session-1"
	if len(remaining) > 0 {
		idx := sort.SearchStrings(remaining, name)
		if idx > 0 {
			next = remaining[idx-1]
		} else {
			next = remaining[0]
		}
	}
	if _, err := a.cfg.EnsureSession(next); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = a.cfg.SetLastSession(next)
	writeJSON(w, http.StatusOK, map[string]any{"name": next, "sessions": a.cfg.ListSessions()})
}

func (a *App) sessionDuplicate(w http.ResponseWriter, r *http.Request) {
	var body sessionBody
	if !decodeBody(w, r, &body) {
		return
	}
	srcDir, err := a.cfg.SessionDir(body.Session)
	if err != nil || !DirExists(srcDir) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	src := filepath.Base(srcDir)
	dst := safeStem(body.As)
	if strings.TrimSpace(body.As) == "" {
		dst = src + "-copy"
		for i := 2; pathExists(filepath.Join(a.cfg.Sessions, dst)); i++ {
			dst = fmt.Sprintf("%s-copy-%d", src, i)
		}
	} else if pathExists(filepath.Join(a.cfg.Sessions, dst)) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "a session with that name already exists"})
		return
	}
	dstDir := filepath.Join(a.cfg.Sessions, dst)
	unlock := a.cfg.LockSession(src)
	err = copyDir(srcDir, dstDir)
	unlock()
	if err != nil {
		_ = os.RemoveAll(dstDir)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	settingPath := filepath.Join(dstDir, "setting.json")
	if data := readJSONObject(settingPath); len(data) > 0 {
		data["session_name"] = dst
		delete(data, "takes")
		_ = writeJSONAtomic(settingPath, data)
	}
	_ = os.WriteFile(filepath.Join(dstDir, "terminal.log"), nil, 0o644)
	_ = a.cfg.SetLastSession(dst)
	writeJSON(w, http.StatusOK, map[string]any{"name": dst, "sessions": a.cfg.ListSessions()})
}

// ── media operations ────────────────────────────────────────────────────

type mediaBody struct {
	Session  string   `json:"session"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Position string   `json:"position"`
	Start    float64  `json:"start"`
	Length   float64  `json:"length"`
	Clips    []string `json:"clips"`
	Starred  bool     `json:"starred"`
}

func (a *App) mediaResult(w http.ResponseWriter, session, name string, err error) {
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	inputs := a.cfg.ListInputs(session)
	a.events.Emit("inputs", map[string]any{"session": session})
	resp := map[string]any{"name": name, "inputs": inputs}
	for _, item := range inputs {
		if item.Name == name {
			resp["kind"], resp["duration"], resp["probe"] = item.Kind, item.Duration, item.Probe
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) upload(w http.ResponseWriter, r *http.Request) {
	session := a.sessionOrLast(sessionParam(r))
	original, err := url.PathUnescape(r.Header.Get("X-Filename"))
	if err != nil {
		original = r.Header.Get("X-Filename")
	}
	name, err := a.cfg.SaveUpload(session, original, r.Body)
	a.mediaResult(w, session, name, err)
}

func (a *App) frame(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	session := a.sessionOrLast(body.Session)
	name, err := a.cfg.ExtractFrame(session, body.Kind, body.Name, body.Position)
	a.mediaResult(w, session, name, err)
}

func (a *App) audio(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	session := a.sessionOrLast(body.Session)
	name, err := a.cfg.ExtractAudio(session, body.Kind, body.Name)
	a.mediaResult(w, session, name, err)
}

func (a *App) useVideo(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	session := a.sessionOrLast(body.Session)
	name, err := a.cfg.ImportAsInput(session, body.Kind, body.Name)
	a.mediaResult(w, session, name, err)
}

func (a *App) trim(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	// Cap a touch under 15s: ffprobe's duration for formats like MP3 is
	// approximate, so a clip cut at exactly 15.0s can probe slightly over.
	length := min(max(body.Length, 2), 14.8)
	if body.Length == 0 {
		length = 14.8
	}
	session := a.sessionOrLast(body.Session)
	name, err := a.cfg.TrimMedia(session, body.Name, body.Start, length)
	a.mediaResult(w, session, name, err)
}

func (a *App) timelineRender(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	session := a.sessionOrLast(body.Session)
	name, err := a.cfg.CombineTimeline(session, body.Name, body.Clips)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a.events.Emit("timeline", map[string]any{"session": session, "name": name})
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "timeline": a.cfg.ListTimeline(session)})
}

func (a *App) deleteMedia(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	session := a.sessionOrLast(body.Session)
	sub := map[string]string{"image": "inputs", "video": "inputs", "audio": "inputs", "input": "inputs",
		"output": "outputs", "timeline": "timeline"}[body.Kind]
	if sub == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "kind must be input, output or timeline"})
		return
	}
	abs, err := a.cfg.MediaPath(session, sub+"/"+filepath.Base(body.Name))
	if err != nil || !FileExists(abs) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "file not found"})
		return
	}
	input := sub == "inputs"
	sidecar := sidecarFor(abs, input)
	if !input {
		if rel, _ := readJSONObject(sidecar)["preview_dir"].(string); rel != "" {
			if dir, err := a.cfg.MediaPath(session, rel); err == nil && strings.HasPrefix(rel, "previews/") {
				_ = os.RemoveAll(dir)
			}
		}
	}
	if err := os.Remove(abs); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_ = os.Remove(sidecar)
	_ = os.Remove(filepath.Join(filepath.Dir(abs), ".thumbs", filepath.Base(abs)+".jpg"))
	a.events.Emit(map[string]string{"inputs": "inputs", "outputs": "takes", "timeline": "timeline"}[sub], map[string]any{"session": session})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) starTake(w http.ResponseWriter, r *http.Request) {
	var body mediaBody
	if !decodeBody(w, r, &body) {
		return
	}
	session := a.sessionOrLast(body.Session)
	abs, err := a.cfg.MediaPath(session, "outputs/"+filepath.Base(body.Name))
	if err != nil || !FileExists(abs) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "take not found"})
		return
	}
	unlock := a.cfg.LockSession(session)
	defer unlock()
	sidecar := sidecarFor(abs, false)
	meta := readJSONObject(sidecar)
	meta["starred"] = body.Starred
	if err := writeJSONAtomic(sidecar, meta); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "starred": body.Starred})
}
