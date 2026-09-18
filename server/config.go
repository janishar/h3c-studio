package server

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Options are the resolved command-line settings the server starts with.
type Options struct {
	H3           string
	Model        string
	Root         string // directory holding sessions/
	Host         string
	Port         int
	Dev          bool
	AllowShell   bool
	AllowedHosts []string // extra Host header names accepted besides IP literals and localhost
	Static       fs.FS
}

type Config struct {
	mu    sync.RWMutex
	h3    string
	model string

	Root         string
	Sessions     string
	Host         string
	Port         int
	Dev          bool
	AllowShell   bool
	allowedHosts map[string]bool
	Static       fs.FS

	FFmpeg  string
	FFprobe string

	// Platform is helmstudio when the studio runs under it, and nil when it
	// runs on its own. Resolved once: the environment it reads is fixed for
	// the life of the process.
	Platform *Platform

	sessionLocks keyedMutex
}

// Session subdirectories that the API may read media from.
var sessionMediaDirs = map[string]bool{"inputs": true, "outputs": true, "timeline": true, "previews": true}

func NewConfig(opts Options) (*Config, error) {
	h3, err := filepath.Abs(expandHome(opts.H3))
	if err != nil {
		return nil, err
	}
	model, err := filepath.Abs(expandHome(opts.Model))
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		return nil, err
	}
	cfg := &Config{
		h3:           h3,
		model:        model,
		Root:         root,
		Sessions:     sessions,
		Platform:     NewPlatform(),
		Host:         opts.Host,
		Port:         opts.Port,
		Dev:          opts.Dev,
		AllowShell:   opts.AllowShell,
		allowedHosts: map[string]bool{"localhost": true},
		Static:       opts.Static,
		FFmpeg:       toolPath("H3_FFMPEG", "ffmpeg"),
		FFprobe:      toolPath("H3_FFPROBE", "ffprobe"),
	}
	for _, host := range opts.AllowedHosts {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			cfg.allowedHosts[host] = true
		}
	}
	if host := strings.ToLower(opts.Host); host != "" && net.ParseIP(host) == nil {
		cfg.allowedHosts[host] = true
	}
	if _, err := cfg.EnsureSession(cfg.LastSession()); err != nil {
		return nil, err
	}
	return cfg, nil
}

// toolPath resolves an external tool from its override env var, then PATH.
// An empty result means the tool isn't available.
func toolPath(envName, name string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	if found, err := exec.LookPath(name); err == nil {
		return found
	}
	return ""
}

func (c *Config) H3() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.h3
}

func (c *Config) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

func (c *Config) Workdir() string { return filepath.Dir(c.H3()) }

func (c *Config) SetH3(path string) {
	c.mu.Lock()
	c.h3 = path
	c.mu.Unlock()
	_ = writeJSONAtomic(filepath.Join(c.Sessions, "h3.json"), map[string]any{"h3": path})
}

func (c *Config) SetModel(path string) {
	c.mu.Lock()
	c.model = path
	c.mu.Unlock()
	_ = writeJSONAtomic(filepath.Join(c.Sessions, "model.json"), map[string]any{"model": path})
}

// SavedPath reads a path remembered from an earlier run (h3.json / model.json).
func SavedPath(root, file, field string) string {
	value, _ := readStringField(filepath.Join(root, "sessions", file), field)
	return value
}

// LockSession serializes read-modify-write cycles on one session's files.
func (c *Config) LockSession(name string) func() { return c.sessionLocks.lock(safeStem(name)) }

// SessionDir returns the directory for a session name without creating it.
func (c *Config) SessionDir(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("session is required")
	}
	dir := filepath.Join(c.Sessions, safeStem(name))
	if filepath.Dir(dir) != c.Sessions {
		return "", errors.New("invalid session name")
	}
	return dir, nil
}

