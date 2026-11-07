# bench

Reproducible agent benchmarks for Pig. Two suites are supported:

| suite | tasks | verifier | container | what it measures |
|---|---|---|---|---|
| `hermesbench` (am423/hermes-bench-tool-call) | 61 | deterministic Python, no LLM judge | none | tool-use reliability across 13 capability categories |
| published reference (Claw-SWE-Bench, HermesBench) | 350 / 27 | official harness | Docker + iptables | repo-level issue resolution / personal-agent workflows |

The Claw-SWE-Bench harness needs a Linux host with Docker, `iptables` and a
`sniproxy` service; it cannot run on this macOS arm64 machine (no Docker
daemon). The `hermesbench` suite is portable, so that is what we execute here.

## hermesbench

The suite ships `task.yaml` (prompt, allowed tools, timeout, fixture), fixtures
and a stdlib-only `verifier.py` exposing `verify(worktree, trace)`. Pig replaces
the Hermes runtime; everything else stays identical.

```bash
git clone --depth 1 https://github.com/am423/hermes-bench-tool-call /tmp/hermesbench
uv venv bench/.venv && bench/.venv/bin/python -m pip install pyyaml   # or uv pip
./pig config init --provider lynshen --kind openai-chat \
    --base-url https://www.lynshen.org/v1 --api-key-env LYNSHEN_API_KEY \
    --model deepseek-v4.1-flash --ref lynshen/deepseek-v4.1-flash \
    --context-window 1000000 --max-output 65536 --input text,image
./pig auth set LYNSHEN_API_KEY

PIG_HOME=$HOME/.pig-bench bench/.venv/bin/python bench/run_hermesbench.py \
    --suite /tmp/hermesbench --run-id full-61 --workers 4
```

Each task gets a fresh worktree (fixture copied per `fixture.globs`) and a fresh
Pig session (`pig run --new`). Artifacts land in `bench/results/<run-id>/`:
`summary.json`, `results.jsonl`, `REPORT.md`, and one directory per task with
`worktree/` and `trace.json`.

### Metrics

