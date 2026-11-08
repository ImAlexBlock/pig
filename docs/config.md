# Configuration

`pig` reads `$PIG_HOME/config.toml` (default `~/.pig/config.toml`). The file is
written with mode `0444`: change it only through `pig config`.

## Commands

```
pig config init --provider mock --model mock
pig config init --provider anthropic --model claude-sonnet-4-5 \
    --context-window 200000 --max-output 64000 --input text,image
pig config get compaction.threshold
pig config set compaction.threshold 0.7
pig config unset ui.theme
pig config validate
```

Model blocks accept optional sampling controls used for reproducible runs:

```toml
[[models]]
ref = "example/model"
temperature = 0.0   # 0..2
top_p = 1.0         # 0..1
seed = 42           # openai-chat only
```

Omitted fields fall back to the provider default.

`pig config set` parses the value as TOML, type-checks it against the schema,
validates the whole document, then rewrites the file atomically with a backup
at `config.toml.bak`. `version` is read-only.

## Layout

| Section | Purpose |
|---|---|
| `[agent]` | system prompt, context files, turn cap |
| `[tools]` | enabled tools, bash timeout, output cap |
| `[daemon]` | socket path, log size, idle exit |
| `[subagents]` | depth cap, default tools, result size |
| `[background]` | auto-fork threshold, queue limits |
| `[providers.*]` | wire kind, base URL, key env var |
| `[[models]]` | model refs, window, output cap, cost |
| `[roles]` | ordered failover chains |
| `[failover]` | cooldown, attempts |
| `[compaction]` | threshold, target, methods |
| `[snapcompact]` | frame geometry and image budgets |
| `[rtk]` | compression switch and cap |
| `[skills]` | search paths, python, timeouts |
| `[browser]` | chrome path, headless, timeout |
| `[search]` | providers, merge, results |
| `[channel.*]` | workdir, reply language, telegram, qq |
| `[ui]` | theme, reasoning, progress interval |

## Validation rules

- `version` must be `1`.
- `models[].ref` must be unique and refer to a declared provider.
- `roles.main` must not be empty; every role ref must exist.
- `roles.compact` and `roles.subagent` fall back to `roles.main` when empty.
- `0 < compaction.threshold < 1`, `0 < compaction.target < threshold`.
- `compaction.methods` is a subset of `snapcompact|shake|llm`.
- `tools.enabled` is a subset of the registered tools.
- `subagents.max_depth` is `0` or `1`.
- `background.max_running_jobs >= 1`.
- `channel.lang` is empty or one of `en|zh|zh-Hant`.

Unknown keys are reported on stderr and ignored. They never block startup.

## Built-in limits

These are constants in the binary, not configuration. A per-deployment knob for
loop guards only invites drift, and the values below are the ones the benchmark
was tuned against.

| Behaviour | Value | Where |
|---|---|---|
| identical tool batch → "change approach" steer | 2nd repeat | `agent.MaxRepeatCalls` |
| identical tool batch → end run as `stalled` | 4th repeat | `agent.MaxRepeatCalls` |
| "wrap up" notice after N tool batches | 30 | `agent.SoftToolRounds` |
| durable facts injected into the prompt | 20 | `tool.InjectFacts` |
| facts stored before trimming the oldest | 200 | `tool.MaxFactStore` |

Facts live in the `memory` table of `pig.db` and are managed through the
`memory` tool (`add|recall|list|remove`). They are read once at the start of a
run, so they never invalidate a warm prompt cache mid-run.

## Secrets

Secrets live in `$PIG_HOME/auth.env` with mode `0600`:

```
pig auth set ANTHROPIC_API_KEY      # prompts without echo on a TTY
pig auth list
pig auth rm ANTHROPIC_API_KEY
```

At startup pig loads the file into the environment. Existing process variables
win. Loaded values are redacted as `***` in logs and tool output.

## Model metadata

A model needs a context window, an output ceiling and its modalities. Writing
them by hand for every relay is noise, so a built-in table fills what the entry
leaves out, keyed on the **standard model name**:

```toml
[[models]]
ref = "relay/gpt-4.1"      # ref is just a label
provider = "relay"
model = "gpt-4.1"          # the standard name resolves the metadata
# context_window = 1000000 # filled from the table
# max_output     = 32768   # filled from the table
# input          = ["text","image"]
```

Explicit values always win. An unknown model without `context_window` and
`max_output` is rejected instead of guessed.

Known families: `claude-sonnet-4`, `claude-opus-4`, `claude-3-7-sonnet`,
`claude-3-5-sonnet`, `claude-3-5-haiku`, `gpt-5`, `gpt-4.1`, `gpt-4o`, `o3`,
`o4-mini`, `deepseek-v4`, `deepseek-reasoner`, `deepseek-v3`, `deepseek-chat`,
`gemini-2.5-pro`, `gemini-2.5-flash`, `qwen3`, `kimi-k2`. Matching is a
case-insensitive prefix on the bare model id, so `claude-sonnet-4-5-20250929`
resolves to the sonnet family.

Inspect and correct it per model:

```
pig models list                     # ref, provider/model, window, max out, input
pig models show relay/gpt-4.1       # resolved values plus the catalog hint
pig models set relay/gpt-4.1 context_window 400000
pig models set relay/gpt-4.1 input '["text","image"]'
pig models add --provider relay --model deepseek-chat   # dimensions optional
```
