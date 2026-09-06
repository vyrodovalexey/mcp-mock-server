#!/usr/bin/env bash
# TASK-029 MOCK-903 split server/client runner (test asset). The server-only RSS
# is sampled via `ps` on the server pid while a SEPARATE client process holds N
# live SSE streams open — so the per-stream figure is attributable to the server,
# not inflated by the client's own socket buffers.
#
# Usage: run_sse.sh <run#> <hold_secs> <steps csv>
set -euo pipefail
RUN="${1:-1}"; HOLD="${2:-25}"; STEPS="${3:-1000,2000,5000,10000,15000}"
ROOT="/Users/alexey/works/programs/golang/mcp-mock-server"
ART="$ROOT/.opencode/output/perf-artifacts"
BIN="/tmp/ssebench"
OUT="$ART/mock903_split_run${RUN}.csv"
mkdir -p "$ART"

# Start the SSE server process.
"$BIN" -mode server >"$ART/sse_srv_run${RUN}.out" 2>"$ART/sse_srv_run${RUN}.err" &
SRV_BG=$!
sleep 1
SADDR=$(grep SSESERVER_ADDR "$ART/sse_srv_run${RUN}.out" | cut -d= -f2)
SPID=$(grep SSESERVER_PID "$ART/sse_srv_run${RUN}.out" | cut -d= -f2)
SBASE=$(grep SSESERVER_BASELINE_RSS_KB "$ART/sse_srv_run${RUN}.out" | cut -d= -f2)
echo "server addr=$SADDR pid=$SPID baseline_rss_kb=$SBASE"
echo "target_streams,client_established,server_rss_kb,server_rss_delta_kb,server_per_stream_bytes,server_goroutines" | tee "$OUT"

IFS=',' read -ra ARR <<< "$STEPS"
CLIENT_BG=""
for N in "${ARR[@]}"; do
  # (Re)start client holding N streams. Kill previous client first.
  if [ -n "$CLIENT_BG" ]; then kill -INT "$CLIENT_BG" 2>/dev/null || true; wait "$CLIENT_BG" 2>/dev/null || true; sleep 2; fi
  "$BIN" -mode client -addr "$SADDR" -n "$N" >"$ART/sse_cli_run${RUN}_n${N}.out" 2>&1 &
  CLIENT_BG=$!
  # wait for client to report establishment
  for i in $(seq 1 60); do
    grep -q CLIENT_ESTABLISHED "$ART/sse_cli_run${RUN}_n${N}.out" 2>/dev/null && break
    sleep 0.5
  done
  EST=$(grep CLIENT_ESTABLISHED "$ART/sse_cli_run${RUN}_n${N}.out" | cut -d= -f2 || echo 0)
  # hold, let keep-alives settle, then sample server RSS via ps
  sleep "$HOLD"
  SRSS=$(ps -o rss= -p "$SPID" | tr -d ' ')
  # server goroutines from its stderr last line
  SGO=$(grep -o 'goroutines=[0-9]*' "$ART/sse_srv_run${RUN}.err" | tail -1 | cut -d= -f2 || echo 0)
  DELTA=$((SRSS - SBASE))
  if [ "${EST:-0}" -gt 0 ]; then PS=$(( DELTA * 1024 / EST )); else PS=0; fi
  echo "$N,$EST,$SRSS,$DELTA,$PS,$SGO" | tee -a "$OUT"
done

kill -INT "$CLIENT_BG" 2>/dev/null || true
kill -INT "$SPID" 2>/dev/null || true
wait 2>/dev/null || true
echo "done run $RUN"
