package server

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Broker fans server events out to SSE subscribers. A subscriber that falls
// behind loses messages rather than being disconnected.
type Broker struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewBroker() *Broker { return &Broker{subs: map[chan []byte]struct{}{}} }

func (b *Broker) Subscribe() chan []byte {
	ch := make(chan []byte, 512)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) Unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

func (b *Broker) Emit(kind string, payload any) {
	data, err := json.Marshal(map[string]any{"kind": kind, "payload": payload})
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- data:
		default:
		}
	}
}

// LogSink appends terminal lines to a session's terminal.log through one
// open file handle, rotating the file when it grows past maxLogBytes.
type LogSink struct {
	cfg     *Config
	mu      sync.Mutex
	session string
	file    *os.File
	size    int64
}

const maxLogBytes = 2 << 20

func NewLogSink(cfg *Config) *LogSink { return &LogSink{cfg: cfg} }

func (l *LogSink) Write(session, line string) {
	if session == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.session != session {
		if l.file != nil {
			l.file.Close()
			l.file = nil
		}
		dir, err := l.cfg.EnsureSession(session)
		if err != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(dir, "terminal.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		info, _ := f.Stat()
		l.file, l.session = f, session
		if info != nil {
			l.size = info.Size()
		}
	}
	if l.size > maxLogBytes {
		if err := l.file.Truncate(0); err == nil {
			l.size = 0
		}
	}
	n, _ := l.file.WriteString(line + "\n")
	l.size += int64(n)
}

func (l *LogSink) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Tail returns up to n trailing bytes of a session's terminal log.
func (l *LogSink) Tail(session string, n int64) string {
	dir, err := l.cfg.SessionDir(session)
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(dir, "terminal.log"))
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := max(info.Size()-n, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, _ := io.ReadAll(f)
	if start > 0 {
		if i := indexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return string(data)
}

func indexByte(data []byte, b byte) int {
	for i, c := range data {
		if c == b {
			return i
		}
	}
	return -1
}
