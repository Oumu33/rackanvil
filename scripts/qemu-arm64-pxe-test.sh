#!/bin/bash
# M5-E: ARM64 (aarch64) PXE full chain in QEMU.
#
# Chain under test:
#   AAVMF UEFI -> SLIRP DHCP (bootfile) -> iPXE arm64 (built from source,
#   embedded script) -> HTTP chain to our server
#   /api/v1/ipxe/bootscript?mac=..&arch=aarch64 -> our server serves
#   /ipxe/arm64/linux + /ipxe/arm64/initrd.gz -> arm64 kernel boots.
#
# Fallback path (if ipxe-arm64.efi cannot be built in this sandbox):
#   AAVMF UEFI -> SLIRP DHCP -> bootnetaa64.efi (Debian d-i GRUB netboot,
#   fetched via our mirror proxy) -> grub.cfg -> kernel+initrd over HTTP
#   from OUR server -> arm64 kernel boots.
# The fallback still validates: our server serving arm64 boot assets +
# an arm64 Linux kernel booting on serial. The report must say which path
# was actually exercised.
#
# PASS: server log shows the arm64 boot request AND serial shows "Linux version".
#   - path A evidence: "bootscript: mac=<MAC> arch=aarch64"
#   - path B evidence: "bootasset: serving linux" + "bootasset: serving initrd.gz"
# Exit 3 (SKIP) only when qemu-system-aarch64 is missing.
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
WORK=$ROOT/.armtest
BIN=$WORK/bin
LOG=$WORK/logs
TFTP=$WORK/tftp
VM_MAC="52:54:00:12:34:77"
HTTP_PORT=19106
MIRROR_PORT=18999
GUEST_HOST="10.0.2.2"
AAVMF_CODE="/usr/share/AAVMF/AAVMF_CODE.fd"

rm -rf "$WORK" && mkdir -p "$BIN" "$LOG" "$TFTP"

if ! command -v qemu-system-aarch64 >/dev/null 2>&1; then echo "SKIP: no qemu-system-aarch64"; exit 3; fi
if [ ! -f "$AAVMF_CODE" ]; then echo "SKIP: no AAVMF firmware ($AAVMF_CODE)"; exit 3; fi

cd "$ROOT" || exit 1
go build -o "$BIN/server" ./cmd/server || exit 1
echo "[arm] server built"

# VARS pflash must be writable: copy to workdir.
cp /usr/share/AAVMF/AAVMF_VARS.fd "$WORK/AAVMF_VARS.fd"

# ---- path A: build ipxe-arm64.efi from source (needs aarch64 cross gcc) ----
IPXE_EFI="$TFTP/ipxe-arm64.efi"
PATH_A_OK=0
if [ ! -f "$IPXE_EFI" ]; then
  echo "[arm] path A: trying to build ipxe-arm64.efi from source..."
  if ! command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
    apt-get install -y gcc-aarch64-linux-gnu binutils-aarch64-linux-gnu >/dev/null 2>&1 || true
  fi
  if command -v aarch64-linux-gnu-gcc >/dev/null 2>&1 && command -v git >/dev/null 2>&1; then
    [ -d "$WORK/ipxe-src" ] || git clone --depth 1 https://github.com/ipxe/ipxe.git "$WORK/ipxe-src" 2>&1 | tail -1
    cat > "$WORK/boot-arm64.ipxe" <<EOF
#!ipxe
dhcp || echo [m5e] dhcp failed
chain http://${GUEST_HOST}:${HTTP_PORT}/api/v1/ipxe/bootscript?mac=\${mac}&arch=aarch64 || echo [m5e] chain failed
EOF
    ( cd "$WORK/ipxe-src/src" && make -j2 bin-arm64-efi/ipxe.efi EMBED="$WORK/boot-arm64.ipxe" >"$LOG/ipxe-build.log" 2>&1 ) && \
      cp "$WORK/ipxe-src/src/bin-arm64-efi/ipxe.efi" "$IPXE_EFI" && PATH_A_OK=1
  fi
fi
[ -f "$IPXE_EFI" ] && PATH_A_OK=1
echo "[arm] path A (iPXE arm64): PATH_A_OK=$PATH_A_OK"

# ---- path B: Debian d-i GRUB netboot (fallback) ----
BOOTFILE=""
if [ "$PATH_A_OK" = "1" ]; then
  BOOTFILE="ipxe-arm64.efi"
else
  echo "[arm] path B: fetching bootnetaa64.efi via mirror proxy..."
  python3 "$ROOT/scripts/mirror-proxy.py" "$MIRROR_PORT" http://mirrors.aliyun.com "$ROOT/.mirror-cache" \
    > "$LOG/mirror.log" 2>&1 &
  MIRROR=$!
  sleep 2
  curl -sf "http://127.0.0.1:$MIRROR_PORT/debian/dists/bookworm/main/installer-arm64/current/images/netboot/debian-installer/arm64/bootnetaa64.efi" \
    -o "$TFTP/bootnetaa64.efi" || { echo "FAIL: cannot fetch bootnetaa64.efi"; exit 1; }
  mkdir -p "$TFTP/grub"
  cat > "$TFTP/grub/grub.cfg" <<EOF
