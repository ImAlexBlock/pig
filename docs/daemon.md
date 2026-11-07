# Daemon

```
pig daemon start              # spawn a detached daemon
pig daemon run --foreground   # run in the foreground
pig serve                     # foreground + require a channel
pig daemon status [--json]
pig daemon reload
pig daemon logs [-f]
pig daemon stop
```

## Lifecycle

`start` probes `$PIG_HOME/pig.lock` with `flock(LOCK_EX|LOCK_NB)`. If the lock is
held it prints the existing pid and exits 0. Otherwise it re-executes itself with
`daemon run --foreground`, `Setsid`, stdin from `/dev/null`, stdout/stderr
appended to `$PIG_HOME/logs/daemon.log`, and `PIG_DAEMON_CHILD=1`.

The foreground process takes the lock, removes a stale socket, listens on the
Unix socket (mode `0600`), writes `pig.pid` and serves JSON-RPC. SIGINT/SIGTERM
shut down gracefully; SIGHUP only reloads configuration.

`stop` calls `daemon.shutdown`, cancels active runs, marks running jobs
`canceled`, waits up to 10 s, then sends SIGTERM.

`reload` is refused while a run or a job is active, and when the `[channel]`
section changed: restart the daemon for channel changes.

## IPC

Newline-delimited JSON-RPC 2.0 over the Unix socket. Requests carry a
monotonically increasing `id`; notifications have no id.

```
{"jsonrpc":"2.0","id":1,"method":"run.start","params":{"prompt":"hi","cwd":"/tmp"}}
{"jsonrpc":"2.0","method":"run.event","params":{"run_id":"run_x","event":{"type":"text_delta","text":"hi"}}}
```

Methods:

- `daemon.status`, `daemon.shutdown`, `daemon.reload`, `daemon.pair`
- `session.list`, `session.load`, `session.new`, `session.rm`
- `session.set_model`, `sessions.usage`
- `run.start`, `run.input`, `run.cancel`, `run.subscribe`, `run.attach`, `run.detach`
- `jobs.list`, `jobs.get`, `jobs.wait`, `jobs.cancel`, `jobs.retry`, `jobs.logs`
- `skills.list`, `rtk.gain`, `compact.now`

Every subscription starts with `run.snapshot` (session plus the last 200
entries) and then streams `run.event`. Each subscriber has a 256-event buffer.
When it is full, `text_delta` and `thinking_delta` are dropped and a single
`slow consumer` notice is queued; tool and terminal events always block.

Peer credentials are checked with `LOCAL_PEERCRED` (macOS) or `SO_PEERCRED`
(`Linux`) when available; otherwise file permissions alone guard the socket.

## Files

| Path | Mode | Purpose |
|---|---|---|
| `pig.sock` | 0600 | IPC endpoint |
| `pig.pid` | 0600 | decimal pid plus newline |
| `pig.lock` | 0600 | flock guard |
| `pig.db` | 0600 | SQLite database |
| `auth.env` | 0600 | secrets |
| `config.toml` | 0444 | configuration |
| `logs/daemon.log` | 0600 | rotating log |
| `artifacts/<session>/` | 0700 | spilled output |

Log rotation renames `daemon.log` to `daemon.log.1` once
`[daemon].log_max_bytes` is reached.

## Crash recovery

On startup every job in `running` becomes `interrupted`. `pig jobs retry <id>`
requeues it under the same id and appends `[previous attempt interrupted]` to
the child session.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime error |
| 2 | usage |
| 3 | missing config or model |
| 4 | storage or migration failure |
| 5 | daemon communication failure |
