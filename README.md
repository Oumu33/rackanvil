# RackAnvil

数据中心裸金属自动化运维平台的 **clean-room 重实现**。

## 快速开始（三步跑起来）

```bash
tar -xzf rackanvil_1.0.0_linux_amd64.tar.gz   # arm64 换对应包
cd rackanvil_1.0.0_linux_amd64
sudo ./install.sh                                  # 装二进制/资产/配置/systemd unit
sudo systemctl start rackanvil-server
journalctl -u rackanvil-server | grep -A3 'INITIAL ADMIN'   # 取首次随机 admin 密码（只打印一次）
```

浏览器打开 `http://<本机IP>:9100/`，用 `admin` + 上一步的密码登录即可。开发模式（源码直跑）：`go run ./cmd/server`（默认 SQLite 本地文件 + 控制台同端口）。

## 文档索引

| 文档 | 读者 | 内容 |
|---|---|---|
| [docs/DEPLOY.md](docs/DEPLOY.md) | 运维 | 系统要求、部署、配置全表、PG/SQLite、升级回滚、备份恢复、排障 |
| [docs/USER-GUIDE.md](docs/USER-GUIDE.md) | 使用者 | 一步一图使用手册（截图全部为本产品控制台实拍） |
| [docs/STATUS.md](docs/STATUS.md) | 项目 | 当前状态总账与剩余项 |
| [docs/M12-REPORT.md](docs/M12-REPORT.md) | 项目 | v1.0.0 产品化验收（发布包、从零实测、截图来源逐张注明） |
| docs/M1–M11-REPORT.md | 项目 | 各里程碑验收报告 |

## 定位声明

- 本项目是独立重实现，**与原厂 DeplOS（hrnr27/DeplOS）划清界限**：不复制原厂二进制、不复制反编译产物、不兼容原厂私有协议。
- 只对标**功能行为**（PXE 装机、agent 上报、CMDB、监控等"做什么"），"怎么做"全部自研。
- server/agent 两端协议自研，不追求与原版互通。
- 安全红线：无硬编码凭据（部署时生成）、标准 JWT+bcrypt（不用 MD5 派生）、自研 license（Ed25519，不用原厂 RSA 体系）。

## 技术栈

- Go 1.24（与原厂同语言，便于对照行为；代码全部自写）
- 数据层：database/sql + migrations；开发期 SQLite（modernc 纯 Go），生产 PostgreSQL（pgx），同一套迁移零改代码
- Web 控制台：`internal/web` 服务端模板渲染（M2 MVP 已落地）

## 目录结构

```
cmd/server/          server 入口（:9100 API + :8085 ticket 中转）
cmd/agent/           agent 入口（跑在被管机器/initrd 里）
internal/api/        路由表与请求/响应 DTO（对标原厂路由行为）
internal/auth/       JWT 会话 + RBAC
internal/pxe/        DHCP/PXE 服务、bootscript/firstboot 模板
internal/agent/      agent 协议：checkin/initialize/report、任务执行
internal/cmdb/       资产入库、BOM 比对
internal/monitor/    采集入库、告警、dashboard 数据
internal/orchestrator/ 交付编排：任务模型与状态机
internal/db/         migrations（表结构见 deplos-reversing/db-tables.txt）
internal/license/    自研 license（Ed25519）
internal/terminal/   WebSSH、RDP/SSH ticket
web/                 前端（M2 再做）
docs/                设计文档
scripts/             部署/验证脚本（含 QEMU 验证）
```

## 里程碑

### M1：虚拟机 PXE 装机链路跑通（P0）

目标：QEMU/KVM 虚拟机模拟裸机，全程无人值守装完系统。

