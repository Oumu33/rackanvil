# RackAnvil 部署文档（运维）

版本：v1.0.0 ｜ 产品：RackAnvil（clean-room 独立重实现，与原厂 DeplOS 无隶属关系，见包内 `NOTICE`）

本文面向负责安装、配置、升级和排障的运维人员。普通使用者请看 `USER-GUIDE.md`（使用手册）。

---

## 1. 系统架构（文字结构图，非界面截图）

```
被管节点（裸机/虚机）                    管理端（一台 Linux 主机）
┌──────────────────┐                   ┌───────────────────────────┐
│ PXE 固件 / iPXE   │ ──引导脚本/内核──▶ │ rackanvil-server (:9100)     │
│ rackanvil-agent      │ ──注册/心跳/上报─▶ │  ├ Web 控制台 + REST API   │
│  · shell 任务执行  │ ◀──任务下发────── │  ├ PXE 引导资产分发        │
│  · 指标/SMART 采集 │                   │  ├ 可选内置 DHCP (:67/udp) │
└──────────────────┘                   │  └ 数据库 SQLite 或 PG     │
                                        │ rackanvil-agent（可选，本机试用）│
                                        └───────────────────────────┘
```

- server 与 agent 是两个独立二进制；server 一台即可，agent 装到每台被管节点。
- 两者之间只有 HTTP（9100）一个通道；agent 主动外连 server，不需要在被管节点开入站端口。

## 2. 系统要求

| 项 | 要求 |
|---|---|
| CPU 架构 | x86_64（amd64）或 ARM64（aarch64），发布包按架构分开，勿混用 |
| 操作系统 | Linux；推荐带 systemd 的发行版（Debian 12 / Ubuntu 22.04+ / Rocky 9 等）。无 systemd 也能跑（手动启动，见 §4.3） |
| 内存 / 磁盘 | 经验建议：server 主机 ≥2 GB 内存、≥5 GB 可用磁盘（引导资产约 122 MB，其余为数据库增长空间）。此为部署建议值，非程序硬性检查 |
| 引导资产来源 | 发布包已内含；若从源码自行打包，资产不随 git 仓库分发，需先跑 `scripts/fetch-assets.sh` 从 Debian 官方镜像下载并按官方 SHA256SUMS 校验（`scripts/release.sh` 会自动执行） |
| 权限 | 安装需 root；server 运行用户为安装时创建的系统用户 `rackanvil`；agent 以 root 运行（要在节点上执行任务，设计使然） |
| 运行时依赖 | 无。server/agent 为静态编译的单个二进制，不依赖任何库或运行时 |
| 可选外部依赖 | PostgreSQL 模式：需要一台可达的 PostgreSQL（建议 14+）；备份 PG 时需要 `pg_dump`。被管节点有 `smartctl`/`storcli` 等厂商工具时，SMART/RAID 采集更全；没有则对应功能如实跳过，不影响其他功能 |

### 端口表

| 端口 | 协议 | 方向 | 用途 | 必需 |
|---|---|---|---|---|
| 9100 | TCP | 被管节点/浏览器 → server | 全部 HTTP：控制台、API、引导脚本、内核/initrd 下载、agent 通信 | 是（`RACKANVIL_ADDR` 可改） |
| 67 | UDP | PXE 客户端 → server | 内置 DHCP/PXE（仅当 `RACKANVIL_DHCP=true` 或 ProxyDHCP 时监听） | 否 |
| 5432 | TCP | server → PostgreSQL | 仅 PG 模式 | 否 |

## 3. 快速部署（三步）

以 amd64 为例（arm64 换对应包名）：

```bash
# 第 1 步：解压发布包
tar -xzf rackanvil_1.0.0_linux_amd64.tar.gz
cd rackanvil_1.0.0_linux_amd64

# 第 2 步：安装（校验包完整性 → 装二进制/资产/配置/systemd unit）
sudo ./install.sh

# 第 3 步：启动并取首次管理员密码
sudo systemctl start rackanvil-server
journalctl -u rackanvil-server | grep -A3 'INITIAL ADMIN'
```

浏览器打开 `http://<server IP>:9100/`，用日志里的 `admin` + 随机密码登录。

> **首次密码说明**：数据库为空时，server 首次启动会生成随机密码并打印到日志**仅此一次**，产品没有默认密码、没有硬编码密码。日志丢了又没改过密码时，只能停服后备份并重建数据库（用户表在库内），请务必首次登录后妥善保管。注意：v1.0.0 控制台暂无改密码与用户管理页面（见 §9 已知限制）。

正式部署前请改两处配置（`/etc/rackanvil/server.env`，改完 `systemctl restart rackanvil-server`）：