set timeout=5
menuentry 'rackanvil arm64 (our server assets)' {
  linux (http,${GUEST_HOST}:${HTTP_PORT})/ipxe/arm64/linux console=ttyAMA0,115200 earlycon
  initrd (http,${GUEST_HOST}:${HTTP_PORT})/ipxe/arm64/initrd.gz
}
EOF
  # also drop copies at other locations GRUB might probe
  mkdir -p "$TFTP/EFI/BOOT" && cp "$TFTP/grub/grub.cfg" "$TFTP/EFI/BOOT/grub.cfg"
  BOOTFILE="bootnetaa64.efi"
fi
echo "[arm] bootfile=$BOOTFILE (path A=$PATH_A_OK)"

"$BIN/server" -addr "127.0.0.1:$HTTP_PORT" -public-url "http://${GUEST_HOST}:${HTTP_PORT}" \
  -assets "$ROOT/assets" \
  > "$LOG/server.log" 2>&1 &
SRV=$!
QEMU=""
cleanup() { [ -n "$QEMU" ] && kill "$QEMU" 2>/dev/null; kill $SRV ${MIRROR:-} 2>/dev/null; }
trap cleanup EXIT
# Server startup can be slow while the sandbox is busy; poll up to ~20s.
up=0
for i in $(seq 1 20); do
  if curl -sf "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null 2>&1; then up=1; break; fi
  sleep 1
done
[ "$up" = "1" ] || { echo "FAIL: server not up"; cat "$LOG/server.log"; exit 1; }
curl -sf "http://127.0.0.1:$HTTP_PORT/api/v1/ipxe/bootscript?mac=selftest&arch=aarch64" \
  | grep -q "ipxe/arm64/linux" || { echo "FAIL: aarch64 bootscript self-test"; exit 1; }
echo "[arm] server up, aarch64 bootscript self-test ok"

timeout 700 qemu-system-aarch64 \
  -M virt -cpu cortex-a72 -m 1024 -display none -serial file:"$LOG/serial.log" \
  -drive if=pflash,format=raw,readonly=on,file="$AAVMF_CODE" \
  -drive if=pflash,format=raw,file="$WORK/AAVMF_VARS.fd" \
  -netdev user,id=n0,tftp="$TFTP",bootfile="$BOOTFILE" \
  -device virtio-net-pci,netdev=n0,mac=$VM_MAC \
  -boot order=n > "$LOG/qemu.log" 2>&1 &
QEMU=$!
echo "[arm] VM booting (path A=$PATH_A_OK, up to ~660s)..."

deadline=$((SECONDS+660))
got_req=0
while [ $SECONDS -lt $deadline ]; do
  if [ "$got_req" = "0" ]; then
    if [ "$PATH_A_OK" = "1" ] && grep -q "bootscript: mac=$VM_MAC arch=aarch64" "$LOG/server.log" 2>/dev/null; then
      got_req=1; echo "[arm] guest fetched aarch64 bootscript"
    elif [ "$PATH_A_OK" != "1" ] && grep -q "bootasset: serving linux" "$LOG/server.log" 2>/dev/null; then
      got_req=1; echo "[arm] guest fetching arm64 kernel from our server (path B)"
    fi
  fi
  if grep -q "Linux version" "$LOG/serial.log" 2>/dev/null; then break; fi
  if ! kill -0 "$QEMU" 2>/dev/null; then echo "[arm] qemu exited early"; break; fi
  sleep 5
done

echo "=== server.log (arm lines) ==="
grep -E "bootscript:|bootasset:" "$LOG/server.log" | head -20
echo "=== serial.log (kernel lines) ==="
grep -m2 "Linux version" "$LOG/serial.log" 2>/dev/null

if grep -q "Linux version" "$LOG/serial.log" 2>/dev/null && [ "$got_req" = "1" ]; then
  echo "PASS(path A=$PATH_A_OK): aarch64 UEFI -> our server -> arm64 Linux kernel booting"
  exit 0
elif grep -q "Linux version" "$LOG/serial.log" 2>/dev/null; then
  echo "PARTIAL: arm64 kernel booted but server request evidence missing"
  exit 1
elif [ "$got_req" = "1" ]; then
  echo "PARTIAL: server saw arm64 request but kernel did not boot"
  exit 1
else
  echo "FAIL: no arm64 boot (see $LOG/serial.log $LOG/qemu.log $LOG/server.log)"
  exit 1
fi
