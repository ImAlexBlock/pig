#!/usr/bin/env python3
"""Run the hermesbench task suite (am423/hermes-bench-tool-call) against Pig.

The suite is 61 deterministic tasks (48 core + 3 real-world + 10 humaneval
micro) with stdlib-only verifiers that check `worktree` state and the agent
trace. This runner replaces the Hermes runtime with Pig and keeps everything
else identical:

  task.yaml -> fixture worktree -> agent run -> trace -> verifier -> metrics

Inputs come from a checkout of the suite:

  git clone --depth 1 https://github.com/am423/hermes-bench-tool-call /tmp/hermesbench

Usage:

  python3 bench/run_hermesbench.py --suite /tmp/hermesbench --run-id pig-1
  python3 bench/run_hermesbench.py --suite /tmp/hermesbench --cats t01,t03 --workers 3
"""

from __future__ import annotations

import argparse
import concurrent.futures
import re
import importlib.util
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import traceback
from dataclasses import dataclass, field
from pathlib import Path

import yaml

# Pig tool -> Hermes capability name, used when building the verifier trace.
# Candidates are tried in order; the task's allowed_tools narrows the choice.
TOOL_MAP = {
    "read": ["read_file"],
    "write": ["write_file"],
    "edit": ["patch"],
    "bash": ["terminal", "execute_code", "process"],
    "search": ["web_search", "web_extract"],
    "todo": ["todo"],
    "job": ["process"],
    "task": [],
    "browser": ["web_extract"],
    # native names used by other harnesses (omp) so the capability is credited
    "grep": ["search_files"],
    "glob": ["search_files"],
    "find": ["search_files"],
    "eval": ["execute_code"],
    "execute": ["execute_code"],
    "fetch": ["web_extract"],
    "read_url": ["web_extract"],
    "web_search": ["web_search"],
    "web_fetch": ["web_extract"],
}


@dataclass
class TaskResult:
    task_id: str
    category: str
    difficulty: int
    status: str
    score: float
    reason: str
    wall_s: float
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    tool_calls: int = 0
    turns: int = 0
    stop: str = ""
    session_id: str = ""
    details: dict = field(default_factory=dict)

    @property
    def passed(self) -> bool:
        return self.status == "PASS"


def load_tasks(suite: Path, cats: list[str] | None, only: list[str] | None) -> list[dict]:
    tasks = []
    for path in sorted((suite / "tasks").glob("*/*/task.yaml")):
        with path.open() as fh:
            task = yaml.safe_load(fh)
        task["_dir"] = str(path.parent)
        category = task["id"].split("/")[0]
        if cats and category not in cats:
            continue
        if only and task["id"] not in only and not any(task["id"].startswith(o) for o in only):
            continue
        tasks.append(task)
    return tasks


def prepare_worktree(task: dict, suite: Path, dest: Path) -> Path:
    """Mirror hermesbench/backend/worktree.py::setup_worktree."""
    if dest.exists():
        shutil.rmtree(dest)
    dest.mkdir(parents=True, exist_ok=True)
    fixture = task.get("fixture") or {}
    source = suite / "fixtures" / str(fixture.get("source", "small_repo"))
    if not source.exists():
        return dest
    globs = fixture.get("globs") or ["**/*"]
    if globs == ["**/*"]:
        for src in source.rglob("*"):
            if src.is_file():
                dst = dest / src.relative_to(source)
                dst.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(src, dst)
    else:
        for pattern in globs:
            for src in source.glob(pattern):
                if src.is_file():
                    dst = dest / src.relative_to(source)
                    dst.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(src, dst)
    return dest


# A shell command can express a Hermes capability that Pig has no dedicated
# tool for. Attribution is evidence-based: the command must actually do the
# thing (run patch(1), grep, python, ...). Segments are split on shell
# operators and matched on their first word, so `"command": "grep -r x ."` is
# classified from `grep`, not from the JSON wrapper.
def bash_command(args_json: str) -> str:
    try:
        parsed = json.loads(args_json)
    except (json.JSONDecodeError, TypeError):
        return str(args_json)
    if isinstance(parsed, dict):
        return str(parsed.get("command", ""))
    return str(parsed)


