package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\x1b\\][^\x07]*\x07")

// stripANSI removes terminal control sequences before assertions.
func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func TestHelpListsBackground(t *testing.T) {
	m := New(Options{})
	m.width, m.height = 100, 40
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/help")})
	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyEnter})
	view := updated.(Model).View()
	if !strings.Contains(view, "/background") {
		t.Fatalf("help output lacks /background:\n%s", view)
	}
}

func TestInputSubmitsAndQueues(t *testing.T) {
	m := New(Options{})
	m.width, m.height = 100, 40
	m.input.SetValue("hello world")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if got.input.Value() != "" {
		t.Fatalf("input not cleared: %q", got.input.Value())
	}
	if !strings.Contains(strings.Join(got.lines, "\n"), "hello world") {
		t.Fatal("submitted text missing from transcript")
	}
}

// TestSmoke drives the real binary in a pty and checks that /help renders.
func TestSmoke(t *testing.T) {
	if os.Getenv("PIG_SKIP_PTY") != "" {
		t.Skip("PIG_SKIP_PTY set")
	}
	bin := os.Getenv("PIG_TEST_BIN")
	if bin == "" {
		dir := t.TempDir()
		bin = filepath.Join(dir, "pig")
		build := exec.Command("go", "build", "-o", bin, "../../cmd/pig")
		build.Env = os.Environ()
		if out, err := build.CombinedOutput(); err != nil {
			t.Skipf("cannot build pig: %v\n%s", err, out)
		}
	}
	home := t.TempDir()
	initCmd := exec.Command(bin, "config", "init", "--provider", "mock", "--model", "mock")
	initCmd.Env = append(os.Environ(), "PIG_HOME="+home)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("config init: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "--no-daemon", "tui")
	cmd.Env = append(os.Environ(), "PIG_HOME="+home, "TERM=xterm-256color")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	var mu sync.Mutex
	var buf bytes.Buffer
	go func() {
		tmp := make([]byte, 4096)
		for {
			n, rerr := f.Read(tmp)
			if n > 0 {
				mu.Lock()
				buf.Write(tmp[:n])
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Wait for the first paint, then retry /help a few times in case the
	// keystrokes arrive before the input is ready.
	sawHelp := false
	for attempt := 0; attempt < 5 && !sawHelp; attempt++ {
		time.Sleep(1500 * time.Millisecond)
		if _, err := f.Write([]byte("/help\r")); err != nil {
			t.Fatalf("write: %v", err)
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) && !sawHelp {
			mu.Lock()
			sawHelp = strings.Contains(stripANSI(buf.String()), "/background")
			mu.Unlock()
			if !sawHelp {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
	if !sawHelp {
		mu.Lock()
		out := buf.String()
		mu.Unlock()
		t.Fatalf("TUI never rendered /help; output:\n%s", out)
	}
	// A real prompt must reach the daemon and stream back.
	if _, err := f.Write([]byte("hi\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline2 := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline2) {
		mu.Lock()
		out := buf.String()
		mu.Unlock()
		if strings.Contains(stripANSI(out), "mock: hi") {
			_, _ = f.Write([]byte{0x04})
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	mu.Lock()
	out := buf.String()
	mu.Unlock()
	t.Fatalf("TUI never rendered the mock answer (%d bytes captured):\n%s", len(out), stripANSI(out))
}
