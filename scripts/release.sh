#!/bin/bash
# rackanvil 发布脚本：交叉编译 + 打包 + 校验，可复跑。
#   ./scripts/release.sh            版本号取仓库根 VERSION 文件
#   VERSION=1.0.1 ./scripts/release.sh
# 产出 dist/rackanvil_<版本>_linux_{amd64,arm64}.tar.gz 与 dist/SHA256SUMS。
# 校验和一律在打包当下对实物生成，不手工维护（M11 教训：手工清单必陈旧）。
# 引导资产（Debian netboot 内核/initrd/GRUB，共约 122MB）不进 git 仓库，
# 打包前由 scripts/fetch-assets.sh 从 Debian 官方镜像下载并按官方
# SHA256SUMS 校验（已存在且校验通过则跳过下载）。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
command -v go >/dev/null 2>&1 || export PATH="$HOME/workspace/toolchain/go/bin:$PATH"
command -v go >/dev/null 2>&1 || { echo "FAIL: go toolchain not found"; exit 1; }

VERSION="${VERSION:-$(tr -d ' \n' < VERSION 2>/dev/null || echo 1.0.0)}"
VERSION="${VERSION#v}"
[ -n "$VERSION" ] || { echo "FAIL: empty version"; exit 1; }
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
MOD="github.com/Oumu33/rackanvil/internal/version"
LDFLAGS="-s -w -X $MOD.Version=$VERSION -X $MOD.Commit=$COMMIT -X $MOD.BuildDate=$DATE"

# 引导资产就绪（缺失/校验不符时从 Debian 官方镜像下载）
bash scripts/fetch-assets.sh

DIST="$ROOT/dist"
STAGE="$DIST/.stage"
rm -rf "$STAGE"
mkdir -p "$STAGE" "$DIST"

echo "[release] version=$VERSION commit=$COMMIT date=$DATE"
for ARCH in amd64 arm64; do
  NAME="rackanvil_${VERSION}_linux_${ARCH}"
  D="$STAGE/$NAME"
  echo "[release] building $NAME"
  mkdir -p "$D/bin" "$D/systemd" "$D/config" "$D/scripts" "$D/docs"

  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "$LDFLAGS" \
    -o "$D/bin/rackanvil-server" ./cmd/server
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "$LDFLAGS" \
    -o "$D/bin/rackanvil-agent" ./cmd/agent

  cp -r assets "$D/assets" && chmod -R a+rX "$D/assets"
  cp packaging/systemd/rackanvil-server.service packaging/systemd/rackanvil-agent.service "$D/systemd/"
  cp packaging/config/server.env.example packaging/config/agent.env.example "$D/config/"
  cp scripts/backup.sh scripts/restore.sh "$D/scripts/"
  cp packaging/install.sh packaging/upgrade.sh "$D/"
  cp packaging/LICENSE packaging/NOTICE README.md "$D/"
  printf '%s\n' "$VERSION" > "$D/VERSION"
  cp docs/DEPLOY.md docs/USER-GUIDE.md "$D/docs/"
  if test -d docs/images; then cp -r docs/images "$D/docs/images" && chmod -R a+rX "$D/docs/images"; fi
  chmod 0755 "$D/install.sh" "$D/upgrade.sh" "$D/scripts/backup.sh" "$D/scripts/restore.sh" \
    "$D/bin/rackanvil-server" "$D/bin/rackanvil-agent"

  # --- 包内文件齐全性自检（缺一即失败，不许带病发布）---
  EXPECTED="
bin/rackanvil-server bin/rackanvil-agent install.sh upgrade.sh LICENSE NOTICE VERSION README.md
systemd/rackanvil-server.service systemd/rackanvil-agent.service
config/server.env.example config/agent.env.example
scripts/backup.sh scripts/restore.sh
docs/DEPLOY.md docs/USER-GUIDE.md
assets/preseed/debian.cfg.tmpl
assets/debian-bookworm/vmlinuz assets/debian-bookworm/initrd.img
assets/debian-bookworm-arm64/linux assets/debian-bookworm-arm64/initrd.gz
"
  for f in $EXPECTED; do
    [ -f "$D/$f" ] || { echo "FAIL: 发布包缺文件 $NAME/$f"; exit 1; }
  done
  echo "[release] $NAME 文件齐全性自检通过"

  # --- 校验和：对实物实时生成并立即复验 ---
  (cd "$D" && find . -type f ! -name 'SHA256SUMS' -print0 | LC_ALL=C sort -z \
      | xargs -0 sha256sum > SHA256SUMS && sha256sum -c SHA256SUMS > /dev/null)
  echo "[release] $NAME SHA256SUMS 已生成并复验（$(wc -l < "$D/SHA256SUMS") 个文件）"

  tar -czf "$DIST/$NAME.tar.gz" -C "$STAGE" "$NAME"
done

# 本机架构的二进制做 -version 冒烟（确认 ldflags 注入生效）
HOSTARCH="$(go env GOHOSTARCH)"
OUT="$("$STAGE/rackanvil_${VERSION}_linux_${HOSTARCH}/bin/rackanvil-server" -version)"
echo "[release] -version 冒烟：$OUT"
case "$OUT" in *"$VERSION"*) ;; *) echo "FAIL: -version 未包含版本号 $VERSION"; exit 1;; esac

# dist 级校验和同样对实物生成
(cd "$DIST" && sha256sum "rackanvil_${VERSION}_linux_amd64.tar.gz" \
    "rackanvil_${VERSION}_linux_arm64.tar.gz" > SHA256SUMS && sha256sum -c SHA256SUMS)
ls -lh "$DIST"/*.tar.gz
echo "PASS: release $VERSION built"