- [x] server 启动：API + DHCP/PXE（`cmd/server`，`-dhcp` 开关）
- [x] iPXE bootscript 下发（`internal/pxe` + `/api/v1/ipxe/bootscript` 按 MAC 渲染）
- [x] agent 跑起来并 `/api/v1/client/initialize` 报到（真进程联调通过，见 `docs/M1-REPORT.md`）
- [x] 任务状态机全流转：enqueue → checkin 取任务 → 执行 → report 落库（server 日志可查）
- [x] 下发 Debian preseed 装机任务，虚拟机自动装完（M3：全自动到 kernel 安装约 90%，见 `docs/M3-REPORT.md` §2；沙箱重启限制致完整闭环待稳定环境，脚本 `scripts/qemu-preseed-test.sh` exit 0 即 PASS）
- [x] `scripts/qemu-pxe-test.sh` 一键跑通（2026-10-19 ✅：QEMU SLIRP 内置 DHCP+TFTP 绕开沙箱 UDP 限制，guest iPXE → 我方 bootscript 全链路 PASS；自研 DHCP 的 live 验证仍待真环境）

### M2：数据持久化 + 采集监控 + Web 控制台 + 真实 boot 资产（P0–P1）✅ 完成（2026-10-19）

- [x] 数据持久化（`internal/db`）：database/sql + 版本化 migrations（10 张核心表：users/hosts/tasks/metrics/smart_history/active_alarms/alert_history/audit_logs/system_settings/schema_migrations）；占位符 `?`→`$n` 自动重绑，SQLite（modernc 纯 Go，开发 fallback）与 PostgreSQL（pgx 已接入）同一套迁移、生产切换零改代码；M1 内存 store 被 DB 实现替换，`api.Store` 接口零改动
- [x] collectors（`internal/collectors`）：CPU（/proc/stat）、内存（/proc/meminfo）、磁盘（/sys/block + statfs）、SMART（smartctl，有则用无则如实跳过）、NVMe 健康；agent `--metrics-interval` 定时经 `/api/v1/metrics` 上报入库
- [x] 存储故障预测：Backblaze/Google 公开研究指导的自研策略——SMART 5/187/188/197/198 阈值 + 趋势加速判定、NVMe percentage_used/media_errors/critical_warning；每次上报全量存档 smart_history，命中即 RaiseAlarm、恢复即转入 alert_history；`/api/v1/hosts/{id}/health` 查询健康状态（验证：构造坏盘 SMART 上报→2 条 critical 告警→健康上报→告警清零入历史，全程 DB 为证）
- [x] Web 控制台 MVP（`internal/web` + `internal/auth`）：bcrypt（cost 12）+ 自研 HS256 JWT（标准库 HMAC）；首次启动生成随机 admin 密码打印到日志一次、绝不硬编码；页面：登录/资产列表/主机详情（含最新采集+活跃告警）/任务下发/任务结果；RBAC：viewer 只读、operator/admin 可下发任务；登录/下发写 audit_logs（验证：登录页→POST 登录 302→cookie 进资产页见主机；未登录 302 跳登录；错密码报错）
- [x] provisioning 真实 boot 资产：Debian bookworm netboot vmlinuz（6.1.0-50-amd64，8.2MB）+ initrd.img（40.8MB）入 `assets/debian-bookworm/`，`/ipxe/{vmlinuz,initrd.img}` 真实 serve（支持 Range）；QEMU 验证推进到**真实内核启动**：虚机 PXE→bootscript→下载内核+initrd→serial 出现 `Linux version 6.1.0-50-amd64`，26 秒 PASS（`scripts/qemu-pxe-test.sh`）
- [x] discovery 网段扫描→BOM 比对（M3 ✅）
- [x] orchestrator 编排流水线（M3 ✅）
- [x] 备份/恢复、Ed25519 license、WebSSH（M3 ✅）

### M3：全部"本环境可实现"功能 ✅ 完成（2026-10-19）

