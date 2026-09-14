package server

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

	envKeyRE     = regexp.MustCompile(`^H3_[A-Z0-9_]{1,64}$`)
	extraFlagRE  = regexp.MustCompile(`^--use-[a-z0-9-]+$`)
	extraValues  = map[string][]string{"--ref-image-size": {"match", "max"}}
	ownedFlagSet = map[string]bool{"--use-int8-row-fc2": true}
)

// Ref is one ordered Ref2VA reference from the UI.
type Ref struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Mode        string   `json:"mode,omitempty"`
	PairedAudio string   `json:"pairedAudio,omitempty"`
	Duration    *float64 `json:"duration,omitempty"`
}

// RenderParams is the render request the UI sends (and setting.json stores).
type RenderParams struct {
	Session          string            `json:"session_name"`
	Label            string            `json:"label"`
	Mode             string            `json:"mode"` // "ref" | "anchor" | "text"
	Prompt           string            `json:"prompt"`
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	RenderWidth      int               `json:"render_width"`
	RenderHeight     int               `json:"render_height"`
	Frames           int               `json:"frames"`
	Steps            int               `json:"steps"`
	Layers           int               `json:"layers"`
	Reuse            int               `json:"reuse"`
	CoreReuse        int               `json:"core_reuse"`
	Seed             int64             `json:"seed"`
	TokenReduction   bool              `json:"token_reduction"`
	Int8RowFC2       bool              `json:"int8_row_fc2"`
	SSDStreaming     bool              `json:"ssd_streaming"`
	RunMode          string            `json:"run_mode"`
	Preview          *bool             `json:"preview"`
	PreviewAllFrames bool              `json:"previewAllFrames"`
	Refs             []Ref             `json:"refs"`
	FirstFrame       string            `json:"first_frame"`
	LastFrame        string            `json:"last_frame"`
	Env              map[string]string `json:"env"`
	ExtraArgs        []string          `json:"extra_args"`
}

// decodeParams decodes a raw request object into typed params, keeping the
// raw object too so UI-only keys (prompt_doc, mode, …) survive into sidecars.
func decodeParams(raw map[string]any) (RenderParams, error) {
	var p RenderParams
	data, err := json.Marshal(raw)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, fmt.Errorf("bad render parameters: %w", err)
	}
	p.normalize()
	return p, nil
}

func (p *RenderParams) normalize() {
	p.Frames = snapFrames(p.Frames)
	if p.Reuse == 0 {
		p.Reuse = 1
	}
	if p.Steps == 0 {
		p.Steps = 4
	}
	if p.Layers == 0 {
		p.Layers = 50
	}
	if p.RunMode != "interactive" {
		p.RunMode = "oneshot"
	}
	switch {
	case p.Mode == "ref" || p.Mode == "anchor" || p.Mode == "text":
	case len(p.Refs) > 0:
		p.Mode = "ref"
	case p.FirstFrame != "" || p.LastFrame != "":
		p.Mode = "anchor"
	default:
		p.Mode = "text"
	}
	// Each mode only sends its own conditioning, whatever else the form holds.
	if p.Mode != "ref" {
		p.Refs = nil
	}
	if p.Mode != "anchor" {
		p.FirstFrame, p.LastFrame = "", ""
	}
	for i := range p.Refs {
		if p.Refs[i].Kind == "video" && p.Refs[i].Mode == "" {
			p.Refs[i].Mode = "keep"
		}
	}
}

func (p RenderParams) PreviewOn() bool { return p.Preview == nil || *p.Preview }

func snapFrames(requested int) int {
	for _, frame := range legalFrames {
		if frame >= requested {
			return frame
		}
	}
	return legalFrames[len(legalFrames)-1]
}

// ModelCaps is what the configured checkpoint can run.
type ModelCaps struct {
	HasFL2VA  bool
	HasRef2VA bool
}

