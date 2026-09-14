package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var stemRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// safeStem turns arbitrary text into a lowercase file/session name made of
// [a-z0-9._-], at most 48 characters. It never returns "", "." or "..".
func safeStem(text string) string {
	stem := stemRE.ReplaceAllString(text, "-")
	stem = strings.Trim(stem, "-.")
	stem = strings.ToLower(stem)
	if len(stem) > 48 {
		stem = strings.Trim(stem[:48], "-.")
	}
	if stem == "" {
		stem = "take"
	}
	return stem
}

// writeJSONAtomic writes obj as indented JSON through a temp file in the same
// directory followed by a rename, so readers never see a half-written file.
func writeJSONAtomic(path string, obj any) error {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// WriteJSONFile is kept for callers outside the package (main.go).
func WriteJSONFile(path string, obj any) error { return writeJSONAtomic(path, obj) }

func readJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func readStringField(path, field string) (string, bool) {
	value, _ := readJSONObject(path)[field].(string)
	value = strings.TrimSpace(value)
	return value, value != ""
}

// keyedMutex hands out one mutex per key (session name), so read-modify-write
// cycles on one session's files never interleave.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	m := k.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// uniquePath returns dir/base+ext, or dir/base-N+ext for the first N not taken.
func uniquePath(dir, base, ext string) string {
	dst := filepath.Join(dir, base+ext)
	for i := 1; pathExists(dst); i++ {
		dst = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
	return dst
}

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func comma(v int) string {
	s := strconv.Itoa(v)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }
