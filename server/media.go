package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var inputKinds = map[string]string{
	".png": "image", ".jpg": "image", ".jpeg": "image", ".webp": "image",
	".mp4": "video", ".mov": "video",
	".wav": "audio", ".mp3": "audio", ".m4a": "audio",
}

var videoExts = map[string]bool{".mp4": true, ".mov": true}

// Probe is the cached ffprobe summary of a media file.
type Probe struct {
	Duration float64 `json:"duration,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	FPS      float64 `json:"fps,omitempty"`
	Frames   int     `json:"frames,omitempty"`
	HasAudio bool    `json:"has_audio,omitempty"`
}

type commandError struct {
	Err    error
	Stderr string
}

func (e commandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return e.Err.Error()
	}
	if len(msg) > 400 {
		msg = msg[len(msg)-400:]
	}
	return msg
}

// runTool runs an external tool with a timeout and returns its stdout.
func runTool(timeout time.Duration, bin string, args ...string) ([]byte, error) {
	if bin == "" {
		return nil, errors.New("ffmpeg/ffprobe not found — install it with `brew install ffmpeg` or set H3_FFMPEG / H3_FFPROBE")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s timed out after %s", filepath.Base(bin), timeout)
	}
	if err != nil {
		return nil, commandError{Err: err, Stderr: stderr.String()}
	}
	return out, nil
}

func (c *Config) ffmpeg(timeout time.Duration, args ...string) error {
	_, err := runTool(timeout, c.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	return err
}

// probeMedia runs one ffprobe for duration, size, fps, frame count and audio.
func (c *Config) probeMedia(path string) Probe {
	out, err := runTool(20*time.Second, c.FFprobe, "-v", "error",
		"-show_entries", "stream=codec_type,width,height,r_frame_rate,nb_frames:format=duration",
		"-of", "json", path)
	if err != nil {
		return Probe{}
	}
	var data struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType  string `json:"codec_type"`
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			RFrameRate string `json:"r_frame_rate"`
			NbFrames   string `json:"nb_frames"`
		} `json:"streams"`
	}
	if json.Unmarshal(out, &data) != nil {
		return Probe{}
	}
	var probe Probe
	if d, err := strconv.ParseFloat(data.Format.Duration, 64); err == nil {
		probe.Duration = round2(d)
	}
	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			if probe.Width != 0 {
				continue
			}
			probe.Width, probe.Height = s.Width, s.Height
			if num, den, ok := strings.Cut(s.RFrameRate, "/"); ok {
				n, err1 := strconv.ParseFloat(num, 64)
				d, err2 := strconv.ParseFloat(den, 64)
				if err1 == nil && err2 == nil && d != 0 {
					probe.FPS = round2(n / d)
				}
			}
			probe.Frames, _ = strconv.Atoi(s.NbFrames)
		case "audio":
			probe.HasAudio = true
		}
	}
	return probe
}

// sidecarFor is where a media file's metadata lives: takes and timeline
// results use <stem>.json, inputs use <name>.json.
func sidecarFor(path string, input bool) string {
	if input {
		return path + ".json"
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
}

// cachedProbe returns the probe stored in the file's sidecar when it's
// newer than the file, probing (and storing) it otherwise.
func (c *Config) cachedProbe(path string, input bool, meta map[string]any) Probe {
	info, err := os.Stat(path)
	if err != nil {
		return Probe{}
	}
	mtime := float64(info.ModTime().UnixNano()) / 1e9
	if raw, ok := meta["probe"]; ok {
		if stamp, _ := meta["probe_mtime"].(float64); stamp >= mtime {
			var probe Probe
			if data, err := json.Marshal(raw); err == nil && json.Unmarshal(data, &probe) == nil && (probe.Duration > 0 || probe.Width > 0) {
				return probe
			}
		}
	}
	if c.FFprobe == "" {
		return Probe{}
	}
	probe := c.probeMedia(path)
	if probe.Duration > 0 || probe.Width > 0 {
		meta["probe"] = probe
		meta["probe_mtime"] = mtime
		_ = writeJSONAtomic(sidecarFor(path, input), meta)
	}
	return probe
}

// MediaItem is one file in inputs/, outputs/ or timeline/.
type MediaItem struct {
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Size     int64          `json:"size"`
	Mtime    float64        `json:"mtime"`
	Duration *float64       `json:"duration,omitempty"`
	Probe    Probe          `json:"probe"`
	Meta     map[string]any `json:"meta,omitempty"`
	URL      string         `json:"url"`
	Thumb    string         `json:"thumb,omitempty"`
}

func mediaURL(session, rel string) string {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		parts[i] = urlPathEscape(part)
	}
	return "/media/" + urlPathEscape(session) + "/" + strings.Join(parts, "/")
}

func thumbURL(session, rel string, mtime float64) string {
	return strings.Replace(mediaURL(session, rel), "/media/", "/thumb/", 1) + "?v=" + strconv.FormatInt(int64(mtime), 10)
}

type dirEntry struct {
	name  string
	path  string
	size  int64
	mtime time.Time
}

// statDir lists regular, non-hidden files in dir, newest first.
func statDir(dir string, keep func(name string) bool) []dirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	files := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || !keep(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, dirEntry{name: name, path: filepath.Join(dir, name), size: info.Size(), mtime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].mtime.Equal(files[j].mtime) {
			return files[i].name > files[j].name
		}
		return files[i].mtime.After(files[j].mtime)
	})
	return files
}

func (c *Config) ListInputs(session string) []MediaItem {
	dir, err := c.SessionSubdir(session, "inputs")
	if err != nil {
		return []MediaItem{}
	}
	files := statDir(dir, func(name string) bool { return inputKinds[strings.ToLower(filepath.Ext(name))] != "" })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	items := make([]MediaItem, 0, len(files))
	for _, f := range files {
		kind := inputKinds[strings.ToLower(filepath.Ext(f.name))]
		meta := readJSONObject(sidecarFor(f.path, true))
		probe := c.cachedProbe(f.path, true, meta)
		item := MediaItem{
			Name: f.name, Kind: kind, Size: f.size, Mtime: float64(f.mtime.UnixNano()) / 1e9,
			Probe: probe, URL: mediaURL(session, "inputs/"+f.name),
		}
		if (kind == "video" || kind == "audio") && probe.Duration > 0 {
			d := probe.Duration
			item.Duration = &d
		}
		if kind == "video" {
			item.Thumb = thumbURL(session, "inputs/"+f.name, item.Mtime)
		}
		items = append(items, item)
	}
	return items
}

// ListTakes lists rendered takes (newest first) from outputs/*.mp4 and their
// sidecars — the sidecar is the only record of a take.
func (c *Config) ListTakes(session string, limit int) []MediaItem {
	return c.listVideos(session, "outputs", limit)
}

func (c *Config) ListTimeline(session string) []MediaItem {
	return c.listVideos(session, "timeline", 0)
}

func (c *Config) listVideos(session, sub string, limit int) []MediaItem {
	dir, err := c.SessionSubdir(session, sub)
	if err != nil {
		return []MediaItem{}
	}
	files := statDir(dir, func(name string) bool { return strings.ToLower(filepath.Ext(name)) == ".mp4" })
	items := make([]MediaItem, 0, len(files))
	for _, f := range files {
		meta := readJSONObject(sidecarFor(f.path, false))
		probe := c.cachedProbe(f.path, false, meta)
		mtime := float64(f.mtime.UnixNano()) / 1e9
		items = append(items, MediaItem{
			Name: f.name, Kind: "video", Size: f.size, Mtime: mtime, Probe: probe, Meta: meta,
			URL: mediaURL(session, sub+"/"+f.name), Thumb: thumbURL(session, sub+"/"+f.name, mtime),
		})
		if limit > 0 && len(items) == limit {
			break
		}
	}
	return items
}

// Thumbnail returns a cached 320px JPEG poster for a video, generating it on
// demand into .thumbs/ next to the video.
func (c *Config) Thumbnail(src string) (string, error) {
	info, err := os.Stat(src)
	if err != nil || info.IsDir() {
		return "", os.ErrNotExist
	}
	thumb := filepath.Join(filepath.Dir(src), ".thumbs", filepath.Base(src)+".jpg")
	if t, err := os.Stat(thumb); err == nil && !t.ModTime().Before(info.ModTime()) {
		return thumb, nil
	}
	if err := os.MkdirAll(filepath.Dir(thumb), 0o755); err != nil {
		return "", err
	}
	tmp := thumb + ".tmp.jpg"
	seek := "0.1"
	if err := c.ffmpeg(30*time.Second, "-ss", seek, "-i", src, "-frames:v", "1", "-vf", "scale=320:-2", "-q:v", "4", tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, thumb); err != nil {
		return "", err
	}
	return thumb, nil
}

// ExtractFrame saves the first or last frame of a take (or timeline result)
// into inputs/ and returns the new input's name.
func (c *Config) ExtractFrame(session, kind, name, position string) (string, error) {
	if kind != "timeline" {
		kind = "outputs"
	}
	src, err := c.MediaPath(session, kind+"/"+filepath.Base(name))
	if err != nil || !FileExists(src) {
		return "", os.ErrNotExist
	}
	inputs, err := c.SessionSubdir(session, "inputs")
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	if position != "first" {
		position = "last"
	}
	dst := filepath.Join(inputs, base+"-"+position+"frame.png")
	if position == "first" {
		err = c.ffmpeg(2*time.Minute, "-i", src, "-frames:v", "1", dst)
	} else {
		err = c.ffmpeg(2*time.Minute, "-sseof", "-0.1", "-i", src, "-frames:v", "1", "-update", "1", dst)
	}
	if err != nil {
		return "", err
	}
	return filepath.Base(dst), nil
}

// ExtractAudio saves a take's audio track into inputs/ as WAV.
func (c *Config) ExtractAudio(session, kind, name string) (string, error) {
	if kind != "timeline" {
		kind = "outputs"
	}
	src, err := c.MediaPath(session, kind+"/"+filepath.Base(name))
	if err != nil || !FileExists(src) {
		return "", os.ErrNotExist
	}
	inputs, err := c.SessionSubdir(session, "inputs")
	if err != nil {
		return "", err
	}
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dst := uniquePath(inputs, base+"-audio", ".wav")
	if err := c.ffmpeg(2*time.Minute, "-i", src, "-vn", "-ac", "2", dst); err != nil {
		return "", err
	}
	return filepath.Base(dst), nil
}

// ImportAsInput copies a take or timeline result into inputs/ so it can be
// attached as a reference — renders only ever read references from inputs/.
func (c *Config) ImportAsInput(session, kind, name string) (string, error) {
	if kind != "timeline" {
		kind = "outputs"
	}
	src, err := c.MediaPath(session, kind+"/"+filepath.Base(name))
	if err != nil || !FileExists(src) {
		return "", os.ErrNotExist
	}
	inputs, err := c.SessionSubdir(session, "inputs")
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(src)
	dst := uniquePath(inputs, strings.TrimSuffix(filepath.Base(src), ext), ext)
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return filepath.Base(dst), nil
}

// TrimMedia cuts [start, start+length) out of an input into a new input.
func (c *Config) TrimMedia(session, name string, start, length float64) (string, error) {
	src, err := c.InputPath(session, name)
	if err != nil || !FileExists(src) {
		return "", os.ErrNotExist
	}
	start = max(start, 0)
	ext := filepath.Ext(src)
	dst := uniquePath(filepath.Dir(src), strings.TrimSuffix(filepath.Base(src), ext)+"-trim", ext)
	// -ss after -i decodes up to the seek point, so the cut lands exactly
	// where asked instead of snapping to the preceding sync point.
	if err := c.ffmpeg(5*time.Minute, "-i", src, "-ss", fmt.Sprintf("%.2f", start), "-t", fmt.Sprintf("%.2f", length), "-c", "copy", dst); err != nil {
		return "", err
	}
	return filepath.Base(dst), nil
}

// SaveUpload streams a request body into inputs/ under a sanitized name and
// records the original name in the input's sidecar.
func (c *Config) SaveUpload(session, original string, body io.Reader) (string, error) {
	inputs, err := c.SessionSubdir(session, "inputs")
	if err != nil {
		return "", err
	}
	original = filepath.Base(original)
	ext := strings.ToLower(filepath.Ext(original))
	if inputKinds[ext] == "" {
		return "", fmt.Errorf("unsupported file type %q", ext)
	}
	stem := safeStem(strings.TrimSuffix(original, filepath.Ext(original)))
	tmp, err := os.CreateTemp(inputs, ".upload-*"+ext)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(tmp, body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && n == 0 {
		err = errors.New("empty upload")
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	dst := uniquePath(inputs, stem, ext)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	_ = writeJSONAtomic(sidecarFor(dst, true), map[string]any{"original_name": original})
	return filepath.Base(dst), nil
}

// mp4Finalized reports whether an MP4's top-level boxes are complete and
// include moov — i.e. the muxer has finished writing it.
func mp4Finalized(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	var offset int64
	moov := false
	header := make([]byte, 16)
	for offset < size {
		if _, err := f.ReadAt(header[:8], offset); err != nil {
			return false
		}
		boxSize := int64(binary.BigEndian.Uint32(header[:4]))
		boxType := string(header[4:8])
		switch boxSize {
		case 0:
			boxSize = size - offset
		case 1:
			if _, err := f.ReadAt(header[8:16], offset+8); err != nil {
				return false
			}
			boxSize = int64(binary.BigEndian.Uint64(header[8:16]))
		}
		if boxSize < 8 || offset+boxSize > size {
			return false
		}
		if boxType == "moov" {
			moov = true
		}
		offset += boxSize
	}
	return moov && offset == size
}
