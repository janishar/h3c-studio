package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	helm "github.com/janishar/helmstudio/packages/helm-runtime-sdk/go"
)

// A Config built without a platform has none, and nothing in the render path
// may care. If this ever panics, a take that rendered fine is lost to it.
func TestANilPlatformRecordsNothingAndDoesNotPanic(t *testing.T) {
	var p *Platform
	if p.Available() {
		t.Fatal("a nil Platform reports itself available")
	}
	p.RecordTake("/tmp/take.mp4", "take", "session-1", Probe{Width: 1280}, map[string]any{"seed": 42})

	// And one that exists but never reached a provider is the same.
	empty := &Platform{}
	if empty.Available() {
		t.Fatal("a Platform with no client reports itself available")
	}
	empty.RecordTake("/tmp/take.mp4", "", "", Probe{}, nil)
}

// What ffprobe could not read is left out, because zero is a claim: a take
// helmstudio believes is 0x0 for 0 seconds is worse than one it knows nothing
// about, and the gallery draws what it is told.
func TestAdoptRequestOmitsWhatTheProbeCouldNotRead(t *testing.T) {
	req := adoptRequestFor("/takes/a.mp4", Probe{})
	if req.Path != "/takes/a.mp4" || req.Kind != helm.AssetKindVideo {
		t.Fatalf("adopt request is %+v", req)
	}
	for name, got := range map[string]any{
		"Width": req.Width, "Height": req.Height, "DurationS": req.DurationS, "FPS": req.FPS,
	} {
		switch v := got.(type) {
		case *int64:
			if v != nil {
				t.Errorf("%s is %d for an empty probe, want unset", name, *v)
			}
		case *float64:
			if v != nil {
				t.Errorf("%s is %v for an empty probe, want unset", name, *v)
			}
		}
	}

	full := adoptRequestFor("/takes/b.mp4", Probe{Width: 1280, Height: 704, Duration: 5.5, FPS: 24})
	if full.Width == nil || *full.Width != 1280 || full.Height == nil || *full.Height != 704 {
		t.Errorf("size is %v x %v, want 1280 x 704", full.Width, full.Height)
	}
	if full.DurationS == nil || *full.DurationS != 5.5 {
		t.Errorf("duration is %v, want 5.5", full.DurationS)
	}
	if full.FPS == nil || *full.FPS != 24 {
		t.Errorf("fps is %v, want 24", full.FPS)
	}

	// A width with no height is not a size, and half of one is not sent.
	half := adoptRequestFor("/takes/c.mp4", Probe{Width: 1280})
	if half.Width != nil || half.Height != nil {
		t.Errorf("a probe with width and no height sent %v x %v, want neither", half.Width, half.Height)
	}
}

// The sidecar is written to disk by finishTake and belongs to it; recording a
// take must not reach into it. If this fails, the session name leaks into the
// file on disk.
func TestItemParamsCopiesTheSidecarRatherThanWritingToIt(t *testing.T) {
	sidecar := map[string]any{"seed": 42, "prompt": "a golden retriever"}
	item := itemParams("session-1", sidecar)

	if _, leaked := sidecar["h3_session"]; leaked {
		t.Error("recording the take wrote the session into the sidecar map")
	}
	if item["h3_session"] != "session-1" {
		t.Errorf("item session is %v, want session-1", item["h3_session"])
	}
	if item["seed"] != 42 || item["prompt"] != "a golden retriever" {
		t.Errorf("item params lost the sidecar's own values: %+v", item)
	}

	// No session: nothing is added, and the copy still stands alone.
	none := itemParams("", sidecar)
	if _, ok := none["h3_session"]; ok {
		t.Error("an empty session was recorded as one")
	}
	none["seed"] = 7
	if sidecar["seed"] != 42 {
		t.Error("the returned params alias the sidecar")
	}
}

// The runner calls these on every render. A render helmstudio opened no job
// for has none, and a nil one must cost nothing and panic never — a render that worked being lost
// to a nil dereference is the failure this whole design avoids.
func TestANilTaskIsANoOp(t *testing.T) {
	var task *Task
	task.Log("a line")
	task.Progress(3, 10)
	task.Finish("done", "")
	task.Finish("done", "") // finishing twice is what a panicking render does

	var p *Platform
	if got := p.StartTask("take"); got != nil {
		t.Errorf("a nil Platform started a task: %v", got)
	}
	if got := (&Platform{}).StartTask("take"); got != nil {
		t.Errorf("a Platform with no client started a task: %v", got)
	}
}

// h3 studio's own states are not helmstudio's. A render that finished is
// "done" here and "succeeded" there, and anything unrecognised must end the
// job rather than leave the launcher waiting on it forever.
func TestTaskStateMapping(t *testing.T) {
	for state, want := range map[string]string{
		"done":      "succeeded",
		"cancelled": "cancelled",
		"failed":    "failed",
		"running":   "failed",
		"":          "failed",
	} {
		if got := taskStateFor(state); got != want {
			t.Errorf("taskStateFor(%q) = %q, want %q", state, got, want)
		}
	}
}