- [x] discovery 网段扫描→BOM 比对（`internal/discovery`）：TCP 探测 + ARP 表 MAC 解析 + 反向 DNS；BOM diff（new/missing/changed）；API + Web `/discovery` 页
- [x] orchestrator 编排流水线（`internal/orchestrator`）：jobs/job_steps 状态机；BMC/RAID（无硬件=显式 mock）、stress（agent 真实执行）、install（布防→等 install-done）；后台 Runner；API + Web `/orch` 页
- [x] preseed 无人值守装机全流程（`assets/preseed/debian.cfg.tmpl`）：bootscript 安装模式、`/preseed/{token}.cfg`、`/agent/download`、late_command 装 agent+systemd+POST install-done；QEMU 验证到 kernel 安装（约 90%，沙箱重启限制，见 `docs/M3-REPORT.md` §2）；install-done 服务端闭环 API 级验证通过（`scripts/qemu-preseed-test.sh`）
- [x] dashboard 出图：`/api/v1/hosts/{id}/metrics/history` + Web `/dashboard` 纯 SVG 折线图（CPU/内存，无外部依赖）
- [x] Ed25519 license（`internal/license`，自研体系）：签发/验签/过期拒绝；server 启动校验；API 状态；Web `/license` 导入页
- [x] 备份/恢复（`scripts/backup.sh`/`restore.sh`）：sqlite 拷贝 / pg_dump；恢复演练验证通过
- [x] WebSSH（`internal/web/webssh.go`）：stdlib RFC 6455 + `x/crypto/ssh`；一次性 token 传密码；Web `/terminal` 页；需登录+operator 权限

### M4：厂商文档对齐 8 功能 ✅ 完成（2026-10-19，用户"全都要"）

- [x] 硬盘定位点灯（`internal/locator`）：ledctl→sg_ses→storcli→nvme 优先级链；agent `disk.scan`/`disk.locate`；API + Web `/locator`（真 LED 需真盘，未验证）
- [x] 配置存档一键恢复（`internal/cfgarchive`）：RAID/分区表/网络快照存 DB，恢复需 confirm=true；真机 e2e 全链路 PASS
- [x] 拓扑映射（`internal/topology`）：lldpctl 解析 + 交换机 FDB 联动（MAC→交换机端口）；API + Web `/topology`（无真交换机，未验证）
- [x] 跨机房代理节点（`internal/relay`）：资产缓存+bootscript 中继+心跳；双进程模拟验证
- [x] ARM64 引导资产：Debian bookworm arm64 官方 netboot（`assets/debian-bookworm-arm64/`）；DHCP option 93 0x000B→bootaa64.efi；bootscript `arch` 参数；ProxyDHCP 旁路模式（`--dhcp-proxy`）
- [x] 固件升级（`internal/firmware`）：7 路径 detect 全实现+单测；upgrade 双门控默认关闭（无真卡，从未真实执行）；M7 已按原厂 fw_agent 反编译逐字对齐分发表/命令模板/判据 + 新增原厂 6 探针 detect（见 `docs/M7-REPORT.md`）
- [x] WinPE 装机链路：iPXE→wimboot→SMB→winpeshl.ini→unattend 模板+API（目录故意留空，需授权镜像未验证）
- [x] RAID 配置（`internal/raid`）：storcli 解析+VD 创建/删除/查询；API + Web `/raid`（无真卡，未验证）
- [x] 平台深健康：CPU MCE/内存 CE-UCE/PCIe AER/NVMe 备用块采集+`platform.deep` 告警（e2e 验证触发）
- 验收见 `docs/M4-REPORT.md`；`go build/vet/test` 全绿

### M5：缺口收官 ✅ 完成（2026-10-20，验收见 `docs/M5-REPORT.md`）

