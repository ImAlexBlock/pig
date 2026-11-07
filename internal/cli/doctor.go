package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/alexblock/pig/internal/config"
	"github.com/alexblock/pig/internal/daemon"
)

func (a *App) cmdDoctor(args []string) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	strict := fs.Bool("strict", false, "exit non-zero when a check fails")
	if err := fs.Parse(args); err != nil {
		return ExitUsage, usagef("%v", err)
	}
	type check struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Info string `json:"info,omitempty"`
	}
	var results []check
	add := func(name string, ok bool, info string) {
		results = append(results, check{Name: name, OK: ok, Info: info})
	}

	cfg, err := config.Load(a.Paths.Config())
	switch {
	case err != nil && a.Mock:
		cfg = config.MockConfig(a.Paths.Config())
		add("config", true, "mock config in memory")
	case err != nil:
		add("config", false, err.Error())
	default:
		add("config", true, cfg.Path)
	}

	if cfg != nil {
		add("roles.main", len(cfg.Roles.Main) > 0, fmt.Sprintf("%d model(s)", len(cfg.Roles.Main)))
		_, _ = config.ApplyAuth(a.Paths.Auth())
		for _, name := range cfg.SortedProviders() {
			p := cfg.Providers[name]
			if p.Kind == "mock" || p.APIKeyEnv == "" {
				add("provider "+name, true, "no key required")
				continue
			}
			_, present := os.LookupEnv(p.APIKeyEnv)
			add("provider "+name, present, p.APIKeyEnv)
		}
	}

	if st, err := a.openStore(); err != nil {
		add("store", false, err.Error())
	} else {
		add("store", true, a.Paths.DB())
		st.Close()
	}

	if py := pythonPath(cfg); py != "" {
		out, err := exec.Command(py, "-c", "print(1)").Output()
		add("python", err == nil && string(out) == "1\n", py)
	} else {
		add("python", false, "not configured")
	}

	if ch, err := chromeAvailable(); err != nil {
		add("chrome", false, err.Error())
	} else {
		add("chrome", true, ch)
	}

	if cfg != nil {
		socket := a.Paths.Socket(cfg.Daemon.Socket)
		if st, err := daemon.Status(context.Background(), socket); err == nil {
			add("daemon", true, fmt.Sprintf("pid %v", st["pid"]))
		} else {
			add("daemon", false, "not running")
		}
		if cfg.Channel.Telegram.Enabled {
			_, ok := os.LookupEnv(envOr(cfg.Channel.Telegram.TokenEnv, "PIG_TELEGRAM_TOKEN"))
			add("telegram", ok, "token")
		}
		if cfg.Channel.QQ.Enabled {
			ok := cfg.Channel.QQ.AppID != ""
			_, secret := os.LookupEnv(envOr(cfg.Channel.QQ.AppSecretEnv, "PIG_QQ_APP_SECRET"))
			add("qq", ok && secret, "app_id and secret")
		}
	}

	failures := 0
	for _, r := range results {
		status := "ok"
		if !r.OK {
			status = "missing"
			failures++
		}
		if a.JSON {
			a.printNDJSON(r)
			continue
		}
		fmt.Fprintf(a.Out, "%-18s %-8s %s\n", r.Name, status, r.Info)
	}
	if *strict && failures > 0 {
		return ExitError, nil
	}
	return ExitOK, nil
}

func envOr(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func pythonPath(cfg *config.Config) string {
	if cfg != nil && cfg.Skills.Python != "" {
		return cfg.Skills.Python
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p
	}
	return ""
}

func chromeAvailable() (string, error) {
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("chrome not found")
}
