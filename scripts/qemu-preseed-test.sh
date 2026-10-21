#!/bin/bash
# M3 core acceptance: FULL unattended Debian install in QEMU + agent re-checkin.
#
# Chain under test:
#   1. server (with --agent-bin + preseed templates) + orchestrator runner
#   2. register host (VM MAC) -> create orch job [install step] -> host armed
#   3. VM phase 1: PXE -> SLIRP DHCP/TFTP -> our bootscript (INSTALL MODE) ->
#      real Debian netboot kernel+initrd -> debian-installer fetches our
#      preseed -> fully automatic install -> late_command installs our agent
#      binary + enables systemd unit + POSTs install-done
#   4. VM phase 2: reboot from disk -> installed system boots -> agent
#      initialize/checkin (same MAC -> re-enroll)
#
# PASS (exit 0): install-done seen AND post-install agent checkin seen.
# Timeouts: install phase 2700s (45 min; debian netboot in soft emulation is slow),
# post-install checkin 300s.
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
WORK=$ROOT/.preseedtest
BIN=$WORK/bin
LOG=$WORK/logs
TFTP=$WORK/tftp
VM_MAC="52:54:00:12:34:99"
HTTP_PORT=19105
MIRROR_PORT=18998
GUEST_HOST="10.0.2.2"
E1000_ROM="/usr/lib/ipxe/e1000_82540.rom"
DISK=$WORK/disk.raw

rm -rf "$WORK" && mkdir -p "$BIN" "$LOG" "$TFTP"

if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then
  echo "[preseed] qemu missing, bootstrapping..."
  # Sandbox resets /etc and /usr: re-point apt at a fast mirror and install.
  if grep -q "azure.archive.ubuntu.com" /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null; then
    sed -i 's|http://azure.archive.ubuntu.com/ubuntu|http://mirrors.aliyun.com/ubuntu|; s|http://mirror.cogentco.com/pub/linux/ubuntu|http://mirrors.aliyun.com/ubuntu|' /etc/apt/sources.list.d/ubuntu.sources
  fi
  apt-get update -o Acquire::Retries=2 >/dev/null 2>&1
  apt-get install -y qemu-system-x86 ipxe >/dev/null 2>&1
fi
if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then echo "SKIP: no qemu"; exit 3; fi
if [ ! -f "$E1000_ROM" ]; then echo "SKIP: no iPXE ROM"; exit 3; fi
# No qemu-img in this environment: use a raw image (fallocate), QEMU reads it natively.
fallocate -l 12G "$DISK" || exit 1
echo "[preseed] disk created (raw 12G)"

cd "$ROOT" || exit 1
go build -o "$BIN/server" ./cmd/server || exit 1
go build -o "$BIN/agent" ./cmd/agent || exit 1
echo "[preseed] binaries built"

fallocate -l 12G "$DISK" || exit 1
echo "[preseed] disk created (raw 12G)"


cat > "$TFTP/boot.ipxe" <<EOF
#!ipxe
dhcp || echo [m3] dhcp failed
chain http://${GUEST_HOST}:${HTTP_PORT}/api/v1/ipxe/bootscript?mac=\${mac} || echo [m3] chain failed
EOF

"$BIN/server" -addr "127.0.0.1:$HTTP_PORT" -public-url "http://${GUEST_HOST}:${HTTP_PORT}" \
  -db "sqlite:$WORK/e2e.db" -assets "$ROOT/assets" -preseed-dir "$ROOT/assets/preseed" \
  -agent-bin "$BIN/agent" \
  -mirror-host "${GUEST_HOST}:${MIRROR_PORT}" -mirror-dir /debian \
  > "$LOG/server.log" 2>&1 &
SRV=$!
# Debian mirror reverse proxy on the host: the QEMU guest cannot reach the
# public internet in this sandbox (diagnosed via iPXE EGRESS_FAIL probe), so
# it installs through this proxy (guest -> 10.0.2.2:PORT -> host -> upstream).
# This mirrors how real deployments use a local mirror ("弹药库").
python3 "$ROOT/scripts/mirror-proxy.py" "$MIRROR_PORT" http://mirrors.aliyun.com "$ROOT/.mirror-cache" \
  > "$LOG/mirror.log" 2>&1 &
MIRROR=$!
QEMU=""
cleanup() { [ -n "$QEMU" ] && kill "$QEMU" 2>/dev/null; kill $SRV $MIRROR 2>/dev/null; }
trap cleanup EXIT
sleep 2
curl -sf "http://127.0.0.1:$MIRROR_PORT/debian/dists/bookworm/InRelease" -o /dev/null \
  || { echo "FAIL: mirror proxy not up"; exit 1; }