def bash_capabilities(args_json: str) -> list[str]:
    command = bash_command(args_json)
    caps: list[str] = []
    for segment in re.split(r"\s*(?:&&|\|\||;|\||\(|\))\s*", command):
        toks = segment.strip().split()
        while toks and (toks[0] in ("sudo", "time", "env", "command") or re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", toks[0])):
            toks = toks[1:]
        if not toks:
            continue
        cmd = os.path.basename(toks[0])
        rest = toks[1:]
        if cmd == "patch" or (cmd == "git" and rest[:1] == ["apply"]) or cmd == "apply_patch":
            caps.append("patch")
        elif cmd in ("sed", "perl") and any(a.startswith("-i") or "i" in a.lstrip("-") and a.startswith("-") for a in rest):
            caps.append("write_file")
        elif cmd == "tee" or (cmd in ("cat", "printf", "echo") and any(">" in a for a in rest)):
            caps.append("write_file")
        elif cmd in ("cat", "head", "tail", "less", "more", "bat"):
            caps.append("read_file")
        elif cmd in ("grep", "rg", "ripgrep", "find", "fd", "ls", "tree"):
            caps.append("search_files")
        elif cmd in ("python", "python3", "pytest", "node", "bun", "uv", "ipython"):
            caps.append("execute_code")
        elif cmd in ("ps", "kill", "pkill", "nohup", "top", "sleep", "pgrep"):
            caps.append("process")
        elif cmd in ("curl", "wget", "http"):
            caps.append("web_extract")
    if command.rstrip().endswith("&"):
        caps.append("process")
    return list(dict.fromkeys(caps))


def hermes_name(pig_tool: str, args: str, allowed: list[str]) -> str:
    candidates = TOOL_MAP.get(pig_tool, [])
    if not candidates:
        return pig_tool
    if not allowed:
        return candidates[0]
    # A shell command that demonstrably performs a specialised capability is
    # attributed to it, even when `terminal` is also allowed: the suite scores
    # the capability, not the transport.
    if pig_tool == "search":
        # A filesystem search (path / file_glob / output_mode) is the
        # `search_files` capability; a bare query is a web search.
        try:
            parsed = json.loads(args)
        except json.JSONDecodeError:
            parsed = {}
        if isinstance(parsed, dict) and any(k in parsed for k in ("path", "file_glob", "output_mode")):
            if not allowed or "search_files" in allowed:
                return "search_files"
    if pig_tool == "bash":
        for cap in bash_capabilities(args):
            if cap in allowed:
                return cap
    for cand in candidates:
        if cand in allowed:
            return cand
    return candidates[0]


def run_pig(task: dict, worktree: Path, pig: Path, home: Path, timeout: int) -> dict:
    env = dict(os.environ)
    env["PIG_HOME"] = str(home)
    env.setdefault("NO_COLOR", "1")
    cmd = [str(pig), "run", "--new", "--json", "--cwd", str(worktree), task["prompt"]]
    start = time.monotonic()
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        start_new_session=True,
    )
    try:
        out, err = proc.communicate(timeout=timeout)
        timed_out = False
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
        out, err = proc.communicate()
    wall = time.monotonic() - start

    session_id = ""
    stop = ""
    usage = {"input": 0, "output": 0, "cache_read": 0, "cache_write": 0}
    tool_calls = 0
    turns = 0
    errors: list[str] = []
    for line in out.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        etype = ev.get("type")
        if etype == "run_start":
            session_id = ev.get("session_id", "")
        elif etype == "usage" and ev.get("usage"):
            u = ev["usage"]
            # usage events carry the latest request; sum them for run totals
            usage["input"] += int(u.get("input", 0))
            usage["output"] += int(u.get("output", 0))
            usage["cache_read"] += int(u.get("cache_read", 0))
            usage["cache_write"] += int(u.get("cache_write", 0))
            turns += 1
        elif etype == "tool_start":
            tool_calls += 1
        elif etype == "run_end":
            stop = ev.get("stop", "")
        elif etype == "error":
            errors.append(ev.get("error", ""))
    return {
        "session_id": session_id,
        "wall_s": wall,
        "timed_out": timed_out,
        "stop": stop,
        "usage": usage,
        "tool_calls": tool_calls,
        "turns": turns,
        "errors": errors,
        "exit_code": proc.returncode,
        "stderr": err[-4000:],
    }


