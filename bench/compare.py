#!/usr/bin/env python3
"""Compare two or more bench runs (and the published Claw-SWE-Bench rows).

  bench/.venv/bin/python bench/compare.py bench/results/full-61 bench/results/full-61-t0
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

# Claw-SWE-Bench Table 2, model DeepSeek-V4.1-Flash, full-350, arXiv:2606.12344v2.
PUBLISHED = [
    ("Hermes Agent", 81.7, 36.0, 492.0, 41.8, 13.3, 96.9),
    ("OpenClaw", 79.8, 50.0, 566.0, 38.5, 25.4, 97.3),
    ("nanobot", 79.6, 78.0, 533.0, 184.9, 13.6, 84.2),
    ("DeepSeek Harness", 76.5, 41.0, 1577.0, 56.4, 12.9, 96.3),
    ("ZeroClaw", 76.1, 99.0, 1432.0, 57.6, 62.2, 95.3),
    ("Meta-Harness", 74.9, 13.0, 1005.0, 14.0, 7.2, 86.6),
    ("GenericAgent", 71.5, 12.0, 839.0, 11.7, 6.8, 88.1),
]


# Claw-SWE-Bench Table A.1, DeepSeek-V4.1-Flash, USD per 1M tokens.
PRICE_FRESH, PRICE_CACHE, PRICE_OUT = 0.30, 0.006, 1.20


def load(path: Path) -> dict:
    summary = json.loads((path / "summary.json").read_text())
    # Recompute cost from tokens so every row uses the published unit prices,
    # whatever the run recorded.
    fresh = summary["input_tokens"] - summary["cache_read_tokens"]
    cost = (fresh * PRICE_FRESH + summary["cache_read_tokens"] * PRICE_CACHE + summary["output_tokens"] * PRICE_OUT) / 1e6
    summary["cost_usd"] = cost
    summary["cost_per_task_usd"] = cost / summary["tasks"] if summary["tasks"] else 0.0
    return summary


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("runs", nargs="+", help="bench/results/<run-id> directories")
    args = ap.parse_args()

    print("| harness | suite | tasks | pass@1 | cost ($) | cost/task | dur/task (s) | tok/task | cache |")
    print("|---|---|---:|---:|---:|---:|---:|---:|---:|")
    for path in args.runs:
        s = load(Path(path))
        n = s["tasks"]
        cache = 100.0 * s["cache_read_tokens"] / s["input_tokens"] if s["input_tokens"] else 0.0
        agent = s.get("agent") or ("omp" if "omp" in str(path) else "pig")
        print(
            f"| {agent} ({s.get('model', '')}) | hermesbench-61 | {n} | {s['pass_rate'] * 100:.1f}% |"
            f" {s['cost_usd']:.3f} | {s['cost_per_task_usd']:.4f} | {s['wall_s_per_task']:.1f} |"
            f" {s['tokens_per_task']:,.0f} | {cache:.1f}% |"
        )
    for name, p1, cost, dur, tin, tout, cache in PUBLISHED:
        per_task = 350
        print(
            f"| {name} | Claw-SWE-350 | {per_task} | {p1:.1f}% | {cost:.0f} | {cost / per_task:.3f} |"
            f" {dur:.0f} | {((tin + tout) * 1e6) / per_task:,.0f} | {cache:.1f}% |"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
