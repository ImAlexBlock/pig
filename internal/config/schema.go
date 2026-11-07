package config

// Version is the only supported config schema version.
const Version = 1

// Config is the on-disk configuration ($PIG_HOME/config.toml).
type Config struct {
	Version     int                 `toml:"version" json:"version"`
	Agent       Agent               `toml:"agent" json:"agent"`
	Tools       Tools               `toml:"tools" json:"tools"`
	Daemon      Daemon              `toml:"daemon" json:"daemon"`
	Subagents   Subagents           `toml:"subagents" json:"subagents"`
	Background  Background          `toml:"background" json:"background"`
	Providers   map[string]Provider `toml:"providers" json:"providers"`
	Models      []Model             `toml:"models" json:"models"`
	Roles       Roles               `toml:"roles" json:"roles"`
	Failover    Failover            `toml:"failover" json:"failover"`
	Compaction  Compaction          `toml:"compaction" json:"compaction"`
	Snapcompact Snapcompact         `toml:"snapcompact" json:"snapcompact"`
	RTK         RTK                 `toml:"rtk" json:"rtk"`
	Skills      Skills              `toml:"skills" json:"skills"`
	Browser     Browser             `toml:"browser" json:"browser"`
	Search      Search              `toml:"search" json:"search"`
	Channel     Channel             `toml:"channel" json:"channel"`
	UI          UI                  `toml:"ui" json:"ui"`

	// Unknown collects dotted paths of keys that are not part of the schema.
	Unknown []string `toml:"-" json:"-"`
	// Path is the file this config was loaded from.
	Path string `toml:"-" json:"-"`
}

type Agent struct {
	SystemPrompt string   `toml:"system_prompt" json:"system_prompt"`
	ContextFiles []string `toml:"context_files" json:"context_files"`
	MaxTurns     int      `toml:"max_turns" json:"max_turns"`
}

type Tools struct {
	Enabled            []string `toml:"enabled" json:"enabled"`
	BashTimeoutSeconds int      `toml:"bash_timeout_seconds" json:"bash_timeout_seconds"`
	MaxOutputBytes     int      `toml:"max_output_bytes" json:"max_output_bytes"`
}

type Daemon struct {
	Socket          string `toml:"socket" json:"socket"`
	LogMaxBytes     int64  `toml:"log_max_bytes" json:"log_max_bytes"`
	IdleExitSeconds int    `toml:"idle_exit_seconds" json:"idle_exit_seconds"`
}

type Subagents struct {
	MaxDepth      int      `toml:"max_depth" json:"max_depth"`
	DefaultTools  []string `toml:"default_tools" json:"default_tools"`
	ResultMaxChar int      `toml:"result_max_chars" json:"result_max_chars"`
}

type Background struct {
	Enabled        bool `toml:"enabled" json:"enabled"`
	AutoFork       bool `toml:"auto_fork" json:"auto_fork"`
	PressureSecs   int  `toml:"pressure_seconds" json:"pressure_seconds"`
	PressureTools  int  `toml:"pressure_tools" json:"pressure_tools"`
	MaxRunningJobs int  `toml:"max_running_jobs" json:"max_running_jobs"`
	MaxQueuedJobs  int  `toml:"max_queued_jobs" json:"max_queued_jobs"`
	Notify         bool `toml:"notify" json:"notify"`
}

type Provider struct {
	Kind      string `toml:"kind" json:"kind"`
	BaseURL   string `toml:"base_url" json:"base_url"`
	APIKeyEnv string `toml:"api_key_env" json:"api_key_env"`
}

type Cache struct {
	TTLSeconds int `toml:"ttl_seconds" json:"ttl_seconds"`
}

type Cost struct {
	Input      float64 `toml:"input" json:"input"`
	Output     float64 `toml:"output" json:"output"`
	CacheRead  float64 `toml:"cache_read" json:"cache_read"`
	CacheWrite float64 `toml:"cache_write" json:"cache_write"`
}

