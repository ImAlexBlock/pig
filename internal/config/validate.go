package config

import (
	"fmt"

	"github.com/ImAlexBlock/pig/internal/i18n"
)

// KnownTools is the set of tool names the binary registers. tools.enabled must
// be a subset of it.
var KnownTools = []string{"read", "write", "edit", "bash", "todo", "task", "job", "browser", "search", "memory", "send"}

// KnownProviders is the set of supported provider wire kinds.
var KnownProviders = []string{"anthropic", "openai-chat", "openai-responses", "mock"}

// KnownMethods is the set of supported compaction methods.
var KnownMethods = []string{"snapcompact", "shake", "llm"}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func errf(key, format string, args ...any) error {
	return fmt.Errorf("config: %s: %s", key, fmt.Sprintf(format, args...))
}

// Validate checks schema invariants. Errors read `config: <key>: <reason>`.
func Validate(c *Config) error {
	if c == nil {
		return errf("config", "nil")
	}
	if c.Version != Version {
		return errf("version", "unsupported (%d)", c.Version)
	}

	// Models.
	seen := map[string]bool{}
	for i, m := range c.Models {
		key := fmt.Sprintf("models[%d]", i)
		if m.Ref == "" {
			return errf(key+".ref", "required")
		}
		if seen[m.Ref] {
			return errf(key+".ref", "duplicate %q", m.Ref)
		}
		seen[m.Ref] = true
		if m.Provider == "" {
			return errf(key+".provider", "required")
		}
		if _, ok := c.Providers[m.Provider]; !ok {
			return errf(key+".provider", "unknown provider %q", m.Provider)
		}
		if m.Model == "" {
			return errf(key+".model", "required")
		}
		if m.ContextWindow <= 0 {
			return errf(key+".context_window", "must be positive")
		}
		if m.MaxOutput <= 0 {
			return errf(key+".max_output", "must be positive")
		}
		if m.MaxOutput > m.ContextWindow {
			return errf(key+".max_output", "must not exceed context_window")
		}
		if m.Temperature != nil && (*m.Temperature < 0 || *m.Temperature > 2) {
			return errf(key+".temperature", "must be between 0 and 2")
		}
		if m.TopP != nil && (*m.TopP <= 0 || *m.TopP > 1) {
			return errf(key+".top_p", "must be between 0 and 1")
		}
		if m.Seed != nil && *m.Seed < 0 {
			return errf(key+".seed", "must not be negative")
		}
		for _, in := range m.Input {
			if in != "text" && in != "image" {
				return errf(key+".input", "unknown input %q", in)
			}
		}
	}

	for name, p := range c.Providers {
		key := "providers." + name
		if !contains(KnownProviders, p.Kind) {
			return errf(key+".kind", "unknown kind %q", p.Kind)
		}
		if p.BaseURL == "" {
			return errf(key+".base_url", "required")
		}
	}

	// Roles.
	if len(c.Roles.Main) == 0 {
		return errf("roles.main", "must not be empty")
	}
	roles := map[string][]string{
		"roles.main":     c.Roles.Main,
		"roles.compact":  c.Roles.Compact,
		"roles.subagent": c.Roles.Subagent,
	}
	for key, chain := range roles {
		for i, ref := range chain {
			if !seen[ref] {
				return errf(fmt.Sprintf("%s[%d]", key, i), "unknown model %q", ref)
			}
		}
	}

	// Failover.
	if c.Failover.CooldownSeconds < 0 {
		return errf("failover.cooldown_seconds", "must not be negative")
	}
	if c.Failover.CooldownMaxSeconds < c.Failover.CooldownSeconds {
		return errf("failover.cooldown_max_seconds", "must be at least cooldown_seconds")
	}
	if c.Failover.MaxAttempts < 1 {
		return errf("failover.max_attempts", "must be at least 1")
	}

	// Channel.
	if _, ok := i18n.Parse(c.Channel.Lang); !ok && c.Channel.Lang != "" {
		return errf("channel.lang", "unknown language %q (en, zh, zh-Hant)", c.Channel.Lang)
	}

	// Compaction.
	if !(c.Compaction.Threshold > 0 && c.Compaction.Threshold < 1) {
		return errf("compaction.threshold", "must be between 0 and 1")
	}
	if !(c.Compaction.Target > 0 && c.Compaction.Target < 1) {
		return errf("compaction.target", "must be between 0 and 1")
	}
	if !(c.Compaction.IdleThreshold > 0 && c.Compaction.IdleThreshold < 1) {
		return errf("compaction.idle_threshold", "must be between 0 and 1")
	}
	if c.Compaction.KeepRecentTokens < 0 {
		return errf("compaction.keep_recent_tokens", "must not be negative")
	}
	for _, m := range c.Compaction.Methods {
		if !contains(KnownMethods, m) {
			return errf("compaction.methods", "unknown method %q", m)
		}
	}

	// Snapcompact.
	if c.Snapcompact.FrameWidth <= 0 {
		return errf("snapcompact.frame_width", "must be positive")
	}
	if c.Snapcompact.MaxFrames < 1 {
		return errf("snapcompact.max_frames", "must be at least 1")
	}
	if c.Snapcompact.ImageTokens < 1 {
		return errf("snapcompact.image_tokens", "must be at least 1")
	}

	// Tools.
	for _, t := range c.Tools.Enabled {
		if !contains(KnownTools, t) {
			return errf("tools.enabled", "unknown tool %q", t)
		}
	}
	if c.Tools.BashTimeoutSeconds < 1 {
		return errf("tools.bash_timeout_seconds", "must be at least 1")
	}
	if c.Tools.MaxOutputBytes < 1024 {
		return errf("tools.max_output_bytes", "must be at least 1024")
	}

	// Daemon.
	if c.Daemon.LogMaxBytes < 0 {
		return errf("daemon.log_max_bytes", "must not be negative")
	}
	if c.Daemon.IdleExitSeconds < 0 {
		return errf("daemon.idle_exit_seconds", "must not be negative")
	}

	// Subagents / background.
	if c.Subagents.MaxDepth < 0 || c.Subagents.MaxDepth > 1 {
		return errf("subagents.max_depth", "must be 0 or 1")
	}
	if c.Subagents.ResultMaxChar < 0 {
		return errf("subagents.result_max_chars", "must not be negative")
	}
	for _, t := range c.Subagents.DefaultTools {
		if !contains(KnownTools, t) {
			return errf("subagents.default_tools", "unknown tool %q", t)
		}
	}
	if c.Background.MaxRunningJobs < 1 {
		return errf("background.max_running_jobs", "must be at least 1")
	}
	if c.Background.MaxQueuedJobs < 0 {
		return errf("background.max_queued_jobs", "must not be negative")
	}
	if c.Background.PressureSecs < 0 {
		return errf("background.pressure_seconds", "must not be negative")
	}
	if c.Background.PressureTools < 0 {
		return errf("background.pressure_tools", "must not be negative")
	}

	// RTK.
	if c.RTK.MaxOutputBytes < 1024 {
		return errf("rtk.max_output_bytes", "must be at least 1024")
	}

	// Browser / search.
	if c.Browser.TimeoutSeconds < 1 {
		return errf("browser.timeout_seconds", "must be at least 1")
	}
	if c.Search.MaxResults < 1 {
		return errf("search.max_results", "must be at least 1")
	}
	for _, p := range c.Search.Providers {
		switch p {
		case "duckduckgo", "brave", "tavily", "exa", "serper":
		default:
			return errf("search.providers", "unknown provider %q", p)
		}
	}

	// Agent.
	if c.Agent.MaxTurns < 0 {
		return errf("agent.max_turns", "must not be negative")
	}

	// UI.
	if c.UI.ProgressIntervalSeconds < 1 {
		return errf("ui.progress_interval_seconds", "must be at least 1")
	}
	return nil
}