def run_omp(task: dict, worktree: Path, timeout: int) -> dict:
    """Run one task with omp in headless JSON mode."""
    cmd = ["omp", "-p", "--mode", "json", "--cwd", str(worktree), task["prompt"]]
    start = time.monotonic()
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        env=dict(os.environ),
        start_new_session=True,
    )
    try:
        out, _ = proc.communicate(timeout=timeout)
        timed_out = False
    except subprocess.TimeoutExpired:
        timed_out = True
        try:
            os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
        except ProcessLookupError:
            pass
        out, _ = proc.communicate()
    wall = time.monotonic() - start
    events = []
    for line in out.splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            continue
    return {"events": events, "wall_s": wall, "timed_out": timed_out}


def omp_trace(events: list[dict], allowed: list[str]) -> list[dict]:
    trace: list[dict] = []
    for ev in events:
        if ev.get("type") == "turn_end":
            msg = ev.get("message") or {}
            if msg.get("role") != "assistant":
                continue
            text_parts: list[str] = []
            calls = []
            for block in msg.get("content") or []:
                btype = block.get("type")
                if btype == "text":
                    text_parts.append(block.get("text", ""))
                elif btype in ("toolCall", "tool_call"):
                    args = block.get("arguments")
                    args_json = json.dumps(args) if isinstance(args, (dict, list)) else (args or "{}")
                    name = hermes_name(block.get("name", ""), args_json, allowed)
                    calls.append(
                        {
                            "id": block.get("id", ""),
                            "type": "function",
                            "function": {"name": name, "arguments": args_json},
                        }
                    )
            entry = {"role": "assistant", "content": "".join(text_parts)}
            if calls:
                entry["tool_calls"] = calls
            trace.append(entry)
        elif ev.get("type") == "tool_execution_end":
            content = (ev.get("result") or {}).get("content") or []
            text = "".join(b.get("text", "") for b in content if isinstance(b, dict))
            trace.append(
                {
                    "role": "tool",
                    "tool_call_id": ev.get("toolCallId", ""),
                    "name": ev.get("toolName", ""),
                    "content": text,
                }
            )
    return trace


def omp_usage(events: list[dict]) -> dict:
    fresh = cached = out = 0
    turns = 0
    for ev in events:
        if ev.get("type") != "turn_end":
            continue
        usage = (ev.get("message") or {}).get("usage") or {}
        if not usage:
            continue
        turns += 1
        fresh += int(usage.get("input", 0))
        cached += int(usage.get("cacheRead", 0))
        out += int(usage.get("output", 0))
    # Normalize to Pig's accounting: `input` includes the cached share.
    return {"input": fresh + cached, "output": out, "cache_read": cached, "cache_write": 0}, turns


def export_trace(session_id: str, pig: Path, home: Path, allowed: list[str]) -> list[dict]:
    """Convert a Pig session into the OpenAI-style trace hermesbench verifiers read."""
    if not session_id:
        return []
    env = dict(os.environ)
    env["PIG_HOME"] = str(home)
    proc = subprocess.run(
        [str(pig), "sessions", "export", session_id],
        capture_output=True,
        text=True,
        env=env,
        timeout=120,
    )
    if proc.returncode != 0:
        return []
    payload = json.loads(proc.stdout)
    trace: list[dict] = []
    pending_assistant: dict | None = None
    for entry in payload.get("entries", []):
        kind = entry.get("kind")
        content = entry.get("content")
        try:
            body = json.loads(content) if content else {}
        except json.JSONDecodeError:
            body = {}
        if kind == "message":
            role = entry.get("role") or "assistant"
            msg = {"role": role, "content": body.get("text", "")}
            if body.get("reasoning"):
                msg["reasoning_content"] = body["reasoning"]
            trace.append(msg)
            pending_assistant = msg if role == "assistant" else None
        elif kind == "tool_call":
            args = body.get("args")
            if isinstance(args, (dict, list)):
                args_json = json.dumps(args)
            else:
                args_json = args or "{}"
            name = hermes_name(body.get("name", ""), args_json, allowed)
            call = {
                "id": body.get("id", ""),
                "type": "function",
                "function": {"name": name, "arguments": args_json},
            }
            if pending_assistant is None or pending_assistant.get("role") != "assistant":
                pending_assistant = {"role": "assistant", "content": "", "tool_calls": []}
                trace.append(pending_assistant)
            pending_assistant.setdefault("tool_calls", []).append(call)
        elif kind == "tool_result":
            trace.append(
                {
                    "role": "tool",
                    "tool_call_id": body.get("call_id", ""),
                    "name": body.get("name", ""),
                    "content": body.get("text", ""),
                }
            )
    return trace


