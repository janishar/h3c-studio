package server

import (
	"testing"

	helm "github.com/janishar/helmstudio/packages/helm-runtime-sdk/go"
)

// A studio running on its own has no platform, and nothing in the render path
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
