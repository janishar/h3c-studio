package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BrowseTimeline lists directories and video clips under the sessions root.
// rel == "" opens the session's outputs, "." the sessions root, anything else
// is a path relative to the sessions root. Hidden entries and previews/ are
// skipped.
func (c *Config) BrowseTimeline(session, rel string) (map[string]any, error) {
	if rel == "" {
		if _, err := c.EnsureSession(session); err != nil {
			return nil, err
		}
		rel = safeStem(session) + "/outputs"
	}
	abs, err := c.ResolveSessionsPath(rel)
	if err != nil {
		return nil, err
	}
	if !DirExists(abs) {
		return nil, errors.New("directory not found")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	dirs := []map[string]any{}
	files := []map[string]any{}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || (entry.IsDir() && name == "previews") {
			continue
		}
		full := filepath.Join(abs, name)
		if entry.IsDir() {
			dirs = append(dirs, map[string]any{"name": name, "path": c.relSessionsPath(full)})
			continue
		}
		if !videoExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		relPath := c.relSessionsPath(full)
		input := filepath.Base(filepath.Dir(full)) == "inputs"
		probe := c.cachedProbe(full, input, readJSONObject(sidecarFor(full, input)))
		mtime := float64(info.ModTime().UnixNano()) / 1e9
		files = append(files, map[string]any{
			"name": name, "path": relPath, "size": info.Size(), "mtime": mtime,
			"duration": probe.Duration, "width": probe.Width, "height": probe.Height,
			"url":   "/sfile/" + escapeRel(relPath),
			"thumb": "/sthumb/" + escapeRel(relPath) + fmt.Sprintf("?v=%d", int64(mtime)),
		})
	}
	var parent any
	if abs != c.Sessions {
		parent = c.relSessionsPath(filepath.Dir(abs))
	}
	return map[string]any{"path": c.relSessionsPath(abs), "parent": parent, "dirs": dirs, "files": files}, nil
}

func escapeRel(rel string) string {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = urlPathEscape(part)
	}
	return strings.Join(parts, "/")
}

func evenUp(v int) int { return v + v%2 }

// CombineTimeline concatenates clips (paths relative to the sessions root) in
// order into <session>/timeline/<name>.mp4. Clips are letterboxed onto the
// largest canvas, resampled to 24 fps, and clips without audio get matching
// silence. Sources are only read, never modified.
func (c *Config) CombineTimeline(session, name string, clipRelPaths []string) (string, error) {
	if len(clipRelPaths) == 0 {
		return "", errors.New("at least one clip is required")
	}
	type clip struct {
		abs   string
		probe Probe
	}
	clips := make([]clip, 0, len(clipRelPaths))
	maxW, maxH := 0, 0
	for _, rel := range clipRelPaths {
		abs, err := c.ResolveSessionsPath(rel)
		if err != nil {
			return "", err
		}
		if !FileExists(abs) || !videoExts[strings.ToLower(filepath.Ext(abs))] {
			return "", fmt.Errorf("not a video clip: %s", rel)
		}
		input := filepath.Base(filepath.Dir(abs)) == "inputs"
		probe := c.cachedProbe(abs, input, readJSONObject(sidecarFor(abs, input)))
		maxW, maxH = max(maxW, probe.Width), max(maxH, probe.Height)
		clips = append(clips, clip{abs: abs, probe: probe})
	}
	if maxW == 0 || maxH == 0 {
		maxW, maxH = 512, 512
	}
	maxW, maxH = evenUp(maxW), evenUp(maxH)

	outDir, err := c.SessionSubdir(session, "timeline")
	if err != nil {
		return "", err
	}
	stem := safeStem(name)
	if strings.TrimSpace(name) == "" {
		stem = fmt.Sprintf("timeline-%s", time.Now().Format("0102-150405"))
	}
	outPath := uniquePath(outDir, stem, ".mp4")

	args := []string{}
	for _, cl := range clips {
		args = append(args, "-i", cl.abs)
	}
	// Silent-audio inputs go after all clip inputs, so clip i stays input i.
	silentIndex := map[int]int{}
	for i, cl := range clips {
		if !cl.probe.HasAudio {
			dur := cl.probe.Duration
			if dur <= 0 {
				dur = 1
			}
			silentIndex[i] = len(clips) + len(silentIndex)
			args = append(args, "-f", "lavfi", "-t", fmt.Sprintf("%.3f", dur), "-i", "anullsrc=r=48000:cl=stereo")
		}
	}
	var filters []string
	var refs strings.Builder
	for i := range clips {
		filters = append(filters, fmt.Sprintf(
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=24[v%d]",
			i, maxW, maxH, maxW, maxH, i))
		audioIn := i
		if idx, silent := silentIndex[i]; silent {
			audioIn = idx
		}
		filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_rates=48000:channel_layouts=stereo[a%d]", audioIn, i))
		fmt.Fprintf(&refs, "[v%d][a%d]", i, i)
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=1[vout][aout]", refs.String(), len(clips)))
	args = append(args,
		"-filter_complex", strings.Join(filters, ";"),
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-preset", "fast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "192k", "-movflags", "+faststart",
		outPath,
	)
	if err := c.ffmpeg(10*time.Minute, args...); err != nil {
		_ = os.Remove(outPath)
		return "", err
	}
	info, _ := os.Stat(outPath)
	meta := map[string]any{
		"clips":   append([]string{}, clipRelPaths...),
		"created": nowSeconds(),
		"probe":   c.probeMedia(outPath),
	}
	if info != nil {
		meta["probe_mtime"] = float64(info.ModTime().UnixNano()) / 1e9
	}
	_ = writeJSONAtomic(sidecarFor(outPath, false), meta)
	return filepath.Base(outPath), nil
}
