package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	legalFrames = func() []int {
		frames := make([]int, 0, 22)
		for n := 0; n < 22; n++ {
			frames = append(frames, 5+17*n)
		}
		return frames
	}()
	maxPixels = 768 * 1344
	stemRE    = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
)

func safeStem(text string) string {
	stem := stemRE.ReplaceAllString(text, "-")
	stem = strings.Trim(stem, "-")
	stem = strings.ToLower(stem)
	if stem == "" {
		stem = "take"
	}
	if len(stem) > 48 {
		stem = stem[:48]
	}
	return stem
}

func defaultSessionSettings(name string) map[string]any {
	return map[string]any{
		"session_name":    safeStem(name),
		"label":           "",
		"prompt":          "",
		"prompt_doc":      []any{map[string]any{"type": "text", "value": ""}},
		"width":           512,
		"height":          512,
		"frames":          22,
		"steps":           4,
		"layers":          50,
		"reuse":           1,
		"seed":            42,
		"run_mode":        "oneshot",
		"token_reduction": false,
		"int8_row_fc2":    false,
		"ssd_streaming":   false,
		"refs":            []any{},
		"env":             map[string]any{"H3_ZERO_COPY_WEIGHTS": "0"},
		"takes":           []any{},
	}
}

func snapFrames(requested int) int {
	for _, frame := range legalFrames {
		if frame >= requested {
			return frame
		}
	}
	return legalFrames[len(legalFrames)-1]
}

func writeSidecar(outPath string, job *Job) {
	meta := job.Summary()
	if job.Started != nil && job.Finished != nil {
		meta["duration_s"] = round2(*job.Finished - *job.Started)
	}
	_ = WriteJSONFile(strings.TrimSuffix(outPath, filepath.Ext(outPath))+".json", meta, false)
}

func saveSession(cfg *Config, params map[string]any) (string, error) {
	name := safeStem(anyToString(firstNonEmpty(params["session_name"], params["label"], "session-1")))
	path := cfg.SessionSetting(name)
	cloned := cloneMap(params)
	cloned["session_name"] = name
	existing := readJSONObject(path)
	takes, ok := existing["takes"].([]any)
	if !ok {
		takes = []any{}
	}
	cloned["takes"] = takes
	if err := WriteJSONFile(path, cloned, true); err != nil {
		return "", err
	}
	return name, nil
}

func recordTake(cfg *Config, job *Job) {
	name := safeStem(anyToString(firstNonEmpty(job.Params["session_name"], "session-1")))
	path := cfg.SessionSetting(name)
	data := readJSONObject(path)
	takes, ok := data["takes"].([]any)
	if !ok {
		takes = []any{}
	}
	entry := job.Summary()
	if job.Started != nil && job.Finished != nil {
		entry["duration_s"] = round2(*job.Finished - *job.Started)
	}
	takes = append(takes, entry)
	data["session_name"] = name
	data["takes"] = takes
	_ = WriteJSONFile(path, data, true)
}

func listSessions(cfg *Config) []map[string]any {
	entries, err := os.ReadDir(cfg.Sessions)
	if err != nil {
		return []map[string]any{}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]map[string]any, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(cfg.Sessions, entry.Name(), "setting.json")
		if !FileExists(path) {
			continue
		}
		data := readJSONObject(path)
		if len(data) == 0 {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"name":   entry.Name(),
			"params": data,
			"mtime":  float64(info.ModTime().UnixNano()) / 1e9,
		})
	}
	return out
}

func listInputs(cfg *Config) []map[string]any {
	exts := map[string]string{
		".png":  "image",
		".jpg":  "image",
		".jpeg": "image",
		".webp": "image",
		".mp4":  "video",
		".mov":  "video",
		".wav":  "audio",
		".mp3":  "audio",
		".m4a":  "audio",
	}
	entries, err := os.ReadDir(cfg.CurrentInputs())
	if err != nil {
		return []map[string]any{}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := make([]map[string]any, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		suffix := strings.ToLower(filepath.Ext(entry.Name()))
		kind, ok := exts[suffix]
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		item := map[string]any{
			"name": entry.Name(),
			"kind": kind,
			"size": info.Size(),
		}
		if kind == "video" || kind == "audio" {
			item["duration"] = probeDuration(filepath.Join(cfg.CurrentInputs(), entry.Name()))
		}
		items = append(items, item)
	}
	return items
}

func probeDuration(path string) any {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return nil
	}
	return round2(value)
}

func listOutputs(cfg *Config) []map[string]any {
	matches, _ := filepath.Glob(filepath.Join(cfg.CurrentOutputs(), "*.mp4"))
	sort.Slice(matches, func(i, j int) bool {
		ai, aerr := os.Stat(matches[i])
		bi, berr := os.Stat(matches[j])
		if aerr != nil || berr != nil {
			return matches[i] > matches[j]
		}
		return ai.ModTime().After(bi.ModTime())
	})
	items := make([]map[string]any, 0, len(matches))
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		meta := map[string]any{}
		side := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
		if FileExists(side) {
			meta = readJSONObject(side)
			if meta == nil {
				meta = map[string]any{}
			}
		}
		items = append(items, map[string]any{
			"name":  filepath.Base(path),
			"size":  info.Size(),
			"mtime": float64(info.ModTime().UnixNano()) / 1e9,
			"meta":  meta,
		})
		if len(items) == 80 {
			break
		}
	}
	return items
}

