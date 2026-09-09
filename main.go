package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"h3studio/server"

	"github.com/fsnotify/fsnotify"
)

func main() {
	h3 := flag.String("h3", "", "path to the h3 binary")
	model := flag.String("model", "", "path to the MiniMax-H3 directory")
	port := flag.Int("port", 8710, "port")
	host := flag.String("host", "127.0.0.1", "host")
	dev := flag.Bool("dev", false, "enable hot reload (dev mode)")
	flag.Parse()
	if *h3 == "" || *model == "" {
		flag.Usage()
		os.Exit(2)
	}
	cfg, err := server.NewConfig(server.Args{H3: *h3, Model: *model})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !server.FileExists(cfg.H3) {
		fmt.Fprintf(os.Stderr, "h3 binary not found: %s\n", cfg.H3)
		os.Exit(1)
	}
	if !server.DirExists(cfg.Model) {
		fmt.Fprintf(os.Stderr, "model directory not found: %s\n", cfg.Model)
		os.Exit(1)
	}
	_ = server.WriteJSONFile(cfg.ModelFile, map[string]any{"model": cfg.Model}, true)
	_ = server.WriteJSONFile(cfg.H3File, map[string]any{"h3": cfg.H3}, true)
	runner := server.NewRunner(cfg)
	app := server.NewApp(cfg, runner)
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", *host, *port), Handler: app}

	// Start file watcher for hot reload (dev only)
	if *dev {
		watcher, err := startFileWatcher(cfg.Static, runner)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: file watcher failed to start: %v\n", err)
		} else {
			defer watcher.Close()
			fmt.Println("  File watcher: enabled (static files will auto-reload)")
		}
	} else {
		fmt.Println("  File watcher: disabled (use --dev to enable)")
	}

	fmt.Printf("h3 studio  →  http://%s:%d\n", *host, *port)
	fmt.Printf("  binary   %s\n", cfg.H3)
	fmt.Printf("  model    %s\n", cfg.Model)
	fmt.Printf("  input    %s\n", cfg.CurrentInputs())
	fmt.Printf("  outputs  %s\n", cfg.CurrentOutputs())

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		if sig != nil {
			fmt.Println("\nstopped")
		}
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, err)
	}
	runner.StopInteractive()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// startFileWatcher watches the static directory for changes and triggers a reload
func startFileWatcher(staticDir string, runner *server.Runner) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Add the static directory to watch
	if err := watcher.Add(staticDir); err != nil {
		watcher.Close()
		return nil, err
	}

	// Watch for changes in static files
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Only reload on write events (create, write, remove)
				if event.Op&fsnotify.Write == fsnotify.Write ||
					event.Op&fsnotify.Create == fsnotify.Create ||
					event.Op&fsnotify.Remove == fsnotify.Remove {
					// Check if it's a static file
					if filepath.Ext(event.Name) != "" {
						fmt.Printf("  [Hot Reload] %s changed - reloading...\n", event.Name)
						runner.ReloadClients()
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Fprintf(os.Stderr, "File watcher error: %v\n", err)
			}
		}
	}()

	return watcher, nil
}