- `RACKANVIL_PUBLIC_URL`：改成被管网段能访问到的 server 地址（写进引导脚本，填错则 PXE 客户端回连不上）。
- 按需选数据库模式（§5）与是否启用 DHCP（§6）。

## 4. 安装布局与手动启动

### 4.1 文件布局（install.sh 落位）

| 路径 | 内容 |
|---|---|
| `/usr/lib/rackanvil/rackanvil-server`、`rackanvil-agent` | 两个二进制 |
| `/usr/lib/rackanvil/backup.sh`、`restore.sh` | 备份/恢复演练脚本 |
| `/usr/share/rackanvil/assets/` | 引导资产树（Debian amd64/arm64 netboot + preseed 模板 + winpe 目录） |
| `/etc/rackanvil/server.env`、`/etc/rackanvil/agent.env` | 配置（安装不覆盖已有文件） |
| `/var/lib/rackanvil/` | 数据目录：SQLite 库文件（PG 模式下仅存备份），属主 `rackanvil` |
| `/var/lib/rackanvil/backups/` | upgrade.sh 的旧版本备份 |
| `/usr/share/doc/rackanvil/` | 本文档、手册、LICENSE、NOTICE |
| `/etc/systemd/system/rackanvil-server.service`、`rackanvil-agent.service` | systemd unit |

### 4.2 systemd 常用命令

```bash
systemctl status rackanvil-server     # 状态
systemctl restart rackanvil-server    # 改完配置重启
journalctl -u rackanvil-server -f     # 跟日志
curl -s http://127.0.0.1:9100/healthz   # 健康检查：{"status":"ok",...}
```

本机试用 agent：改 `/etc/rackanvil/agent.env` 的 `RACKANVIL_SERVER` 指向 server 后：

```bash
systemctl enable --now rackanvil-agent
```

### 4.3 无 systemd 环境手动启动

install.sh 会检测并跳过 unit 注册。手动启动（参数与 unit 一致，取 env 文件的值）：

```bash
set -a; . /etc/rackanvil/server.env; set +a
sudo -u rackanvil /usr/lib/rackanvil/rackanvil-server \
  -addr=$RACKANVIL_ADDR -public-url=$RACKANVIL_PUBLIC_URL -db=$RACKANVIL_DB \
  -assets=$RACKANVIL_ASSETS -preseed-dir=$RACKANVIL_PRESEED_DIR -agent-bin=$RACKANVIL_AGENT_BIN
```

## 5. 配置项全表

配置入口是 `/etc/rackanvil/server.env`（agent 为 `agent.env`），由 systemd 以 EnvironmentFile 方式加载，unit 再把变量传给二进制参数。下表「参数」列为二进制命令行参数（手动启动时直接用），默认值全部从代码实读。

### 5.1 server 参数

| 参数 | env 变量 | 默认值 | 说明 |
|---|---|---|---|
| `-addr` | `RACKANVIL_ADDR` | `:9100` | HTTP 监听地址（控制台/API/引导资产共用） |
| `-public-url` | `RACKANVIL_PUBLIC_URL` | `http://127.0.0.1:9100` | 对外基地址，写进 iPXE 引导脚本与装机配置 |
| `-db` | `RACKANVIL_DB` | `sqlite:./rackanvil.db` | 数据库 DSN，格式见 §6（安装包默认已改为 `/var/lib/rackanvil/rackanvil.db` 绝对路径） |
| `-assets` | `RACKANVIL_ASSETS` | `./assets` | 引导资产目录 |
| `-preseed-dir` | `RACKANVIL_PRESEED_DIR` | `./assets/preseed` | preseed 模板目录 |
| `-agent-bin` | `RACKANVIL_AGENT_BIN` | （空） | `/agent/download` 提供的 agent 二进制路径；装机下发必需，空则下载接口返回 501 |
| `-mirror-host` | `RACKANVIL_MIRROR_HOST` | （空，用 deb.debian.org） | preseed 用的 Debian 镜像主机，如 `10.0.0.5:8080` |
| `-mirror-dir` | `RACKANVIL_MIRROR_DIR` | （空，用 `/debian`） | Debian 镜像目录 |
| `-dhcp` | `RACKANVIL_DHCP` | `false` | 启用内置 DHCP/PXE（UDP 67）。unit 已给 server 进程放行特权端口能力（CAP_NET_BIND_SERVICE） |
| `-dhcp-proxy` | `RACKANVIL_DHCP_PROXY` | `false` | ProxyDHCP 旁路：只回引导信息不分配地址，不动现有 DHCP |
| `-dhcp-server-ip` | `RACKANVIL_DHCP_SERVER_IP` | `192.168.100.1` | DHCP 服务器标识 IP |
| `-dhcp-pool-start` | `RACKANVIL_DHCP_POOL_START` | `192.168.100.100` | 地址池起 |
| `-dhcp-pool-end` | `RACKANVIL_DHCP_POOL_END` | `192.168.100.200` | 地址池止 |
| `-license-pubkey` | `RACKANVIL_LICENSE_PUBKEY` | （空） | Ed25519 公钥文件路径（base64url）；设置后启用授权校验，空=开发模式 |
| `-license` | `RACKANVIL_LICENSE_FILE` | （空） | 启动时校验的 license 文件路径；校验失败 server 拒绝启动 |
| `-log-requests` | `RACKANVIL_LOG_REQUESTS` | `false` | 记录每条 HTTP 请求（原始路径+状态码），排引导链问题时开 |
| `-version` | — | — | 打印版本（版本/commit/构建时间）后退出 |