def run_verifier(task: dict, worktree: Path, trace: list[dict]) -> tuple[str, float, str, dict]:
    spec = task.get("verifier") or {"module": "verifier", "fn": "verify"}
    verifier_path = Path(task["_dir"]) / f"{spec.get('module', 'verifier')}.py"
    if not verifier_path.exists():
        return "VERIFIER_ERROR", 0.0, f"verifier not found: {verifier_path}", {}
    mod_name = "hb_verifier_" + task["id"].replace("/", "_").replace("-", "_")
    try:
        mod_spec = importlib.util.spec_from_file_location(mod_name, verifier_path)
        mod = importlib.util.module_from_spec(mod_spec)  # type: ignore[arg-type]
        sys.modules[mod_name] = mod
        mod_spec.loader.exec_module(mod)  # type: ignore[union-attr]
        fn = getattr(mod, spec.get("fn", "verify"))
        result = fn(worktree, trace)
        return (
            str(getattr(result, "status", "VERIFIER_ERROR")),
            float(getattr(result, "score", 0.0)),
            str(getattr(result, "reason", "")),
            dict(getattr(result, "details", {}) or {}),
        )
    except Exception as exc:  # noqa: BLE001 - verifier bugs must not kill the run
        return "VERIFIER_ERROR", 0.0, f"{type(exc).__name__}: {exc}", {"tb": traceback.format_exc()[-1200:]}


def run_task(task: dict, args, suite: Path, results_dir: Path) -> TaskResult:
    task_id = task["id"]
    category = task_id.split("/")[0]
    safe = task_id.replace("/", "__")
    worktree = prepare_worktree(task, suite, results_dir / safe / "worktree")
    allowed = task.get("allowed_tools") or []
    timeout = int((task.get("timeout_seconds") or 180) * args.timeout_scale)
    res = TaskResult(
        task_id=task_id,
        category=category,
        difficulty=int(task.get("difficulty") or 0),
        status="FAIL",
        score=0.0,
        reason="",
        wall_s=0.0,
    )
    try:
        if args.agent == "omp":
            res, done = run_task_omp(task, worktree, timeout, allowed, res)
            if done:
                return res
        run = run_pig(task, worktree, Path(args.pig), Path(args.home), timeout)
        res.wall_s = run["wall_s"]
        res.session_id = run["session_id"]
        res.stop = run["stop"]
        res.tool_calls = run["tool_calls"]
        res.turns = run["turns"]
        res.input_tokens = run["usage"]["input"]
        res.output_tokens = run["usage"]["output"]
        res.cache_read_tokens = run["usage"].get("cache_read", 0)
        if run["timed_out"]:
            res.status = "BUDGET_EXCEEDED"
            res.reason = f"timeout after {timeout}s"
            return res
        trace = export_trace(run["session_id"], Path(args.pig), Path(args.home), allowed)
        (results_dir / safe / "trace.json").write_text(json.dumps(trace, indent=1))
        status, score, reason, details = run_verifier(task, worktree, trace)
        res.status = status
        res.score = score
        res.reason = reason
        res.details = details
        if run["errors"]:
            res.details = {**res.details, "agent_errors": run["errors"]}
    except Exception as exc:  # noqa: BLE001
        res.status = "HARNESS_ERROR"
        res.reason = f"{type(exc).__name__}: {exc}"
    return res


