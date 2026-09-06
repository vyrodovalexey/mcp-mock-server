#!/usr/bin/env bash
# TASK-029 throughput runner (test asset). Launches perfserver, runs a k6
# steady-state scenario, samples server + k6 CPU/RSS at 1 Hz for headroom
# evidence, and writes raw JSON + samplers into the artifacts dir.
#
# Usage: run_throughput.sh <name> <journal off|full> <rate> <gomaxprocs> <run#>
set -euo pipefail

NAME="$1"; JOURNAL="$2"; RATE="$3"; GMP="$4"; RUN="$5"
ROOT="/Users/alexey/works/programs/golang/mcp-mock-server"
ART="$ROOT/.opencode/output/perf-artifacts"
BIN="/tmp/perfserver"
mkdir -p "$ART"

TAG="${NAME}_gmp${GMP}_run${RUN}"
SRV_OUT="$ART/${TAG}.server.out"
K6_JSON="$ART/${TAG}.k6.json"
SRV_SAMP="$ART/${TAG}.server.samples.csv"
K6_SAMP="$ART/${TAG}.k6.samples.csv"
K6_SUMMARY="$ART/${TAG}.k6.summary.txt"

# Launch server with pinned GOMAXPROCS.
GOMAXPROCS="$GMP" "$BIN" -journal "$JOURNAL" >"$SRV_OUT" 2>"$ART/${TAG}.server.err" &
SRV_BGPID=$!
sleep 1
URL=$(grep PERFSERVER_URL "$SRV_OUT" | cut -d= -f2)
SRV_PID=$(grep PERFSERVER_PID "$SRV_OUT" | cut -d= -f2)
echo "[$TAG] server pid=$SRV_PID url=$URL gomaxprocs=$GMP journal=$JOURNAL"

# 1 Hz server sampler: epoch,cpu%,rss_kb
( echo "t,cpu_pct,rss_kb"
  for i in $(seq 1 150); do
    line=$(ps -o %cpu=,rss= -p "$SRV_PID" 2>/dev/null || true)
    [ -z "$line" ] && break
    echo "$(date +%s),$(echo "$line" | awk '{print $1","$2}')"
    sleep 1
  done ) > "$SRV_SAMP" &
SRV_SAMP_PID=$!

# Run k6; sample the k6 process CPU in parallel for generator headroom.
TARGET_URL="$URL" RATE="$RATE" MODE=steady \
  k6 run --no-color --summary-trend-stats='avg,p(50),p(90),p(95),p(99),p(99.9),max' \
  --out "json=$K6_JSON" "$ROOT/test/perf/k6/tools_call.js" >"$K6_SUMMARY" 2>&1 &
K6_BGPID=$!
# find the k6 engine pid (the child doing the work is this pid)
sleep 1
( echo "t,cpu_pct,rss_kb"
  while kill -0 "$K6_BGPID" 2>/dev/null; do
    line=$(ps -o %cpu=,rss= -p "$K6_BGPID" 2>/dev/null || true)
    [ -n "$line" ] && echo "$(date +%s),$(echo "$line" | awk '{print $1","$2}')"
    sleep 1
  done ) > "$K6_SAMP" &
K6_SAMP_PID=$!

set +e
wait "$K6_BGPID"; K6_RC=$?
set -e
kill "$SRV_SAMP_PID" "$K6_SAMP_PID" 2>/dev/null || true
kill -INT "$SRV_PID" 2>/dev/null || true
wait "$SRV_BGPID" 2>/dev/null || true

echo "[$TAG] k6 rc=$K6_RC ; artifacts in $ART"
exit 0
