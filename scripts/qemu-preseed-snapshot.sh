#!/bin/bash
# M6-D: preseed install closed loop via QEMU SNAPSHOT RESUME.
#
# Problem (M3/M5, 5 attempts): a full TCG Debian install needs 40-60 min but
# this sandbox restarts every ~30-45 min and kills everything.
# Strategy: install into a qcow2 disk; every ~12 min take an internal
# snapshot (HMP savevm, RAM included, -m 1024 to keep it fast). All state
# (disk, server DB, state file) lives in .snaptest/ inside the workspace,
# which survives sandbox restarts. Re-invoking this script resumes from
# the latest snapshot (-loadvm) instead of starting over.
#
# Phases: 1 = PXE install (resumable), 2 = boot installed disk, wait for
# agent re-checkin. PASS = install-done seen AND initialize count >= 2.
#
# Exit codes: 0 PASS · 1 real FAIL · 2 progress made, invoke again to
# resume · 3 setup missing.
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
WORK=$ROOT/.snaptest
BIN=$WORK/bin
LOG=$WORK/logs
TFTP=$WORK/tftp
STATE=$WORK/state.env
VM_MAC="52:54:00:12:34:99"
HTTP_PORT=19105
MIRROR_PORT=18998
GUEST_HOST="10.0.2.2"
E1000_ROM="/usr/lib/ipxe/e1000_82540.rom"
DISK=$WORK/disk.qcow2
SNAP_TAG="m6snap"
BUDGET=${SNAPTEST_BUDGET:-1500}     # per-invocation watch budget (s)
SNAP_EVERY=${SNAPTEST_SNAP_EVERY:-720} # snapshot cadence (s)

mkdir -p "$BIN" "$LOG" "$TFTP"
# shellcheck disable=SC1090
[ -f "$STATE" ] && . "$STATE"
PHASE=${PHASE:-1}
RUN=${RUN:-0}
LAST_SNAP=${LAST_SNAP:-0}
HID=${HID:-}
RUN=$((RUN+1))
save_state() {
  cat > "$STATE" <<EOF
PHASE=$PHASE
RUN=$RUN
LAST_SNAP=$LAST_SNAP
HID=$HID
EOF
}
save_state

