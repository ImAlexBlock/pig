package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ImAlexBlock/pig/internal/store"
)

func (a *App) cmdJobs(args []string) (int, error) {
	if len(args) == 0 {
		args = []string{"list"}
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

	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("jobs list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		status := fs.String("status", "", "queued|running|done|failed|canceled|interrupted")
		limit := fs.Int("limit", 20, "how many jobs")
		if err := fs.Parse(args[1:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		var out struct {
			Jobs []*store.Job `json:"jobs"`
		}
		if err := a.call(ctx, c, "jobs.list", map[string]any{"status": *status, "limit": *limit}, &out); err != nil {
			return ExitError, err
		}
		if a.JSON {
			a.printJSON(out.Jobs)
			return ExitOK, nil
		}
		for _, j := range out.Jobs {
			fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", j.ID, j.Kind, j.Status, time.Unix(j.CreatedAt, 0).Format("2006-01-02 15:04"))
		}
		return ExitOK, nil
	case "show":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig jobs show <id>")
		}
		var out struct {
			Job *store.Job `json:"job"`
		}
		if err := a.call(ctx, c, "jobs.get", map[string]any{"id": args[1]}, &out); err != nil {
			return ExitError, err
		}
		if a.JSON {
			a.printJSON(out.Job)
			return ExitOK, nil
		}
		fmt.Fprintf(a.Out, "%s %s %s\n", out.Job.ID, out.Job.Kind, out.Job.Status)
		if out.Job.Error != "" {
			fmt.Fprintf(a.Out, "error: %s\n", out.Job.Error)
		}
		if out.Job.Result != "" {
			fmt.Fprintf(a.Out, "result:\n%s\n", out.Job.Result)
		}
		return ExitOK, nil
	case "wait":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig jobs wait <id> [--timeout 60]")
		}
		fs := flag.NewFlagSet("jobs wait", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		timeout := fs.Int("timeout", 60, "seconds to wait")
		if err := fs.Parse(args[2:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		var out struct {
			Job *store.Job `json:"job"`
		}
		if err := a.call(ctx, c, "jobs.wait", map[string]any{"id": args[1], "timeout_seconds": *timeout}, &out); err != nil {
			fmt.Fprintf(a.Err, "pig: %v\n", err)
			return ExitUsage, nil
		}
		if a.JSON {
			a.printJSON(out.Job)
		} else {
			fmt.Fprintf(a.Out, "%s %s\n", out.Job.ID, out.Job.Status)
			if out.Job.Result != "" {
				fmt.Fprintln(a.Out, out.Job.Result)
			}
		}
		switch out.Job.Status {
		case store.JobDone:
			return ExitOK, nil
		default:
			return ExitError, nil
		}
	case "cancel":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig jobs cancel <id>")
		}
		if err := a.call(ctx, c, "jobs.cancel", map[string]any{"id": args[1]}, nil); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "canceled %s\n", args[1])
		return ExitOK, nil
	case "retry":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig jobs retry <id>")
		}
		if err := a.call(ctx, c, "jobs.retry", map[string]any{"id": args[1]}, nil); err != nil {
			return ExitError, err
		}
		fmt.Fprintf(a.Out, "queued %s\n", args[1])
		return ExitOK, nil
	case "logs":
		if len(args) < 2 {
			return ExitUsage, usagef("usage: pig jobs logs <id> [--tail 50]")
		}
		fs := flag.NewFlagSet("jobs logs", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		tail := fs.Int("tail", 50, "lines")
		if err := fs.Parse(args[2:]); err != nil {
			return ExitUsage, usagef("%v", err)
		}
		var out struct {
			Text string `json:"text"`
		}
		if err := a.call(ctx, c, "jobs.logs", map[string]any{"id": args[1], "tail": *tail}, &out); err != nil {
			return ExitError, err
		}
		fmt.Fprint(a.Out, out.Text)
		return ExitOK, nil
	}
	return ExitUsage, usagef("unknown jobs action %q", args[0])
}