- [x] A. PostgreSQL 真迁移：便携 PG 17.11 上同一套 migrations + 全流程测试 PASS；修 3 个方言 bug（见 M5 报告 §1）
- [x] B. 交换机管理（`internal/switchdrv`）：Cisco IOS + H3C Comware SSH 驱动（FDB/端口/VLAN 解析 + 配置模板），mock SSH 端到端验证；M7 按原厂 switchdriver 包补齐华为/Arista/Ruijie/Maipu/ZTE/Juniper 共 8 家（Raisecom 原厂不存在、未补造）；M8 反编译 GetDriver 定案原厂全集 9 具名 + Generic 兜底（Ruijie/Raisecom 均走 Generic），补齐 Inspur/TPLink/Generic 并复刻工厂分发
- [x] C. 虚拟化管理（`internal/virt`）：govmomi 客户端（ListVMs/PowerOn/PowerOff），VMware 官方 vcsim 进程内验证；PVE 客户端骨架
- [x] G. 云主机当被管对象全套真跑（`scripts/m5g-host-e2e.sh`）：注册/心跳/任务/采集 PASS，firmware/raid/locator 真执行返回明确负结果
- [x] D. preseed 装后闭环 → M6 完成（见下）
- [x] E. ARM PXE → M6 完成（见下）
- [x] F. 自研 DHCP live → M6 完成（见下）

### M6：D/E/F 三项收尾 ✅ 完成（2026-10-20，验收见 `docs/M6-REPORT.md`）

- [x] E. ARM PXE：grub-mkimage 自制 grubaa64.efi（配置嵌入镜像），AAVMF HTTP Boot 直拉我方资产，串口实录 arm64 内核启动；配套 grub.cfg 动态渲染、双斜杠归一化、`-log-requests` 全量请求日志
- [x] F. 自研 DHCP live：双虚机 socket 组网抓包实录完整 DORA（OFFER/REQUEST/ACK）+ bootscript + 内核启动；iPXE 三路识别（opt175/opt77/opt60）、file 字段双写、AF_PACKET raw 广播回包
- [x] D. preseed 装后闭环：`scripts/qemu-preseed-snapshot.sh` 快照续跑扛过 3 次沙箱重启跑完全装，install-done + 装后 agent 重报到（run B 干净重跑 PASS）；修产品真 bug：late_command 未 sync 就 POST install-done 会被随后的断电丢掉 agent 文件，已加 sync 前置 + 顺序断言单测

### M7：「原厂数据对齐」✅ 完成（2026-10-21，验收见 `docs/M7-REPORT.md`）

把原厂包里生产验证过的数据挖出对齐物理层（数据级对齐，不等于真机验证）：

- [x] 固件：fw_agent 全量反编译对齐——7 路径分发表、storcli64/arcconf/sas3flash 等命令模板、成功判据、9361 interim 门限 0x72038、PM8060 stdin、3008 SAS 回写五步序、原厂 6 探针 detect；旧实现 4 处错误参数已纠正
- [x] 交换机：2 家 → 8 家（+华为/Arista/Ruijie/Maipu/ZTE/Juniper）；Raisecom 原厂零命中未补造
- [x] SMART 评分：换原厂 `disk_dual_track` v3.1 规则全量（6 维分级 + dmesg 即时轨 5 正则；证据存逆向区）
- [x] RAID/点灯：点灯改原厂 storcli 结构化寻址；建 VD 语法按原厂捆绑 storcli 帮助修正；MegaRAID 直通 SMART 按原厂格式串
- [x] BMC：mock 模板换原厂 Redfish（gofish）路径/动作 + IPMI 命令组，执行仍 mock
- [x] WinPE：原厂逐字路由 `/api/v1/ipxe/win/winpeshl.ini` + winpeshl/unattend 模板 + boot.ipxe 原厂链路

### M8：「深挖定向反编译 + 全量验证数据盘点」✅ 完成（2026-10-21，验收见 `docs/M8-REPORT.md`）

M7 用 strings 没挖到的 6 个点改用 Ghidra 定向反编译挖到底，并系统盘点原厂全部生产验证数据（总目录 `~/workspace/deplos-reversing/VALIDATED-DATA-CATALOG.md`，约 60 条）：

