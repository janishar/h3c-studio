package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"h3studio/server"
)

//go:embed static
var embeddedStatic embed.FS

func main() {
	h3 := flag.String("h3", "", "path to the h3 binary (env H3STUDIO_H3; defaults to the last one used)")
	model := flag.String("model", "", "path to the MiniMax-H3 directory (env H3STUDIO_MODEL; defaults to the last one used)")
	host := flag.String("host", "127.0.0.1", "bind address")
	port := flag.Int("port", 8710, "bind port")
	dev := flag.Bool("dev", false, "serve static/ from disk without caching, for front-end work")
	allowShell := flag.Bool("allow-shell", false, "enable the shell terminal and changing the h3 binary from the browser")
	allowHosts := flag.String("allow-host", "", "comma-separated extra Host names to accept (IP addresses and localhost are always accepted)")
	root := flag.String("root", "", "directory holding sessions/ (default: next to the binary, or the current directory)")
	flag.Parse()

	rootDir := *root
	if rootDir == "" {
		rootDir = discoverRoot()
	}
	h3Path := firstNonEmpty(*h3, os.Getenv("H3STUDIO_H3"), server.SavedPath(rootDir, "h3.json", "h3"))
	modelPath := firstNonEmpty(*model, os.Getenv("H3STUDIO_MODEL"), server.SavedPath(rootDir, "model.json", "model"))
	if h3Path == "" || modelPath == "" {
		fmt.Fprintln(os.Stderr, "h3 studio needs --h3 and --model the first time it runs.")
		flag.Usage()
		os.Exit(2)
	}

	static, err := staticFS(rootDir, *dev)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := server.NewConfig(server.Options{
		H3: h3Path, Model: modelPath, Root: rootDir, Host: *host, Port: *port,
		Dev: *dev, AllowShell: *allowShell, AllowedHosts: strings.Split(*allowHosts, ","), Static: static,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !server.FileExists(cfg.H3()) {
		fmt.Fprintf(os.Stderr, "h3 binary not found: %s\n", cfg.H3())
		os.Exit(1)
	}
	if !server.DirExists(cfg.Model()) {
		fmt.Fprintf(os.Stderr, "model directory not found: %s\n", cfg.Model())
		os.Exit(1)
	}
	// Remember the paths so later runs can omit the flags.
	cfg.SetH3(cfg.H3())
	cfg.SetModel(cfg.Model())

	events := server.NewBroker()
	runner := server.NewRunner(cfg, events)
	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           server.NewApp(cfg, runner, events),
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Printf("h3 studio  →  http://%s\n", cfg.Addr())
	fmt.Printf("  binary    %s\n", cfg.H3())
	fmt.Printf("  model     %s\n", cfg.Model())
	fmt.Printf("  sessions  %s\n", cfg.Sessions)
	if *dev {
		fmt.Println("  static    served from disk (dev)")
	}
	if cfg.FFmpeg == "" || cfg.FFprobe == "" {
		fmt.Println("  warning   ffmpeg/ffprobe not found — frame extraction, thumbnails and the timeline won't work")
	}
	if ip := net.ParseIP(*host); !(*host == "localhost" || (ip != nil && ip.IsLoopback())) {
		fmt.Println("  warning   no authentication — anyone who can reach this port can run renders")
	}
	if *allowShell {
		fmt.Println("  warning   --allow-shell: the browser can run shell commands in the h3 work directory")
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		fmt.Println("\nstopping")
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	runner.Shutdown()
}

// discoverRoot finds the directory that holds sessions/: next to the binary,
// its parent (dist/h3studio), or the current directory.
func discoverRoot() string {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, dir, filepath.Dir(dir))
	}
	cwd, err := os.Getwd()
	if err == nil {
		candidates = append(candidates, cwd)
	}
	for _, dir := range candidates {
		if server.DirExists(filepath.Join(dir, "sessions")) || server.FileExists(filepath.Join(dir, "go.mod")) {
			return dir
		}
	}
	if cwd != "" {
		return cwd
	}
	return "."
}

// staticFS serves the embedded UI, or static/ from disk in dev mode.
func staticFS(root string, dev bool) (fs.FS, error) {
	if dev {
		dir := filepath.Join(root, "static")
		if !server.FileExists(filepath.Join(dir, "index.html")) {
			return nil, fmt.Errorf("--dev needs %s/index.html", dir)
		}
		return os.DirFS(dir), nil
	}
	return fs.Sub(embeddedStatic, "static")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