type Model struct {
	Ref            string   `toml:"ref" json:"ref"`
	Provider       string   `toml:"provider" json:"provider"`
	Model          string   `toml:"model" json:"model"`
	ContextWindow  int      `toml:"context_window" json:"context_window"`
	MaxOutput      int      `toml:"max_output" json:"max_output"`
	Input          []string `toml:"input" json:"input"`
	Cache          Cache    `toml:"cache" json:"cache"`
	Cost           Cost     `toml:"cost" json:"cost"`
	MaxTokensField string   `toml:"max_tokens_field" json:"max_tokens_field"`
	// Sampling. Nil means "provider default".
	Temperature *float64 `toml:"temperature" json:"temperature,omitempty"`
	TopP        *float64 `toml:"top_p" json:"top_p,omitempty"`
	Seed        *int64   `toml:"seed" json:"seed,omitempty"`
}

// SupportsImages reports whether the model accepts image input.
func (m Model) SupportsImages() bool {
	for _, in := range m.Input {
		if in == "image" {
			return true
		}
	}
	return false
}

type Roles struct {
	Main     []string `toml:"main" json:"main"`
	Compact  []string `toml:"compact" json:"compact"`
	Subagent []string `toml:"subagent" json:"subagent"`
}

type Failover struct {
	CooldownSeconds    int `toml:"cooldown_seconds" json:"cooldown_seconds"`
	CooldownMaxSeconds int `toml:"cooldown_max_seconds" json:"cooldown_max_seconds"`
	MaxAttempts        int `toml:"max_attempts" json:"max_attempts"`
}

type Compaction struct {
	Threshold          float64  `toml:"threshold" json:"threshold"`
	Target             float64  `toml:"target" json:"target"`
	IdleThreshold      float64  `toml:"idle_threshold" json:"idle_threshold"`
	IdleTimeoutSeconds int      `toml:"idle_timeout_seconds" json:"idle_timeout_seconds"`
	KeepRecentTokens   int      `toml:"keep_recent_tokens" json:"keep_recent_tokens"`
	Methods            []string `toml:"methods" json:"methods"`
	MidTurn            bool     `toml:"mid_turn" json:"mid_turn"`
}

type Snapcompact struct {
	Summarize            bool `toml:"summarize" json:"summarize"`
	FrameWidth           int  `toml:"frame_width" json:"frame_width"`
	MaxFrames            int  `toml:"max_frames" json:"max_frames"`
	MaxFrameHeight       int  `toml:"max_frame_height" json:"max_frame_height"`
	MaxImagesPerRequest  int  `toml:"max_images_per_request" json:"max_images_per_request"`
	MaxRequestImageBytes int  `toml:"max_request_image_bytes" json:"max_request_image_bytes"`
	ImageTokens          int  `toml:"image_tokens" json:"image_tokens"`
	ToolResultMaxChars   int  `toml:"tool_result_max_chars" json:"tool_result_max_chars"`
	ToolArgMaxChars      int  `toml:"tool_arg_max_chars" json:"tool_arg_max_chars"`
}

type RTK struct {
	Enabled        bool `toml:"enabled" json:"enabled"`
	MaxOutputBytes int  `toml:"max_output_bytes" json:"max_output_bytes"`
}

type Skills struct {
	Paths              []string `toml:"paths" json:"paths"`
	Python             string   `toml:"python" json:"python"`
	CallTimeoutSeconds int      `toml:"call_timeout_seconds" json:"call_timeout_seconds"`
	IdleTimeoutSeconds int      `toml:"idle_timeout_seconds" json:"idle_timeout_seconds"`
}

type Browser struct {
	ChromePath       string `toml:"chrome_path" json:"chrome_path"`
	Headless         bool   `toml:"headless" json:"headless"`
	TimeoutSeconds   int    `toml:"timeout_seconds" json:"timeout_seconds"`
	MaxSnapshotChars int    `toml:"max_snapshot_chars" json:"max_snapshot_chars"`
}

type Search struct {
	Providers  []string `toml:"providers" json:"providers"`
	Merge      bool     `toml:"merge" json:"merge"`
	MaxResults int      `toml:"max_results" json:"max_results"`
	FetchPages int      `toml:"fetch_pages" json:"fetch_pages"`
}

type Channel struct {
	Workdir  string          `toml:"workdir" json:"workdir"`
	Telegram TelegramChannel `toml:"telegram" json:"telegram"`
	QQ       QQChannel       `toml:"qq" json:"qq"`
}

type TelegramChannel struct {
	Enabled  bool   `toml:"enabled" json:"enabled"`
	TokenEnv string `toml:"token_env" json:"token_env"`
}