- [x] WinPE：winpeshl.ini 正文逐字实锤（`[LaunchApp]` + install.bat 路径）；autounattend 模板全文三变体整份提取，语言判定/FirstLogonCommands 片段对齐；install.bat 为 JIT 生成 + SambaManager 动态共享 + decryptor 解密链路定案
- [x] 评分聚合：趋势分 = 100 逐条扣分（HDD/SSD/NVMe 三轨规则表全量对齐 `scoring_trend.go`）；推翻 M7「SATA 无评分轨」结论；等级带 ≥60 Warning / <60 Critical、显示封顶 99
- [x] Maipu 逐字模板对齐（switchport/vlan/name + write memory），读路径纠正为 SNMP（企业 OID 5651）
- [x] 厂商全集定性：GetDriver 反编译实锤 9 具名 + Generic 兜底；Ruijie/Raisecom 原厂无驱动（走 Generic）；补齐 Inspur/TPLink/Generic + 工厂分发复刻
- [x] SAS3008 IR/IT：设备 ID ∈ {000b,0012,000B} → IR（反编译+rodata 实读），已对齐 `SAS3008Model`
- [x] LPE1250 传递机制：server 解压固件包到运行目录、以该目录为 CWD 启动 fw_agent，elxflash /auto 扫 CWD（命令模板逐字存档）

### M9：「爆到底」✅ 完成（2026-10-21，验收见 `docs/M9-REPORT.md`）

清掉 M8 全部剩余项 + 两硬核，总目录扩至 89 条终版：

- [x] definitions 54 份 JSON 全份转储 + `internal/osimage` 实现原厂镜像识别算法 + `/api/v1/ipxe/supported-os`
- [x] initrd 全量解包盘点（18,416 条目；loader/heartbeat/pxe-agent 客户端三件套；288 脚本存档）
- [x] 前端整份提取（499 文件）+ 路由 diff，补 7 条实调用路由（server-time/supported-os/network-config/dashboard/raid topology/license import/install.bat）
- [x] install.bat JIT 生成器复刻（`internal/api/winpe_jit.go`，逐字片段 + 我方逐次密钥，不含原厂密钥）
- [x] SSD 子轨 Snic/Host 逐条展开对齐 `scoring_trend.go`；并纠正 M8「严重度码」实为建议文字节长（字段改名 Deduct）
- [x] netplan 模板切换：`internal/networkconfig` 逐字采用原厂模板，agent 新增 `net.config` 任务
- [x] DB 71 表字段级还原，65 张缺表补迁移（`internal/db/m9_migrations.go`）；sys_roles_v 定性不可还原
- [x] 硬核 .hup：AES-256-GCM 整包解密成功（settings.DecryptFile 反编译定位），发行包 28 条目全盘点
- [x] 硬核 agent 字符串：protector 定性为 garble（静态全量恢复不可行，附三证）；动态实跑恢复完整上报协议

### M10：「扫尾全盘点」✅ 完成（2026-10-21，验收见 `docs/M10-REPORT.md`）

文件级覆盖台账清零（42,830 项：✅1,036 / ⛔41,794 / 🔶0 / 👁0），总目录扩至 109 条终版：

- [x] 双架构 .hup 全解密盘点：增量有且仅有一条（pxe-first-boot.sh.tmpl 网络就绪等待+下载重试）；server 双构建同标 1.3.5.1 定案并更正 M9
- [x] loader 函数表 100% 恢复：47aa574d 定性为 garble 构建家族（推翻 M9「独立 protector」），恢复方法固化为脚本；职责定性为 initrd 一次性核心初始化
- [x] heartbeat 第二套协议全恢复（SignedHeartbeat 14 字段、/proc/cmdline 配置、Rust core_secure 签名）
- [x] pxe-agent 数据全量对齐：新增 `internal/collectors/pxealign.go`（lsblk -J / storcli 文本盘表 / sas3ircu DISPLAY 三解析器 + 擦盘序列构建器）与 agent `disk.wipe` 任务（confirm 门控 + 整盘名白名单）
- [x] deplosData 定性结清（全源零命中，驱动运维侧另行分发）；内核选型重构存档（无 IKCONFIG，btrfs 双架构差异）；GitHub 旧版本定性（无包可 diff）；捆绑工具版本全盘点（rclone 双架构版本不一致等）