### 5.2 agent 参数

| 参数 | env 变量 | 默认值 | 说明 |
|---|---|---|---|
| `-server` | `RACKANVIL_SERVER` | `http://127.0.0.1:9100` | 管理 server 基地址 |
| `-interval` | `RACKANVIL_INTERVAL` | `30s` | 心跳（checkin）间隔 |
| `-metrics-interval` | `RACKANVIL_METRICS_INTERVAL` | `60s` | 指标上报间隔，`0s` 关闭 |
| `-version` | — | — | 打印版本后退出 |

agent 还直读一个环境变量（不在参数体系内）：`RACKANVIL_ALLOW_FW_UPGRADE`——只有等于 `1` 时 agent 才执行固件升级任务，且任务侧还需显式确认（双门控）。默认不设即拒绝固件升级。

## 6. 数据库：SQLite 与 PostgreSQL 两种模式

同一套版本化迁移在两种引擎上自动建表/升级，业务代码无差别。

### 6.1 SQLite（默认）

- DSN：`sqlite:/var/lib/rackanvil/rackanvil.db`（相对路径写法 `sqlite:./rackanvil.db` 亦可，相对于 server 工作目录）。
- 引擎为纯 Go 实现，无需安装任何数据库软件；日志模式 WAL（允许并发读）。
- 适合：试用、中小规模（数百台以内经验值）。注意单写者模型，高频写入场景建议 PG。

### 6.2 PostgreSQL（生产推荐）

1. 在 PG 上建库与用户（示例）：

```sql
CREATE USER rackanvil WITH PASSWORD '<强密码>';
CREATE DATABASE rackanvil OWNER rackanvil;
```

2. 改 `/etc/rackanvil/server.env`：

```
RACKANVIL_DB=postgres://rackanvil:<强密码>@127.0.0.1:5432/rackanvil
```

3. `systemctl restart rackanvil-server`，日志出现 `db: connected (postgres://...)` 即成功，迁移自动执行。

> **切换须知（如实说明）**：v1.0.0 没有内置的 SQLite→PG 数据搬迁工具。换 DSN 等于启用一个新库：主机、任务、用户（含 admin 密码）都需要重新建立。计划切换请在正式纳管前完成，或接受重新纳管。

## 7. 升级与回滚

在**新版**发布包解压目录内执行：

```bash
sudo ./upgrade.sh
```

upgrade.sh 依次做：校验新包 SHA256SUMS → 打印新旧版本 → 把当前二进制备份到 `/var/lib/rackanvil/backups/upgrade-<时间戳>/` → 停服换件（**不动** `/etc/rackanvil/*.env` 配置与数据库）→ 起服 → 轮询 `/healthz` 15 秒。健康检查失败**自动回滚**并退出报错。

手动回滚：

```bash
systemctl stop rackanvil-server
cp -a /var/lib/rackanvil/backups/upgrade-<时间戳>/rackanvil-server \
      /var/lib/rackanvil/backups/upgrade-<时间戳>/rackanvil-agent /usr/lib/rackanvil/
systemctl start rackanvil-server
```

注意：数据库迁移随新版 server 首次启动自动执行；若新版已升级过表结构，回滚到旧版二进制前请先确认旧版兼容该结构（保守做法：回滚二进制同时用 §8 的备份恢复数据库）。

## 8. 数据备份与恢复

发布包装有维护脚本（源自产品自带能力）：

```bash
# 备份：数据库 + 引导资产打成一个带时间戳的 tar.gz
/usr/lib/rackanvil/backup.sh "sqlite:/var/lib/rackanvil/rackanvil.db" \
  /usr/share/rackanvil/assets /var/backups/rackanvil
# PG 模式把第一个参数换成 postgres://... DSN（走 pg_dump，需装 postgresql-client）
```