def run_task_omp(task: dict, worktree: Path, timeout: int, allowed: list[str], res: "TaskResult"):
    run = run_omp(task, worktree, timeout)
    res.wall_s = run["wall_s"]
    res.stop = "timeout" if run["timed_out"] else "end"
    trace = omp_trace(run["events"], allowed)
    usage, turns = omp_usage(run["events"])
    res.input_tokens = usage["input"]
    res.output_tokens = usage["output"]
    res.cache_read_tokens = usage["cache_read"]
    res.turns = turns
    res.tool_calls = sum(len(m.get("tool_calls") or []) for m in trace if m.get("role") == "assistant")
    safe = task["id"].replace("/", "__")
    if run["timed_out"]:
        res.status = "BUDGET_EXCEEDED"
        res.reason = f"timeout after {timeout}s"
        return res, True
    status, score, reason, details = run_verifier(task, worktree, trace)
    res.status = status
    res.score = score
    res.reason = reason
    res.details = details
    return res, True


def summarize(results: list[TaskResult], price_in: float, price_out: float, price_cache: float = 0.006) -> dict:
    total = len(results)
    passed = sum(1 for r in results if r.passed)
    by_cat: dict[str, dict] = {}
    for r in results:
        cat = by_cat.setdefault(r.category, {"total": 0, "passed": 0, "wall_s": 0.0, "tokens": 0})
        cat["total"] += 1
        cat["passed"] += 1 if r.passed else 0
        cat["wall_s"] += r.wall_s
        cat["tokens"] += r.input_tokens + r.output_tokens
    tin = sum(r.input_tokens for r in results)
    tout = sum(r.output_tokens for r in results)
    tcached = sum(r.cache_read_tokens for r in results)
    wall = sum(r.wall_s for r in results)
    # Input tokens reported by the provider already include cache hits; bill the
    # cached share at the cache-hit price.
    cost = (tin - tcached) / 1e6 * price_in + tcached / 1e6 * price_cache + tout / 1e6 * price_out
    return {
        "tasks": total,
        "passed": passed,
        "pass_rate": passed / total if total else 0.0,
        "difficulty_weighted": (
            sum(r.score * (r.difficulty or 1) for r in results)
            / sum((r.difficulty or 1) for r in results)
            if results
            else 0.0
        ),
        "input_tokens": tin,
        "output_tokens": tout,
        "cache_read_tokens": tcached,
        "tokens_per_task": (tin + tout) / total if total else 0,
        "cost_usd": cost,
        "cost_per_task_usd": cost / total if total else 0,
        "wall_s_total": wall,
        "wall_s_per_task": wall / total if total else 0,
        "tool_calls": sum(r.tool_calls for r in results),
        "tool_calls_per_task": sum(r.tool_calls for r in results) / total if total else 0,
        "turns": sum(r.turns for r in results),
        "categories": {
            k: {
                **v,
                "pass_rate": v["passed"] / v["total"] if v["total"] else 0.0,
                "wall_s": round(v["wall_s"], 1),
            }
            for k, v in sorted(by_cat.items())
        },
        "statuses": {
            s: sum(1 for r in results if r.status == s)
            for s in sorted({r.status for r in results})
        },
    }


