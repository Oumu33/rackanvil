#!/bin/bash
# M1 PXE validation (v2): QEMU user-mode networking (SLIRP) provides DHCP+TFTP
# INSIDE the QEMU process — no host UDP sockets are needed, so this works in
# sandboxes that block UDP transmission (sendto -> EPERM).
#
# Chain under test:
#   guest iPXE ROM -> SLIRP DHCP -> SLIRP TFTP (boot.ipxe) ->
#   HTTP chain to our server at 10.0.2.2:9100 -> /api/v1/ipxe/bootscript
# 10.0.2.2 is SLIRP's host address as seen from the guest; QEMU forwards it to
# the host's 127.0.0.1 (loopback TCP, sandbox-safe).
#
# Success criterion (from server logs, no faking):
#   bootscript: mac=<guest-MAC>     <- the guest really fetched our script
# Exit 3 (SKIP) only when qemu-system-x86_64 is not installed.
# M2: the server now serves REAL boot assets (Debian bookworm netboot
# vmlinuz+initrd from ./assets), so we push validation one step further:
# guest iPXE downloads kernel+initrd and the kernel starts booting.
# PASS = "Linux version" appears on the guest serial console.
# (Full preseed unattended install is M3; honestly out of scope here.)
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
WORK=$ROOT/.pxetest
BIN=$WORK/bin
LOG=$WORK/logs
TFTP=$WORK/tftp
VM_MAC="52:54:00:12:34:56"
HTTP_PORT=19102
GUEST_HOST="10.0.2.2"   # SLIRP host address, guest's view
E1000_ROM="/usr/lib/ipxe/e1000_82540.rom"

rm -rf "$WORK" && mkdir -p "$BIN" "$LOG" "$TFTP"

# Preflight: only hard-require the qemu binary. Everything else (DHCP/TFTP)
# happens inside the QEMU process via SLIRP.
if ! command -v qemu-system-x86_64 >/dev/null 2>&1; then
  echo "SKIP: qemu-system-x86_64 not installed; cannot run live PXE validation."
  exit 3
fi
if [ ! -f "$E1000_ROM" ]; then
  echo "SKIP: iPXE ROM $E1000_ROM missing."
  exit 3
fi

cd "$ROOT" || exit 1
go build -o "$BIN/server" ./cmd/server || exit 1
echo "[pxe] server built"

# TFTP root served by QEMU's internal SLIRP TFTP server.
cat > "$TFTP/boot.ipxe" <<EOF
#!ipxe
dhcp || echo [m1] dhcp failed
chain http://${GUEST_HOST}:${HTTP_PORT}/api/v1/ipxe/bootscript?mac=\${mac} || echo [m1] chain failed
EOF
echo "[pxe] tftp root ready ($TFTP/boot.ipxe)"

# public-url uses the guest-visible host address so rendered kernel/initrd
# URLs are correct from inside the VM. --assets serves the real Debian
# netboot vmlinuz+initrd (M2 provisioning).
"$BIN/server" -addr "127.0.0.1:$HTTP_PORT" -public-url "http://${GUEST_HOST}:${HTTP_PORT}" \
  -assets "$ROOT/assets" \
  > "$LOG/server.log" 2>&1 &
SRV=$!
QEMU=""
cleanup() { [ -n "$QEMU" ] && kill "$QEMU" 2>/dev/null; kill $SRV 2>/dev/null; }
trap cleanup EXIT

sleep 1
curl -sf "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null || { echo "FAIL: server not up"; exit 1; }
# self-test the exact URL the guest will chain (via loopback, same handler).
curl -sf "http://127.0.0.1:$HTTP_PORT/api/v1/ipxe/bootscript?mac=selftest" \
  | grep -q "rackanvil" || { echo "FAIL: bootscript self-test"; exit 1; }
echo "[pxe] server up, bootscript self-test ok"

# Boot the VM: e1000 PXE ROM (iPXE) -> SLIRP DHCP -> SLIRP TFTP boot.ipxe ->
# chain our bootscript over HTTP -> download real vmlinuz+initrd -> boot.
# No KVM here; pure emulation is fine. 1G RAM for the 40MB initrd.
# Generous timeout: ~49MB over emulated SLIRP takes a while.
timeout 620 qemu-system-x86_64 \
  -m 1024 -display none -serial file:"$LOG/serial.log" \
  -netdev user,id=n0,tftp="$TFTP",bootfile=boot.ipxe \
  -device e1000,netdev=n0,mac=$VM_MAC,romfile=$E1000_ROM \
  -boot order=n > "$LOG/qemu.log" 2>&1 &
QEMU=$!
echo "[pxe] VM booting via SLIRP (up to ~600s: bootscript -> kernel download -> boot)..."

deadline=$((SECONDS+600))
got_bootscript=0
while [ $SECONDS -lt $deadline ]; do
  if [ "$got_bootscript" = "0" ] && grep -q "bootscript: mac=$VM_MAC" "$LOG/server.log" 2>/dev/null; then
    got_bootscript=1
    echo "[pxe] bootscript fetched by guest; waiting for kernel boot on serial..."
  fi
  if grep -q "Linux version" "$LOG/serial.log" 2>/dev/null; then
    break
  fi
  # bail early if qemu died
  if ! kill -0 "$QEMU" 2>/dev/null; then
    echo "[pxe] qemu exited early"
    break
  fi
  sleep 5
done

echo "=== server.log (bootscript/bootasset lines) ==="
grep -E "bootscript:|bootasset:" "$LOG/server.log" | head -20
echo "=== serial.log (kernel lines) ==="
grep -m3 "Linux version" "$LOG/serial.log" 2>/dev/null

if grep -q "Linux version" "$LOG/serial.log" 2>/dev/null; then
  echo "PASS: QEMU guest PXE -> SLIRP DHCP/TFTP -> our bootscript -> real Debian kernel booting"
  exit 0
elif [ "$got_bootscript" = "1" ]; then
  echo "PARTIAL: bootscript fetched but kernel did not boot (see $LOG/serial.log, $LOG/qemu.log)"
  exit 1
else
  echo "FAIL: guest never fetched bootscript (see $LOG/server.log, $LOG/qemu.log)"
  exit 1
fi