### M11：「优化」✅ 完成（2026-10-21，验收见 `docs/M11-REPORT.md`）

产品优化轮：原厂 12 条短板逐条对回我方产品（证据→处置全表在报告 §1），落地 5 项优化：

- [x] **agent 协议签名**：`internal/signreq` HMAC-SHA256（per-host 密钥注册时下发、±120s 时间窗、nonce 防重放），checkin/report/metrics 对签名主机强制验签，旧 agent 兼容 token-only
- [x] **first-boot 网络就绪对齐**：agent 首启轮询 server-time ≤120s 并随注册上报 network_ready；preseed 下载改 3 次重试（对齐原厂新构建增量）
- [x] **资产完整性校验**：`/agent/download.sha256` 与 `/checksums.sha256` 由实物实时生成（治原厂 checksums 陈旧/清单不符）；late_command sha256sum -c 验证后才发 install-done，杜绝假报成功
- [x] **评分文案一致性**：NVMe 趋势 key 改为阈值常量生成（治原厂 95/90 文案 vs 96/91 阈值，我们曾逐字复制同款毛病）；新增一致性单测三组
- [x] **proxmox 定义显式停用**：`osimage.Definition.Disabled` + Identify 跳过（此前为保真误当启用）
- [x] 自有优化：preseed 模板解析缓存、collectors 并发采集（两路并发+逐设备有界并发 8）；DHCP 热路径/DB 查询/bootscript 渲染审计后不动（理由在报告 §3）

### M12：「产品化」✅ 完成（2026-10-21，验收见 `docs/M12-REPORT.md`）

从开发仓库变成可发布的产品（自有版本线 **v1.0.0**，不沿用原厂版本号）：

- [x] 发布工程：`scripts/release.sh` 交叉编译 amd64/arm64（-trimpath + ldflags 版本注入，`-version` 可查），版本化 tar 包含二进制/systemd unit/示例配置/install.sh/upgrade.sh/LICENSE/NOTICE(clean-room)；SHA256SUMS 全部对实物实时生成并复验，包内文件齐全性自检
- [x] 从零实测：全新目录解压发布包 → install.sh → systemd 起 server → agent 注册/心跳 → Web 登录 → shell 任务 done 回传 → 坏盘告警触发 → upgrade.sh 同版升级 PASS（完整日志 `docs/m12-evidence/e2e.log`）；实测抓修 install.sh 两个真 bug（umask 770 目录权限、写入链空格损坏）
- [x] 部署文档 `docs/DEPLOY.md`：系统要求/端口/配置全表（从代码实读）/PG 与 SQLite/升级回滚/备份恢复/排障表/已知限制
- [x] 使用手册 `docs/USER-GUIDE.md`：一步一图、UI 标签反引号、术语大白话；8 张配图全部为本产品控制台实拍（逐张来源见 M12 报告），无任何外部图片

### 环境限制（云主机无此硬件，代码就绪即封顶——用户 2026-10-20 口径）

- [x] GPU 压测（无 GPU）/ IPMI 带外执行（无 BMC）/ 固件真刷（无目标卡）/ 点灯真 LED（无 SES 背板）/ RAID 真卡执行（无 RAID 卡）/ SMART 真盘校准（virtio 无 SMART）/ WinPE 真实启动（需授权 Windows 镜像）
  —— 以上解析器/驱动/流程代码均已实现并经样本回归或真执行验证负结果，本环境视为完成。

## 参考资料（只读行为，不搬代码）

- `~/workspace/deplos-reversing/REPORT.md`：逆向深挖报告
- `~/workspace/deplos-reversing/api-routes.md`：API 路由表（行为对标）
- `~/workspace/deplos-reversing/config-data.md`：配置与表结构
- `~/workspace/deplos-reversing/FEATURE_MATRIX.md`：功能还原矩阵
- `~/workspace/deplos-reversing/decompiled/`：关键函数反编译（读逻辑，不抄代码）

## 当前状态

