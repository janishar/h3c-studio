package server

import (
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func newTestConfig(t *testing.T) *Config {
	t.Helper()
	root := t.TempDir()
	cfg, err := NewConfig(Options{H3: "/bin/echo", Model: root, Root: root, Host: "127.0.0.1", Port: 8710, Static: os.DirFS(root)})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSafeStem(t *testing.T) {
	cases := map[string]string{
		"My Shot 01":   "my-shot-01",
		"../../etc":    "etc",
		"  ":           "take",
		"..":           "take",
		"a/b\\c":       "a-b-c",
		"UPPER.case_x": "upper.case_x",
	}
	for in, want := range cases {
		if got := safeStem(in); got != want {
			t.Errorf("safeStem(%q) = %q, want %q", in, got, want)
		}
	}
	if got := safeStem(strings.Repeat("x", 80)); len(got) > 48 {
		t.Errorf("safeStem length = %d, want <= 48", len(got))
	}
}

func TestSnapFrames(t *testing.T) {
	for in, want := range map[int]int{0: 5, 5: 5, 6: 22, 22: 22, 124: 124, 125: 141, 10000: 362} {
		if got := snapFrames(in); got != want {
			t.Errorf("snapFrames(%d) = %d, want %d", in, got, want)
		}
	}
}

func validParams() RenderParams {
	p := RenderParams{
		Session: "s", Prompt: "a shot", Width: 512, Height: 512, Frames: 22, Steps: 4, Layers: 50, Reuse: 1,
		Seed: 7, Mode: "text", Env: map[string]string{"H3_ZERO_COPY_WEIGHTS": "0"},
	}
	p.normalize()
	return p
}

func TestValidate(t *testing.T) {
	caps := ModelCaps{HasFL2VA: true, HasRef2VA: true}
	exists := func(name string) bool { return name == "a.png" || name == "clip.mp4" || name == "voice.wav" }
	if errs := validParams().Validate(caps, exists); len(errs) != 0 {
		t.Fatalf("valid params rejected: %v", errs)
	}
	dur := func(v float64) *float64 { return &v }
	cases := []struct {
		name   string
		mutate func(*RenderParams)
		caps   ModelCaps
		want   string
	}{
		{"grid", func(p *RenderParams) { p.Width = 500 }, caps, "multiples of 32"},
		{"ceiling", func(p *RenderParams) { p.Width, p.Height = 1024, 1024 }, caps, "ceiling"},
		{"prompt", func(p *RenderParams) { p.Prompt = " " }, caps, "Write a prompt"},
		{"no fl2va", func(p *RenderParams) {}, ModelCaps{}, "FL2VA"},
		{"no ref2va", func(p *RenderParams) { p.Mode = "ref"; p.Refs = []Ref{{Name: "a.png", Kind: "image"}} }, ModelCaps{HasFL2VA: true}, "Ref2VA"},
		{"missing input", func(p *RenderParams) { p.Mode = "anchor"; p.FirstFrame = "nope.png" }, caps, "not in this session's inputs"},
		{"traversal", func(p *RenderParams) { p.Mode = "anchor"; p.FirstFrame = "../outputs/x.png" }, caps, "invalid file name"},
		{"anchor empty", func(p *RenderParams) { p.Mode = "anchor" }, caps, "first or last frame"},
		{"ref empty", func(p *RenderParams) { p.Mode = "ref" }, caps, "at least one reference"},
		{"audio alone", func(p *RenderParams) {
			p.Mode = "ref"
			p.Refs = []Ref{{Name: "voice.wav", Kind: "audio", Duration: dur(5)}}
		}, caps, "must accompany"},
		{"video too long", func(p *RenderParams) {
			p.Mode = "ref"
			p.Refs = []Ref{{Name: "clip.mp4", Kind: "video", Duration: dur(20)}}
		}, caps, "between 2 and 15"},
		{"replace needs audio", func(p *RenderParams) {
			p.Mode = "ref"
			p.Refs = []Ref{{Name: "clip.mp4", Kind: "video", Mode: "replace", Duration: dur(5)}}
		}, caps, "Replacement audio"},
		{"interactive video", func(p *RenderParams) {
			p.Mode, p.RunMode = "ref", "interactive"
			p.Refs = []Ref{{Name: "clip.mp4", Kind: "video", Duration: dur(5)}}
		}, caps, "image references only"},
		{"env key", func(p *RenderParams) { p.Env = map[string]string{"DYLD_INSERT_LIBRARIES": "/tmp/x.dylib"} }, caps, "not allowed"},
		{"env value", func(p *RenderParams) { p.Env = map[string]string{"H3_X": "a\nb"} }, caps, "invalid value"},
		{"extra arg", func(p *RenderParams) { p.ExtraArgs = []string{"-o", "/tmp/x.mp4"} }, caps, "not allowed"},
		{"steps", func(p *RenderParams) { p.Steps = 1 }, caps, "Steps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validParams()
			tc.mutate(&p)
			errs := p.Validate(tc.caps, exists)
			if !strings.Contains(strings.Join(errs, "\n"), tc.want) {
				t.Fatalf("errors %q do not mention %q", errs, tc.want)
			}
		})
	}
}

func TestCheckExtraArgs(t *testing.T) {
	ok := [][]string{nil, {"--use-reference-rope"}, {"--ref-image-size", "max", "--use-slower-bf16-mlp"}}
	for _, args := range ok {
		if err := checkExtraArgs(args); err != nil {
			t.Errorf("checkExtraArgs(%q) = %v", args, err)
		}
	}
	bad := [][]string{{"--frames-dir", "/tmp"}, {"--ref-image-size"}, {"--ref-image-size", "huge"}, {"--use-x=1"}, {"--seed", "1"}, {"--use-int8-row-fc2"}}
	for _, args := range bad {
		if err := checkExtraArgs(args); err == nil {
			t.Errorf("checkExtraArgs(%q) accepted", args)
		}
	}
}

func TestNormalizeDropsOtherModesConditioning(t *testing.T) {
	raw := map[string]any{"mode": "anchor", "first_frame": "a.png", "refs": []any{map[string]any{"name": "b.png", "kind": "image"}}, "frames": 30}
	p, err := decodeParams(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Refs != nil || p.FirstFrame != "a.png" || p.Frames != 39 || p.RunMode != "oneshot" {
		t.Fatalf("normalize: %+v", p)
	}
}

func TestBuildOneShotArgs(t *testing.T) {
	p := validParams()
	p.Mode = "ref"
	p.Refs = []Ref{
		{Name: "a.png", Kind: "image"},
		{Name: "clip.mp4", Kind: "video", Mode: "replace", PairedAudio: "voice.wav"},
		{Name: "quiet.mp4", Kind: "video", Mode: "silent"},
		{Name: "voice.wav", Kind: "audio"},
	}
	p.RenderWidth, p.RenderHeight = 384, 384
	p.TokenReduction = true
	p.Int8RowFC2 = true
	p.ExtraArgs = []string{"--use-reference-rope"}
	got := BuildOneShotArgs(p, "/m", "/in", "/out/take.mp4")
	want := []string{
		"--profile", "-d", "/m", "-p", "a shot", "--show", "--preview-mode", "estimate",
		"--ref-image", "/in/a.png", "--ref-video-audio", "/in/clip.mp4", "/in/voice.wav",
		"--ref-silent-video", "/in/quiet.mp4", "--ref-audio", "/in/voice.wav",
		"--width", "512", "--height", "512", "--render-width", "384", "--render-height", "384",
		"--frames", "22", "--steps", "4", "--layers", "50", "--reuse", "1",
		"--token-reduction", "--use-int8-row-fc2", "--use-reference-rope", "--seed", "7", "-o", "/out/take.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv mismatch\n got %q\nwant %q", got, want)
	}

	off := false
	p = validParams()
	p.Mode, p.FirstFrame, p.LastFrame = "anchor", "f.png", "l.png"
	p.Preview = &off
	p.SSDStreaming, p.Int8RowFC2, p.CoreReuse = true, true, 4
	got = BuildOneShotArgs(p, "/m", "/in", "/o.mp4")
	joined := strings.Join(got, " ")
	for _, want := range []string{"--first-frame /in/f.png", "--last-frame /in/l.png", "--core-reuse 4", "--ssd-streaming"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q missing %q", joined, want)
		}
	}
	for _, unwanted := range []string{"--show", "--reuse", "--use-int8-row-fc2"} {
		if strings.Contains(joined, unwanted+" ") {
			t.Errorf("argv %q should not contain %q", joined, unwanted)
		}
	}
}

