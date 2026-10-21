#!/bin/bash
# M1 end-to-end: real server process + real agent process.
# Flow: initialize -> checkin -> enqueue shell task -> report -> verify.
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
BIN=$ROOT/.m1e2e/bin
LOG=$ROOT/.m1e2e/logs
PORT=19100

rm -rf "$ROOT/.m1e2e" && mkdir -p "$BIN" "$LOG"
cd "$ROOT" || exit 1
go build -o "$BIN/server" ./cmd/server || exit 1
go build -o "$BIN/agent" ./cmd/agent || exit 1
echo "[e2e] built"

"$BIN/server" -addr "127.0.0.1:$PORT" -public-url "http://127.0.0.1:$PORT" > "$LOG/server.log" 2>&1 &
SRV=$!
"$BIN/agent" -server "http://127.0.0.1:$PORT" -interval 2s > "$LOG/agent.log" 2>&1 &
AGT=$!
cleanup() { kill $SRV $AGT 2>/dev/null; }
trap cleanup EXIT

# wait for server
for i in $(seq 1 30); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null && break
  sleep 0.5
done
curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null || { echo "FAIL: server not up"; exit 1; }
echo "[e2e] server up"

# wait for agent initialize (appears in /debug/hosts)
HOST_ID=""
for i in $(seq 1 30); do
  HOST_ID=$(curl -sf "http://127.0.0.1:$PORT/debug/hosts" | grep -o '"ID":"[^"]*"' | head -1 | cut -d'"' -f4)
  [ -n "$HOST_ID" ] && break
  sleep 0.5
done
[ -n "$HOST_ID" ] || { echo "FAIL: agent never initialized"; exit 1; }
echo "[e2e] agent initialized: $HOST_ID"

# enqueue a shell task
TASK_ID=$(curl -sf -X POST "http://127.0.0.1:$PORT/debug/enqueue-task" \
  -H 'Content-Type: application/json' \
  -d "{\"host_id\":\"$HOST_ID\",\"command\":\"echo hello-m1 && hostname\",\"timeout_sec\":10}" \
  | grep -o '"task_id":"[^"]*"' | cut -d'"' -f4)
[ -n "$TASK_ID" ] || { echo "FAIL: enqueue failed"; exit 1; }
echo "[e2e] task enqueued: $TASK_ID"

# wait for task completion (agent polls every 2s)
STATUS=""
for i in $(seq 1 30); do
  STATUS=$(curl -sf "http://127.0.0.1:$PORT/debug/tasks" | grep -o '"Status":"[^"]*"' | head -1 | cut -d'"' -f4)
  { [ "$STATUS" = "done" ] || [ "$STATUS" = "failed" ]; } && break
  sleep 0.5
done
echo "[e2e] task status: $STATUS"
[ "$STATUS" = "done" ] || { echo "FAIL: task not done"; tail -20 "$LOG/agent.log"; exit 1; }

# verify stdout contains our marker
curl -sf "http://127.0.0.1:$PORT/debug/tasks" | grep -q "hello-m1" || { echo "FAIL: stdout mismatch"; exit 1; }

# bootscript check
curl -sf "http://127.0.0.1:$PORT/api/v1/ipxe/bootscript?mac=52:54:00:aa:bb:cc" | grep -q "#!ipxe" \
  || { echo "FAIL: bootscript"; exit 1; }

echo "PASS: M1 e2e (initialize -> checkin -> task -> report -> bootscript)"
echo "--- server.log (tail) ---"; tail -8 "$LOG/server.log"
echo "--- agent.log (tail) ---"; tail -8 "$LOG/agent.log"