- SQLite 备份是文件拷贝：请在 server 停服时执行，或确认 WAL 已合并，避免拷到写一半的状态。
- `restore.sh <备份包> <目标目录>` 是**恢复验证演练**：解压并以只读方式打开数据库校验（主机数、迁移版本可读），不碰线上库。
- 真恢复步骤：停服 → 解压备份包 → 把 `rackanvil.db`（及同名 `-wal`/`-shm` 如存在）放回 `/var/lib/rackanvil/`、资产目录覆盖回 `/usr/share/rackanvil/assets` → 起服 → 看日志与控制台核对。

## 9. 已知限制（v1.0.0，如实清单）

- 控制台无用户管理/改密码页面：仅首次启动创建 `admin` 一个账号；RBAC 角色（admin/operator/viewer）在服务端已生效，但新建用户暂无界面入口（后续版本补）。
- 控制台界面为服务端渲染的基础页面，无独立前端工程；功能完整但样式朴素。
- SQLite→PostgreSQL 无内置数据搬迁工具（见 §6.2）。
- 需真实硬件的功能（点灯、RAID 真卡、固件真刷、BMC、GPU 压测、WinPE 装机）在无硬件环境返回明确的「未检测到设备/未启用」结果，不会假报成功。

## 10. 常见故障排查

| 现象 | 常见原因 | 处置 |
|---|---|---|
| systemctl start 后立刻退出 | 配置错/DB 不可达/端口被占 | `journalctl -u rackanvil-server -n 50` 看 `Fatal` 行；逐项核对 env |
| `address already in use :9100` | 9100 被别的进程占用或 server 已在跑 | `ss -lntp \| grep 9100` 查占用；改 `RACKANVIL_ADDR` 或停掉占用者 |
| 浏览器打不开控制台 | 防火墙未放行 9100；server 没起来 | 先本机 `curl 127.0.0.1:9100/healthz`，再查云防火墙/iptables |
| 找不到首次 admin 密码 | 已不是首次启动（库里已有用户，不再打印）；或日志被轮转 | 密码只在建号那次打印。翻 `journalctl -u rackanvil-server --no-pager \| grep -B1 -A3 'INITIAL ADMIN'`；确实丢失只能重建库（先备份） |
| agent 不在资产列表出现 | agent 连不上 server：`RACKANVIL_SERVER` 写错、网络不通、9100 未放行 | 在节点上 `curl <RACKANVIL_SERVER>/healthz`；看 `journalctl -u rackanvil-agent` 的 initialize 报错 |
| PXE 客户端拿不到引导脚本/内核 | `RACKANVIL_PUBLIC_URL` 填了客户端到不了的地址（如 127.0.0.1） | 改成客户端网段可达的 server IP，重启 server，再让客户端重试 |
| 装机时 agent 下载失败 | `-agent-bin` 未配（env `RACKANVIL_AGENT_BIN` 为空） | env 指向 `/usr/lib/rackanvil/rackanvil-agent` 后重启 server |
| DHCP 开了但客户端拿不到地址 | 与现有 DHCP 冲突；或池子网段不对 | 二选一：关掉一个；或改用 ProxyDHCP（`RACKANVIL_DHCP_PROXY=true`）旁路现有 DHCP |
| PG 模式启动报 db 连接失败 | DSN 密码含特殊字符未转义；PG 未允许该来源 | 密码中的 `@:/?` 等需 URL 编码；查 PG 的 pg_hba 与监听地址 |
| 固件升级任务被拒 | 双门控未满足（设计行为） | 任务侧勾选确认 + 节点 env `RACKANVIL_ALLOW_FW_UPGRADE=1` 后重启 agent，缺一不可 |
| 仪表盘没有曲线 | agent 指标上报未开或刚启动样本不足 | 确认 agent `-metrics-interval` 非 0；等 2–3 个上报周期后刷新 |
| license 启动校验失败 | 文件与公钥不配对/已过期 | 日志有 `license: INVALID` 具体原因；开发试用可清空两个 license 配置走开发模式 |

## 11. 卸载

```bash
systemctl disable --now rackanvil-server rackanvil-agent 2>/dev/null
rm -f /etc/systemd/system/rackanvil-*.service && systemctl daemon-reload
rm -rf /usr/lib/rackanvil /usr/share/rackanvil /usr/share/doc/rackanvil
# 数据与配置默认保留；确认不要了再删：
# rm -rf /var/lib/rackanvil /etc/rackanvil
userdel rackanvil 2>/dev/null
```
