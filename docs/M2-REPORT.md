# M2 验收报告：从"能跑通"到"能用"

> 日期：2026-10-19
> 范围：M2a 数据持久化、M2b collectors+存储故障预测、M2c Web 控制台 MVP、M2d provisioning 真实 boot 资产
> 原则：全部 clean-room 原创代码；验证证据为实测日志/DB 记录；降级如实标注

## 0. 结论

四个模块全部交付并通过真实联调验证。`go build` ✅ `go vet` ✅ `go test ./...` ✅ 全绿。

## 1. M2a 数据持久化（internal/db）

**实现**：`internal/db/db.go`（~700 行）+ `db_test.go`。database/sql + 版本化 migrations，10 张核心表：
`schema_migrations, users, hosts, tasks, metrics, smart_history, active_alarms, alert_history, audit_logs, system_settings`
（参考 `deplos-reversing/db-tables.txt` 的命名，71 张中的核心子集；其余按模块增量加）。

**关键工程决策**（如实记录）：
- SQL 写在可移植子集内（TEXT/INTEGER/REAL、无序列、无 RETURNING、时间戳用 INTEGER unix epoch），占位符统一写 `?`，open 时按 driver 重绑为 PG 的 `$n`。**同一套 migrations 跑 SQLite 和 PostgreSQL**。
- 开发 fallback 用 modernc.org/sqlite（纯 Go，无 cgo）：本沙箱装 PG 不可靠（apt 源超时、`/usr` 会被重置）。pgx v5 驱动已接入，**生产切 `postgres://...` DSN 零改代码**——不是口号，driver import 和 rebind 都已就位。
- M1 的内存 store 被 `*db.DB` 替换，`api.Store` 接口（8 个方法）零改动；`New(s Store, ms MetricsStore, ...)` 多传一个 metrics 接口。

**验证**：`go test ./internal/db/` 4 个测试全过（迁移幂等、host 注册/重注册换 token、任务 enqueue→checkin→report 全流转、metrics/smart/告警/用户/audit/settings）。

## 2. M2b collectors + 存储故障预测（internal/collectors）

**实现**：`collectors.go`（~400 行）+ 测试。采集源：
- CPU：/proc/stat 两次采样（500ms 间隔）
- 内存：/proc/meminfo（MemTotal/MemAvailable）
- 磁盘：/sys/block（物理盘过滤 loop/ram/dm）+ /proc/mounts + statfs 用量
- SMART：`smartctl --json -a`（未安装则如实记入 `skipped`，不伪造）
- NVMe：/sys/class/nvme 枚举 + smartctl/`nvme` cli 健康日志

**存储故障预测**（用户点名功能）：自研策略，依据公开研究（Backblaze drive-stats：SMART 5/187/188/197/198 与故障强相关；Google 磁盘故障论文），**不是复制任何厂商模型**（我们也没拿到原厂模型）：
- ATA 阈值：187>0→critical；197≥10→critical、>0→warning；198>0→warning；5≥50→critical、>0→warning；188>0→warning
- 趋势：最近 3 个样本中 5/197/198 任一严格递增→warning"degrading fast"
- NVMe：critical_warning≠0→critical；media_errors>0→critical；percentage_used≥95→critical、≥85→warning
- 每次上报全量存档 `smart_history`；命中→`RaiseAlarm`（按 host+kind 去重），恢复→`ResolveAlarm` 转入 `alert_history`

**验证证据**（真机实测，本机）：
- agent 采集循环 8s 间隔上报，server 日志：`metrics: host=host-8f5ab65e843ad24e cpu=23.5% mem=6800/7935MB disks=4 smart=0 nvme=0`（smart=0 且 skipped 诚实记录 smartctl 未安装）
- 构造坏盘 SMART 上报（187=2, 197=15, 5=12；nvme percentage_used=96, media_errors=3）→ DB 出现 2 条 critical 告警：
  `smart.sda|[critical] SMART 187 ... uncorrectable sectors seen`
  `nvme.nvme0|[critical] NVMe media_errors=3; [critical] NVMe percentage_used=96%: end of life`
- 再上报健康样本→`active_alarms` 清零，2 条记录转入 `alert_history`（告警生命周期闭环 ✅）
- `/api/v1/hosts/{id}/health` 返回最新采集 + 活跃告警（实测 200）

## 3. M2c Web 控制台 MVP（internal/web + internal/auth）

