package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestApp(t *testing.T, allowShell bool) (*App, *Config) {
	t.Helper()
	root := t.TempDir()
	model := filepath.Join(root, "model")
	for _, dir := range []string{"FL2VA/transformer", "Ref2VA/transformer"} {
		if err := os.MkdirAll(filepath.Join(model, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(model, "FL2VA/transformer/config.json"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(model, "Ref2VA/transformer/model.safetensors.index.json"), []byte("{}"), 0o644)
	cfg, err := NewConfig(Options{
		H3: "/usr/bin/true", Model: model, Root: root, Host: "127.0.0.1", Port: 8710, AllowShell: allowShell,
		Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>ok")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := NewBroker()
	runner := &Runner{cfg: cfg, events: events, logs: NewLogSink(cfg), jobs: map[string]*Job{}} // no queue worker
	return NewApp(cfg, runner, events), cfg
}

func do(app *App, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:8710"
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

var jsonHeaders = map[string]string{"Content-Type": "application/json"}

func TestGuardRejectsCrossSiteAndRebinding(t *testing.T) {
	app, _ := newTestApp(t, true)
	body := `{"session":"s1","command":"touch /tmp/pwned"}`
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"simple text/plain POST", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"no content type", map[string]string{}, http.StatusUnsupportedMediaType},
		{"foreign origin", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, http.StatusForbidden},
		{"other local port", map[string]string{"Content-Type": "application/json", "Origin": "http://127.0.0.1:9999"}, http.StatusForbidden},
		{"null origin", map[string]string{"Content-Type": "application/json", "Origin": "null"}, http.StatusForbidden},
		{"cross-site fetch metadata", map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"rebound host", map[string]string{"Content-Type": "application/json", "Host": "evil.example:8710"}, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(app, http.MethodPost, "/api/shell", body, tc.headers); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
	if rec := do(app, http.MethodGet, "/api/config", "", map[string]string{"Host": "evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("GET with rebound Host = %d", rec.Code)
	}
	same := map[string]string{"Content-Type": "application/json", "Origin": "http://127.0.0.1:8710"}
	if rec := do(app, http.MethodPost, "/api/session/save", `{"session":"s1","settings":{}}`, same); rec.Code != http.StatusOK {
		t.Fatalf("same-origin POST = %d: %s", rec.Code, rec.Body.String())
	}
}

// The guard sits in front of the /helm/ proxy too, and helmstudio's SDK does
// not write in application/json alone: a sequence edit is a merge patch, and
// :cancel and DELETE carry no body and no content type. Refusing those is what
// made Remove in the timeline editor fail with no reason given.
func TestGuardLetsTheHelmProxyKeepItsContentTypes(t *testing.T) {
	app, _ := newTestApp(t, true)
	same := "http://127.0.0.1:8710"
	cases := []struct {
		name, method, path, body string
		headers                  map[string]string
	}{
		{"merge patch", http.MethodPatch, "/helm/api/v1/timeline/abc", `{"tracks":[]}`,
			map[string]string{"Content-Type": "application/merge-patch+json", "Origin": same}},
		{"bodyless cancel", http.MethodPost, "/helm/api/v1/jobs/abc:cancel", "",
			map[string]string{"Origin": same}},
		{"bodyless delete", http.MethodDelete, "/helm/api/v1/gallery/items/abc", "",
			map[string]string{"Origin": same}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Nothing is behind the proxy here, so the proxy's own 404 is the
			// pass: the guard forwarded rather than refusing.
			if rec := do(app, tc.method, tc.path, tc.body, tc.headers); rec.Code == http.StatusUnsupportedMediaType {
				t.Fatalf("the guard refused the content type: %s", rec.Body.String())
			}
		})
	}
	// The relaxation is the proxy's alone, and it is not a way past the origin
	// check or into the studio's own API.
	if rec := do(app, http.MethodPost, "/api/shell", `{"session":"s1","command":"x"}`,
		map[string]string{"Content-Type": "application/merge-patch+json"}); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("a merge patch reached the studio's own API: %d", rec.Code)
	}
	if rec := do(app, http.MethodPatch, "/helm/api/v1/timeline/abc", `{"tracks":[]}`,
		map[string]string{"Content-Type": "application/merge-patch+json", "Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a cross-origin merge patch was forwarded: %d", rec.Code)
	}
	if rec := do(app, http.MethodPost, "/helm/api/v1/jobs/abc:cancel", "",
		map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Fatalf("a cross-site bodyless write was forwarded: %d", rec.Code)
	}
}

// A refusal says what it is in "message" as well as "error": helmstudio's SDK
// reads "error" as a code and shows "message", so with only "error" a studio
// component has nothing to show.
func TestAGuardRefusalCarriesAMessage(t *testing.T) {
	app, _ := newTestApp(t, true)
	rec := do(app, http.MethodPost, "/api/shell", `{}`, map[string]string{"Content-Type": "text/plain"})
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["message"] == nil || got["message"] != got["error"] {
		t.Fatalf("message = %v, error = %v", got["message"], got["error"])
	}
}

func TestShellDisabledByDefault(t *testing.T) {
	app, _ := newTestApp(t, false)
	rec := do(app, http.MethodPost, "/api/shell", `{"session":"s1","command":"echo hi"}`, jsonHeaders)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "--allow-shell") {
		t.Fatalf("shell = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(app, http.MethodPost, "/api/h3", `{"h3":"/bin/sh"}`, jsonHeaders)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/h3 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRenderValidationAndCommand(t *testing.T) {
	app, cfg := newTestApp(t, false)
	inputs, _ := cfg.SessionSubdir("s1", "inputs")
	_ = os.WriteFile(filepath.Join(inputs, "a.png"), []byte("x"), 0o644)

	bad := `{"session_name":"s1","mode":"anchor","first_frame":"../../../etc/passwd","prompt":"x","width":512,"height":512,"frames":22,"steps":4,"layers":50,"reuse":1,"env":{"DYLD_INSERT_LIBRARIES":"/x"}}`
	rec := do(app, http.MethodPost, "/api/render", bad, jsonHeaders)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid file name") || !strings.Contains(rec.Body.String(), "not allowed") {
		t.Fatalf("render bad = %d %s", rec.Code, rec.Body.String())
	}

	good := `{"session_name":"s1","mode":"anchor","first_frame":"a.png","prompt":"it's a shot","width":512,"height":512,"frames":22,"steps":4,"layers":50,"reuse":1,"seed":9}`
	rec = do(app, http.MethodPost, "/api/command", good, jsonHeaders)
	var resp struct {
		Errors  []string `json:"errors"`
		Argv    []string `json:"argv"`
		Display string   `json:"display"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("command = %d %s", rec.Code, rec.Body.String())
	}
	if len(resp.Errors) != 0 || !strings.Contains(resp.Display, "--first-frame "+filepath.Join(inputs, "a.png")) || !strings.Contains(resp.Display, `'it'\''s a shot'`) {
		t.Fatalf("command response = %+v", resp)
	}
}

func TestMediaRangeAndTraversal(t *testing.T) {
	app, cfg := newTestApp(t, false)
	outputs, _ := cfg.SessionSubdir("s1", "outputs")
	_ = os.WriteFile(filepath.Join(outputs, "take.mp4"), []byte("0123456789"), 0o644)
	rec := do(app, http.MethodGet, "/media/s1/outputs/take.mp4", "", map[string]string{"Range": "bytes=2-5"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("range = %d %q", rec.Code, rec.Body.String())
	}
	for _, target := range []string{"/media/s1/setting.json", "/media/s1/outputs/..%2f..%2fmodel.json", "/sfile/..%2f..%2fetc%2fpasswd"} {
		if rec := do(app, http.MethodGet, target, "", nil); rec.Code == http.StatusOK {
			t.Errorf("%s served: %q", target, rec.Body.String())
		}
	}
}

func TestSessionLifecycleAndDelete(t *testing.T) {
	app, cfg := newTestApp(t, false)
	if rec := do(app, http.MethodPost, "/api/session/activate", `{"session":"Shot A"}`, jsonHeaders); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"shot-a"`) {
		t.Fatalf("activate = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(app, http.MethodPost, "/api/session/save", `{"session":"shot-a","settings":{"prompt":"hello","takes":[1,2]}}`, jsonHeaders); rec.Code != http.StatusOK {
		t.Fatalf("save = %d", rec.Code)
	}
	data := readJSONObject(filepath.Join(cfg.Sessions, "shot-a", "setting.json"))
	if data["prompt"] != "hello" || data["takes"] != nil {
		t.Fatalf("saved setting = %v", data)
	}
	if rec := do(app, http.MethodPost, "/api/session/duplicate", `{"session":"shot-a","as":"shot-b"}`, jsonHeaders); rec.Code != http.StatusOK {
		t.Fatalf("duplicate = %d %s", rec.Code, rec.Body.String())
	}
	outputs, _ := cfg.SessionSubdir("shot-b", "outputs")
	previews, _ := cfg.SessionSubdir("shot-b", "previews")
	_ = os.WriteFile(filepath.Join(outputs, "take.mp4"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(previews, "job1"), 0o755)
	_ = writeJSONAtomic(filepath.Join(outputs, "take.json"), map[string]any{"preview_dir": "previews/job1"})
	if rec := do(app, http.MethodPost, "/api/delete", `{"session":"shot-b","name":"take.mp4","kind":"output"}`, jsonHeaders); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	if pathExists(filepath.Join(outputs, "take.json")) || pathExists(filepath.Join(previews, "job1")) {
		t.Fatal("delete left the sidecar or previews behind")
	}
	if rec := do(app, http.MethodPost, "/api/session/delete", `{"session":"shot-b"}`, jsonHeaders); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"shot-a"`) {
		t.Fatalf("session delete = %d %s", rec.Code, rec.Body.String())
	}
}

func TestUploadSanitizesName(t *testing.T) {
	app, cfg := newTestApp(t, false)
	headers := map[string]string{"X-Filename": "%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E.png"}
	rec := do(app, http.MethodPost, "/api/upload?session=s1", "pngdata", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if strings.ContainsAny(resp.Name, "<>= ()") || !FileExists(filepath.Join(cfg.Sessions, "s1", "inputs", resp.Name)) {
		t.Fatalf("uploaded name %q", resp.Name)
	}
	if rec := do(app, http.MethodPost, "/api/upload?session=s1", "x", map[string]string{"X-Filename": "run.sh"}); rec.Code == http.StatusOK {
		t.Fatal("accepted an unsupported file type")
	}
}
