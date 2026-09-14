package server

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ModelInfo is the setup check shown in the Model dialog.
type ModelInfo struct {
	Path        string   `json:"path"`
	Exists      bool     `json:"exists"`
	HasFL2VA    bool     `json:"has_fl2va"`
	HasRef2VA   bool     `json:"has_ref2va"`
	BrokenLinks []string `json:"broken_links"`
	SizeBytes   int64    `json:"size_bytes"`
	H3          string   `json:"h3"`
	H3Runs      bool     `json:"h3_runs"`
	H3Error     string   `json:"h3_error,omitempty"`
	FFmpeg      string   `json:"ffmpeg"`
	FFprobe     string   `json:"ffprobe"`
}

func (m ModelInfo) Caps() ModelCaps { return ModelCaps{HasFL2VA: m.HasFL2VA, HasRef2VA: m.HasRef2VA} }

type modelCache struct {
	mu   sync.Mutex
	key  string
	info ModelInfo
}

var inspectCache modelCache

// InspectModel checks the checkpoint layout h3.c requires (FL2VA always,
// Ref2VA for references), resolves symlinks, sums file sizes without
// following links and makes sure the h3 binary starts. Results are cached per
// model/h3 path pair; refresh forces a new check.
func (c *Config) InspectModel(refresh bool) ModelInfo {
	model, h3 := c.Model(), c.H3()
	key := model + "\x00" + h3
	inspectCache.mu.Lock()
	defer inspectCache.mu.Unlock()
	if !refresh && inspectCache.key == key {
		return inspectCache.info
	}
	info := ModelInfo{Path: model, H3: h3, FFmpeg: c.FFmpeg, FFprobe: c.FFprobe, BrokenLinks: []string{}}
	info.Exists = DirExists(model)
	if info.Exists {
		info.HasFL2VA = FileExists(filepath.Join(model, "FL2VA", "transformer", "config.json"))
		info.HasRef2VA = FileExists(filepath.Join(model, "Ref2VA", "transformer", "model.safetensors.index.json"))
		_ = filepath.WalkDir(model, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				if _, err := os.Stat(path); err != nil {
					rel, _ := filepath.Rel(model, path)
					info.BrokenLinks = append(info.BrokenLinks, rel)
				}
				return nil
			}
			if entry.Type().IsRegular() {
				if fi, err := entry.Info(); err == nil {
					info.SizeBytes += fi.Size()
				}
			}
			return nil
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, h3, "--help").CombinedOutput()
	if err != nil && len(out) == 0 {
		info.H3Error = err.Error()
	} else {
		info.H3Runs = true
	}
	inspectCache.key, inspectCache.info = key, info
	return info
}