def write_report(out: Path, summary: dict, results: list[TaskResult], args) -> None:
    lines = [
        "# Pig × hermesbench",
        "",
        f"- suite: `{args.suite}` (am423/hermes-bench-tool-call, 61 deterministic tasks)",
        f"- model: `{args.model_note}`",
        f"- tasks run: {summary['tasks']}",
        f"- pass@1: **{summary['passed']}/{summary['tasks']} = {summary['pass_rate'] * 100:.1f}%**",
        f"- difficulty-weighted score: {summary['difficulty_weighted'] * 100:.1f}%",
        f"- input tokens: {summary['input_tokens']:,} · output tokens: {summary['output_tokens']:,}"
        f" ({summary['tokens_per_task']:,.0f}/task)",
        f"- cache-hit input tokens: {summary['cache_read_tokens']:,}",
        f"- cost: ${summary['cost_usd']:.4f} (${summary['cost_per_task_usd']:.4f}/task)"
        f" at ${args.price_in}/M in, ${args.price_out}/M out",
        f"- wall clock: {summary['wall_s_total']:.0f}s total, {summary['wall_s_per_task']:.1f}s/task",
        f"- tool calls: {summary['tool_calls']} ({summary['tool_calls_per_task']:.1f}/task)",
        "",
        "## Per category",
        "",
        "| category | pass | rate | wall s | tokens |",
        "|---|---:|---:|---:|---:|",
    ]
    for name, cat in summary["categories"].items():
        lines.append(
            f"| {name} | {cat['passed']}/{cat['total']} | {cat['pass_rate'] * 100:.1f}% |"
            f" {cat['wall_s']} | {cat['tokens']:,} |"
        )
    lines += ["", "## Per task", "", "| task | status | wall s | tok in | tok out | tools | reason |", "|---|---|---:|---:|---:|---:|---|"]
    for r in results:
        reason = (r.reason or "").replace("|", "/")[:90]
        lines.append(
            f"| {r.task_id} | {r.status} | {r.wall_s:.1f} | {r.input_tokens} | {r.output_tokens} |"
            f" {r.tool_calls} | {reason} |"
        )
    out.write_text("\n".join(lines) + "\n")


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--suite", default="/tmp/hermesbench", help="checkout of am423/hermes-bench-tool-call")
    ap.add_argument("--pig", default=str(Path(__file__).resolve().parents[1] / "pig"))
    ap.add_argument("--home", default=str(Path.home() / ".pig-bench"))
    ap.add_argument("--run-id", default=time.strftime("pig-%Y%m%d-%H%M%S"))
    ap.add_argument("--cats", default="", help="comma separated category prefixes")
    ap.add_argument("--only", default="", help="comma separated task ids or prefixes")
    ap.add_argument("--workers", type=int, default=2)
    ap.add_argument("--agent", choices=["pig", "omp"], default="pig", help="harness under test")
    ap.add_argument("--timeout-scale", type=float, default=1.0, help="multiply every task budget")
    # Defaults are the Claw-SWE-Bench Table A.1 prices for DeepSeek-V4.1-Flash.
    ap.add_argument("--price-in", type=float, default=0.30, help="USD per 1M uncached input tokens")
    ap.add_argument("--price-out", type=float, default=1.20, help="USD per 1M output tokens")
    ap.add_argument("--price-cache", type=float, default=0.006, help="USD per 1M cached input tokens")
    ap.add_argument("--model-note", default="lynshen/deepseek-v4.1-flash")
    args = ap.parse_args()

    suite = Path(args.suite)
    tasks = load_tasks(
        suite,
        [c for c in args.cats.split(",") if c] or None,
        [t for t in args.only.split(",") if t] or None,
    )
    if not tasks:
        print("no tasks matched", file=sys.stderr)
        return 2

    results_dir = Path(__file__).resolve().parent / "results" / args.run_id
    results_dir.mkdir(parents=True, exist_ok=True)
    print(f"run {args.run_id}: {len(tasks)} tasks, workers={args.workers}", flush=True)

    results: list[TaskResult] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = {pool.submit(run_task, t, args, suite, results_dir): t for t in tasks}
        for fut in concurrent.futures.as_completed(futures):
            r = fut.result()
            results.append(r)
            print(
                f"  {r.task_id:<42} {r.status:<16} {r.wall_s:6.1f}s"
                f" in={r.input_tokens:<7} out={r.output_tokens:<6} {r.reason[:60]}",
                flush=True,
            )
    results.sort(key=lambda r: r.task_id)
    summary = summarize(results, args.price_in, args.price_out, args.price_cache)
    summary["run_id"] = args.run_id
    summary["agent"] = args.agent
    summary["model"] = args.model_note
    summary["prices"] = {"input_per_m": args.price_in, "output_per_m": args.price_out}
    (results_dir / "summary.json").write_text(json.dumps(summary, indent=2))
    (results_dir / "results.jsonl").write_text(
        "\n".join(json.dumps(r.__dict__) for r in results) + "\n"
    )
    write_report(results_dir / "REPORT.md", summary, results, args)
    print(
        f"\npass@1 {summary['passed']}/{summary['tasks']} = {summary['pass_rate'] * 100:.1f}%"
        f" · ${summary['cost_usd']:.4f} · {summary['wall_s_total']:.0f}s"
        f" · {summary['tokens_per_task']:,.0f} tok/task",
        flush=True,
    )
    print(f"report: {results_dir / 'REPORT.md'}", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
