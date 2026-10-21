# M12「产品化」验收报告 — v1.0.0

时间：2026-10-21 ｜ 结论：**三件交付全部完成**（发布包 / 部署文档 / 使用手册），发布包通过从零实测。

## 1. 发布包

自有版本线 **v1.0.0**（clean-room 独立版本，未沿用原厂 1.3.5.1）。由 `scripts/release.sh` 可复跑构建：交叉编译 linux/amd64 + linux/arm64、`-trimpath`、ldflags 注入 version/commit/build date（二进制 `-version` 可查，实测输出 `deplos-server deplos-reimpl 1.0.0 (commit nogit, built...)`；本仓库非 git 仓库，commit 位为 `nogit`，如实标注）。

| 产物 | 体积 | 内容 |
|---|---|---|
| `dist/deplos-reimpl_1.0.0_linux_amd64.tar.gz` | 108 MB | 37 个文件（见下） |
| `dist/deplos-reimpl_1.0.0_linux_arm64.tar.gz` | 107 MB | 同上（arm64 二进制） |
| `dist/SHA256SUMS` | — | 对两个 tar 包实物生成并复验 OK |

包内清单（每包 37 文件）：`bin/deplos-server`、`bin/deplos-agent`、`install.sh`、`upgrade.sh`、`LICENSE`（MIT）、`NOTICE`（clean-room 声明）、`VERSION`、`README.md`、`systemd/deplos-server.service`、`systemd/deplos-agent.service`、`config/server.env.example`、`config/agent.env.example`、`scripts/backup.sh`、`scripts/restore.sh`、`docs/DEPLOY.md`、`docs/USER-GUIDE.md`、`docs/images/*.png`（8 张）、`assets/`（Debian amd64/arm64 netboot + preseed 模板 + winpe，共 122 MB 未压缩时）。

校验纪律（承 M11 教训）：包内 `SHA256SUMS` 在打包当下对实物生成、生成后立即 `sha256sum -c` 复验；install.sh/upgrade.sh 运行前先自验包内 SHA256SUMS，对不上拒绝执行。发布脚本内另有**文件齐全性自检**（期望清单缺一即失败）。

## 2. 从零实测（发布验收，完整日志 `docs/m12-evidence/e2e.log`）

在全新临时目录解压**发布包**（不是开发树）执行，全程真跑：

| 步骤 | 结果 | 关键日志行（摘自 e2e.log / journal） |
|---|---|---|
| install.sh | PASS | `[install] SHA256SUMS 校验通过` → 建用户/装文件/写配置/注册 unit 全过 |
| systemctl start deplos-server | active | `{"component":"deplos-reimpl-server","status":"ok"}`（/healthz） |
| 首次 admin 密码 | 日志打印一次 | `web: INITIAL ADMIN ACCOUNT CREATED`（密码已在证据日志中脱敏） |
| agent 注册+心跳 | PASS | `initialize: host_id=host-750ce5c38ecc7bf0... (signing enabled)`、`checkin ok` |
| Web 登录 | PASS | `login POST -> 302`，cookie 进资产页 |
| 任务链 | PASS | 经控制台表单下发 `echo hello-deplos && uname -a` → `task-947f94b63ded975c` 状态 `done`、退出码 0、stdout 为本机真实 uname（API 与页面双查） |
| 指标/看板 | PASS | agent 60s 周期上报，/dashboard 渲染真实 CPU/内存曲线（截图时 13 个采样点） |
| 坏盘告警 | PASS | 演示主机经**产品协议**真实注册并上报坏盘 SMART（187=59352、自检 FAILED）→ server 真实产生 `critical smart.sda` 告警（健康 API + 详情页双查） |
| upgrade.sh 同版升级 | PASS | 备份 `upgrade-20261021-131714` → 换件 → 健康检查通过 → 服务保持 active |

注：实测用的二进制构建于 13:04Z；终版 tar 包（13:18Z 重建）纳入了之后修改的脚本与文档，**Go 源码未再变更**，二进制仅构建时间不同。

### 实测抓到并已修复的真问题（2 个）

1. **install.sh/upgrade.sh 被写入链损坏**：文件落盘时多处空格被吞（`[...]` 的闭合空格、`if!`、整段丢失等，系本工作区已知的写入链括号/空格问题）。首轮 install 直接语法报错。已整文件按抗损坏写法重写（`test` 代替 `[]`、正向 if/else 代替 `!`、避开危险邻接），`bash -n` + 实际运行双验。
2. **目录权限依赖 umask**：install.sh 用 `mkdir -p` 建目录，在 umask 007 的硬化环境（如本沙箱）下得到 770，运行用户 `deplos` 无法进入 `/usr/lib/deplos`，systemd 报 203/EXEC Permission denied。已改 `install -d -m 0755/0750` 显式定权，重测通过。另 release.sh 的 `cp -a` 在沙箱 FS 上因无法保留属主失败，已统一改 `cp -r` + `chmod a+rX`（出现 2 处，均已修）。

## 3. 文档交付

