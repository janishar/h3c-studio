package server

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// previewFrame is one decoded --show frame reassembled from Kitty chunks.
type previewFrame struct {
	Data       string // base64 RGB24 payload
	Width      int
	Height     int
	Step       int
	Total      int
	FrameIndex int
	FrameTotal int
}

// h3PreviewState tracks Kitty graphics-protocol parsing across lines.
type h3PreviewState struct {
	inPreview  bool
	width      int
	height     int
	chunk      []byte
	step       int
	total      int
	frameIndex int
	frameTotal int
}

func newH3PreviewState() *h3PreviewState {
	return &h3PreviewState{step: -1, total: -1, frameIndex: -1, frameTotal: -1}
}

func (s *h3PreviewState) reset() {
	*s = h3PreviewState{step: -1, total: -1, frameIndex: -1, frameTotal: -1}
}

// feed consumes one output line and returns a completed frame when the line
// finished one. It understands both preview status formats:
// interactive "h3: preview N/T, frame M/F" and one-shot
// "h3: denoise preview N/T, video frame M/F via <protocol>".
func (s *h3PreviewState) feed(line string) (previewFrame, bool) {
	const gStart, gTerm = "\033_G", "\033\\"
	if idx := strings.Index(line, gStart); idx >= 0 {
		rest := line[idx+len(gStart):]
		if attrEnd := strings.Index(rest, ";"); attrEnd > 0 {
			attrs := rest[:attrEnd]
			if v, err := strconv.Atoi(findKV(attrs, "s=")); err == nil && v > 0 && s.width == 0 {
				s.width = v
			}
			if v, err := strconv.Atoi(findKV(attrs, "v=")); err == nil && v > 0 && s.height == 0 {
				s.height = v
			}
			if s.step > 0 && s.total > 0 {
				s.inPreview = true
			}
		}
	}
	if !s.inPreview && strings.HasPrefix(line, "h3: ") {
		if pIdx := strings.Index(line, "preview "); pIdx >= 0 {
			if n, t, ok := parseFraction(line[pIdx+len("preview "):]); ok {
				s.step, s.total = n, t
			}
			if fIdx := strings.Index(line, "frame "); fIdx >= 0 {
				if n, t, ok := parseFraction(line[fIdx+len("frame "):]); ok {
					s.frameIndex, s.frameTotal = n, t
				}
			}
		}
	}
	if !s.inPreview {
		return previewFrame{}, false
	}
	// h3.c writes every chunk of a frame back-to-back with only one trailing
	// newline, so one line usually carries dozens of chunks.
	cursor := 0
	for {
		relStart := strings.Index(line[cursor:], gStart)
		if relStart < 0 {
			break
		}
		segStart := cursor + relStart
		rest := line[segStart+len(gStart):]
		semi := strings.Index(rest, ";")
		if semi < 0 {
			break
		}
		relTerm := strings.Index(rest[semi+1:], gTerm)
		if relTerm < 0 {
			break
		}
		attrs := rest[:semi]
		s.chunk = append(s.chunk, strings.TrimRight(rest[semi+1:semi+1+relTerm], " \t\r\n")...)
		cursor = segStart + len(gStart) + semi + 1 + relTerm + len(gTerm)
		if more := findKV(attrs, "m="); more == "" || more == "0" {
			frame := previewFrame{
				Data: string(s.chunk), Width: s.width, Height: s.height,
				Step: s.step, Total: s.total, FrameIndex: s.frameIndex, FrameTotal: s.frameTotal,
			}
			s.reset()
			return frame, frame.Data != "" && frame.Width > 0 && frame.Height > 0
		}
	}
	return previewFrame{}, false
}

// parseFraction reads a leading "N/T" token.
func parseFraction(text string) (int, int, bool) {
	end := strings.IndexAny(text, ", \t")
	if end < 0 {
		end = len(text)
	}
	num, den, ok := strings.Cut(text[:end], "/")
	if !ok {
		return 0, 0, false
	}
	n, err1 := strconv.Atoi(num)
	t, err2 := strconv.Atoi(den)
	return n, t, err1 == nil && err2 == nil
}

func findKV(attrs, prefix string) string {
	for _, part := range strings.Split(attrs, ",") {
		if strings.HasPrefix(part, prefix) {
			return part[len(prefix):]
		}
	}
	return ""
}

// kittyPayload reports whether a line carries Kitty image data, and returns
// any ordinary text printed before the escape sequence.
func kittyPayload(line string) (string, bool) {
	idx := strings.Index(line, "\033_G")
	if idx < 0 {
		return line, false
	}
	return strings.TrimSpace(line[:idx]), true
}

// writePreviewPNG decodes the RGB24 payload (h3_terminal.c's f=24 transfer,
// not a PNG) and writes it as a PNG.
func writePreviewPNG(path string, frame previewFrame) error {
	raw, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		return err
	}
	w, h := frame.Width, frame.Height
	if len(raw) < w*h*3 {
		return errors.New("short preview payload")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	pix := img.Pix
	for i, o := 0, 0; i < w*h; i, o = i+1, o+3 {
		p := i * 4
		pix[p], pix[p+1], pix[p+2], pix[p+3] = raw[o], raw[o+1], raw[o+2], 255
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	buf := bufio.NewWriter(f)
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(buf, img); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := buf.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

var previewNameRE = regexp.MustCompile(`^s(\d+)of(\d+)(?:-f(\d+)of(\d+))?\.png$`)

func previewFileName(frame previewFrame) string {
	name := fmt.Sprintf("s%03dof%03d", max(frame.Step, 0), max(frame.Total, 0))
	if frame.FrameTotal > 0 {
		name += fmt.Sprintf("-f%03dof%03d", max(frame.FrameIndex, 0), frame.FrameTotal)
	}
	return name + ".png"
}

// listPreviewFiles returns preview PNG names in step, then frame order.
func listPreviewFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		name        string
		step, frame int
	}
	items := []item{}
	for _, entry := range entries {
		if m := previewNameRE.FindStringSubmatch(entry.Name()); m != nil {
			step, _ := strconv.Atoi(m[1])
			frame, _ := strconv.Atoi(m[3])
			items = append(items, item{entry.Name(), step, frame})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].step != items[j].step {
			return items[i].step < items[j].step
		}
		return items[i].frame < items[j].frame
	})
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.name
	}
	return names
}

// prunePreviews keeps every frame of the final step and one frame (the
// middle one) of each earlier step, so an "all frames" render doesn't keep
// hundreds of megabytes of previews once the take is done.
func prunePreviews(dir string) []string {
	names := listPreviewFiles(dir)
	byStep := map[int][]string{}
	lastStep := -1
	for _, name := range names {
		m := previewNameRE.FindStringSubmatch(name)
		step, _ := strconv.Atoi(m[1])
		byStep[step] = append(byStep[step], name)
		lastStep = max(lastStep, step)
	}
	kept := []string{}
	for _, name := range names {
		m := previewNameRE.FindStringSubmatch(name)
		step, _ := strconv.Atoi(m[1])
		group := byStep[step]
		if step == lastStep || len(group) == 1 || group[len(group)/2] == name {
			kept = append(kept, name)
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
	return kept
}
