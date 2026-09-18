package server

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	helm "github.com/janishar/helmstudio/packages/helm-runtime-sdk/go"
)

// h3 studio under helmstudio.
//
// A take is h3 studio's own file under the session's outputs/ and stays that
// way; this records it with helmstudio as well, so what this studio made is
// in the gallery every studio shares and can be pulled into a timeline. The
// file is adopted by hardlink, not copied — the same bytes, counted once.
//
// Standalone there is no platform: HELM_API is unset, every method here is a
// no-op on a nil Platform, and h3 studio behaves exactly as it did before.
// Nothing in the render path depends on any of it succeeding.

// Platform is helmstudio, when h3 studio is running under it or under
// `helm dev`. It is nil when the studio runs on its own.
type Platform struct {
	client *helm.Client
}

// NewPlatform returns helmstudio if this process is running under it, and nil
// if it is not. A studio that cannot reach the platform is not a broken
// studio, so the only thing an error earns is a line in the log.
func NewPlatform() *Platform {
	client, err := helm.FromEnv()
	if err != nil {
		if !errors.Is(err, helm.ErrNoProvider) {
			// HELM_API set but unusable — worth saying, because under
			// helmstudio this means takes will not reach the gallery.
			log.Printf("helmstudio: not recording takes: %v", err)
		}
		return nil
	}
	return &Platform{client: client}
}

// Available reports whether takes are being recorded with helmstudio.
func (p *Platform) Available() bool { return p != nil && p.client != nil }

// RecordTake adopts a finished take and records it in the gallery, with the
// parameters it was made from. One call per take, which is what the gallery's
// contract asks for.
//
// It never fails a render. The take is already on disk and that is the source
// of truth; this is a record of it, and a platform that refuses is a line in
// the log rather than a lost generation.
//
// The session is deliberately not sent. helmstudio validates session_id
// against its own live sessions for this studio, and h3 studio's sessions are
// still its own directories — so the name travels in params until sessions
// are helmstudio sessions too.
func (p *Platform) RecordTake(path, title, session string, probe Probe, params map[string]any) {
	if !p.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	asset, err := p.client.Assets.Adopt(ctx, adoptRequestFor(path, probe))
	if err != nil {
		log.Printf("helmstudio: could not adopt %s: %v", path, err)
		return
	}
	create := helm.ItemCreate{Kind: helm.AssetKindVideo, AssetID: asset.ID, Params: itemParams(session, params)}
	if title != "" {
		create.Title = &title
	}
	if _, err := p.client.Gallery.Add(ctx, create); err != nil {
		log.Printf("helmstudio: adopted %s as %s but could not record it: %v", path, asset.ID, err)
	}
}

// adoptRequestFor is what helmstudio is told about a take: where it is, that
// it is video, and whatever ffprobe could read. A dimension ffprobe could not
// read is left out rather than sent as zero, because zero is a claim.
func adoptRequestFor(path string, probe Probe) helm.AdoptRequest {
	req := helm.AdoptRequest{Path: path, Kind: helm.AssetKindVideo}
	if probe.Width > 0 && probe.Height > 0 {
		w, h := int64(probe.Width), int64(probe.Height)
		req.Width, req.Height = &w, &h
	}
	if probe.Duration > 0 {
		d := probe.Duration
		req.DurationS = &d
	}
	if probe.FPS > 0 {
		f := probe.FPS
		req.FPS = &f
	}
	return req
}

// itemParams is the take's sidecar, plus the h3 session it came from.
//
// It copies: params is the map finishTake writes to disk as the sidecar, and
// the caller still owns it. The session travels as a parameter because
// helmstudio validates session_id against its own live sessions and h3
// studio's are still its own directories.
func itemParams(session string, params map[string]any) map[string]any {
	item := make(map[string]any, len(params)+1)
	for k, v := range params {
		item[k] = v
	}
	if session != "" {
		item["h3_session"] = session
	}
	return item
}