func TestBuildInteractiveCommands(t *testing.T) {
	p := validParams()
	p.Mode = "ref"
	p.Refs = []Ref{{Name: "a.png", Kind: "image"}}
	p.Prompt = "line one\n\nline two"
	got := BuildInteractiveCommands(p, "/in", "/work")
	if got[0] != "!output /work" {
		t.Fatalf("first command = %q", got[0])
	}
	if last := got[len(got)-1]; last != "line one line two" {
		t.Fatalf("prompt not flattened: %q", last)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"!size 512x512", "!frames 22", "!seed 7", "!show on", "!render-size native", "!refs clear", "!ref-image /in/a.png"} {
		if !strings.Contains(joined, want) {
			t.Errorf("commands missing %q:\n%s", want, joined)
		}
	}
}

func TestEnvFor(t *testing.T) {
	p := validParams()
	p.Env["PATH"] = "/evil"
	env := envFor(p)
	if contains(env, "PATH=/evil") || !contains(env, "H3_ZERO_COPY_WEIGHTS=0") || !contains(env, "KITTY_WINDOW_ID=1") {
		t.Fatalf("envFor = %q", env)
	}
}

func TestShellQuote(t *testing.T) {
	if got := displayCommand([]string{"H3_X=a b"}, []string{"h3", "-p", "it's"}); got != `H3_X='a b' h3 -p 'it'\''s'` {
		t.Fatalf("displayCommand = %s", got)
	}
}