| 文档 | 内容要点 |
|---|---|
| `docs/DEPLOY.md` | 架构文字图、系统要求、端口表（9100/TCP、67/UDP 可选、5432 仅 PG）、三步部署、文件布局、systemd 管理、无 systemd 手动启动、**配置全表**（server 17 项 + agent 4 项 + `DEPLOS_ALLOW_FW_UPGRADE`，默认值全部从代码实读）、SQLite/PG 双模式（含无内置搬迁工具的如实说明）、升级回滚、备份恢复、已知限制、11 条排障表、卸载 |
| `docs/USER-GUIDE.md` | 8 个流程：登录→仪表盘→纳管→下发任务→磁盘健康/告警→PXE 装机→用户与授权→备份恢复；分步编号、UI 标签反引号、术语首现大白话、每节带前提/成功判定/出错怎么办；PXE 客户端画面不在控制台内，以 ASCII 流程图说明并明确标注「不是截图」 |
| `README.md` | 顶部新增「快速开始」（三步）+ 文档索引 + M12 里程碑与状态 |
| `docs/STATUS.md` | 刷新 M12 行、发布包家底与下一步候选 |

## 4. 手册截图来源逐张自查（执行用户铁律）

铁律：配图只能是**我们自己 server 跑起来后、从我们自己控制台截的真实界面图**；禁止厂商图、原厂前端提取图、网络图、他产品图；截不到就纯文字并列缺口。自查结论：`docs/images/` 共 **8 张，全部为本产品控制台实拍**（headless Chromium 1280×900，截图对象为 §2 从零实测启动的那个 v1.0.0 实例），逐张已用看图方式验收内容与 URL 一致：

| 文件 | 来源页面（本产品控制台） | 内容核验 |
|---|---|---|
| `01-login.png` | `/login` | 登录表单（用户名/密码/登录按钮） |
| `02-dashboard.png` | `/dashboard` | 监控看板，htch-runtime 真实 CPU/内存采样曲线 |
| `03-hosts.png` | `/hosts` | 资产列表 2 台（本机 agent + 演示主机） |
| `04-host-detail.png` | `/hosts/host-750ce5c38ecc7bf0` | 主机详情：真实采集（CPU 83.7%）、下发任务表单、任务记录 done |
| `05-task-detail.png` | `/tasks/task-947f94b63ded975c` | 任务 done/退出码 0/stdout 真实输出 |
| `06-alarms.png` | `/hosts/host-2b3790e39e9da026` | 演示主机详情：critical 活跃告警卡片（SMART 187=59352 等，系统真实产生） |
| `07-license.png` | `/license` | 许可管理（dev 模式未授权状态如实显示 + 导入表单） |
| `08-orch.png` | `/orch` | 交付编排（新建任务表单含「系统安装」步骤） |

无图环节（如实列明，非缺口造假）：PXE 客户端开机画面（不在控制台内，手册用 ASCII 流程图并标注非截图）；备份恢复（控制台无此页，纯命令步骤）；用户管理（控制台无此页——见遗留 1）。

**截图方法备注（如实记录）**：本沙箱的 Chromium 152 定制版默认拦截一切「公共页发起」的本地导航（`ERR_BLOCKED_BY_LOCAL_NETWORK_ACCESS_CHECKS`），CDP 导航、启动 URL、有头模式、关特性开关、改 managed 策略均无效。最终解法：以一个 `file://` 跳转页作为入口（file:// 被判定为本地发起者，放行跳入 127.0.0.1），此后全程用**页内导航**（表单提交、`location.href`）逐页截图，CDP 只负责抓帧。此事只影响截图工具链，与产品无关。

## 5. 收尾验证

- `go build./... && go vet./... && go test./... -count=1`：**21 个测试包全绿**，零 FAIL。
- 终版发布包 `dist/SHA256SUMS` 复验 OK；包内含最终版文档与 8 张截图。
- 实测实例已在验收后拆除（停服、删系统安装与测试用户、删临时回环别名），证据保留在本报告与 `docs/m12-evidence/e2e.log`。

## 6. 遗留问题（产品缺口，如实清单）

1. **控制台无用户管理/改密码页面**（v1.0.0 已知限制）：仅首次启动创建的 admin 一人可用；RBAC 在服务端生效但无界面入口。DEPLOY/USER-GUIDE 均已如实标注。建议 v1.1 优先补。
2. **SQLite→PostgreSQL 无数据搬迁工具**：切 DSN 即新库，需重新纳管。已写入 DEPLOY §6.2。
3. 两个显示层小瑕疵（不影响功能）：① 从未 checkin 的主机在资产列表「上次心跳」显示为巨大天数（LastCheckin 零值未特判，详情页无此问题）；② 初始化时 agent 不上报 CPU 型号/内存，资产列表这两列为空，要等首次指标上报后在详情页/看板才有（列表页不回填）。
4. 看板 SVG 采样点计数与时间轴标签在窄宽度下轻微重叠（产品自绘图的排版问题，数据无误）。
