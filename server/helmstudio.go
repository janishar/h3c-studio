package server

import (
	"context"
	"errors"
	"log"
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