- **pass@1** — verifier status `PASS` over all attempted tasks.
- **difficulty-weighted score** — mean of `score × difficulty`, weighted by
  difficulty (matches the suite's `difficulty_weighted`).
- **tokens** — summed `usage` events from Pig's NDJSON run stream; cached input
  is reported separately.
- **cost** — `(in − cached)·p_in + cached·p_cache + out·p_out`, using the unit
  prices from Claw-SWE-Bench Table A.1 (DeepSeek-V4.1-Flash: $0.30 / $0.006 /
  $1.20 per 1M tokens) so the numbers are comparable to the published table.
- **wall clock** — process wall time per task, including provider latency.
- **tool calls** — `tool_start` events per task.

### Tool mapping

Hermes tool names are what verifiers check. Pig's tools are mapped as:

| Pig | Hermes capability |
|---|---|
| `read` | `read_file` |
| `edit` | `patch` |
| `write` | `write_file` |
| `bash` | `terminal`, then `execute_code`, then `process` |
| `search` | `web_search`, `web_extract` |
| `todo` | `todo` |
| `job` | `process` |
| `grep`, `glob`, `find` (other harnesses) | `search_files` |
| `eval`, `execute` | `execute_code` |
| `fetch`, `read_url` | `web_extract` |

When the native name is not in the task's `allowed_tools`, a `bash` call is
attributed by evidence from its command instead:

| command pattern | capability |
|---|---|
| `patch …`, `git apply` | `patch` |
| `sed -i`, `perl -i`, `tee`, `cat >`, `apply_patch` | `write_file` |
| `cat`, `head`, `tail`, `less` | `read_file` |
| `grep`, `rg`, `find`, `fd`, `ls` | `search_files` |
| `python3`, `pytest`, `node`, `uv run` | `execute_code` |
| `ps`, `kill`, `nohup`, `top`, trailing `&` | `process` |
| `curl`, `wget` | `web_extract` |

Attribution only fires when the command actually performs that capability, and
at most one name is attributed per call.

### Deviations from the suite's native run

- Pig's tool allowlist is not restricted per task (Pig has no such flag), so a
  task may use any Pig tool; the verifier still checks the required capability.
- `max_turns` from `task.yaml` is not enforced; `timeout_seconds` is.
- Sampling is fixed through the model config (`[models].temperature = 0.0`),
  not per task; seeds are not forwarded to the provider.
- No tmux/`unshare` isolation, no network block, no 5 Hz hardware telemetry.
- Sessions are isolated per task and deleted from the measurement path.

## Published reference points

Claw-SWE-Bench (arXiv 2606.12344v2, Table 2), model DeepSeek-V4.1-Flash,
full-350, 3600 s per instance, three runs averaged:

| harness | pass@1 | cost ($) | mean duration (s) | in (M) | out (M) | cache hit |
|---|---:|---:|---:|---:|---:|---:|
| Hermes Agent | 81.7 | 36 | 492 | 41.8 | 13.3 | 96.9% |
| OpenClaw | 79.8 | 50 | 566 | 38.5 | 25.4 | 97.3% |
| nanobot | 79.6 | 78 | 533 | 184.9 | 13.6 | 84.2% |
| DeepSeek Harness | 76.5 | 41 | 1577 | 56.4 | 12.9 | 96.3% |
| ZeroClaw | 76.1 | 99 | 1432 | 57.6 | 62.2 | 95.3% |
| Meta-Harness | 74.9 | 13 | 1005 | 14.0 | 7.2 | 86.6% |
| GenericAgent | 71.5 | 12 | 839 | 11.7 | 6.8 | 88.1% |

HermesBench (verkyyi, 27 LLM-judged personal-agent recipes), baseline
`verkyyi/default` on gpt-5.5: score 78.20.

Neither suite has published numbers for Pig, and the two suites above are not
the `hermesbench` 61-task set, so cross-suite scores are not comparable. The
`hermesbench` rows in this directory are the only same-task measurements.

## Measured results (hermesbench-61, DeepSeek-V4.1-Flash, temperature 0)

Cost uses the published Table A.1 unit prices ($0.30 uncached / $0.006 cached
/ $1.20 output per 1M tokens), so both rows are on the same tariff.

Pig's final row is the mean of three runs of the shipped code
(`final-61`, `final-61-r2`, `final-61-r3`); the others are single runs.

| harness | pass@1 | cost | $/task | wall/task | tok/task | cache | fresh in | tools/task |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Pig before optimisation | 47/61 = 77.0% | $0.141 | $0.0023 | 22.1 s | 18,880 | 76.3% | 261k | 4.6 |
| Pig, loop + tool fixes | 51/61 = 83.6% | $0.135 | $0.0022 | 23.0 s | 16,080 | 75.0% | 232k | 4.0 |
| Pig, cache-stable prompt | 51/61 = 83.6% | $0.084 | $0.0014 | 19.1 s | 14,347 | 89.7% | 85k | 3.4 |
| **Pig final (3-run mean)** | **49.7/61 = 81.4%** | **$0.087** | **$0.0014** | **20.3 s** | 14,355 | 89.2% | ~90k | 3.8 |
| omp (`--agent omp`) | 50/61 = 82.0% | $0.125 | $0.0020 | 9.4 s | 67,695 | 97.0% | 121k | 3.3 |

What changed between the Pig rows: one-shot system prompt with no session-specific
text before the context files (the cwd used to make every session's prefix
unique), built-in convergence control (repeat/stall detection + wrap-up notice),
filesystem search with `file_glob`/`output_mode`, and a `memory` tool. Details in
`docs/config.md` and `docs/architecture.md`.

Single-run scores swing by ±2.5 points, so Pig's 81.4% and omp's 82.0% are within
noise; the cost and latency gaps are not.

Prompts-cache probe (same prompt, two working directories, one request each):

| session | prompt prefix | cached |
|---|---|---|
| same cwd as the previous session | cwd in the prompt | 96% |
| **different cwd** | cwd in the prompt | **0%** |
| **different cwd** | cwd removed from the prompt | **95%** |

Runs: `results/official-t0` (Pig), `results/omp-61-v2` (omp). Earlier runs
(`full-61`, `omp-61`) used an older tool-attribution policy and are kept for
variance only.

| category | Pig | omp | Δ |
|---|---:|---:|---:|
| t01_terminal_smoke | 5/5 | 5/5 | 0 |
| t02_file_read | 4/6 | 5/6 | −1 |
| t03_patch_edit | 4/5 | 4/5 | 0 |
| t04_search_grep | 4/5 | 4/5 | 0 |
| t05_write_new | 5/5 | 5/5 | 0 |
| t06_process_mgmt | 3/5 | 3/5 | 0 |
| t07_todo_plan | 2/3 | 2/3 | 0 |
| t08_execute_code | 2/5 | 4/5 | −2 |
| t09_web_lookup | 3/3 | 2/3 | +1 |
| t10_memory_facts | 0/3 | 0/3 | 0 |
| t11_error_recovery | 2/3 | 3/3 | −1 |
| t12_real_world | 3/3 | 3/3 | 0 |
| t13_humaneval_micro | 10/10 | 10/10 | 0 |

Pig's 10 misses after optimisation: 7 budget timeouts (t06 kill/poll, t08
datascience/stateful, all three t10 memory tasks) and 3 verifier failures (V4A
patch format, paginated reads, 4-item todo). Doubling every budget before the
optimisation recovered one task; the remaining timeouts are non-converging
loops, not slow-but-correct runs.
