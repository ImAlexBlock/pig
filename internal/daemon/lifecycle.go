package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ImAlexBlock/pig/internal/config"
	"github.com/ImAlexBlock/pig/internal/ipc"
)

// Version is the reported binary version.
const Version = "0.1.0"

// ErrNotRunning reports a missing daemon.
var ErrNotRunning = errors.New("daemon: not running")

// AcquireLock takes an exclusive non-blocking lock on the pid lock file.
func AcquireLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: lock: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: lock: %v", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("daemon: lock busy")
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

// WritePID stores the daemon pid.
func WritePID(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o600)
}

// ReadPID reads the stored pid.
func ReadPID(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// RemovePID deletes the pid file.
func RemovePID(path string) { _ = os.Remove(path) }

// Listen creates the Unix socket.
func Listen(path string) (net.Listener, error) {
	if len(path) > 100 {
		return nil, fmt.Errorf("daemon: socket path too long")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: socket: %v", err)
	}
	if st, err := os.Stat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("daemon: socket: %s exists and is not a socket", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: socket: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("daemon: socket: %v", err)
	}
	return ln, nil
}

// Status asks a running daemon for its state.
func Status(ctx context.Context, socket string) (map[string]any, error) {
	client, err := ipc.Dial(ctx, socket)
	if err != nil {
		return nil, ErrNotRunning
	}
	defer client.Close()
	raw, err := client.Call(ctx, "daemon.status", nil)
	if err != nil {
		return nil, fmt.Errorf("daemon: %v", err)
	}
	var out map[string]any
	if err := ipc.Decode(raw, &out); err != nil {
		return nil, fmt.Errorf("daemon: %v", err)
	}
	return out, nil
}

// Start spawns the daemon as a detached child and waits for its socket.
func Start(cfg *config.Config, paths config.Paths) error {
	socket := paths.Socket(cfg.Daemon.Socket)
	if _, err := Status(context.Background(), socket); err == nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("daemon: %v", err)
	}
	logPath := paths.Log()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("daemon: %v", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("daemon: %v", err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		logFile.Close()
		return fmt.Errorf("daemon: %v", err)
	}
	cmd := exec.Command(exe, "daemon", "run", "--foreground")
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "PIG_DAEMON_CHILD=1")
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("daemon: %v", err)
	}
	go func() {
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := Status(context.Background(), socket); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return fmt.Errorf("daemon: failed to start (see daemon.log)")
}

// Stop asks the daemon to shut down.
func Stop(ctx context.Context, cfg *config.Config, paths config.Paths) error {
	socket := paths.Socket(cfg.Daemon.Socket)
	client, err := ipc.Dial(ctx, socket)
	if err != nil {
		return ErrNotRunning
	}
	defer client.Close()
	if _, err := client.Call(ctx, "daemon.shutdown", nil); err != nil {
		return fmt.Errorf("daemon: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !socketAlive(socket) {
			_ = os.Remove(paths.PID())
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if pid, ok := ReadPID(paths.PID()); ok {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	return nil
}

func socketAlive(socket string) bool {
	c, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Ensure starts the daemon when it is not already listening.
func Ensure(ctx context.Context, cfg *config.Config, paths config.Paths) error {
	socket := paths.Socket(cfg.Daemon.Socket)
	if socketAlive(socket) {
		return nil
	}
	return Start(cfg, paths)
}