# --- self-heal toolchain (sandbox wipes /usr on restart) ---
if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then
  echo "[snap] qemu missing, restoring from apt cache..."
  dpkg -i /var/cache/apt/archives/*.deb >/dev/null 2>&1
fi
if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then echo "SETUP-MISSING: qemu-system-x86_64"; exit 3; fi
if [ ! -f "$E1000_ROM" ]; then echo "SETUP-MISSING: iPXE ROM $E1000_ROM"; exit 3; fi

cd "$ROOT" || exit 1
go build -o "$BIN/server" ./cmd/server || exit 1
go build -o "$BIN/agent" ./cmd/agent || exit 1
echo "[snap] run #$RUN phase=$PHASE; binaries built"

if [ ! -f "$DISK" ]; then
  qemu-img create -f qcow2 "$DISK" 12G >/dev/null || exit 1
  echo "[snap] qcow2 disk created"
fi

cat > "$TFTP/boot.ipxe" <<EOF
#!ipxe
dhcp || echo [m6d] dhcp failed
chain http://${GUEST_HOST}:${HTTP_PORT}/api/v1/ipxe/bootscript?mac=\${mac} || echo [m6d] chain failed
EOF

# Server DB persists in $WORK so host/token/job survive sandbox restarts.
"$BIN/server" -addr "127.0.0.1:$HTTP_PORT" -public-url "http://${GUEST_HOST}:${HTTP_PORT}" \
  -db "sqlite:$WORK/e2e.db" -assets "$ROOT/assets" -preseed-dir "$ROOT/assets/preseed" \
  -agent-bin "$BIN/agent" \
  -mirror-host "${GUEST_HOST}:${MIRROR_PORT}" -mirror-dir /debian \
  >> "$LOG/server.log" 2>&1 &
SRV=$!
python3 "$ROOT/scripts/mirror-proxy.py" "$MIRROR_PORT" http://mirrors.aliyun.com "$ROOT/.mirror-cache" \
  >> "$LOG/mirror.log" 2>&1 &
MIRROR=$!
QEMU=""
cleanup() { [ -n "$QEMU" ] && kill "$QEMU" 2>/dev/null; kill $SRV $MIRROR 2>/dev/null; }
trap cleanup EXIT
sleep 2
curl -sf "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null || { echo "FAIL: server not up"; exit 1; }
curl -sf "http://127.0.0.1:$MIRROR_PORT/debian/dists/bookworm/InRelease" -o /dev/null \
  || { echo "FAIL: mirror proxy not up"; exit 1; }
echo "[snap] server + mirror proxy up"

# Register host once (persisted HID); create install job once per phase-1
# entry if not armed. On resume the guest is mid-install and does not need
# re-arming; the persisted DB keeps token/install state valid.
if [ -z "$HID" ]; then
  REG=$(curl -s -X POST "http://127.0.0.1:$HTTP_PORT/api/v1/client/initialize" \
    -d "{\"hostname\":\"qemu-preseed-snap\",\"macs\":[\"$VM_MAC\"],\"ips\":[]}")
  HID=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['host_id'])" 2>/dev/null)
  [ -n "$HID" ] || { echo "FAIL: register: $REG"; exit 1; }
  save_state
  echo "[snap] registered host=$HID"
fi
if [ "$PHASE" = "1" ]; then
  # (Re)arm if the server does not report the host armed shortly. Creating
  # a fresh install job is the arming mechanism; harmless on resume since
  # the running guest never re-fetches the bootscript.
  sleep 3
  if ! grep -q "armed for preseed install" "$LOG/server.log" 2>/dev/null; then
    curl -s -X POST "http://127.0.0.1:$HTTP_PORT/api/v1/orch/jobs" \
      -d "{\"host_id\":\"$HID\",\"name\":\"m6-snap-install\",\"steps\":[{\"name\":\"系统安装\",\"kind\":\"install\"}]}" \
      > "$LOG/job.json" 2>/dev/null
    echo "[snap] install job (re)created: $(cat "$LOG/job.json" 2>/dev/null | head -c 120)"
  fi
fi

hmp() { # send a human-monitor command via the unix monitor socket
  python3 - "$WORK/mon.sock" "$1" 2>&1 << 'PYEOF'
import socket, sys, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(300)
s.connect(sys.argv[1])
s.recv(4096)
s.sendall((sys.argv[2] + "\n").encode())
time.sleep(0.5)
out = b""
try:
    while b"(qemu)" not in out:
        chunk = s.recv(4096)
        if not chunk:
            break
        out += chunk
except socket.timeout:
    pass
print(out.decode(errors="replace")[-300:])
s.close()
PYEOF
}

launch_phase1() {
  local extra=()
  if [ "${RESUME:-0}" = "1" ]; then extra=(-loadvm "$SNAP_TAG"); echo "[snap] resuming from snapshot $SNAP_TAG"; fi
  qemu-system-x86_64 \
    -m 1024 -smp 2 -accel tcg,thread=multi \
    -display none -serial file:"$LOG/serial-run$RUN.log" \
    -drive file="$DISK",format=qcow2,if=virtio \
    -netdev user,id=n0,tftp="$TFTP",bootfile=boot.ipxe \
    -device e1000,netdev=n0,mac=$VM_MAC,romfile=$E1000_ROM \
    -monitor unix:"$WORK/mon.sock",server,nowait \
    -boot order=n "${extra[@]}" > "$LOG/qemu-run$RUN.log" 2>&1 &
  QEMU=$!
}

take_snapshot() {
  echo "[snap] savevm $SNAP_TAG ..."
  local out
  out=$(hmp "savevm $SNAP_TAG")
  echo "$out" | tail -2
  if echo "$out" | grep -qi "traceback\|error"; then
    echo "[snap] WARN: savevm output looks failed; LAST_SNAP NOT updated"
    return 1
  fi
  LAST_SNAP=$(date +%s)
  save_state
  echo "[snap] snapshot recorded at $LAST_SNAP"
}

if [ "$PHASE" = "1" ]; then
  # Resume decision — FAIL CLOSED. Run 7 (M6) silently fresh-booted over a
  # late-stage install because this check raced the sandbox's /usr restore
  # (qemu-img transiently broken, error hidden by 2>/dev/null). A fresh
  # boot's partman would have wiped the snapshotted install. Now: retry the
  # listing, and if a previous run exists but the snapshot cannot be
  # positively confirmed, ABORT instead of booting.
  RESUME=0
  snaplist=""
  for try in 1 2 3 4 5; do
    snaplist=$(qemu-img snapshot -l "$DISK" 2>&1) && break
    echo "[snap] qemu-img snapshot list failed (try $try): $(echo "$snaplist" | head -1)"
    sleep 6
  done
  if echo "$snaplist" | grep -q "$SNAP_TAG"; then
    RESUME=1
  elif [ "$RUN" -gt 1 ]; then
    echo "ABORT: run #$RUN with existing state but snapshot '$SNAP_TAG' not confirmable; refusing to fresh-boot over an in-progress install"
    exit 3
  fi
  launch_phase1
  deadline=$(( $(date +%s) + BUDGET ))
  outcome=""
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if grep -q "install-done: host=$HID" "$LOG/server.log" 2>/dev/null; then
      # Grace period: let the guest finish late_command and unmount /target
      # before phase transition kills QEMU. Killing the instant install-done
      # landed once cost us the agent files (page-cache loss, M6 run 9/10);
      # the template now syncs before posting, belt and braces here too.
      outcome="done"; echo "[snap] install-done received; 60s guest grace period"
      sleep 60
      break
    fi
    if ! kill -0 "$QEMU" 2>/dev/null; then outcome="qemu-exit"; echo "[snap] qemu exited early"; break; fi
    now=$(date +%s)
    if [ $((now - LAST_SNAP)) -ge "$SNAP_EVERY" ]; then take_snapshot; fi
    sleep 15
  done
  if [ "$outcome" = "done" ]; then
    PHASE=2; save_state
    kill "$QEMU" 2>/dev/null; QEMU=""; sleep 3
  else
    # Leave a fresh snapshot behind for the next invocation, then report
    # resumable progress (unless QEMU died on its own — then still try).
    if kill -0 "$QEMU" 2>/dev/null; then take_snapshot; fi
    echo "[snap] progress saved; invoke again to resume (state: phase=$PHASE run=$RUN)"
    echo "=== serial tail ==="; tail -4 "$LOG/serial-run$RUN.log" 2>/dev/null
    exit 2
  fi
fi

# ---- phase 2: boot installed system from disk ----
# The installed kernel has no serial console (stock grub.cfg), so phase 2
# is blind on serial: the only progress signals are the server log and VGA
# screendumps. A TCG boot to login+agent can take a long while; the
# original 400s window proved too short (M6 run 9), hence 1500s default.
P2_TIMEOUT=${PHASE2_TIMEOUT:-1500}
echo "[snap] phase 2: booting installed disk, waiting for agent re-checkin (window ${P2_TIMEOUT}s)..."
timeout $((P2_TIMEOUT + 60)) qemu-system-x86_64 \
  -m 1024 -smp 2 -accel tcg,thread=multi \
  -display none -serial file:"$LOG/serial-phase2-run$RUN.log" \
  -drive file="$DISK",format=qcow2,if=virtio \
  -netdev user,id=n0 \
  -device e1000,netdev=n0,mac=$VM_MAC \
  -monitor unix:"$WORK/mon2.sock",server,nowait \
  -boot order=c > "$LOG/qemu-phase2-run$RUN.log" 2>&1 &
QEMU=$!
deadline=$(( $(date +%s) + P2_TIMEOUT ))
recheckin=0
while [ "$(date +%s)" -lt "$deadline" ]; do
  n=$(grep -c "initialize:.*id=$HID" "$LOG/server.log" 2>/dev/null || echo 0)
  if [ "$n" -ge 2 ]; then recheckin=1; echo "[snap] agent re-checkin seen (initialize #$n)"; break; fi
  if ! kill -0 "$QEMU" 2>/dev/null; then echo "[snap] phase2 qemu exited early"; break; fi
  sleep 10
done
if [ "$recheckin" = "1" ]; then
  echo "PASS: preseed unattended install completed (snapshot-resumed) + agent re-enrolled after reboot"
  exit 0
fi
echo "FAIL: install-done seen but no agent re-checkin (see $LOG/serial-phase2-run$RUN.log)"
exit 1