// Validate returns human-readable problems with a render request. exists
// reports whether a bare input name is present in the session's inputs.
func (p RenderParams) Validate(caps ModelCaps, exists func(string) bool) []string {
	errs := []string{}
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	w, h := p.Width, p.Height
	if w%32 != 0 || h%32 != 0 {
		add("Width and height must be multiples of 32.")
	}
	if w < 32 || h < 32 {
		add("Width and height must be at least 32.")
	}
	if w*h > maxPixels {
		add("%dx%d is %s pixels; the ceiling is %s.", w, h, comma(w*h), comma(maxPixels))
	}
	if (p.RenderWidth == 0) != (p.RenderHeight == 0) || p.RenderWidth < 0 || p.RenderHeight < 0 ||
		p.RenderWidth%32 != 0 || p.RenderHeight%32 != 0 {
		add("Internal render size must be a pair of multiples of 32.")
	}
	if strings.TrimSpace(p.Prompt) == "" {
		add("Write a prompt.")
	}
	if p.Steps < 2 || p.Steps > 1000 {
		add("Steps must be between 2 and 1000.")
	}
	if p.Layers < 35 || p.Layers > 50 {
		add("Layers must be between 35 and 50.")
	}
	if p.Reuse < 1 || p.Reuse > 3 {
		add("Reuse must be 1, 2 or 3.")
	}
	if p.CoreReuse < 0 || p.CoreReuse > 16 {
		add("Core reuse must be between 0 and 16.")
	}
	if !caps.HasFL2VA {
		add("The model directory has no FL2VA/ pipeline, which every render needs.")
	}

	checkInput := func(label, name string) {
		if name == "" {
			return
		}
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			add("%s has an invalid file name.", label)
		} else if exists != nil && !exists(name) {
			add("%s %s is not in this session's inputs.", label, name)
		}
	}

	images, videos, audio := 0, 0, 0
	videoDuration, audioDuration := 0.0, 0.0
	for _, ref := range p.Refs {
		switch ref.Kind {
		case "image":
			images++
		case "video":
			videos++
			if ref.Duration != nil {
				videoDuration += *ref.Duration
			}
			if ref.Mode == "replace" {
				if ref.PairedAudio == "" {
					add("Replacement audio is required for videos in replace mode.")
				}
				checkInput("Replacement audio", ref.PairedAudio)
			}
		case "audio":
			audio++
			if ref.Duration != nil {
				audioDuration += *ref.Duration
			}
		default:
			add("Each reference must be an image, video or audio file.")
			continue
		}
		checkInput("Reference", ref.Name)
		if (ref.Kind == "video" || ref.Kind == "audio") && ref.Duration != nil && *ref.Duration != 0 &&
			(*ref.Duration < 2 || *ref.Duration > 15) {
			add("%s must be between 2 and 15 seconds.", ref.Name)
		}
	}
	switch p.Mode {
	case "ref":
		if len(p.Refs) == 0 {
			add("Reference mode needs at least one reference.")
		}
		if len(p.Refs) > 0 && !caps.HasRef2VA {
			add("References need the Ref2VA pipeline, which the model directory doesn't have.")
		}
	case "anchor":
		if p.FirstFrame == "" && p.LastFrame == "" {
			add("Set a first or last frame, or switch to Prompt mode.")
		}
	}
	checkInput("First frame", p.FirstFrame)
	checkInput("Last frame", p.LastFrame)
	if audio > 0 && images == 0 && videos == 0 {
		add("A standalone audio reference must accompany an image or video.")
	}
	if images > 9 {
		add("At most 9 image references.")
	}
	if videos > 3 {
		add("At most 3 video references.")
	}
	if audio > 3 {
		add("At most 3 audio references.")
	}
	if videoDuration > 15 {
		add("Combined video reference duration is %.1fs; the limit is 15s.", videoDuration)
	}
	if audioDuration > 15 {
		add("Combined audio reference duration is %.1fs; the limit is 15s.", audioDuration)
	}
	if p.RunMode == "interactive" && (videos > 0 || audio > 0) {
		add("Interactive h3.c supports image references only; use One-shot for video or audio references.")
	}
	for key, value := range p.Env {
		if !envKeyRE.MatchString(key) {
			add("Environment variable %q is not allowed (only H3_* variables).", key)
		} else if len(value) > 256 || strings.ContainsAny(value, "\x00\n\r") {
			add("Environment variable %s has an invalid value.", key)
		}
	}
	if err := checkExtraArgs(p.ExtraArgs); err != nil {
		add("%s", err.Error())
	}
	return errs
}

// checkExtraArgs allows only h3 flags that can't redirect files or override
// settings the form owns: boolean --use-* switches and --ref-image-size.
func checkExtraArgs(args []string) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if allowed, ok := extraValues[arg]; ok {
			if i+1 >= len(args) || !contains(allowed, args[i+1]) {
				return fmt.Errorf("extra argument %s needs one of: %s", arg, strings.Join(allowed, ", "))
			}
			i++
			continue
		}
		if !extraFlagRE.MatchString(arg) || ownedFlagSet[arg] {
			return fmt.Errorf("extra argument %q is not allowed; only --use-* switches and --ref-image-size are accepted", arg)
		}
	}
	return nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// BuildOneShotArgs returns the h3 argv (without the binary) for a one-shot
