package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var videoExts = map[string]bool{".mp4": true, ".mov": true}

// resolveSessionPath cleans a path relative to the sessions root and checks
// that it does not escape it. rel == "" resolves to the sessions root itself.
func resolveSessionPath(cfg *Config, rel string) (string, error) {
	rel = strings.TrimPrefix(filepath.Clean("/"+rel), "/")
	abs := cfg.Sessions
	if rel != "" && rel != "." {
		abs = filepath.Join(cfg.Sessions, rel)
	}
	abs = filepath.Clean(abs)
	if abs != cfg.Sessions && !strings.HasPrefix(abs, cfg.Sessions+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes sessions directory")
	}
	return abs, nil
}

// relSessionPath returns abs relative to the sessions root, using forward slashes.
func relSessionPath(cfg *Config, abs string) string {
	rel, err := filepath.Rel(cfg.Sessions, abs)
	if err != nil {
		return filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

// browseTimeline lists directories and video files under the sessions root.
// relPath == ""  → default view (the current session's outputs directory).
// relPath == "." → the sessions root itself (lists every session).
// Anything else is a path relative to the sessions root.
func browseTimeline(cfg *Config, relPath string) (map[string]any, error) {
	if relPath == "" {
		relPath = filepath.ToSlash(filepath.Join(cfg.CurrentSession(), "outputs"))
	} else if relPath == "." {
		relPath = ""
	}
	abs, err := resolveSessionPath(cfg, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("directory not found")
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	dirs := make([]map[string]any, 0)
	files := make([]map[string]any, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, map[string]any{
				"name": entry.Name(),
				"path": relSessionPath(cfg, filepath.Join(abs, entry.Name())),
			})
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !videoExts[ext] {
			continue
		}
		fi, err := entry.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(abs, entry.Name())
		files = append(files, map[string]any{
			"name":     entry.Name(),
			"path":     relSessionPath(cfg, full),
			"size":     fi.Size(),
			"mtime":    float64(fi.ModTime().UnixNano()) / 1e9,
			"duration": probeDuration(full),
		})
	}

	relClean := relSessionPath(cfg, abs)
	var parent any
	if abs != cfg.Sessions {
		parent = relSessionPath(cfg, filepath.Dir(abs))
	}
	return map[string]any{
		"path":   relClean,
		"parent": parent,
		"dirs":   dirs,
		"files":  files,
	}, nil
}

func listTimeline(cfg *Config) []map[string]any {
	return listVideoDir(cfg.CurrentTimeline(), 0)
}

func probeHasAudio(path string) bool {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "a", "-show_entries", "stream=codec_type", "-of", "csv=p=0", path)
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func probeDims(path string) (int, int) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), "x")
	if len(parts) != 2 {
		return 0, 0
	}
	w, err1 := strconv.Atoi(parts[0])
	h, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return w, h
}

func evenUp(v int) int {
	if v%2 != 0 {
		return v + 1
	}
	return v
}

// combineTimeline concatenates the given clips (paths relative to the sessions
// root) in order, using ffmpeg, and writes the result into the current
// session's timeline directory. Source clips are only referenced, never
// modified or removed.
func combineTimeline(cfg *Config, name string, clipRelPaths []string) (string, error) {
	if len(clipRelPaths) == 0 {
		return "", fmt.Errorf("at least one clip is required")
	}
	type clip struct {
		abs      string
		hasAudio bool
		duration float64
	}
	clips := make([]clip, 0, len(clipRelPaths))
	maxW, maxH := 0, 0
	for _, rel := range clipRelPaths {
		abs, err := resolveSessionPath(cfg, rel)
		if err != nil {
			return "", err
		}
		if !FileExists(abs) {
			return "", fmt.Errorf("clip not found: %s", rel)
		}
		if !videoExts[strings.ToLower(filepath.Ext(abs))] {
			return "", fmt.Errorf("not a video file: %s", rel)
		}
		w, h := probeDims(abs)
		if w > maxW {
			maxW = w
		}
		if h > maxH {
			maxH = h
		}
		dur, _ := probeDuration(abs).(float64)
		clips = append(clips, clip{abs: abs, hasAudio: probeHasAudio(abs), duration: dur})
	}
	if maxW == 0 || maxH == 0 {
		maxW, maxH = 512, 512
	}
	maxW, maxH = evenUp(maxW), evenUp(maxH)

	if name == "" {
		name = fmt.Sprintf("timeline-%d", time.Now().Unix())
	}
	name = safeStem(name)
	outDir := cfg.CurrentTimeline()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	outPath := filepath.Join(outDir, name+".mp4")
	for i := 1; FileExists(outPath); i++ {
		outPath = filepath.Join(outDir, fmt.Sprintf("%s-%d.mp4", name, i))
	}

	args := []string{"-y"}
	for _, c := range clips {
		args = append(args, "-i", c.abs)
	}
	// Silent-audio inputs are appended after all clip inputs, so clip i's
	// video/audio stays addressable as ffmpeg input index i.
	silentIndex := map[int]int{} // clip index -> lavfi input index
	nextInput := len(clips)
	for i, c := range clips {
		if !c.hasAudio {
			dur := c.duration
			if dur <= 0 {
				dur = 1
			}
			args = append(args, "-f", "lavfi", "-t", fmt.Sprintf("%.3f", dur), "-i", "anullsrc=r=44100:cl=stereo")
			silentIndex[i] = nextInput
			nextInput++
		}
	}

	var filters []string
	var refs strings.Builder
	for i := range clips {
		filters = append(filters, fmt.Sprintf(
			"[%d:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1,fps=24[v%d]",
			i, maxW, maxH, maxW, maxH, i))
		if idx, silent := silentIndex[i]; silent {
			filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_rates=44100:channel_layouts=stereo[a%d]", idx, i))
		} else {
			filters = append(filters, fmt.Sprintf("[%d:a]aformat=sample_rates=44100:channel_layouts=stereo[a%d]", i, i))
		}
		refs.WriteString(fmt.Sprintf("[v%d][a%d]", i, i))
	}
	filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=1[vout][aout]", refs.String(), len(clips)))
	filterComplex := strings.Join(filters, ";")

	args = append(args,
		"-filter_complex", filterComplex,
		"-map", "[vout]", "-map", "[aout]",
		"-c:v", "libx264", "-preset", "fast", "-crf", "20",
		"-c:a", "aac", "-b:a", "128k",
		outPath,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.FFmpeg, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", commandError{Err: err, Stderr: string(output)}
	}

	sources := make([]string, len(clipRelPaths))
	copy(sources, clipRelPaths)
	_ = WriteJSONFile(strings.TrimSuffix(outPath, filepath.Ext(outPath))+".json", map[string]any{
		"clips":   sources,
		"created": nowSeconds(),
	}, false)

	return filepath.Base(outPath), nil
}
