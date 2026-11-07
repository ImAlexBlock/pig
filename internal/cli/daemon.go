package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alexblock/pig/internal/daemon"
)

func (a *App) cmdDaemon(args []string) (int, error) {
	if len(args) == 0 {
		return ExitUsage, usagef("usage: pig daemon start|stop|status|reload|logs|run")
	}
	action, rest := args[0], args[1:]
	switch action {
	case "start":
		if err := a.requireConfig(); err != nil {
			return ExitConfig, err
		}
		if err := a.Paths.Ensure(); err != nil {
			return ExitStorage, err
		}
		if err := daemon.Start(a.Config, a.Paths); err != nil {
			return ExitDaemon, err
		}
		ctx := context.Background()
		st, err := daemon.Status(ctx, a.Paths.Socket(a.Config.Daemon.Socket))
		if err != nil {
			return ExitDaemon, err
		}
		if a.JSON {
			a.printJSON(st)
		} else {
			fmt.Fprintf(a.Out, "daemon running (pid %v)\n", st["pid"])
		}
		return ExitOK, nil
	case "stop":
		if err := a.requireConfig(); err != nil {
			return ExitConfig, err
		}
		if err := daemon.Stop(context.Background(), a.Config, a.Paths); err != nil {
			if err == daemon.ErrNotRunning {
				fmt.Fprintln(a.Err, "pig: daemon: not running")
				return ExitDaemon, nil
			}
			return ExitDaemon, err
		}
		fmt.Fprintln(a.Out, "daemon stopped")
		return ExitOK, nil
	case "status":
		if err := a.requireConfig(); err != nil {
			return ExitConfig, err
		}
		st, err := daemon.Status(context.Background(), a.Paths.Socket(a.Config.Daemon.Socket))
		if err != nil {
			fmt.Fprintln(a.Err, "pig: daemon: not running")
			return ExitDaemon, nil
		}
		if a.JSON {
			a.printJSON(st)
		} else {
			fmt.Fprintf(a.Out, "pid: %v\nuptime: %vs\nversion: %v\nruns: %v\njobs: %v queued, %v running\n",
				st["pid"], st["uptime"], st["version"], st["runs"],
				asMap(st["jobs"])["queued"], asMap(st["jobs"])["running"])
		}
		return ExitOK, nil
	case "reload":
		if err := a.requireConfig(); err != nil {
			return ExitConfig, err
		}
		ctx := context.Background()
		c, err := a.client(ctx)
		if err != nil {
			return ExitDaemon, err
		}
		defer c.Close()
		if err := a.call(ctx, c, "daemon.reload", nil, nil); err != nil {
			return ExitDaemon, err
		}
		fmt.Fprintln(a.Out, "config reloaded")
		return ExitOK, nil
	case "logs":
		return a.daemonLogs(rest)
	case "run":
		return a.daemonForeground(rest)
	}
	return ExitUsage, usagef("unknown daemon action %q", action)
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func (a *App) daemonLogs(args []string) (int, error) {
	fs := flag.NewFlagSet("daemon logs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	follow := fs.Bool("f", false, "follow the log")
	if err := fs.Parse(args); err != nil {
		return ExitUsage, usagef("%v", err)
	}
	path := a.Paths.Log()
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(a.Err, "pig: daemon: no log file")
		return ExitDaemon, nil
	}
	defer f.Close()
	if _, err := io.Copy(a.Out, f); err != nil {
		return ExitError, err
	}
	if !*follow {
		return ExitOK, nil
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			fmt.Fprint(a.Out, line)
			continue
		}
		if err == io.EOF {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if err != nil {
			return ExitError, err
		}
	}
}

func (a *App) cmdServe(args []string) (int, error) {
	return a.daemonForeground(append([]string{"--foreground", "--require-channel"}, args...))
}

func (a *App) daemonForeground(args []string) (int, error) {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var foreground, requireChannel bool
	fs.BoolVar(&foreground, "foreground", false, "run in the foreground")
	fs.BoolVar(&requireChannel, "require-channel", false, "fail when no channel is enabled")
	if err := fs.Parse(args); err != nil {
		return ExitUsage, usagef("%v", err)
	}
	if err := a.requireConfig(); err != nil {
		return ExitConfig, err
	}
	if err := a.Paths.Ensure(); err != nil {
		return ExitStorage, err
	}
	release, err := daemon.AcquireLock(a.Paths.Lock())
	if err != nil {
		fmt.Fprintln(a.Err, "pig: daemon: already running")
		return ExitDaemon, nil
	}
	defer release()

	logger, err := daemon.NewLogger(a.Paths.Log(), a.Config.Daemon.LogMaxBytes)
	if err != nil {
		return ExitStorage, err
	}
	if !foreground {
		foreground = true
	}
	srv, err := daemon.New(daemon.Options{
		Config:         a.Config,
		Paths:          a.Paths,
		Logger:         logger,
		RequireChannel: requireChannel,
	})
	if err != nil {
		return ExitStorage, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sig {
			switch s {
			case syscall.SIGHUP:
				logger.Printf("reload requested")
			default:
				cancel()
				return
			}
		}
	}()
	fmt.Fprintf(a.Err, "pig: daemon listening on %s\n", a.Paths.Socket(a.Config.Daemon.Socket))
	if err := srv.Run(ctx); err != nil {
		if strings.Contains(err.Error(), "no channels enabled") {
			return ExitError, err
		}
		return ExitDaemon, err
	}
	return ExitOK, nil
}
