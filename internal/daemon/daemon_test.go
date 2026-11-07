package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexblock/pig/internal/agent"
	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/store"
)

func TestFanoutDropsDeltasKeepsTerminal(t *testing.T) {
	f := newFanout()
	received := make(chan agent.Event, 1024)
	sub := f.addRunFunc("run_x", func(ev agent.Event) error {
		time.Sleep(500 * time.Microsecond)
		received <- ev
		return nil
	})
	defer f.remove(sub)

	total := 600
	for range total {
		f.emit("run_x", "ses_x", agent.Event{Type: agent.EvTextDelta, Text: "x"})
	}
	f.emit("run_x", "ses_x", agent.Event{Type: agent.EvRunEnd})

	deadline := time.After(10 * time.Second)
	deltas := 0
	for {
		select {
		case ev := <-received:
			if ev.Type == agent.EvRunEnd {
				if deltas >= total {
					t.Fatalf("received %d deltas; expected drops", deltas)
				}
				return
			}
			if ev.Type == agent.EvTextDelta {
				deltas++
			}
		case <-deadline:
			t.Fatalf("terminal event not delivered after %d deltas", deltas)
		}
	}
}

func startTestServer(t *testing.T, cfg *config.Config, paths config.Paths) *Server {
	t.Helper()
	if err := paths.Ensure(); err != nil {
		t.Fatalf("paths.Ensure: %v", err)
	}
	logger, err := NewLogger(paths.Log(), 1<<20)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	srv, err := New(Options{Config: cfg, Paths: paths, Logger: logger, NoChannels: true})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		defer close(done)
		runErr <- srv.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Log("daemon did not stop in time")
		}
	})
	socket := paths.Socket(cfg.Daemon.Socket)
	for range 200 {
		if _, err := os.Stat(socket); err == nil {
			return srv
		}
		select {
		case err := <-runErr:
			t.Fatalf("daemon exited: %v", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("daemon socket never appeared at %s", socket)
	return nil
}

// shortHome returns a PIG_HOME short enough for a Unix socket path.
func shortHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pig")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestAutoFork(t *testing.T) {
	home := shortHome(t)
	t.Setenv("PIG_HOME", home)
	t.Setenv("PIG_MOCK_SCRIPT", `{"turns":[{"text":"","tool":{"name":"bash","args":{"command":"echo hi"}},"delay_ms":1200},{"text":"done"}]}`)

	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	cfg.Background.AutoFork = true
	cfg.Background.PressureSecs = 1
	cfg.Background.PressureTools = 1
	cfg.Background.MaxRunningJobs = 2
	cfg.Background.MaxQueuedJobs = 4
	paths := config.Paths{Home: home}

	srv := startTestServer(t, cfg, paths)
	sess, err := srv.Store().CreateSession(store.SessionOptions{CWD: t.TempDir(), Model: "mock/mock"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	runID, sessionID, queued, err := srv.StartRun(runSpec{SessionID: sess.ID, Prompt: "start", Queue: true})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if queued {
		t.Fatal("first run reported queued")
	}
	// Queue a second prompt while the first turn is still streaming.
	time.Sleep(300 * time.Millisecond)
	if _, _, q, err := srv.StartRun(runSpec{SessionID: sessionID, Prompt: "second", Queue: true}); err != nil || !q {
		t.Fatalf("second StartRun queued=%v err=%v", q, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.WaitRun(ctx, runID, 25*time.Second); err != nil {
		t.Fatalf("WaitRun: %v", err)
	}

	jobs, err := srv.Store().ListJobs("", 10)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	var continuation *store.Job
	for _, j := range jobs {
		if j.Kind == "continuation" && j.ParentSessionID == sessionID {
			continuation = j
			break
		}
	}
	if continuation == nil {
		t.Fatalf("no continuation job; jobs = %+v", jobs)
	}

	// The parent transcript must record the detach notice.
	entries, err := srv.Store().AllEntries(sessionID)
	if err != nil {
		t.Fatalf("AllEntries: %v", err)
	}
	notice := false
	for _, e := range entries {
		if e.Kind == store.KindNotice {
			notice = true
		}
	}
	if !notice {
		t.Fatal("parent session has no detach notice")
	}

	// The queued prompt must start a new foreground run.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, ok := srv.ActiveRun(sessionID); !ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	entries, _ = srv.Store().AllEntries(sessionID)
	answered := false
	for _, e := range entries {
		if e.Kind == store.KindMessage && e.Role == "user" && e.Message().Text == "second" {
			answered = true
		}
	}
	if !answered {
		t.Fatal("queued prompt was dropped")
	}
}

func TestShutdownStopsRun(t *testing.T) {
	home := shortHome(t)
	cfg := config.MockConfig(filepath.Join(home, "config.toml"))
	paths := config.Paths{Home: home}
	srv := startTestServer(t, cfg, paths)

	done := make(chan error, 1)
	go func() {
		_, err := srv.handleIPC(context.Background(), nil, "daemon.shutdown", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown rpc: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon.shutdown did not return")
	}
	// The server context must be cancelled so Run unwinds.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		ctx := srv.ctx
		srv.mu.Unlock()
		if ctx != nil && ctx.Err() != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("server context was not cancelled by daemon.shutdown")
}
