# Pig

A personal assistant in a single Go binary. One long-lived daemon owns sessions,
jobs and channels; the terminal UI, `pig run` and the messaging bots are thin
clients over a Unix socket.

```
CLI (Bubble Tea) ─┐
pig run            ├── Unix socket, JSON-RPC 2.0 ──► daemon ──► provider chain
Telegram           │                                  ├── SQLite
QQ official bot ───┘                                  ├── job workers
                                                      └── skill tool servers
```

## Features

- **One binary, one daemon.** `pig daemon start` detaches itself; the TUI and
  `pig run` reconnect.
- **Provider failover.** Ordered chains per role with failure classification,
  cooldown, sticky success, prompt-cache-friendly requests and usage/cost
  accounting. Wire adapters: `anthropic`, `openai-chat`, `openai-responses`,
  `mock`.
- **Tool loop with safe boundaries.** Steering, queueing, auto-fork of long
  turns into background jobs, subagents with their own sessions, built-in
  convergence control (repeat/stall detection, wrap-up notice).
- **Context maintenance.** snapcompact archives old turns as glyph-rendered PNG
  frames, `shake` elides oversized tool output, plus LLM summaries and an idle
  scheduler.
- **Tools.** `read`, `write`, `edit`, `bash`, `todo`, `task`, `job`, `browser`,
  `search` (web and filesystem), `memory` (durable facts).
- **Bash output compression (RTK)** with a rule table and a savings ledger.
- **Skills.** `SKILL.md` discovery plus stdio JSON-RPC tool servers.
- **Channels.** Telegram long polling and the QQ official bot v2 gateway, with
  pairing codes, slash commands and job notifications.
- **Read-only config.** `config.toml` is `0444` and only changes through
  `pig config`; secrets live in `auth.env` (`0600`) via `pig auth`.

## Quick start

```bash
go build -o pig ./cmd/pig

export PIG_HOME=~/.pig
./pig config init --provider anthropic --model claude-sonnet-4-5 \
    --context-window 200000 --max-output 64000 --input text,image
./pig auth set ANTHROPIC_API_KEY      # prompts without echo

./pig daemon start
./pig run "summarize this repo"        # one shot
./pig tui                              # terminal UI (default command)
```

Channels are optional:

```bash
./pig auth set PIG_TELEGRAM_TOKEN
./pig config set channel.telegram.enabled true
./pig serve                            # foreground daemon, requires a channel
```

`pig help` lists every command; `pig docs` prints the embedded documentation.

## Layout

| Path | Contents |
|---|---|
| `cmd/pig` | entry point |
| `cmd/genfont` | rasterises the snapcompact glyph table |
| `internal/config` | TOML schema, validation, atomic writes, `auth.env` |
| `internal/store` | SQLite: sessions, entries, compactions, jobs, facts |
| `internal/provider` | wire adapters, failover runner, usage accounting |
| `internal/agent` | run loop, boundaries, convergence, subagents |
| `internal/compact` | projection, snapcompact, shake, LLM summaries |
| `internal/job` | priority queue, workers, crash recovery |
| `internal/tool` | registry and the built-in tools |
| `internal/rtk` | bash output compression |
| `internal/skill` | skill discovery and tool servers |
| `internal/channel` | channel contract, pairing, rendering |
| `internal/i18n` | reply catalogs: en, zh, zh-Hant |
| `internal/daemon` | runtime, IPC methods, channels, scheduler |
| `internal/tui` | Bubble Tea client |
| `internal/ipc` | JSON-RPC framing over a Unix socket |
| `bench` | agent benchmark harness (hermesbench 61-task adapter) |
| `docs` | embedded docs: config, architecture, daemon, skill authoring |
| `test` | `e2e.sh` |

## Tests

```bash
go build ./... && go vet ./...
go test ./...        # 84 tests
./test/e2e.sh        # daemon lifecycle, tools, jobs, config, rtk, snapcompact
```

## Benchmark

`bench/run_hermesbench.py` runs the deterministic 61-task suite from
[am423/hermes-bench-tool-call](https://github.com/am423/hermes-bench-tool-call)
against Pig (or any harness with a JSON event stream), scores it with the
suite's own verifiers and reports pass@1, tokens, cost at published unit prices
and wall-clock time. `bench/README.md` documents the tool mapping, the
deviations and the measured results.

## Status

macOS and Linux. Windows is not supported: `flock`, `Setsid` and Unix sockets
are used directly.

No credentials are stored in this repository. `auth.env`, `config.toml`, the
database, artifacts and benchmark worktrees are git-ignored.
