package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ImAlexBlock/pig/internal/agent"
	"github.com/ImAlexBlock/pig/internal/ipc"
	"github.com/ImAlexBlock/pig/internal/store"
)

func (a *App) cmdRun(args []string) (int, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cwd, model string
	var background, fresh bool
	fs.BoolVar(&fresh, "new", false, "start a new session instead of continuing the latest")
	fs.StringVar(&cwd, "c", "", "working directory")
	fs.StringVar(&cwd, "cwd", "", "working directory")
	fs.StringVar(&model, "model", "", "model ref")
	fs.BoolVar(&background, "background", false, "run as a background job")
	if err := fs.Parse(args); err != nil {
		return ExitUsage, usagef("%v", err)
	}
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		b, err := io.ReadAll(a.In)
		if err != nil {
			return ExitError, err
		}
		prompt = strings.TrimSpace(string(b))
	}
	if prompt == "" {
		return ExitUsage, usagef("run: prompt is required")
	}
	if cwd == "" {
		cwd = a.CWD
	}
	if err := a.requireConfig(); err != nil {
		return ExitConfig, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer a.closeInProcess()

	client, err := a.client(ctx)
	if err != nil {
		return ExitDaemon, err
	}
	defer client.Close()

	sessionID := ""
	if fresh {
		var created struct {
			Session *store.Session `json:"session"`
		}
		if err := a.call(ctx, client, "session.new", map[string]any{"cwd": cwd}, &created); err != nil {
			return ExitError, err
		}
		if created.Session != nil {
			sessionID = created.Session.ID
		}
	}
	var started struct {
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
		Queued    bool   `json:"queued"`
		JobID     string `json:"job_id"`
	}
	if err := a.call(ctx, client, "run.start", map[string]any{
		"prompt":     prompt,
		"session_id": sessionID,
		"cwd":        cwd,
		"model":      model,
		"client":     "cli",
		"background": background,
	}, &started); err != nil {
		return ExitError, err
	}
	if started.JobID != "" {
		fmt.Fprintln(a.Out, started.JobID)
		return ExitOK, nil
	}

	events := make(chan agent.Event, 256)
	done := make(chan struct{})
	client.On("run.event", func(raw json.RawMessage) {
		var payload struct {
			RunID string      `json:"run_id"`
			Event agent.Event `json:"event"`
		}
		if err := ipc.Decode(raw, &payload); err != nil {
			return
		}
		select {
		case events <- payload.Event:
		case <-done:
		}
	})
	client.On("run.snapshot", func(json.RawMessage) {})
	if err := a.call(ctx, client, "run.subscribe", map[string]any{"run_id": started.RunID}, nil); err != nil {
		return ExitError, err
	}

	exitCode := ExitOK
	for {
		select {
		case <-ctx.Done():
			return exitCode, nil
		case <-client.Done():
			fmt.Fprintln(a.Err, "pig: daemon: connection lost")
			return ExitDaemon, nil
		case ev := <-events:
			if a.JSON {
				a.printNDJSON(ev)
			}
			switch ev.Type {
			case agent.EvTextDelta:
				if !a.JSON {
					fmt.Fprint(a.Out, ev.Text)
				}
			case agent.EvToolStart:
				if !a.JSON {
					fmt.Fprintf(a.Err, "→ %s %s\n", ev.ToolName, ev.Args)
				}
			case agent.EvToolEnd:
				if !a.JSON {
					status := "ok"
					if ev.IsError {
						status = "error"
					}
					fmt.Fprintf(a.Err, "← %s %s\n", ev.ToolName, status)
				}
			case agent.EvNotice:
				if !a.JSON {
					fmt.Fprintf(a.Err, "· %s\n", ev.Notice)
				}
			case agent.EvError:
				if !a.JSON {
					fmt.Fprintf(a.Err, "✗ %s\n", ev.Error)
				}
				exitCode = ExitError
			case agent.EvRunDetached:
				if !a.JSON {
					fmt.Fprintln(a.Err, "· detached to background")
				}
			case agent.EvRunEnd:
				if !a.JSON {
					fmt.Fprintln(a.Out)
				}
				close(done)
				return exitCode, nil
			}
		}
	}
}
