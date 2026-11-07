# Architecture

Pig is one Go binary with one long-lived daemon. The TUI, `pig run` and the
messaging channels are all clients of that daemon.

```
CLI (Bubble Tea) ─┐
pig run            ├── Unix socket, JSON-RPC 2.0 ──► daemon ──► provider chain
Telegram           │                                  │
QQ official bot ───┘                                  ├── SQLite
                                                      ├── job workers
                                                      └── skill servers
```

## Packages

| Package | Role |
|---|---|
| `internal/config` | TOML schema, validation, atomic writes, auth.env |
| `internal/store` | SQLite schema, sessions, entries, artifacts, jobs |
| `internal/provider` | wire adapters, failover runner, usage accounting |
| `internal/agent` | run loop, safe boundaries, tool batches, subagents |
| `internal/compact` | projection, snapcompact, shake, LLM summaries |
| `internal/job` | priority queue, workers, crash recovery |
| `internal/tool` | tool registry, core tools, filesystem + web search, browser |
| `internal/store/memory.go` | durable facts injected into the stable prompt |
| `internal/rtk` | bash output compression |
| `internal/skill` | SKILL.md discovery and tool servers |
| `internal/channel` | adapter contract, pairing and rendering |
| `internal/daemon` | runtime, IPC methods, channels, scheduler |
| `internal/tui` | Bubble Tea client |
| `internal/ipc` | JSON-RPC framing |

## Session model

A session owns an append-only entry log. Entry kinds are `message`,
`tool_call`, `tool_result`, `notice`, `todo`, `job_result` and `compaction`.
Entries are numbered per session and never rewritten, except for `shake`, which
replaces oversized tool results in place.

Compaction writes a `compactions` row plus a `compaction` marker entry. The
projection is the latest summary, then every entry with
`seq >= first_kept_seq`.

## Run lifecycle

```
run.start → append user entry → provider chain → assistant entry
          → tool batch (persisted one result at a time)
          → safe boundary → steering / auto-fork / continue
          → ... → run_end
```

A safe boundary is only between a completed tool batch and the next provider
request. Nothing observes a half-written tool batch.

The system prompt is assembled once per run and contains no session-specific
text before the context-files section: not the cwd, not the todo list, not tool
results. Provider prompt caches match a byte-identical prefix, so a single
varying byte would throw away the whole cache. Volatile state therefore travels
in messages. At each safe boundary the agent runs built-in convergence control:
the second identical tool batch injects a "change approach" steer, the fourth
ends the run as `stalled`, and round 30 adds one wrap-up notice.

## Failover

Each role has an ordered model chain. The runner skips models in cooldown,
retries `timeout`/`network` twice, switches on `context_overflow` when a larger
window exists, and never retries `auth`/`invalid_request` on the same model.
`usage` rows record every successful call with cost.

## Background work

Long turns can leave the foreground. At a safe boundary the daemon forks the
session (`ForkSession`), enqueues a continuation job and ends the current run.
The job result is appended to the parent session as `job_result`, and delivered
to the origin chat when `[background].notify` is on.

## Storage

`$PIG_HOME/pig.db` uses WAL with `synchronous=NORMAL`. Artifacts live under
`$PIG_HOME/artifacts/<session>/<id>` and are addressed as `artifact://<id>`.