**实现**：
- `internal/auth`：bcrypt（cost 12）+ 自研 HS256 JWT（标准库 HMAC-SHA256，按 RFC 7519 语义实现，无第三方 JWT 依赖）
- `internal/web`：服务端模板渲染。路由：`GET /login`、`POST /login`（cookie `sess`）、`GET /logout`、`GET /hosts`（资产列表）、`GET /hosts/{id}`（详情：信息+最新采集+活跃告警+下发任务+任务记录）、`POST /tasks`（下发）、`GET /tasks/{id}`（结果 stdout/stderr）
- RBAC：viewer 只读；operator/admin 可下发任务；登录成功/失败、下发任务写 `audit_logs`
- **admin  bootstrap**：`users` 表为空时生成 16 位随机密码，bcrypt 入库，**打印到 server 日志一次**，绝不硬编码（原厂 `HrnrKit_2025_Secure!` 这类坑我们不踩）；JWT secret 32 字节随机生成存 `system_settings.jwt_secret`，重启不失效
- 挂载：`api.Server.Mount("/", console.Handler())`，Go 1.22 mux 最长前缀优先，`/api/...`、`/ipxe/...`、`/healthz` 不受影响

**验证证据**（curl 实测）：
- `GET /login` → 200 登录页；`POST /login` 正确密码 → 302 到 `/hosts`（cookie 生效）；错误密码 → "用户名或密码错误"
- 带 cookie `GET /hosts` → 200，页面含 agent 上报的主机 `host-8f5ab65e843ad24e`
- 未登录 `GET /hosts` → 302 跳 `/login`

## 4. M2d provisioning 真实 boot 资产

**实现**：Debian bookworm netboot（`deb.debian.org` 官方源）：
- `assets/debian-bookworm/vmlinuz`：Linux kernel x86 boot executable bzImage, **version 6.1.0-50-amd64**（8.2MB）
- `assets/debian-bookworm/initrd.img`（40.8MB）
- `/ipxe/{vmlinuz,initrd.img}` 改为真实 serve（`http.ServeContent`，支持 Range；`--assets` 指定目录，缺失回退 501 诚实占位）

**验证证据**（`scripts/qemu-pxe-test.sh`，exit 0，26 秒）：
```
bootscript: mac=52:54:00:12:34:56 from=127.0.0.1:39174
bootasset: serving vmlinuz (8222656 bytes) to 127.0.0.1:39174
bootasset: serving initrd.img (40810276 bytes) to 127.0.0.1:39174
[    0.000000] Linux version 6.1.0-50-amd64 (debian-kernel@lists.debian.org) ...
PASS: QEMU guest PXE -> SLIRP DHCP/TFTP -> our bootscript -> real Debian kernel booting
```
虚机 PXE → bootscript → 下载真实内核+initrd → 内核在 serial 控制台打印启动日志。**M1 的"链路通"推进到了"内核真启动"**。

## 5. 诚实降级项

1. **SQLite 是开发 fallback**：本沙箱跑 PG 不可靠（apt 源超时、`/usr` 重置会删 qemu 等包——本次实测中 qemu-system-x86_64 二进制消失，用 `apt-get install --reinstall` 恢复）。PG 切换已做到零改代码，但**尚未在真 PG 上跑过迁移**——生产上线前必须在 PG 跑一遍 `db_test` 同等验证。
2. **自研 DHCP（internal/pxe）的 live 验证仍待真环境**：沙箱禁 UDP，QEMU 验证用的是 SLIRP 内置 DHCP（绕开的是验证手段）。协议逻辑由包级单测全覆盖。
3. **完整 preseed 无人值守装机是 M3**：本次只到内核启动；debian-installer 进安装器后的自动应答、装完重启、agent 重报到还没做。
4. **SMART 真盘验证缺失**：本机无 smartctl、无真盘 SMART 数据；阈值/趋势逻辑由单测 + 构造数据端到端验证，真盘调优待有盘环境。
5. **Web 控制台是 MVP**：无 dashboard 图表、无用户管理页（admin 外的账号需手动入库）、JWT 12h 有效期无刷新机制——M3 补。
6. **QEMU 二进制在沙箱重置后会消失**：`scripts/qemu-pxe-test.sh` 的预检只报 SKIP；如需持久化，考虑把 qemu 放 `~/workspace`（本次用 reinstall 恢复，未做持久化）。

## 6. M3 建议

1. discovery 网段扫描 → 资产入库 → BOM 比对（CMDB 闭环）
2. orchestrator 编排流水线（BMC→RAID→压测→装机，RAID/固件步骤 mock）
3. preseed 模板 + 无人值守装机全流程（QEMU 验证到"系统装完+agent 重报到"）
4. collectors dashboard 出图（Web 加图表页）
5. license（Ed25519 自研）、备份/恢复、WebSSH
6. 生产 PG 实测迁移；真机补 SMART 真盘调优与自研 DHCP live 验证