echo "[preseed] mirror proxy up (guest -> ${GUEST_HOST}:${MIRROR_PORT}/debian)"
curl -sf "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null || { echo "FAIL: server not up"; exit 1; }

# 1. register the VM's MAC, 2. create install-only orch job
REG=$(curl -s -X POST "http://127.0.0.1:$HTTP_PORT/api/v1/client/initialize" \
  -d "{\"hostname\":\"qemu-preseed\",\"macs\":[\"$VM_MAC\"],\"ips\":[]}")
HID=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['host_id'])")
TOKEN=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['agent_token'])")
[ -n "$HID" ] || { echo "FAIL: register"; echo "$REG"; exit 1; }
JID=$(curl -s -X POST "http://127.0.0.1:$HTTP_PORT/api/v1/orch/jobs" \
  -d "{\"host_id\":\"$HID\",\"name\":\"m3-preseed-e2e\",\"steps\":[{\"name\":\"系统安装\",\"kind\":\"install\"}]}" \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['job_id'])")
echo "[preseed] host=$HID job=$JID"

# wait for the orchestrator runner to arm install_pending
armed=0
for i in $(seq 1 24); do
  if grep -q "armed for preseed install" "$LOG/server.log" 2>/dev/null; then armed=1; break; fi
  sleep 5
done
[ "$armed" = "1" ] || { echo "FAIL: host never armed"; tail -5 "$LOG/server.log"; exit 1; }
echo "[preseed] host armed, self-testing preseed fetch..."
curl -sf "http://127.0.0.1:$HTTP_PORT/preseed/${TOKEN}.cfg" | grep -q "rackanvil" \
  || { echo "FAIL: preseed self-test (fetch with agent token)"; exit 1; }
echo "[preseed] preseed self-test ok, booting VM phase 1 (PXE -> installer)..."

# No `timeout` wrapper here: the install-done polling loop below (45 min)
# owns the deadline, and trap cleanup kills QEMU on exit.
qemu-system-x86_64 \
  -m 2048 -display none -serial file:"$LOG/serial1.log" \
  -drive file="$DISK",format=raw,if=virtio \
  -netdev user,id=n0,tftp="$TFTP",bootfile=boot.ipxe \
  -device e1000,netdev=n0,mac=$VM_MAC,romfile=$E1000_ROM \
  -boot order=n > "$LOG/qemu1.log" 2>&1 &
QEMU=$!

# Full Debian install under QEMU soft emulation takes ~35 min; allow 45.
# Override with PRESEED_TIMEOUT env (seconds).
TIMEOUT=${PRESEED_TIMEOUT:-2700}
deadline=$((SECONDS+TIMEOUT))
done_install=0
while [ $SECONDS -lt $deadline ]; do
  if grep -q "install-done: host=$HID" "$LOG/server.log" 2>/dev/null; then
    done_install=1; echo "[preseed] install-done received"; break
  fi
  if ! kill -0 "$QEMU" 2>/dev/null; then echo "[preseed] qemu phase1 exited early"; break; fi
  sleep 15
done
kill "$QEMU" 2>/dev/null; QEMU=""; sleep 3  # let phase-1 qemu release the disk image
echo "=== installer serial tail ==="; tail -5 "$LOG/serial1.log" 2>/dev/null
if [ "$done_install" != "1" ]; then
  echo "FAIL: install-done not received within ${TIMEOUT}s (see $LOG/serial1.log)"
  exit 1
fi

echo "[preseed] phase 2: boot installed system from disk, wait for agent re-checkin..."
timeout 600 qemu-system-x86_64 \
  -m 1024 -display none -serial file:"$LOG/serial2.log" \
  -drive file="$DISK",format=raw,if=virtio \
  -netdev user,id=n0 \
  -device e1000,netdev=n0,mac=$VM_MAC \
  -boot order=c > "$LOG/qemu2.log" 2>&1 &
QEMU=$!
deadline=$((SECONDS+360))
recheckin=0
while [ $SECONDS -lt $deadline ]; do
  if grep -q "initialize:.*id=$HID" "$LOG/server.log" 2>/dev/null; then
    # second initialize (re-enroll after install) — count occurrences >= 2
    n=$(grep -c "initialize:.*id=$HID" "$LOG/server.log")
    if [ "$n" -ge 2 ]; then recheckin=1; echo "[preseed] agent re-checkin seen (initialize #$n)"; break; fi
  fi
  if ! kill -0 "$QEMU" 2>/dev/null; then echo "[preseed] qemu phase2 exited early"; break; fi
  sleep 10
done

if [ "$recheckin" = "1" ]; then
  echo "PASS: preseed unattended install completed + agent re-enrolled after reboot"
  exit 0
else
  echo "FAIL: no agent re-checkin after install (see $LOG/serial2.log $LOG/server.log)"
  exit 1
fi
