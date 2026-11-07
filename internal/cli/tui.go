package cli

import (
	"context"
	"flag"
	"io"

	"github.com/alexblock/pig/internal/store"
	"github.com/alexblock/pig/internal/tui"
)

func (a *App) cmdTUI(args []string) (int, error) {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var sessionID, model string
	fs.StringVar(&sessionID, "session", "", "session id")
	fs.StringVar(&model, "model", "", "model ref")
	if err := fs.Parse(args); err != nil {
		return ExitUsage, usagef("%v", err)
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

	if sessionID == "" {
		var out struct {
			Session *store.Session `json:"session"`
		}
		if err := a.call(ctx, client, "session.load", map[string]any{"tail": 0}, &out); err == nil && out.Session != nil {
			sessionID = out.Session.ID
		} else {
			var created struct {
				Session *store.Session `json:"session"`
			}
			if err := a.call(ctx, client, "session.new", map[string]any{"cwd": a.CWD}, &created); err == nil && created.Session != nil {
				sessionID = created.Session.ID
			}
		}
	}
	if err := tui.Run(ctx, tui.Options{
		Client:    client,
		Config:    a.Config,
		Paths:     a.Paths,
		SessionID: sessionID,
		Model:     model,
	}); err != nil {
		return ExitError, err
	}
	return ExitOK, nil
}