type QQChannel struct {
	Enabled      bool   `toml:"enabled" json:"enabled"`
	AppID        string `toml:"app_id" json:"app_id"`
	AppSecretEnv string `toml:"app_secret_env" json:"app_secret_env"`
	APIBase      string `toml:"api_base" json:"api_base"`
	Markdown     bool   `toml:"markdown" json:"markdown"`
}

type UI struct {
	Theme                   string `toml:"theme" json:"theme"`
	ShowReasoning           bool   `toml:"show_reasoning" json:"show_reasoning"`
	ProgressIntervalSeconds int    `toml:"progress_interval_seconds" json:"progress_interval_seconds"`
}

// Default returns the documented default configuration.
func Default() *Config {
	return &Config{
		Version: Version,
		Agent: Agent{
			ContextFiles: []string{"AGENTS.md"},
		},
		Tools: Tools{
			Enabled:            []string{"read", "write", "edit", "bash", "todo", "task", "job", "browser", "search", "memory"},
			BashTimeoutSeconds: 120,
			MaxOutputBytes:     65536,
		},
		Daemon: Daemon{
			LogMaxBytes:     10485760,
			IdleExitSeconds: 0,
		},
		Subagents: Subagents{
			MaxDepth:      1,
			DefaultTools:  []string{"read", "write", "edit", "bash", "todo", "browser", "search", "memory"},
			ResultMaxChar: 8000,
		},
		Background: Background{
			Enabled:        true,
			AutoFork:       true,
			PressureSecs:   30,
			PressureTools:  8,
			MaxRunningJobs: 4,
			MaxQueuedJobs:  16,
			Notify:         true,
		},
		Providers: map[string]Provider{},
		Roles:     Roles{},
		Failover: Failover{
			CooldownSeconds:    60,
			CooldownMaxSeconds: 900,
			MaxAttempts:        4,
		},
		Compaction: Compaction{
			Threshold:          0.75,
			Target:             0.80,
			IdleThreshold:      0.55,
			IdleTimeoutSeconds: 300,
			KeepRecentTokens:   20000,
			Methods:            []string{"snapcompact", "shake", "llm"},
			MidTurn:            true,
		},
		Snapcompact: Snapcompact{
			Summarize:            true,
			FrameWidth:           1568,
			MaxFrames:            40,
			MaxFrameHeight:       2048,
			MaxImagesPerRequest:  16,
			MaxRequestImageBytes: 3000000,
			ImageTokens:          1100,
			ToolResultMaxChars:   2000,
			ToolArgMaxChars:      2000,
		},
		RTK: RTK{
			Enabled:        true,
			MaxOutputBytes: 16384,
		},
		Skills: Skills{
			Python:             "python3",
			CallTimeoutSeconds: 120,
			IdleTimeoutSeconds: 60,
		},
		Browser: Browser{
			Headless:         true,
			TimeoutSeconds:   30,
			MaxSnapshotChars: 20000,
		},
		Search: Search{
			Providers:  []string{"duckduckgo", "brave", "tavily", "exa", "serper"},
			MaxResults: 8,
			FetchPages: 2,
		},
		Channel: Channel{
			Telegram: TelegramChannel{TokenEnv: "PIG_TELEGRAM_TOKEN"},
			QQ: QQChannel{
				AppSecretEnv: "PIG_QQ_APP_SECRET",
				APIBase:      "https://api.bot.qq.com",
				Markdown:     true,
			},
		},
		UI: UI{
			Theme:                   "auto",
			ProgressIntervalSeconds: 90,
		},
	}
}

// Model returns the model with the given ref.
func (c *Config) Model(ref string) (Model, bool) {
	for _, m := range c.Models {
		if m.Ref == ref {
			return m, true
		}
	}
	return Model{}, false
}

// RoleChain returns the ordered model refs for a role, falling back to
// roles.main for compact/subagent when unset.
func (c *Config) RoleChain(role string) []string {
	switch role {
	case "compact":
		if len(c.Roles.Compact) > 0 {
			return c.Roles.Compact
		}
	case "subagent":
		if len(c.Roles.Subagent) > 0 {
			return c.Roles.Subagent
		}
	}
	return c.Roles.Main
}