// A render writes thousands of lines from the goroutine reading h3's output
// while the job finishes on another. Nothing may race, and nothing may block.
func TestLoggingWhileFinishingDoesNotRaceOrBlock(t *testing.T) {
	task := &Task{flushed: make(chan struct{})} // no client: flush is never reached
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				task.Log(fmt.Sprintf("line %d-%d", n, j))
			}
		}(i)
	}
	wg.Wait()

	task.mu.Lock()
	held := len(task.pending)
	task.mu.Unlock()
	if held > maxPendingLines {
		t.Errorf("buffered %d lines, which is past the %d cap", held, maxPendingLines)
	}

	// Closing marks it closed; later lines are dropped rather than queued for
	// a flush that will never come.
	task.mu.Lock()
	task.closed = true
	task.mu.Unlock()
	task.Log("after the end")
	task.mu.Lock()
	after := len(task.pending)
	task.mu.Unlock()
	if after != held {
		t.Errorf("a line was buffered after the task closed: %d then %d", held, after)
	}
}

// Finish waits for the pump's last flush so the end of a render is not lost
// to a 409 on a job helmstudio already closed. A Task with no pump has
// nothing to wait for, and must not wait forever on a nil channel.
func TestFinishDoesNotHangWithoutAPump(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		task := &Task{flushed: make(chan struct{})} // no client, no pump, no done
		task.Finish("done", "")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Finish blocked on a Task that has no pump")
	}
}

// Progress must never reach the network from the render's goroutine: it
// records the latest and the pump sends it. A hundred updates between ticks
// are one request, and the last one wins.
func TestProgressIsRecordedNotSent(t *testing.T) {
	task := &Task{flushed: make(chan struct{})}
	for i := 1; i <= 50; i++ {
		task.Progress(i, 50)
	}
	task.mu.Lock()
	got := task.progress
	task.mu.Unlock()
	if len(got) != 2 || got[0] != 50 || got[1] != 50 {
		t.Errorf("progress is %v, want the latest 50/50", got)
	}

	task.mu.Lock()
	task.closed = true
	task.mu.Unlock()
	task.Progress(7, 50)
	task.mu.Lock()
	after := task.progress
	task.mu.Unlock()
	if after[0] != 50 {
		t.Errorf("progress moved to %v after the task closed", after)
	}
}

// The TIMELINE panel lists the sequences helmstudio holds for this studio.
// They are edits the platform keeps, not files in this session, so they carry
// no URL and no thumbnail and the panel must be able to tell them apart.
func TestSequencesListsWhatHelmstudioHoldsForThisStudio(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"next_cursor":null,"items":[
			{"id":"01TL","name":"h3 sequence 9/19/2025","revision":1,"duration_s":12.5,"etag":"\"1\"",
			 "studio_id":"h3-studio","created_at":"2026-09-19T18:00:00Z","updated_at":"2026-09-19T18:04:00Z",
			 "target":{"width":800,"height":448,"fps":24,"sample_rate":48000},
			 "tracks":[{"kind":"video","name":"V1","clips":[{"asset_id":"A1"},{"asset_id":"A2"}]}]}]}`)
	}))
	defer srv.Close()

	p := &Platform{client: helm.NewRemote(srv.URL, "a-token")}
	items := p.Sequences(context.Background())
	if len(items) != 1 {
		t.Fatalf("got %d sequences, want 1 (asked %s)", len(items), asked)
	}
	it := items[0]
	if it.Name != "h3 sequence 9/19/2025" || it.Kind != "timeline" {
		t.Errorf("sequence decoded as %+v", it)
	}
	if it.URL != "" || it.Thumb != "" {
		t.Errorf("a sequence is not a file here, so it carries no url or thumb: %+v", it)
	}
	if it.Meta["source"] != "helmstudio" {
		t.Errorf("the panel cannot tell it apart: meta = %+v", it.Meta)
	}
	if clips, ok := it.Meta["clips"].([]string); !ok || len(clips) != 2 {
		t.Errorf("clips = %+v, want the two the sequence names", it.Meta["clips"])
	}
	if it.Duration == nil || *it.Duration != 12.5 {
		t.Errorf("duration = %+v, want 12.5", it.Duration)
	}
}

// No platform is the same answer as no sequences: h3 standalone lists its own
// combined videos and nothing else, and never panics reaching for a daemon
// that is not there.
func TestSequencesWithoutAPlatformAreNone(t *testing.T) {
	var nilPlatform *Platform
	if got := nilPlatform.Sequences(context.Background()); got != nil {
		t.Errorf("a nil Platform listed %d sequences", len(got))
	}
	if got := (&Platform{}).Sequences(context.Background()); got != nil {
		t.Errorf("a Platform with no client listed %d sequences", len(got))
	}
}