func TestMediaPathStaysInsideSession(t *testing.T) {
	cfg := newTestConfig(t)
	good, err := cfg.MediaPath("s1", "inputs/a.png")
	if err != nil || good != filepath.Join(cfg.Sessions, "s1", "inputs", "a.png") {
		t.Fatalf("MediaPath good = %q, %v", good, err)
	}
	for _, rel := range []string{"../../etc/passwd", "secrets/x", "inputs/../../../x", "inputs/.thumbs/x.jpg", "setting.json", "inputs", "../s2/inputs/a.png"} {
		if abs, err := cfg.MediaPath("s1", rel); err == nil {
			t.Errorf("MediaPath(%q) accepted: %q", rel, abs)
		}
	}
	if dir, err := cfg.SessionDir("../../etc"); err != nil || filepath.Dir(dir) != cfg.Sessions {
		t.Errorf("SessionDir escaped: %q %v", dir, err)
	}
	if _, err := cfg.InputPath("s1", "../x.png"); err == nil {
		t.Error("InputPath accepted a path")
	}
	for _, rel := range []string{"../x", "s1/../../x", ".hidden/x"} {
		if abs, err := cfg.ResolveSessionsPath(rel); err == nil && !strings.HasPrefix(abs, cfg.Sessions) {
			t.Errorf("ResolveSessionsPath(%q) escaped: %q", rel, abs)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	cfg := newTestConfig(t)
	for host, want := range map[string]bool{
		"127.0.0.1:8710": true, "localhost:8710": true, "[::1]:8710": true, "192.168.1.5:8710": true,
		"evil.example:8710": false, "": false,
	} {
		if got := cfg.hostAllowed(host); got != want {
			t.Errorf("hostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestWriteJSONAtomicConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setting.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := writeJSONAtomic(path, map[string]any{"i": i, "pad": strings.Repeat("x", 4096)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if data := readJSONObject(path); data["pad"] == nil {
		t.Fatal("file did not parse after concurrent writes")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %d entries", len(entries))
	}
}

func kittyLine(rgb []byte, w, h int) string {
	b := base64.StdEncoding.EncodeToString(rgb)
	parts := []string{b[:8], b[8:16], b[16:]}
	var line strings.Builder
	for i, part := range parts {
		attrs := "m=1"
		if i == 0 {
			attrs = "a=T,f=24,t=d,s=" + itoa(w) + ",v=" + itoa(h) + ",w=8,h=4,m=1"
		} else if i == len(parts)-1 {
			attrs = "m=0"
		}
		line.WriteString("\033_G" + attrs + ";" + part + "\033\\")
	}
	return line.String()
}

func itoa(v int) string { return strconv.Itoa(v) }

func TestPreviewParser(t *testing.T) {
	rgb := make([]byte, 4*2*3)
	for i := range rgb {
		rgb[i] = byte(i)
	}
	for _, status := range []string{
		"h3: denoise preview 3/8, video frame 5/22 via kitty",
		"h3: preview 3/8, frame 5/22",
	} {
		state := newH3PreviewState()
		if _, ok := state.feed(status); ok {
			t.Fatal("status line produced a frame")
		}
		frame, ok := state.feed(kittyLine(rgb, 4, 2))
		if !ok {
			t.Fatalf("no frame after %q", status)
		}
		if frame.Width != 4 || frame.Height != 2 || frame.Step != 3 || frame.Total != 8 || frame.FrameIndex != 5 || frame.FrameTotal != 22 {
			t.Fatalf("frame = %+v", frame)
		}
		path := filepath.Join(t.TempDir(), previewFileName(frame))
		if filepath.Base(path) != "s003of008-f005of022.png" {
			t.Fatalf("preview name = %s", filepath.Base(path))
		}
		if err := writePreviewPNG(path, frame); err != nil {
			t.Fatal(err)
		}
		if _, ok := state.feed("unrelated line"); ok {
			t.Fatal("state not reset")
		}
	}
	if text, kitty := kittyPayload("prefix \033_Ga=T;xx\033\\"); !kitty || text != "prefix" {
		t.Fatalf("kittyPayload = %q %v", text, kitty)
	}
}

func TestPrunePreviews(t *testing.T) {
	dir := t.TempDir()
	names := []string{"s001of002-f000of003.png", "s001of002-f001of003.png", "s001of002-f002of003.png",
		"s002of002-f000of003.png", "s002of002-f001of003.png", "s002of002-f002of003.png"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kept := prunePreviews(dir)
	want := []string{"s001of002-f001of003.png", "s002of002-f000of003.png", "s002of002-f001of003.png", "s002of002-f002of003.png"}
	if !reflect.DeepEqual(kept, want) {
		t.Fatalf("kept %q, want %q", kept, want)
	}
}

func TestLineSplitter(t *testing.T) {
	type event struct {
		line      string
		overwrite bool
	}
	var emitted []event
	var committed []string
	s := &lineSplitter{
		emit:   func(line string, overwrite bool) { emitted = append(emitted, event{line, overwrite}) },
		commit: func(line string) { committed = append(committed, line) },
	}
	// h3's progress format: "\r%-25s %4d/%-4d" repeated, "\n" when a phase ends.
	stream := "Seed: 7\n\rdenoise      0/3   \rdenoise      1/3   \rdenoise      3/3   \n\rFFmpeg   0/22\rFFmpeg  22/22\nDone -> x.mp4 [1.0s]\n"
	for i := 0; i < len(stream); i += 5 { // feed in awkward chunks
		end := min(i+5, len(stream))
		s.feed([]byte(stream[i:end]))
	}
	s.close()
	wantEmitted := []event{
		{"Seed: 7", false}, {"denoise      0/3", false}, {"denoise      1/3", true}, {"denoise      3/3", true},
		{"FFmpeg   0/22", false}, {"FFmpeg  22/22", true}, {"Done -> x.mp4 [1.0s]", false},
	}
	if !reflect.DeepEqual(emitted, wantEmitted) {
		t.Fatalf("emitted %#v", emitted)
	}
	wantCommitted := []string{"Seed: 7", "denoise      3/3", "FFmpeg  22/22", "Done -> x.mp4 [1.0s]"}
	if !reflect.DeepEqual(committed, wantCommitted) {
		t.Fatalf("committed %q", committed)
	}
}

func TestProgressAndStages(t *testing.T) {
	phase, n, total, ok := parseProgress("denoise                      3/8   ")
	if !ok || phase != "denoise" || n != 3 || total != 8 || stageFor(phase) != "denoise" {
		t.Fatalf("parseProgress = %q %d %d %v", phase, n, total, ok)
	}
	if _, _, _, ok := parseProgress("h3: preview 3/8"); ok {
		t.Fatal("h3 diagnostic parsed as progress")
	}
	for phase, stage := range map[string]string{"text encoder": "encode", "load transformer core": "load", "video VAE load": "decode", "FFmpeg": "mp4"} {
		if got := stageFor(phase); got != stage {
			t.Errorf("stageFor(%q) = %q, want %q", phase, got, stage)
		}
	}
	row, ok := parseProfile("h3 profile: H3 DiT  GPU Euler denoise wall=  335.518s wait= 1.0s")
	if !ok || row.Component != "H3 DiT" || row.Stage != "GPU Euler denoise" || row.Wall != 335.518 {
		t.Fatalf("parseProfile = %+v %v", row, ok)
	}
}

func TestFailureHints(t *testing.T) {
	cases := map[string]string{
		"h3: command buffer failed: kIOGPUCommandBufferCallbackErrorImpactingInteractivity": "watchdog",
		"h3: out of memory allocating attention":                                            "memory",
		"h3: cannot open /m/FL2VA/transformer/config.json":                                  "Model dialog",
	}
	for line, want := range cases {
		if hint := hintFor([]string{"FFmpeg   0/22", line}); !strings.Contains(hint, want) {
			t.Errorf("hintFor(%q) = %q, want mention of %q", line, hint, want)
		}
	}
	if hint := hintFor([]string{"FFmpeg  22/22"}); hint != "" {
		t.Errorf("progress line produced a hint: %q", hint)
	}
	if got := lastMeaningfulLine([]string{"denoise  8/8", "h3: bad thing", "h3 profile: x  y wall= 1s"}); got != "h3: bad thing" {
		t.Errorf("lastMeaningfulLine = %q", got)
	}
}

func TestMP4Finalized(t *testing.T) {
	dir := t.TempDir()
	box := func(kind string, size int) []byte {
		b := make([]byte, size)
		binary.BigEndian.PutUint32(b, uint32(size))
		copy(b[4:], kind)
		return b
	}
	complete := append(append(box("ftyp", 16), box("mdat", 64)...), box("moov", 32)...)
	partial := append(box("ftyp", 16), box("mdat", 64)[:40]...)
	noMoov := append(box("ftyp", 16), box("mdat", 64)...)
	for name, data := range map[string][]byte{"complete.mp4": complete, "partial.mp4": partial, "nomoov.mp4": noMoov} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !mp4Finalized(filepath.Join(dir, "complete.mp4")) || mp4Finalized(filepath.Join(dir, "partial.mp4")) || mp4Finalized(filepath.Join(dir, "nomoov.mp4")) {
		t.Fatal("mp4Finalized misclassified a file")
	}
}

func TestEstimate(t *testing.T) {
	cfg := newTestConfig(t)
	outputs, _ := cfg.SessionSubdir("s1", "outputs")
	p := validParams()
	write := func(name string, params RenderParams, duration float64) {
		raw, _ := jsonRoundTrip(params)
		_ = writeJSONAtomic(filepath.Join(outputs, name), map[string]any{"state": "done", "duration_s": duration, "params": raw})
	}
	write("a.json", p, 100)
	write("b.json", p, 120)
	bigger := p
	bigger.Steps = 8
	write("c.json", bigger, 400)
	if seconds, samples, exact := cfg.Estimate("s1", p); seconds != 110 || samples != 2 || !exact {
		t.Fatalf("exact estimate = %v %d %v", seconds, samples, exact)
	}
	other := p
	other.Steps = 16
	if seconds, samples, exact := cfg.Estimate("s1", other); exact || samples != 3 || seconds <= 400 {
		t.Fatalf("scaled estimate = %v %d %v", seconds, samples, exact)
	}
}

func TestStaleConfigDoesNotOverrideFlags(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sessions"), 0o755)
	_ = writeJSONAtomic(filepath.Join(root, "sessions", "model.json"), map[string]any{"model": "/old/model"})
	cfg, err := NewConfig(Options{H3: "/bin/echo", Model: root, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model() != root {
		t.Fatalf("model = %q, want the flag value %q", cfg.Model(), root)
	}
	if SavedPath(root, "model.json", "model") != "/old/model" {
		t.Fatal("SavedPath did not read the remembered value")
	}
}