改名（2026-10-21）：产品定名 **RackAnvil**（仓库 `Oumu33/rackanvil`），工作名 deplos-reimpl 弃用；以下里程碑记录中的旧名与旧包名均为当时史实。另注意：引导资产（Debian 官方 netboot，约 122 MB）不进 git 仓库——从源码打包前先跑 `scripts/fetch-assets.sh`（或直接跑 `scripts/release.sh`，它会自动获取并按 Debian 官方 SHA256SUMS 校验）；发布包 tar.gz 内已包含资产，无需此步。
M12 已完成（2026-10-21）：「产品化」——自有版本 v1.0.0 发布包（双架构）构建并通过从零实测（解压→install.sh→systemd 起服→agent 注册→Web 登录→任务链→告警→同版升级，全绿）；部署文档与一步一图使用手册（8 张控制台实拍）交付；`go build/vet/test -count=1` 全绿（21 个测试包）。验收见 `docs/M12-REPORT.md`。
M11 已完成（2026-10-21）：「优化」产品轮——《原厂短板清单》12 条全表（硬编码凭据/NVMe 1739°C/固件后缀误判/late_command 无 sync/checksums 陈旧/双构建同标版本/initrd 内置 root 密钥/autounattend 明文/manifest 不符/评分文案阈值不符/厂商名实不符/proxmox .bak 出货）逐条标注我方处置；新落地：agent HMAC 签名协议、first-boot 就绪对齐、资产 sha256 实时清单+装机验证门、评分文案常量化+一致性单测、proxmox 显式停用、模板缓存与采集并发。`go build/vet/test -count=1` 全绿（21 个测试包，新增 signreq），-race 干净。验收见 `docs/M11-REPORT.md`。
M10 已完成（2026-10-21）：「扫尾全盘点」——文件级覆盖台账 42,830 项清零（🔶/👁 归零），双架构 .hup 增量定案（仅一条模板增量），loader 函数表 100% 恢复、heartbeat 协议全恢复、pxe-agent 数据对齐落地三解析器+disk.wipe 任务，deplosData/内核选型/GitHub 旧版三项定性结清。总目录终版 109 条，`go build/vet/test -count=1` 全绿（19 个测试包）。验收见 `docs/M10-REPORT.md`。
M9 已完成（2026-10-21）：「爆到底」清掉 M8 全部剩余项——definitions 54 份全份转储并实现识别引擎（internal/osimage）、initrd 全量盘点、前端整份提取+补 7 条路由、install.bat JIT 生成器复刻、SSD 子轨逐条对齐、netplan 模板切换（internal/networkconfig）、DB 65 张缺表补迁移；两硬核：.hup 更新包 AES-256-GCM 整包解密成功、agent garble 字符串保护定性+动态恢复协议。总目录终版 89 条，`go build/vet/test -count=1` 全绿（19 个测试包）。验收见 `docs/M9-REPORT.md`。
M8 已完成（2026-10-21）：6 个深挖点全部挖出数据或定性实锤（WinPE 正文/评分聚合/Maipu 逐字/厂商全集/IR-IT 字段/LPE1250 机制），扩围盘点约 60 条验证数据成目录，对齐代码 + 单测落地，`go build/vet/test -count=1` 全绿。验收见 `docs/M8-REPORT.md`。
M7 已完成（2026-10-21）：原厂数据对齐六项全落地（固件/交换机/SMART 评分/RAID 点灯/BMC/WinPE），物理项状态升级为「原厂生产数据对齐」（仍非真机验证）。验收见 `docs/M7-REPORT.md`，`go build/vet/test -count=1` 全绿。
M6 已完成（2026-10-20）：M5 遗留 D/E/F 三项全部闭环——ARM PXE 内核真启动、自研 DHCP live 完整 DORA、preseed 全装+装后 agent 重报到（快照续跑，干净重跑 PASS），并修掉 late_command 未 sync 的产品真 bug。物理/授权项按【环境限制（云主机无此硬件）】口径封顶。验收见 `docs/M6-REPORT.md`。M1–M5 见对应报告。
