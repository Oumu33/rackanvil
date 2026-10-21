#!/bin/bash
# rackanvil 升级脚本：在发布包解压目录内以 root 运行（bash upgrade.sh）
# 流程：校验新包，备份当前版本到 /var/lib/rackanvil/backups/，换二进制与资产，
# 重启并做健康检查；健康检查失败自动回滚到备份版本。
# /etc/rackanvil 下的 env 配置与 /var/lib/rackanvil 数据在升级中原样保留。
set -u

fail() { echo "FAIL: $1"; exit 1;}

test "$(id -u)" = "0" || fail "必须以 root 运行"
SRC="$(cd "$(dirname "$0")" && pwd)"
PREFIX=/usr/lib/rackanvil
SHARE=/usr/share/rackanvil
STATE=/var/lib/rackanvil
TS=$(date +%Y%m%d-%H%M%S)
BK="$STATE/backups/upgrade-$TS"

SYSTEMD_OK=0
if test "$(ps -p 1 -o comm= 2>/dev/null)" = "systemd"; then
if command -v systemctl >/dev/null 2>&1; then
SYSTEMD_OK=1
fi
fi

test -x "$PREFIX/rackanvil-server" || fail "未发现已安装版本（$PREFIX/rackanvil-server 不存在），请先用 install.sh 全新安装"

# 0) 新包完整性自检
if test -f "$SRC/SHA256SUMS"; then
(cd "$SRC" && sha256sum -c SHA256SUMS >/dev/null) || fail "新包 SHA256SUMS 校验不通过，已中止升级"
echo "[upgrade] 新包校验通过"
fi

OLDVER=$("$PREFIX/rackanvil-server" -version 2>/dev/null | head -1)
NEWVER=$("$SRC/bin/rackanvil-server" -version 2>/dev/null | head -1)
echo "[upgrade] 当前版本：${OLDVER:-未知}"
echo "[upgrade] 目标版本：${NEWVER:-未知}"

# 1) 备份当前二进制与资产清单
mkdir -p "$BK"
cp "$PREFIX/rackanvil-server" "$BK/rackanvil-server"
cp "$PREFIX/rackanvil-agent" "$BK/rackanvil-agent"
chmod 0755 "$BK/rackanvil-server" "$BK/rackanvil-agent"
if test -d "$SHARE/assets"; then
find "$SHARE/assets" -type f | sort > "$BK/assets-files.txt"
fi
echo "[upgrade] 旧版本已备份到 $BK"

rollback() {
echo "[upgrade] 正在回滚到备份版本……"
cp "$BK/rackanvil-server" "$PREFIX/rackanvil-server"
cp "$BK/rackanvil-agent" "$PREFIX/rackanvil-agent"
chmod 0755 "$PREFIX/rackanvil-server" "$PREFIX/rackanvil-agent"
if test "$SYSTEMD_OK" = "1"; then
systemctl start rackanvil-server || true
fi
echo "FAIL: 升级失败，已回滚。备份保留在 $BK"
exit 1
}

# 2) 停服、换件、起服
if test "$SYSTEMD_OK" = "1"; then
systemctl stop rackanvil-server || true
fi
install -m 0755 "$SRC/bin/rackanvil-server" "$PREFIX/rackanvil-server"
install -m 0755 "$SRC/bin/rackanvil-agent" "$PREFIX/rackanvil-agent"
install -m 0755 "$SRC/scripts/backup.sh" "$PREFIX/backup.sh"
install -m 0755 "$SRC/scripts/restore.sh" "$PREFIX/restore.sh"
rm -rf "$SHARE/assets"
cp -r "$SRC/assets" "$SHARE/assets"
chmod -R a+rX "$SHARE/assets"
if test "$SYSTEMD_OK" = "1"; then
install -m 0644 "$SRC/systemd/rackanvil-server.service" /etc/systemd/system/rackanvil-server.service
install -m 0644 "$SRC/systemd/rackanvil-agent.service" /etc/systemd/system/rackanvil-agent.service
systemctl daemon-reload
systemctl start rackanvil-server || rollback
fi
echo "[upgrade] 新版本文件已就位"

# 3) 健康检查（systemd 模式查 /healthz；端口从 env 读，默认 9100）
if test "$SYSTEMD_OK" = "1"; then
PORT=9100
if test -f /etc/rackanvil/server.env; then
A=$(grep -E "^RACKANVIL_ADDR=" /etc/rackanvil/server.env | tail -1 | cut -d= -f2)
case "$A" in
*:*) PORT="${A##*:}";;
esac
fi
OK=0
for i in $(seq 1 15); do
if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
OK=1
break
fi
sleep 1
done
test "$OK" = "1" || rollback
echo "[upgrade] 健康检查通过（http://127.0.0.1:$PORT/healthz）"
fi

echo
echo "PASS: 升级完成。旧版本备份在 $BK"
echo "手动回滚：停服后把 $BK 里的两个二进制拷回 $PREFIX 再起服即可。"