// ---------------------------------------------------------------- task jobs

// A render, mirrored to helmstudio as a task job.
//
// h3 studio's own runner is unchanged and stays in charge: it queues, runs,
// reports and cancels exactly as it did. This reports the same render to
// helmstudio in parallel, so the launcher can show what this studio is doing
// and helm-terminal has a log to stream. Nothing here can fail a render — a
// platform that refuses gets a line in the log and the render carries on.
//
// A nil *Task is the standalone case and every method is a no-op on it, so
// the runner never asks whether there is a platform.
type Task struct {
	client *helm.Client
	id     string

	mu      sync.Mutex
	pending []string
	closed  bool
	flushed chan struct{}
}

// logFlush is how often buffered lines are sent. A render writes thousands of
// them and one request per line would be a request per frame; this trades a
// little latency for a request every half second.
const logFlush = 500 * time.Millisecond

// maxPendingLines bounds what a flush can owe, so a studio that floods its
// output cannot grow this without limit. The oldest go: helm-terminal is for
// watching a render, and the studio's own log keeps everything.
const maxPendingLines = 2000

// StartTask reports a render to helmstudio and returns the job to report it
// on, or nil when there is no platform or it refused.
func (p *Platform) StartTask(label string) *Task {
	if !p.Available() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	state := string(helm.JobStateRunning)
	job, err := p.client.Jobs.Create(ctx, helm.TaskCreate{State: &state})
	if err != nil {
		log.Printf("helmstudio: not reporting this render: %v", err)
		return nil
	}
	t := &Task{client: p.client, id: job.ID, flushed: make(chan struct{})}
	go t.pump()
	return t
}

// Log buffers one line for helmstudio. It never blocks the render and never
// writes from the caller's goroutine.
func (t *Task) Log(line string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.pending = append(t.pending, line)
	if over := len(t.pending) - maxPendingLines; over > 0 {
		t.pending = t.pending[over:]
	}
}

// pump sends buffered lines until the task is finished.
func (t *Task) pump() {
	tick := time.NewTicker(logFlush)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			t.flush()
		case <-t.flushed:
			t.flush()
			return
		}
	}
}

func (t *Task) flush() {
	t.mu.Lock()
	lines := t.pending
	t.pending = nil
	t.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := t.client.Jobs.AppendLog(ctx, t.id, helm.LogAppend{Lines: lines}); err != nil {
		log.Printf("helmstudio: %d log lines not delivered: %v", len(lines), err)
	}
}

// Progress reports how far along the render is. The caller throttles: this is
// called where h3 studio already decided to tell its own page.
func (t *Task) Progress(num, den int) {
	if t == nil || den <= 0 {
		return
	}
	t.patch(map[string]any{"progress_num": num, "progress_den": den})
}

// Finish closes the job in the state h3 studio's own job ended in. Anything
// that is not done or cancelled is a failure, because a job left running is
// one the launcher would wait on forever.
func (t *Task) Finish(state, message string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	already := t.closed
	t.closed = true
	t.mu.Unlock()
	if already {
		return
	}
	close(t.flushed)

	body := map[string]any{"state": taskStateFor(state)}
	if body["state"] == string(helm.JobStateFailed) && message != "" {
		body["last_error"] = map[string]any{"code": "render_failed", "message": truncate(message, 4000)}
	}
	t.patch(body)
}

// taskStateFor maps h3 studio's own job states onto the four a task job may
// be set to. "done" is what h3 studio calls a finished render.
func taskStateFor(state string) string {
	switch state {
	case "done":
		return string(helm.JobStateSucceeded)
	case "cancelled":
		return string(helm.JobStateCancelled)
	default:
		return string(helm.JobStateFailed)
	}
}

func (t *Task) patch(body map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := t.client.Jobs.Update(ctx, t.id, body); err != nil {
		log.Printf("helmstudio: job %s not updated: %v", t.id, err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
