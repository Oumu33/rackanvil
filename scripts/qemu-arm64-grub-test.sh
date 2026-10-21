#!/bin/bash
# M6-E: ARM64 PXE last mile — boot a SELF-BUILT grubaa64.efi whose config is
# EMBEDDED (grub-mkimage -c), fetched by AAVMF HTTP Boot straight from our
# server. No shim, no config-file discovery: the embedded script does
# net_bootp, then linux+initrd over HTTP from our server, then boot.
#
# Why: M5 proved the shim->Debian-GRUB chain loads GRUB but GRUB then sits
# at an empty prompt without fetching any grub.cfg (its config prefix over
# an HTTP/shim chain never resolved). Embedding the config removes the
# discovery step entirely. AAVMF here runs with Secure Boot off, so our
# unsigned GRUB is accepted when the firmware loads it directly.
#
# Chain: SLIRP DHCP (bootfile=http://10.0.2.2:19110/ipxe/arm64/grubaa64.efi)
#   -> PXEv4 attempt fails on the URL (expected, see M5 serial9)
#   -> EDK2 "UEFI HTTPv4" entry loads our grubaa64.efi
#   -> embedded cfg: net_bootp; linux/initrd from our server; boot
# PASS: serial shows "Linux version" AND server reqlog shows the guest
# fetched /ipxe/arm64/linux + /ipxe/arm64/initrd.gz.
set -u
export PATH=~/workspace/toolchain/go/bin:$PATH
ROOT=~/workspace/deplos-reimpl
WORK=$ROOT/.m6-armtest
LOG=$WORK/logs
PORT=19110
GRUBX=~/workspace/toolchain/grub/x
VM_MAC="52:54:00:12:34:77"
AAVMF_CODE="/usr/share/AAVMF/AAVMF_CODE.fd"

mkdir -p "$LOG"
rm -f "$WORK/RESULT.txt"

if ! command -v qemu-system-aarch64 >/dev/null 2>&1; then
  echo "SETUP-MISSING: qemu-system-aarch64 (restore: dpkg -i /var/cache/apt/archives/*.deb)" | tee "$WORK/RESULT.txt"; exit 3
fi
if [ ! -x "$GRUBX/usr/bin/grub-mkimage" ]; then
  echo "SETUP-MISSING: grub-mkimage (see docs/M6-progress.md)" | tee "$WORK/RESULT.txt"; exit 3
fi

cd "$ROOT" || exit 1
go build -o "$WORK/server" ./cmd/server || exit 1
echo "[m6e] server built"

# --- scratch assets: repo assets with grubaa64.efi replaced by our build ---
rm -rf "$WORK/assets"
cp -r "$ROOT/assets" "$WORK/assets"
cat > "$WORK/embedded.cfg" <<EOF
net_bootp
linux (http,10.0.2.2:${PORT})/ipxe/arm64/linux console=ttyAMA0,115200 earlycon=pl011,0x9000000
initrd (http,10.0.2.2:${PORT})/ipxe/arm64/initrd.gz
boot
EOF
"$GRUBX/usr/bin/grub-mkimage" -O arm64-efi \
  -o "$WORK/assets/debian-bookworm-arm64/grubaa64.efi" \
  -c "$WORK/embedded.cfg" -d "$GRUBX/usr/lib/grub/arm64-efi" -p /ipxe/arm64 \
  boot linux normal configfile http efinet net tftp part_gpt part_msdos fat ext2 search echo test gzio \
  > "$LOG/grub-mkimage.log" 2>&1 || { echo "FAIL: grub-mkimage"; cat "$LOG/grub-mkimage.log"; exit 1; }
echo "[m6e] self-built grubaa64.efi: $(stat -c%s "$WORK/assets/debian-bookworm-arm64/grubaa64.efi") bytes"

cp /usr/share/AAVMF/AAVMF_VARS.fd "$WORK/AAVMF_VARS.fd"

rm -f /tmp/m6e.db
"$WORK/server" -addr "127.0.0.1:$PORT" -public-url "http://10.0.2.2:$PORT" \
  -assets "$WORK/assets" -db "sqlite:/tmp/m6e.db" -log-requests \
  > "$LOG/server.log" 2>&1 &
SRV=$!
cleanup() { kill "$SRV" 2>/dev/null; [ -n "${QEMU:-}" ] && kill "$QEMU" 2>/dev/null; }
trap cleanup EXIT
up=0
for i in $(seq 1 20); do
  curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && { up=1; break; }
  sleep 1
done
[ "$up" = "1" ] || { echo "FAIL: server not up"; cat "$LOG/server.log"; exit 1; }
echo "[m6e] server up on :$PORT with request logging"

timeout 660 qemu-system-aarch64 \
  -M virt -cpu cortex-a72 -m 1024 -display none -serial file:"$LOG/serial.log" \
  -drive if=pflash,format=raw,readonly=on,file="$AAVMF_CODE" \
  -drive if=pflash,format=raw,file="$WORK/AAVMF_VARS.fd" \
  -netdev user,id=n0,bootfile="http://10.0.2.2:$PORT/ipxe/arm64/grubaa64.efi" \
  -device virtio-net-pci,netdev=n0,mac=$VM_MAC \
  -boot order=n > "$LOG/qemu.log" 2>&1 &
QEMU=$!
echo "[m6e] VM booting; waiting for 'Linux version' (up to ~600s)..."

deadline=$((SECONDS+600))
while [ $SECONDS -lt $deadline ]; do
  grep -q "Linux version" "$LOG/serial.log" 2>/dev/null && break
  kill -0 "$QEMU" 2>/dev/null || { echo "[m6e] qemu exited early"; break; }
  sleep 5
done

echo "=== reqlog (guest requests) ==="
grep "reqlog:" "$LOG/server.log" | grep -v "127.0.0.1" | head -20
grep "reqlog:" "$LOG/server.log" | head -20
echo "=== serial (key lines) ==="
grep -a -m3 -E "Linux version|GRUB|error|failed" "$LOG/serial.log" 2>/dev/null | head -10

if grep -q "Linux version" "$LOG/serial.log" 2>/dev/null && \
   grep -q "reqlog: GET /ipxe/arm64/linux " "$LOG/server.log" && \
   grep -q "reqlog: GET /ipxe/arm64/initrd.gz " "$LOG/server.log"; then
  echo "PASS: AAVMF HTTP Boot -> self-built GRUB (embedded cfg) -> our arm64 kernel booting" | tee "$WORK/RESULT.txt"
  exit 0
fi
echo "NOT-PASS: see $LOG/serial.log and $LOG/server.log" | tee "$WORK/RESULT.txt"
exit 1
