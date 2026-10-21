#!/bin/bash
# M5-G: run the REAL agent against the REAL server on THIS cloud host.
# initialize -> full collectors (/proc, /sys) -> heartbeat -> shell task ->
# report -> DB; then firmware.detect / raid.query / disk.scan executed for
# real on this host (no SES tools / no storcli / no RAID cards -> expect
# honest negative results, never faked success).
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
BIN=$ROOT/.m5g/bin
LOG=$ROOT/.m5g/logs
PORT=19102
DB="$ROOT/.m5g/rackanvil.db"

rm -rf "$ROOT/.m5g" && mkdir -p "$BIN" "$LOG"
cd "$ROOT" || exit 1
go build -o "$BIN/server" ./cmd/server || exit 1
go build -o "$BIN/agent" ./cmd/agent || exit 1
echo "[m5g] built"

"$BIN/server" -addr "127.0.0.1:$PORT" -public-url "http://127.0.0.1:$PORT" -db "sqlite:$DB" > "$LOG/server.log" 2>&1 &
SRV=$!
"$BIN/agent" -server "http://127.0.0.1:$PORT" -interval 2s -metrics-interval 5s > "$LOG/agent.log" 2>&1 &
AGT=$!
cleanup() { kill $SRV $AGT 2>/dev/null; }
trap cleanup EXIT

for i in $(seq 1 30); do curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null && break; sleep 0.5; done
curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null || { echo "FAIL: server not up"; exit 1; }
echo "[m5g] server up"

HOST_ID=""
for i in $(seq 1 30); do
  HOST_ID=$(curl -sf "http://127.0.0.1:$PORT/debug/hosts" | grep -o '"ID":"[^"]*"' | head -1 | cut -d'"' -f4)
  [ -n "$HOST_ID" ] && break; sleep 0.5
done
[ -n "$HOST_ID" ] || { echo "FAIL: agent never initialized"; exit 1; }
echo "[m5g] agent initialized: $HOST_ID"
curl -sf "http://127.0.0.1:$PORT/debug/hosts" | head -c 600; echo

# 1) shell task with real host facts
TASK_ID=$(curl -sf -X POST "http://127.0.0.1:$PORT/debug/enqueue-task" \
  -H 'Content-Type: application/json' \
  -d "{\"host_id\":\"$HOST_ID\",\"command\":\"echo m5g-real && uname -a && nproc && cat /proc/meminfo | head -1\",\"timeout_sec\":15}" \
  | grep -o '"task_id":"[^"]*"' | cut -d'"' -f4)
[ -n "$TASK_ID" ] || { echo "FAIL: enqueue"; exit 1; }
STATUS=""
for i in $(seq 1 30); do
  STATUS=$(curl -sf "http://127.0.0.1:$PORT/debug/tasks" | grep -o '"Status":"[^"]*"' | head -1 | cut -d'"' -f4)
  { [ "$STATUS" = "done" ] || [ "$STATUS" = "failed" ]; } && break; sleep 0.5
done
echo "[m5g] shell task status: $STATUS"
curl -sf "http://127.0.0.1:$PORT/api/v1/tasks/$TASK_ID" | head -c 500; echo

# 2) wait for collectors cycle, then read real metrics via health API
sleep 8
echo "[m5g] health (real collectors):"
curl -sf "http://127.0.0.1:$PORT/api/v1/hosts/$HOST_ID/health" | head -c 1200; echo

# helper: enqueue typed task via M4 API, wait, print result
typed_task() {
  local url=$1 desc=$2 payload=$3
  local tid st out
  tid=$(curl -sf -X POST "http://127.0.0.1:$PORT$url" -H 'Content-Type: application/json' \
    -d "$payload" | grep -o '"task_id":"[^"]*"' | cut -d'"' -f4)
  [ -n "$tid" ] || { echo "[m5g] $desc: FAIL enqueue"; return 1; }
  for i in $(seq 1 30); do
    st=$(curl -sf "http://127.0.0.1:$PORT/api/v1/tasks/$tid" | grep -o '"Status":"[^"]*"' | cut -d'"' -f4)
    { [ "$st" = "done" ] || [ "$st" = "failed" ]; } && break; sleep 0.5
  done
  out=$(curl -sf "http://127.0.0.1:$PORT/api/v1/tasks/$tid")
  echo "[m5g] $desc: status=$st"
  echo "$out" | head -c 700; echo
}

# 3) firmware.detect for real (no RAID/HBA cards expected)
typed_task "/api/v1/firmware/detect" "firmware.detect" "{\"host_id\":\"$HOST_ID\"}"
# 4) raid.query for real (no storcli expected -> honest error)
typed_task "/api/v1/raid/query" "raid.query" "{\"host_id\":\"$HOST_ID\"}"
# 5) disk.scan for real (no SES tools expected)
typed_task "/api/v1/locator/scan" "disk.scan" "{\"host_id\":\"$HOST_ID\"}"

echo "[m5g] block devices on this host: $(ls /sys/block/ | tr '\n' ' ')"
echo "PASS: M5-G host e2e"