// render. inputs is the session's inputs directory.
func BuildOneShotArgs(p RenderParams, model, inputs, output string) []string {
	args := []string{"--profile", "-d", model, "-p", p.Prompt}
	if p.PreviewOn() {
		args = append(args, "--show", "--preview-mode", "estimate")
		if p.PreviewAllFrames {
			// h3.c clamps to whatever the middle chunk can supply, so a large
			// sentinel requests "as many as possible".
			args = append(args, "--preview-frames", "999")
		}
	}
	for _, ref := range p.Refs {
		path := filepath.Join(inputs, ref.Name)
		switch {
		case ref.Kind == "image":
			args = append(args, "--ref-image", path)
		case ref.Kind == "audio":
			args = append(args, "--ref-audio", path)
		case ref.Mode == "silent":
			args = append(args, "--ref-silent-video", path)
		case ref.Mode == "replace" && ref.PairedAudio != "":
			args = append(args, "--ref-video-audio", path, filepath.Join(inputs, ref.PairedAudio))
		default:
			args = append(args, "--ref-video", path)
		}
	}
	if p.FirstFrame != "" {
		args = append(args, "--first-frame", filepath.Join(inputs, p.FirstFrame))
	}
	if p.LastFrame != "" {
		args = append(args, "--last-frame", filepath.Join(inputs, p.LastFrame))
	}
	args = append(args, "--width", strconv.Itoa(p.Width), "--height", strconv.Itoa(p.Height))
	if p.RenderWidth != 0 && p.RenderHeight != 0 {
		args = append(args, "--render-width", strconv.Itoa(p.RenderWidth), "--render-height", strconv.Itoa(p.RenderHeight))
	}
	args = append(args, "--frames", strconv.Itoa(p.Frames), "--steps", strconv.Itoa(p.Steps), "--layers", strconv.Itoa(p.Layers))
	if p.CoreReuse != 0 {
		args = append(args, "--core-reuse", strconv.Itoa(p.CoreReuse))
	} else {
		args = append(args, "--reuse", strconv.Itoa(p.Reuse))
	}
	if p.TokenReduction {
		args = append(args, "--token-reduction")
	}
	if p.SSDStreaming {
		args = append(args, "--ssd-streaming")
	} else if p.Int8RowFC2 {
		args = append(args, "--use-int8-row-fc2")
	}
	args = append(args, p.ExtraArgs...)
	return append(args, "--seed", strconv.FormatInt(p.Seed, 10), "-o", output)
}

// BuildInteractiveLaunchArgs is the argv that starts a resident h3 REPL.
func BuildInteractiveLaunchArgs(p RenderParams, model string) []string {
	args := []string{"--profile", "-d", model, "--width", strconv.Itoa(p.Width), "--height", strconv.Itoa(p.Height)}
	return append(args, p.ExtraArgs...)
}

// BuildInteractiveCommands returns the REPL commands that configure one
// render, ending with the prompt itself. outputs is where h3 should write.
func BuildInteractiveCommands(p RenderParams, inputs, outputs string) []string {
	commands := []string{
		"!output " + outputs,
		fmt.Sprintf("!size %dx%d", p.Width, p.Height),
		fmt.Sprintf("!frames %d", p.Frames),
		fmt.Sprintf("!steps %d", p.Steps),
		fmt.Sprintf("!layers %d", p.Layers),
	}
	if p.CoreReuse != 0 {
		commands = append(commands, fmt.Sprintf("!core-reuse %d", p.CoreReuse))
	} else {
		commands = append(commands, fmt.Sprintf("!reuse %d", p.Reuse))
	}
	commands = append(commands, fmt.Sprintf("!seed %d", p.Seed))
	if p.PreviewOn() {
		commands = append(commands, "!show on", "!preview-mode estimate")
	} else {
		commands = append(commands, "!show off")
	}
	if p.RenderWidth != 0 && p.RenderHeight != 0 {
		commands = append(commands, fmt.Sprintf("!render-size %dx%d", p.RenderWidth, p.RenderHeight))
	} else {
		commands = append(commands, "!render-size native")
	}
	commands = append(commands,
		"!token-reduction "+onOff(p.TokenReduction),
		"!ssd-streaming "+onOff(p.SSDStreaming),
		"!int8-row-fc2 "+onOff(p.Int8RowFC2 && !p.SSDStreaming),
		"!refs clear",
		"!first clear",
		"!last clear",
	)
	for _, ref := range p.Refs {
		commands = append(commands, "!ref-image "+filepath.Join(inputs, ref.Name))
	}
	if p.FirstFrame != "" {
		commands = append(commands, "!first "+filepath.Join(inputs, p.FirstFrame))
	}
	if p.LastFrame != "" {
		commands = append(commands, "!last "+filepath.Join(inputs, p.LastFrame))
	}
	return append(commands, flattenLine(p.Prompt))
}

// envFor returns the allowlisted environment entries for a render.
func envFor(p RenderParams) []string {
	env := []string{}
	for key, value := range p.Env {
		if envKeyRE.MatchString(key) && value != "" {
			env = append(env, key+"="+value)
		}
	}
	if p.PreviewOn() {
		// h3.c only emits --show frames for a terminal it recognizes; claim Kitty.
		env = append(env, "KITTY_WINDOW_ID=1")
	}
	return env
}

// flattenLine joins a multi-line prompt into one REPL line: h3 reads stdin a
// line at a time and treats a blank line as "repeat the last command".
func flattenLine(text string) string { return strings.Join(strings.Fields(text), " ") }

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

var shellSafeRE = regexp.MustCompile(`^[A-Za-z0-9_./:=@+,-]+$`)

func shellQuote(arg string) string {
	if shellSafeRE.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// displayCommand renders env + argv as a copy-pasteable shell line.
func displayCommand(env []string, argv []string) string {
	parts := make([]string, 0, len(env)+len(argv))
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		parts = append(parts, key+"="+shellQuote(value))
	}
	for _, arg := range argv {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}