func extractLastFrame(cfg *Config, videoName string) (string, error) {
	src := filepath.Join(cfg.CurrentOutputs(), videoName)
	if !FileExists(src) {
		return "", os.ErrNotExist
	}
	dst := filepath.Join(cfg.CurrentInputs(), strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+"-lastframe.png")
	cmd := exec.Command(cfg.FFmpeg, "-y", "-sseof", "-0.2", "-i", src, "-vsync", "0", "-update", "1", "-q:v", "2", dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", commandError{Err: err, Stderr: string(out)}
	}
	return filepath.Base(dst), nil
}

func validate(params map[string]any) []string {
	errs := []string{}
	w, h := intFrom(params["width"], 0), intFrom(params["height"], 0)
	if w%32 != 0 || h%32 != 0 {
		errs = append(errs, "Width and height must be multiples of 32.")
	}
	if w < 32 || h < 32 {
		errs = append(errs, "Width and height must be at least 32.")
	}
	if w*h > maxPixels {
		errs = append(errs, fmt.Sprintf("%dx%d is %s pixels; the ceiling is %s.", w, h, comma(w*h), comma(maxPixels)))
	}
	if stringsTrimSpace(anyToString(params["prompt"])) == "" {
		errs = append(errs, "Write a prompt.")
	}
	refs, ok := params["refs"].([]any)
	if !ok {
		if params["refs"] != nil {
			errs = append(errs, "References must be an ordered list.")
		}
		refs = []any{}
	}
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if !ok {
			errs = append(errs, "Each reference must have a valid kind.")
			continue
		}
		kind := anyToString(ref["kind"])
		if kind != "image" && kind != "video" && kind != "audio" {
			errs = append(errs, "Each reference must have a valid kind.")
			continue
		}
		if kind == "video" && anyToString(ref["mode"]) == "replace" && anyToString(ref["pairedAudio"]) == "" {
			errs = append(errs, "Replacement audio is required for videos in replace mode.")
		}
		duration, err := floatFromStrict(ref["duration"])
		if err != nil {
			errs = append(errs, fmt.Sprintf("Invalid duration for %s.", firstString(anyToString(ref["name"]), "reference")))
			duration = 0
		}
		if (kind == "video" || kind == "audio") && duration != 0 && (duration < 2 || duration > 15) {
			name := anyToString(ref["name"])
			if name == "" {
				name = "Reference"
			}
			errs = append(errs, fmt.Sprintf("%s must be between 2 and 15 seconds.", name))
		}
	}
	anchors := anyToString(params["first_frame"]) != "" || anyToString(params["last_frame"]) != ""
	if len(refs) > 0 && anchors {
		errs = append(errs, "Ref2VA references can't be combined with first/last frame anchors.")
	}
	images, videos, audio := 0, 0, 0
	totalDuration := 0.0
	for _, raw := range refs {
		ref, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch anyToString(ref["kind"]) {
		case "image":
			images++
		case "video":
			videos++
		case "audio":
			audio++
		}
		if value, err := floatFromStrict(ref["duration"]); err == nil {
			totalDuration += value
		}
	}
	if audio > 0 && images == 0 && videos == 0 {
		errs = append(errs, "A standalone audio reference must accompany an image or video.")
	}
	if images > 9 {
		errs = append(errs, "At most 9 image references.")
	}
	if videos > 3 {
		errs = append(errs, "At most 3 video references.")
	}
	if audio > 3 {
		errs = append(errs, "At most 3 audio references.")
	}
	if totalDuration > 15 {
		errs = append(errs, fmt.Sprintf("Combined reference duration is %.1fs; the limit is 15s.", totalDuration))
	}
	return errs
}

type commandError struct {
	Err    error
	Stderr string
}

func (e commandError) Error() string { return e.Err.Error() }

func WriteJSONFile(path string, obj any, newline bool) error {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	if newline {
		data = append(data, '\n')
	}
	return os.WriteFile(path, data, 0o644)
}

func readJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func readStringField(path, field string) (string, bool) {
	obj := readJSONObject(path)
	value := stringsTrimSpace(anyToString(obj[field]))
	return value, value != ""
}

func cloneMap(src map[string]any) map[string]any {
	data, err := json.Marshal(src)
	if err != nil {
		out := make(map[string]any, len(src))
		for k, v := range src {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func anyToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case json.Number:
		return t.String()
	case float64:
		if math.Trunc(t) == t {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func firstNonEmpty(values ...any) any {
	for _, v := range values {
		if stringsTrimSpace(anyToString(v)) != "" {
			return v
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values[len(values)-1]
}

func firstString(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func intFrom(v any, fallback int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case float32:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		i, err := t.Int64()
		if err == nil {
			return int(i)
		}
		f, err := t.Float64()
		if err == nil {
			return int(f)
		}
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return i
		}
	}
	return fallback
}

func boolFrom(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(t))
		return err == nil && parsed
	case float64:
		return t != 0
	case int:
		return t != 0
	default:
		return false
	}
}

func floatFromStrict(v any) (float64, error) {
	if v == nil {
		return 0, nil
	}
	switch t := v.(type) {
	case float64:
		return t, nil
	case float32:
		return float64(t), nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case json.Number:
		return t.Float64()
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, nil
		}
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	default:
		return 0, errors.New("invalid float")
	}
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func ensureFile(path string) error {
	if FileExists(path) {
		return nil
	}
	return os.WriteFile(path, []byte{}, 0o644)
}

func comma(v int) string {
	s := strconv.Itoa(v)
	if len(s) <= 3 {
		return s
	}
	neg := ""
	if s[0] == '-' {
		neg, s = "-", s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return neg + strings.Join(parts, ",")
}

func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func stringsTrimSpace(s string) string { return strings.TrimSpace(s) }

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
