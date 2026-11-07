package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger writes daemon.log with size-based rotation.
type Logger struct {
	path string
	max  int64

	mu   sync.Mutex
	f    *os.File
	size int64

	Prefix string
}

// NewLogger opens (creating if needed) the log file.
func NewLogger(path string, max int64) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: log: %v", err)
	}
	l := &Logger{path: path, max: max}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: log: %v", err)
	}
	l.f = f
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	}
	return l, nil
}

// Printf writes one line.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return
	}
	line := fmt.Sprintf("%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	n, err := l.f.WriteString(line)
	if err != nil {
		return
	}
	l.size += int64(n)
	if l.max > 0 && l.size > l.max {
		l.rotateLocked()
	}
}

func (l *Logger) rotateLocked() {
	_ = l.f.Close()
	l.f = nil
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	l.f = f
	l.size = 0
}

// Close flushes and closes the log.
func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Sync()
		_ = l.f.Close()
		l.f = nil
	}
}
