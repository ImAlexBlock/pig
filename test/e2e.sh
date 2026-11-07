#!/usr/bin/env bash
# End-to-end checks for the pig binary. Requires bash, go and python3.
# Run from the repository root: ./test/e2e.sh
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN=${BIN:-$ROOT/pig}
WORK=$(mktemp -d)
trap 'set +e; PIG_HOME=$PIG_HOME "$BIN" daemon stop >/dev/null 2>&1; rm -rf "$WORK"' EXIT

pass() { printf 'ok   %s\n' "$1"; }
fail() { printf 'FAIL %s\n' "$1" >&2; exit 1; }

step() { printf '\n== %s\n' "$1"; }

step "build"
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$BIN" ./cmd/pig)
pass "binary built"

export PIG_HOME=$WORK/home
export PIG_NO_DAEMON=
unset PIG_NO_DAEMON

step "config init"
"$BIN" config init --provider mock --model mock >/dev/null
[ -f "$PIG_HOME/config.toml" ] || fail "config.toml missing"
mode=$(python3 -c "import os,sys;print(oct(os.stat(sys.argv[1]).st_mode & 0o777))" "$PIG_HOME/config.toml")
[ "$mode" = "0o444" ] || fail "config.toml mode $mode, want 0444"
pass "config written with mode 0444"

step "daemon lifecycle"
"$BIN" daemon start >/dev/null
"$BIN" daemon status --json | python3 -c 'import sys,json; d=json.load(sys.stdin); assert d["pid"]>0 and d["jobs"]["running"]==0'
kill -0 "$(cat "$PIG_HOME/pig.pid")"
pass "daemon started and reports status"

step "client persistence"
out=$("$BIN" run --mock "say hi")
case "$out" in
  *"mock: say hi"*) pass "run returned the mock answer" ;;
  *) fail "unexpected run output: $out" ;;
esac
"$BIN" daemon status --json | grep -q '"runs": 0' || fail "run did not finish"
pass "run completed"

step "subagent"
out=$("$BIN" run --mock "spawn: report status" 2>"$WORK/sub.err")
case "$out" in
  *"mock: report status"*) pass "subagent result surfaced" ;;
  *) fail "subagent output missing: $out" ;;
esac
grep -q "task" "$WORK/sub.err" || fail "task tool was not used"
"$BIN" agents list | grep -q "parent=" || fail "no child session recorded"
pass "child session recorded"

step "background job"
job=$("$BIN" run --mock --background "slow: long task")
case "$job" in
  job_*) pass "job id printed: $job" ;;
  *) fail "no job id: $job" ;;
esac
"$BIN" jobs list --json | grep -q "$job" || fail "job not listed"
"$BIN" jobs wait "$job" --timeout 30 >/dev/null || fail "job did not finish"
pass "job finished"

step "job crash recovery"
export PIG_MOCK_SCRIPT='{"turns":[{"text":"slow done","delay_ms":8000}]}'
"$BIN" daemon stop >/dev/null
"$BIN" daemon start >/dev/null
job=$("$BIN" run --mock --background "long task")
sleep 1
kill -9 "$(cat "$PIG_HOME/pig.pid")"
sleep 1
"$BIN" daemon start >/dev/null
"$BIN" jobs list --json | grep -q '"status": "interrupted"' || fail "job was not marked interrupted"
"$BIN" jobs retry "$job" >/dev/null
"$BIN" jobs wait "$job" --timeout 30 >/dev/null || fail "retried job did not finish"
pass "interrupted job retried"
unset PIG_MOCK_SCRIPT

step "config is read-only through the CLI"
"$BIN" config set agent.max_turns 3 >/dev/null
[ "$("$BIN" config get agent.max_turns)" = "3" ] || fail "config set/get mismatch"
if "$BIN" config set compaction.threshold 2.0 2>/dev/null; then fail "out-of-range value accepted"; fi
if "$BIN" config set version 2 2>/dev/null; then fail "version was writable"; fi
pass "config validation enforced"

step "rtk"
printf ' M a\n M b\n M c\n?? x\n?? y\n' > "$WORK/status.txt"
"$BIN" rtk preview --command "git status" < "$WORK/status.txt" > "$WORK/rtk.out" 2>/dev/null
grep -q "M 3" "$WORK/rtk.out" || fail "rtk preview lost the M count"
grep -q "?? 2" "$WORK/rtk.out" || fail "rtk preview lost the ?? count"
pass "rtk preview"

step "snapcompact"
"$BIN" debug render-snap --input "$ROOT/testdata/snapcompact/mixed.txt" --out "$WORK/snap" >/dev/null
python3 - "$WORK/snap/frame-000.png" <<'PY'
import struct, sys
with open(sys.argv[1], "rb") as f:
    d = f.read(33)
w, h = struct.unpack(">II", d[16:24])
assert w == 1568, w
assert h > 0, h
PY
pass "frames rendered at 1568 px"

step "doctor"
"$BIN" doctor >/dev/null
pass "doctor ran"

step "channels"
if "$BIN" serve >/dev/null 2>&1; then fail "serve started without channels"; fi
pass "serve refuses to start without channels"
printf 'skip telegram/qq live checks: [未验证: no bot token in this environment]\n'

printf '\nall checks passed\n'
