#!/bin/bash
# rackanvil 安装脚本：在发布包解压目录内以 root 运行（bash install.sh）
# 幂等：可重复执行；已存在的 /etc/rackanvil/server.env 与 agent.env 不会被覆盖。
# 布局：
#   /usr/lib/rackanvil/            二进制与维护脚本
#   /usr/share/rackanvil/assets/   引导资产（Debian netboot 加 preseed 模板）
#   /etc/rackanvil/                server.env / agent.env 配置
#   /var/lib/rackanvil/            数据目录（SQLite 库），属主 rackanvil
#   /usr/share/doc/rackanvil/ 文档与 LICENSE/NOTICE
set -u

fail() { echo "FAIL: $1"; exit 1; }

test "$(id -u)" = "0" || fail "必须以 root 运行"
SRC="$(cd "$(dirname "$0")" && pwd)"
PREFIX=/usr/lib/rackanvil
SHARE=/usr/share/rackanvil
CONF=/etc/rackanvil
STATE=/var/lib/rackanvil
DOCDIR=/usr/share/doc/rackanvil
UNITDIR=/etc/systemd/system

# 0) 包完整性自检：SHA256SUMS 由发布脚本对实物生成，对不上就别装。
if test -f "$SRC/SHA256SUMS"; then
  (cd "$SRC" && sha256sum -c SHA256SUMS >/dev/null) || fail "SHA256SUMS 校验不通过，发布包可能损坏或被篡改"
  echo "[install] SHA256SUMS 校验通过"
fi

# 1) 专用系统用户（已存在则跳过）
if id rackanvil >/dev/null 2>&1; then
  echo "[install] 系统用户 rackanvil 已存在，跳过创建"
else
  useradd --system --home-dir "$STATE" --shell /usr/sbin/nologin rackanvil
  echo "[install] 已创建系统用户 rackanvil"
fi
# 显式给目录定权限：不许依赖 umask（umask 007 的硬化系统上 mkdir 默认 770，
# 运行用户 rackanvil 会连目录都进不去——M12 从零实测实锤）。
install -d -m 0755 "$PREFIX" "$SHARE" "$CONF" "$DOCDIR"
install -d -m 0750 -o rackanvil -g rackanvil "$STATE"

# 2) 二进制与资产（整体替换，保证与发布包一致）
install -m 0755 "$SRC/bin/rackanvil-server" "$PREFIX/rackanvil-server"
install -m 0755 "$SRC/bin/rackanvil-agent" "$PREFIX/rackanvil-agent"
install -m 0755 "$SRC/scripts/backup.sh" "$PREFIX/backup.sh"
install -m 0755 "$SRC/scripts/restore.sh" "$PREFIX/restore.sh"
rm -rf "$SHARE/assets"
cp -r "$SRC/assets" "$SHARE/assets"
chmod -R a+rX "$SHARE/assets"
echo "[install] 二进制与引导资产已安装"

# 3) 配置：只有不存在时才写入示例，绝不覆盖用户已改的配置
if test -f "$CONF/server.env"; then
  echo "[install] 保留已有配置 $CONF/server.env"
else
  install -m 0640 -o root -g rackanvil "$SRC/config/server.env.example" "$CONF/server.env"
  echo "[install] 已写入默认配置 $CONF/server.env（按需修改 RACKANVIL_PUBLIC_URL 等）"
fi
if test -f "$CONF/agent.env"; then
  echo "[install] 保留已有配置 $CONF/agent.env"
else
  install -m 0644 "$SRC/config/agent.env.example" "$CONF/agent.env"
  echo "[install] 已写入默认配置 $CONF/agent.env"
fi

# 4) 文档
install -m 0644 "$SRC/LICENSE" "$DOCDIR/LICENSE"
install -m 0644 "$SRC/NOTICE" "$DOCDIR/NOTICE"
if test -f "$SRC/docs/DEPLOY.md"; then
  install -m 0644 "$SRC/docs/DEPLOY.md" "$DOCDIR/DEPLOY.md"
fi
if test -f "$SRC/docs/USER-GUIDE.md"; then
  install -m 0644 "$SRC/docs/USER-GUIDE.md" "$DOCDIR/USER-GUIDE.md"
fi

# 5) 数据目录属主
chown -R rackanvil:rackanvil "$STATE"

# 6) systemd：仅当 systemd 是 PID 1 时注册（容器/沙箱自动跳过）
SYSTEMD_OK=0
if test "$(ps -p 1 -o comm= 2>/dev/null)" = "systemd"; then
  if command -v systemctl >/dev/null 2>&1; then
    SYSTEMD_OK=1
  fi
fi
if test "$SYSTEMD_OK" = "1"; then
  install -m 0644 "$SRC/systemd/rackanvil-server.service" "$UNITDIR/rackanvil-server.service"
  install -m 0644 "$SRC/systemd/rackanvil-agent.service" "$UNITDIR/rackanvil-agent.service"
  systemctl daemon-reload
  systemctl enable rackanvil-server.service >/dev/null 2>&1 || true
  echo "[install] systemd unit 已注册，rackanvil-server 已设为开机自启"
  echo "启动服务：systemctl start rackanvil-server"
  echo "本机试用 agent：改好 /etc/rackanvil/agent.env 里的 RACKANVIL_SERVER 后，"
  echo "执行 systemctl enable --now rackanvil-agent"
else
  echo "[install] 未检测到 systemd（PID 1 非 systemd），跳过 unit 注册。"
  echo "手动启动方法见 DEPLOY.md 第 4.3 节。"
fi

echo
echo "==============================================================="
echo "首次启动说明：server 第一次启动（数据库为空）时会自动创建管理员"
echo "账号 admin，并把随机密码打印到日志一次（不写死、无默认密码）："
echo "systemd 环境：journalctl -u rackanvil-server，然后搜 INITIAL ADMIN"
echo "手动启动：看 server 输出里 INITIAL ADMIN ACCOUNT CREATED 段落"
echo "登录地址：http://主机IP:9100/ （用户名 admin）"
echo "==============================================================="
echo "PASS"
