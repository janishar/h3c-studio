package server

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// h3 prints progress as "\r%-25s %4d/%-4d" (phase, completed, total).
	progressRE = regexp.MustCompile(`^(\S.*?)\s+(\d+)/(\d+)\s*$`)
	profileRE  = regexp.MustCompile(`^h3 profile:\s+(.*?)\s{2,}(\S.*?)\s+wall=\s*([\d.]+)s`)
	doneRE     = regexp.MustCompile(`Done -> (.+?) \[`)
)

// Stages shown by the UI's stepper, in order.
var stageOrder = []string{"load", "encode", "denoise", "decode", "mp4"}

// phaseStages maps h3.c's progress phase names (h3.c h3_progress_emit and
// h3_dit.c report calls) onto the stepper stages.
var phaseStages = map[string]string{
	"tokenizer":             "load",
	"load transformer core": "load",
	"preview VAE load":      "load",
	"text encoder":          "encode",
	"Qwen vision":           "encode",
	"video VAE encoder":     "encode",
	"audio VAE encoder":     "encode",
	"refine text":           "encode",
	"precompute AdaLN":      "encode",
	"denoise enqueue":       "denoise",
	"denoise":               "denoise",
	"audio VAE":             "decode",
	"video VAE load":        "decode",
	"FFmpeg":                "mp4",
}

// stageFor returns the stepper stage for a phase, or "" when unknown.
func stageFor(phase string) string { return phaseStages[strings.TrimSpace(phase)] }

// parseProgress recognizes an h3 progress line. Lines from h3's own
// diagnostics ("h3: …") never count as progress.
func parseProgress(line string) (phase string, n, total int, ok bool) {
	if strings.HasPrefix(line, "h3") {
		return "", 0, 0, false
	}
	m := progressRE.FindStringSubmatch(line)
	if m == nil {
		return "", 0, 0, false
	}
	n, _ = strconv.Atoi(m[2])
	total, _ = strconv.Atoi(m[3])
	if total <= 0 {
		return "", 0, 0, false
	}
	return strings.TrimSpace(m[1]), n, total, true
}

// ProfileRow is one "h3 profile:" timing line.
type ProfileRow struct {
	Component string  `json:"component"`
	Stage     string  `json:"stage"`
	Wall      float64 `json:"wall"`
}

func parseProfile(line string) (ProfileRow, bool) {
	if !strings.HasPrefix(line, "h3 profile:") {
		return ProfileRow{}, false
	}
	m := profileRE.FindStringSubmatch(line)
	if m == nil {
		return ProfileRow{}, false
	}
	wall, _ := strconv.ParseFloat(m[3], 64)
	return ProfileRow{Component: strings.TrimSpace(m[1]), Stage: strings.TrimSpace(m[2]), Wall: wall}, true
}

type failureHint struct {
	re   *regexp.Regexp
	hint string
}

// failureHints turn known failure output into advice. They only explain:
// the studio never changes driver or h3 settings on the user's behalf.
var failureHints = []failureHint{
	{regexp.MustCompile(`Impacting ?Interactivity`),
		"macOS's GPU watchdog stopped long-running GPU work while the display was active. " +
			"Known workarounds: let the display sleep during the render, or start the studio with " +
			"AGX_RELAX_CDM_CTXSTORE_TIMEOUT=1 in its environment (the UI may feel less responsive). " +
			"h3 studio never sets this for you."},
	{regexp.MustCompile(`(?i)out of memory|insufficient memory|OutOfMemory|failed to allocate|cannot allocate`),
		"The GPU or system ran out of memory. Try a smaller canvas, fewer frames, Internal render 75%, " +
			"token reduction, or Stream DiT from disk."},
	{regexp.MustCompile(`(?i)ffmpeg|ffprobe`),
		"FFmpeg or FFprobe failed or wasn't found. Install it with `brew install ffmpeg`, or point " +
			"H3_FFMPEG / H3_FFPROBE at the executables."},
	{regexp.MustCompile(`FL2VA/|Ref2VA/|model\.safetensors|safetensors|config\.json`),
		"The model directory is missing files h3 needs. Open the Model dialog: FL2VA/ is required, " +
			"and Ref2VA/ is needed for references."},
	{regexp.MustCompile(`(?i)no such file or directory|cannot open|cannot read`),
		"A file h3 needed couldn't be read — check that references and anchors still exist in this session's inputs."},
}

// hintFor returns advice for the first matching line (last lines first).
func hintFor(tail []string) string {
	for _, fh := range failureHints {
		for i := len(tail) - 1; i >= 0; i-- {
			if _, _, _, progress := parseProgress(tail[i]); progress || strings.HasPrefix(tail[i], "h3 profile:") {
				continue
			}
			if fh.re.MatchString(tail[i]) {
				return fh.hint
			}
		}
	}
	return ""
}

// lastMeaningfulLine picks the error to show for a failed render: the last
// "h3: " line if there is one, else the last non-progress line.
func lastMeaningfulLine(tail []string) string {
	for i := len(tail) - 1; i >= 0; i-- {
		line := strings.TrimSpace(tail[i])
		if strings.HasPrefix(line, "h3: ") && !strings.Contains(line, "preview") && !strings.Contains(line, "cache") {
			return line
		}
	}
	for i := len(tail) - 1; i >= 0; i-- {
		line := strings.TrimSpace(tail[i])
		if _, _, _, ok := parseProgress(line); line != "" && !ok && !strings.HasPrefix(line, "h3 profile:") {
			return line
		}
	}
	return ""
}
