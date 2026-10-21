#!/bin/bash
# RackAnvil 引导资产获取：从 Debian 官方镜像下载 bookworm netboot 文件并校验。
#
# 仓库内不存放这些大二进制（各文件与官方发布逐字节一致，2026-10-21 已逐个
# 比对 sha256 实锤）。本脚本按 Debian 官方 images/SHA256SUMS 校验每个文件，
# 已存在且校验通过的文件跳过下载。release.sh 打包前自动调用本脚本。
#
# 文件映射（官方路径 -> 仓库 assets 路径）：
#   installer-amd64/.../netboot/debian-installer/amd64/linux        -> assets/debian-bookworm/vmlinuz
#   installer-amd64/.../netboot/debian-installer/amd64/initrd.gz    -> assets/debian-bookworm/initrd.img
#   installer-arm64/.../netboot/debian-installer/arm64/linux        -> assets/debian-bookworm-arm64/linux
#   installer-arm64/.../netboot/debian-installer/arm64/initrd.gz    -> assets/debian-bookworm-arm64/initrd.gz
#   installer-arm64/.../netboot/debian-installer/arm64/bootnetaa64.efi -> assets/debian-bookworm-arm64/bootnetaa64.efi
#   installer-arm64/.../netboot/debian-installer/arm64/grubaa64.efi -> assets/debian-bookworm-arm64/grubaa64.efi
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIRROR="${DEBIAN_MIRROR:-https://deb.debian.org/debian}"
DIST="bookworm"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fetch_one() {
  local arch="$1" remote="$2" dest="$3"
  local sums="$WORK/SHA256SUMS-$arch"
  if [ ! -f "$sums" ]; then
    curl -fsSL "$MIRROR/dists/$DIST/main/installer-$arch/current/images/SHA256SUMS" -o "$sums" \
      || { echo "FAIL: 下载官方 SHA256SUMS（$arch）失败"; exit 1; }
  fi
  local want
  want="$(grep -E "  \./$remote\$" "$sums" | awk '{print $1}')"
  [ -n "$want" ] || { echo "FAIL: 官方 SHA256SUMS 中找不到 $remote"; exit 1; }
  if [ -f "$dest" ] && [ "$(sha256sum "$dest" | awk '{print $1}')" = "$want" ]; then
    echo "[assets] 已存在且校验通过，跳过：${dest#"$ROOT"/}"
    return
  fi
  echo "[assets] 下载 $remote -> ${dest#"$ROOT"/}"
  mkdir -p "$(dirname "$dest")"
  curl -fsSL "$MIRROR/dists/$DIST/main/installer-$arch/current/images/$remote" -o "$dest.tmp" \
    || { echo "FAIL: 下载 $remote 失败"; exit 1; }
  local got
  got="$(sha256sum "$dest.tmp" | awk '{print $1}')"
  [ "$got" = "$want" ] || { echo "FAIL: $remote 校验不符（got $got want $want）"; rm -f "$dest.tmp"; exit 1; }
  mv "$dest.tmp" "$dest"
  echo "[assets] 校验通过：${dest#"$ROOT"/}"
}

A="assets/debian-bookworm"
G="assets/debian-bookworm-arm64"
fetch_one amd64 "netboot/debian-installer/amd64/linux"        "$ROOT/$A/vmlinuz"
fetch_one amd64 "netboot/debian-installer/amd64/initrd.gz"    "$ROOT/$A/initrd.img"
fetch_one arm64 "netboot/debian-installer/arm64/linux"        "$ROOT/$G/linux"
fetch_one arm64 "netboot/debian-installer/arm64/initrd.gz"    "$ROOT/$G/initrd.gz"
fetch_one arm64 "netboot/debian-installer/arm64/bootnetaa64.efi" "$ROOT/$G/bootnetaa64.efi"
fetch_one arm64 "netboot/debian-installer/arm64/grubaa64.efi" "$ROOT/$G/grubaa64.efi"
echo "[assets] 全部引导资产就绪（Debian 官方 netboot，sha256 校验通过）"