// EnsureSession creates the session's directory layout and a default
// setting.json if it doesn't exist yet, and returns the session directory.
func (c *Config) EnsureSession(name string) (string, error) {
	dir, err := c.SessionDir(name)
	if err != nil {
		return "", err
	}
	for _, sub := range []string{"inputs", "outputs", "timeline", "previews"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "", err
		}
	}
	setting := filepath.Join(dir, "setting.json")
	if !FileExists(setting) {
		if err := writeJSONAtomic(setting, defaultSessionSettings(filepath.Base(dir))); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// SessionSubdir returns <session>/<sub>, creating the session if needed.
func (c *Config) SessionSubdir(name, sub string) (string, error) {
	dir, err := c.EnsureSession(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sub), nil
}

// MediaPath resolves a path relative to a session directory whose first
// segment is one of inputs/outputs/timeline/previews, refusing anything that
// escapes that directory or names a hidden file.
func (c *Config) MediaPath(session, rel string) (string, error) {
	dir, err := c.SessionDir(session)
	if err != nil {
		return "", err
	}
	clean := strings.TrimPrefix(filepath.Clean("/"+filepath.FromSlash(rel)), string(os.PathSeparator))
	parts := strings.Split(clean, string(os.PathSeparator))
	if len(parts) < 2 || !sessionMediaDirs[parts[0]] {
		return "", errors.New("invalid media path")
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") {
			return "", errors.New("invalid media path")
		}
	}
	return filepath.Join(dir, clean), nil
}

// InputPath resolves a bare file name inside a session's inputs directory.
func (c *Config) InputPath(session, name string) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", errors.New("invalid input name: " + name)
	}
	return c.MediaPath(session, "inputs/"+name)
}

// ResolveSessionsPath cleans a path relative to the sessions root and checks
// that it doesn't escape it. rel == "" or "." resolves to the root itself.
func (c *Config) ResolveSessionsPath(rel string) (string, error) {
	clean := strings.TrimPrefix(filepath.Clean("/"+filepath.FromSlash(rel)), string(os.PathSeparator))
	if clean == "" || clean == "." {
		return c.Sessions, nil
	}
	for _, part := range strings.Split(clean, string(os.PathSeparator)) {
		if strings.HasPrefix(part, ".") {
			return "", errors.New("path escapes sessions directory")
		}
	}
	abs := filepath.Join(c.Sessions, clean)
	if abs != c.Sessions && !strings.HasPrefix(abs, c.Sessions+string(os.PathSeparator)) {
		return "", errors.New("path escapes sessions directory")
	}
	return abs, nil
}

func (c *Config) relSessionsPath(abs string) string {
	rel, err := filepath.Rel(c.Sessions, abs)
	if err != nil {
		return filepath.Base(abs)
	}
	return filepath.ToSlash(rel)
}

// ListSessions returns the names of session directories, sorted.
func (c *Config) ListSessions() []string {
	entries, err := os.ReadDir(c.Sessions)
	if err != nil {
		return []string{}
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		root := filepath.Join(c.Sessions, entry.Name())
		if FileExists(filepath.Join(root, "setting.json")) || DirExists(filepath.Join(root, "outputs")) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// LastSession is the session the UI opens on start: the one recorded in
// last_session.json if it still exists, else the first existing, else session-1.
func (c *Config) LastSession() string {
	for _, file := range []string{"last_session.json", "setting.json"} {
		data := readJSONObject(filepath.Join(c.Sessions, file))
		if raw, _ := data["last_session"].(string); strings.TrimSpace(raw) != "" {
			if name := safeStem(raw); DirExists(filepath.Join(c.Sessions, name)) {
				return name
			}
		}
	}
	if names := c.ListSessions(); len(names) > 0 {
		return names[0]
	}
	return "session-1"
}

func (c *Config) SetLastSession(name string) error {
	return writeJSONAtomic(filepath.Join(c.Sessions, "last_session.json"), map[string]any{"last_session": safeStem(name)})
}

// hostAllowed reports whether a request's Host header names this server:
// an IP literal, localhost, the configured --host name or an --allow-host
// name. Other names are refused so DNS rebinding can't reach the API.
func (c *Config) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "" {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return c.allowedHosts[host]
}

func (c *Config) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

func defaultSessionSettings(name string) map[string]any {
	return map[string]any{
		"session_name":    safeStem(name),
		"label":           "",
		"prompt":          "",
		"prompt_doc":      []any{map[string]any{"type": "text", "value": ""}},
		"mode":            "ref",
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
	}
}
