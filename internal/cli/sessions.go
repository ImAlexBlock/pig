package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/ImAlexBlock/pig/internal/internalurl"
	"github.com/ImAlexBlock/pig/internal/store"
)

func (a *App) cmdSessions(args []string) (int, error) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("sessions list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		limit := fs.Int("limit", 20, "how many sessions")
		if err := fs.Parse(args[1:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		st, err := a.openStore()
		if err != nil {
			return ExitStorage, err
		}
		defer st.Close()
		sessions, err := st.ListSessions(*limit)
		if err != nil {
			return ExitError, err
		}
		if a.JSON {
			a.printJSON(sessions)
			return ExitOK, nil
		}
		for _, s := range sessions {
			fmt.Fprintf(a.Out, "%s\t%s\t%d entries\t%s\n", s.ID, s.Model, countEntries(st, s.ID), time.Unix(s.UpdatedAt, 0).Format("2006-01-02 15:04"))
		}
		return ExitOK, nil
	case "show":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig sessions show <id> [--limit n]")
		}
		limit := 50
		fs := flag.NewFlagSet("sessions show", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.IntVar(&limit, "limit", 50, "entries to show")
		if err := fs.Parse(args[2:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		st, err := a.openStore()
		if err != nil {
			return ExitStorage, err
		}
		defer st.Close()
		sess, err := st.GetSession(args[1])
		if err != nil {
			return ExitError, err
		}
		entries, err := st.TailEntries(sess.ID, limit)
		if err != nil {
			return ExitError, err
		}
		if a.JSON {
			a.printJSON(map[string]any{"session": sess, "entries": entries})
			return ExitOK, nil
		}
		fmt.Fprintf(a.Out, "%s  model=%s  cwd=%s\n\n", sess.ID, sess.Model, sess.CWD)
		for _, e := range entries {
			fmt.Fprintln(a.Out, internalurl.RenderEntry(e))
		}
		return ExitOK, nil
	case "rm":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig sessions rm <id>")
		}
		if err := a.requireConfig(); err != nil {
			return ExitConfig, err
		}
		ctx := context.Background()
		c, err := a.client(ctx)
		if err != nil {
			return ExitDaemon, err
		}
		defer c.Close()
		defer a.closeInProcess()
		if err := a.call(ctx, c, "session.rm", map[string]any{"id": args[1]}, nil); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "removed %s\n", args[1])
		return ExitOK, nil
	case "export":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig sessions export <id> [--out file]")
		}
		fs := flag.NewFlagSet("sessions export", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		out := fs.String("out", "", "output file")
		if err := fs.Parse(args[2:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		st, err := a.openStore()
		if err != nil {
			return ExitStorage, err
		}
		defer st.Close()
		sess, err := st.GetSession(args[1])
		if err != nil {
			return ExitError, err
		}
		entries, err := st.AllEntries(sess.ID)
		if err != nil {
			return ExitError, err
		}
		payload := map[string]any{"session": sess, "entries": entries}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return ExitError, err
		}
		if *out == "" {
			fmt.Fprintln(a.Out, string(b))
			return ExitOK, nil
		}
		if err := writeFile(*out, b); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "wrote %s\n", *out)
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown sessions action %q", args[0])
}

func countEntries(st *store.Store, sessionID string) int {
	entries, err := st.AllEntries(sessionID)
	if err != nil {
		return 0
	}
	return len(entries)
}

func writeFile(path string, data []byte) error {
	abs := absPath(path)
	if err := ensureDir(filepath.Dir(abs)); err != nil {
		return err
	}
	return osWriteFile(abs, data)
}
