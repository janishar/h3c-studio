package server

import (
	"encoding/json"
	"math"
	"net/url"
	"path/filepath"
	"sort"
)

// Estimate predicts a render's wall time from the session's finished takes:
// the median of takes with identical settings when there are any (exact),
// otherwise the median of same-run-mode takes scaled by relative work
// (render pixels × frames × steps × layers). Returns 0 when nothing fits.
func (c *Config) Estimate(session string, p RenderParams) (seconds float64, samples int, exact bool) {
	dir, err := c.SessionDir(session)
	if err != nil {
		return 0, 0, false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "outputs", "*.json"))
	var same, scaled []float64
	target := renderWork(p)
	for _, path := range matches {
		meta := readJSONObject(path)
		duration, _ := meta["duration_s"].(float64)
		if duration <= 0 || meta["state"] != "done" {
			continue
		}
		raw, ok := meta["params"].(map[string]any)
		if !ok {
			continue
		}
		prior, err := decodeParams(raw)
		if err != nil || prior.RunMode != p.RunMode {
			continue
		}
		if sameWorkload(prior, p) {
			same = append(same, duration)
		}
		if work := renderWork(prior); work > 0 && target > 0 {
			scaled = append(scaled, duration*target/work)
		}
	}
	switch {
	case len(same) > 0:
		return math.Round(median(same)), len(same), true
	case len(scaled) > 0:
		return math.Round(median(scaled)), len(scaled), false
	}
	return 0, 0, false
}

func renderWork(p RenderParams) float64 {
	w, h := p.Width, p.Height
	if p.RenderWidth > 0 && p.RenderHeight > 0 {
		w, h = p.RenderWidth, p.RenderHeight
	}
	return float64(w) * float64(h) * float64(p.Frames) * float64(p.Steps) * float64(p.Layers) / 50
}

func sameWorkload(a, b RenderParams) bool {
	return a.Width == b.Width && a.Height == b.Height && a.RenderWidth == b.RenderWidth &&
		a.RenderHeight == b.RenderHeight && a.Frames == b.Frames && a.Steps == b.Steps &&
		a.Layers == b.Layers && a.Reuse == b.Reuse && a.CoreReuse == b.CoreReuse &&
		a.TokenReduction == b.TokenReduction && a.SSDStreaming == b.SSDStreaming &&
		a.Int8RowFC2 == b.Int8RowFC2 && a.Mode == b.Mode && a.PreviewOn() == b.PreviewOn()
}

func median(values []float64) float64 {
	sorted := append([]float64{}, values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func cloneMap(src map[string]any) map[string]any {
	out := map[string]any{}
	data, err := json.Marshal(src)
	if err != nil {
		for k, v := range src {
			out[k] = v
		}
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func jsonRoundTrip(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, json.Unmarshal(data, &out)
}

func urlPathEscape(s string) string { return url.PathEscape(s) }
