# DeplOS 重实现 M2 执行计划（草案 · 待拍板）

> 状态：草案，2026-10-19 编写。M1 已于 2026-10-19 凌晨全线 PASS（见 `docs/M1-REPORT.md`），
> Hana 在 side chat 问"要接着干 M2 吗？"，等你一句话。
> 本计划只做**准备**：你点头才开工；点头前一行代码不写。

## M1 交付盘点（已实锤）

- ~1300 行原创 Go：server（:9100：initialize/checkin/report/bootscript）、agent（注册→心跳→领任务执行→回传）、自研 DHCP（internal/pxe，DISCOVER→OFFER/REQUEST→ACK，包级单测覆盖）
- QEMU SLIRP 全链路 PASS：虚机 PXE→SLIRP 内置 DHCP/TFTP→我方 bootscript，server 日志见真实 MAC `52:54:00:12:34:56`，exit 0
- 诚实边界（带入 M2）：自研 DHCP 的 live 验证仍待真环境（沙箱禁 UDP，SLIRP 绕开的是"验证手段"不是"DHCP 本身"）；`/ipxe/{vmlinuz,initrd.img}` 目前 501 占位；数据层是内存实现（`internal/store/store.go`），`internal/db/` 还是空的

## M2 目标

把 M1 的"链路通"变成"能用"：**资产能入库、机器能装完、人能登录看、采集能出图**。
全部在 QEMU 虚拟机里验证，**不依赖物理机**（需真机的项明确划到 M3）。

## 阶段划分（顺序有依赖，别跳）

### Phase 2.0 · PostgreSQL 接入（地基，先干这个）

- 按 `~/workspace/deplos-reversing/db-tables.txt`（71 张）建 migrations；先落**核心子集约 15 张**（hosts/资产、tasks/任务、users/用户、audit、alarms、reports），其余 56 张按后续模块增量加
- `internal/store` 切到 PG（连接池、事务），M1 的 API 行为不变；内存实现保留作测试替身
- 部署时生成凭据，**无硬编码**（延续红线）
- 验收：server 连真 PG 启动；initialize→checkin→report 数据进 PG 表；`scripts/m1-e2e.sh` 全绿

### Phase 2.1 · provisioning 装机闭环（M1 欠的债）

- 把 501 占位的 `/ipxe/{vmlinuz,initrd.img}` 换成真资产：拉 Debian netboot 内核+initrd 入库，bootscript 按 MAC 渲染
- preseed 模板变量替换（参考反编译逻辑重写）；装机任务状态机：下发→执行中→装完→agent 重报到
- 验收：QEMU 虚机**全程无人值守装完 Debian**，重启后新 agent 自动 initialize，server 日志有完整状态机流转

### Phase 2.2 · discovery + CMDB 资产入库

- 网段扫描→资产发现→入库→BOM 比对（`discovery.HandleRangeScan`、`assetmanager` 行为参考重写）
- 验收：QEMU 虚机扫出来进 hosts 表；API 可查资产；BOM diff 能展示（CPU/内存/盘位变化）

### Phase 2.3 · 认证 + Web 控制台

- JWT+bcrypt 登录，RBAC 权限隔离（**不用原厂弱实现**，自研）
- 自研极简 Web（先够用：资产列表、任务下发、状态看板）；API 保持对标原厂路由行为（`api-routes.md` 12 模块 147 条，按需实现）
- 验收：浏览器登录；不同角色权限隔离验证通过

### Phase 2.4 · collectors + 监控 + dashboard（含存储故障预测）

- 采集器协议自研：CPU/内存/磁盘/SMART/网卡 → server 入库 → dashboard 出图
- 存储故障预测（你在矩阵里点名的 P1）：SMART 采集 + 公开阈值/趋势判定 + 告警闭环；**GPU 项标注需真机**
- 验收：虚机采集→入库→出图；SMART 阈值告警能触发；active_alarms/alert_history 表有记录

### Phase 2.5 · orchestrator 交付编排

- 任务模型+步骤状态机（参考 `HandleCreateJob`）；流水线：BMC→RAID→压测→装机，其中 RAID/固件步骤 mock 或跳过
- 验收：虚机跑完一条完整编排流水线，状态机可查

### Phase 2.6 · license + 备份/升级 + WebSSH

- license 自研（Ed25519，不碰原厂 RSA 体系）：签发→验证→过期拒绝
- 备份：pg_dump+文件打包；升级：不用原厂加密 .hup 格式
- WebSSH：标准 WebSocket+SSH 客户端，浏览器开终端连虚机
- 验收：license 过期拒绝；备份→恢复演练通过；浏览器终端连通

## 明确不做（M3，真机项）

GPU 压测（需真卡）、IPMI/Redfish 真机带外、固件刷写（先只做 detect，防变砖）、真交换机驱动（先 Cisco/H3C 两家）、ESXi/PVE 嵌套、自研 DHCP 的 live 验证（待真环境）。M2 不被这些阻塞。

## 需要你拍的 3 个点

1. **71 张表**：先核心 15 张起步、按模块增量加（推荐），还是一次性全建？
2. **Web UI**：自研极简版先用（推荐，能验证闭环），还是只做 API、前端以后再说？
3. **存储故障预测深度**：SMART 阈值+趋势告警（推荐，公开资料可验证）即可，还是要上预测模型？

回"开工"即按推荐项启动；有改的直接说哪条。
