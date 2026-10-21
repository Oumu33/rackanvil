# M4 验收报告 — 厂商文档对齐的 8 个功能（2026-10-19）

目标：把厂商公开文档（`~/workspace/deplos-reversing/vendor-docs/HrnrOS/`，23 篇文章）里有、而我们 M1–M3 还没有的 8 个功能全部 clean-room 实现。用户原话"全都要"。

结论：**8 项全部交付，`go build` / `go vet` / `go test ./...` 全绿**；其中需要真硬件/真授权才能验证的 4 项（点灯、固件升级、RAID 真卡、WinPE 镜像）如实标注"未验证"，代码写完、单测+mock 通过。

## 一、文档依据（精读 7 篇，提取的实现细节）

| # | 文章 | 挖到的硬细节 | 落到实现 |
|---|------|-------------|---------|
| 1 | 硬盘健康分析.md | SMART 判定不能只看阈值，要"阈值+趋势"双预警；Backblaze 公开数据验证过的关键属性是 5/187/188/197/198 | M2 已有 Backblaze 策略，本次补上厂商强调的**平台级故障感知**：CPU MCE、内存 CE/UCE、PCIe AER、NVMe 备用块余量（见下） |
| 2 | 介绍一款给服务器硬盘定位（点灯）工具.md | Web 远程点灯→现场"一秒找对盘"；点亮/熄灭是成对按钮状态机 | `internal/locator`：ledctl→sg_ses→storcli→nvme 优先级链，API+Web 页面 |
| 3 | 介绍一款 RAID-HBA 固件升级工具.md + `fw_upgradeDevice.c` 反编译 | 7 条分发路径按型号后缀走：9361→storcli、PM8060→arcconf、3008→sas3flash(+mpt3x64.rom)、QLogic→qaucli、Emulex/LPe1250→hbacmd、Mellanox→mlxfwmanager | `internal/firmware`：detect 全实现；upgrade 代码写完但**默认关闭、双门控** |
| 4 | 介绍一款支持多网段跨机房的管理软件.md | 代理节点=本地镜像缓存+PXE bootscript 中继+向上游同步心跳/任务；各网段独立服务栈（接口 Tags：PXE 网络/业务网络/通用网络） | `internal/relay`：资产缓存、中继 bootscript、心跳；`relay_nodes` 表 |
| 5 | 信创异构服务器批量 PXE 部署工程实践与架构设计.md | DHCP option 93：`0x0007/0x0009→x86_64 UEFI 给 bootx64.efi`，`0x000B→ARM64 UEFI 给 bootaa64.efi`；ProxyDHCP 旁路方案（RFC 2131：客户端拿到 IP 后，旁路只回 PXE 引导信息，不动原有地址分配）；BMC 三原子操作（上电/设启动源/查电源） | DHCP 增加 `Proxy` 模式（yiaddr=0.0.0.0，不消耗租约）+ `BootFileARM64`；bootscript `arch` 参数 |
| 6 | 大规模数据中心裸金属自动化装机与纳管实践.md | "引导靠推，配置靠拉"异步解耦；anaconda `reqpart` 把分区决策下放给安装程序原生探针；**硬件故障感知**："CPU 的 MCE 报错、内存的 CE/UCE 纠错码、PCIe 链路的 AER 错误、NVMe 盘的可备用块余量"；拓扑："基于 MAC 地址逆向溯源，与核心交换机 ARP 与 FDB 表联动，自动标定上联交换机名称及物理端口" | `collectors/deephealth.go`（EDAC/AER/MCE/NVMe）+ 服务端 `evaluateDeep` 告警；`internal/topology`（lldpctl 解析 + FDB 联动） |
| 7 | HrnrOS 完整技术手册.md（通览） | 查漏：WinPE 链（iPXE→wimboot→SMB 挂载→winpeshl.ini→unattend）；配置存档一键恢复；RAID 创建/删除/查询 | `assets/winpe/` 模板+流程代码（无镜像）；`internal/cfgarchive`；`internal/raid` |

## 二、8 项实现 → 验证证据 → 诚实边界

### 1. 硬盘定位（点灯）`internal/locator` — PASS（单测+mock；无真盘未验证）
- 实现：`Scan()`（lsblk -J 枚举）→ `Locate(device, on)` 按工具优先级链 `ledctl --identify → sg_ses --set=ident → storcli /c0/eX/sY start locate → nvme set-feature -f 0x0c` 执行点亮/熄灭；槽位→设备映射由 Scan 输出的 enclosure/slot 字段承载。agent 新增 `disk.scan` / `disk.locate` 任务类型；API `/api/v1/locator/{scan,locate}`；Web `/locator` 页面。
- 验证：单测 6 个（优先级链顺序、命令构造、未知工具拒绝、JSON 解析、dry-run）；真机 e2e：server+agent 实跑，`disk.scan` 返回 3 块盘（tool:"none"，沙箱无 LED 工具——如实）；`disk.locate` 在无工具时返回明确错误"no locate tool available"，不伪造成功。
- 边界：无真盘/真背板，点灯的真实 LED 效果**未验证**。

### 2. 配置存档一键恢复 `internal/cfgarchive` — PASS（完整验证）
- 实现：`Take()` 抓 RAID（storcli show / sas3ircu display，缺工具则如实记 unavailable）、分区表（sfdisk -d）、网络（ip -j addr/route）、已装包；存 `cfg_snapshots` 表；`RestorePlan()` 需 `confirm=true` 否则拒绝，网络恢复步骤只生成命令、RAID 重建标记为手动（防误伤）。
- 验证：真机 e2e 全链路跑通：`cfg.snapshot` 下发→agent 执行→`/cfgarchive/save` 归档→`/cfgarchive/list` 查到→`/cfgarchive/restore` 无 confirm 被拒（400）→有 confirm 生成恢复任务。API 单测覆盖。
- 边界：纯软件项，无需真硬件。

### 3. 拓扑映射 `internal/topology` — PASS（单测+mock；无真交换机未验证）
- 实现：`ParseLLDP()` 解析 lldpctl JSON（邻居=上联交换机+端口）；`ResolveByFDB()` 实现文档里的"ARP 与 FDB 表联动"：交换机 FDB（port,MAC）+ 主机 MAC 列表 → 上联映射；存 `topo_links` 表；API `/api/v1/topology/{collect,links,fdb-import}`；Web `/topology` 页面。
- 验证：单测（lldpctl 解析、FDB 解析大小写/分隔符归一化、空输入）；API 测试导入一条 mock FDB 记录→`/topology/links` 查到 SW-1/Gi1/0/1。
- 边界：沙箱无 lldpd、无真交换机，`topo.collect` 在真机 e2e 中如实失败（lldpctl 不存在）。

### 4. 跨机房代理节点 `internal/relay` — PASS（双进程模拟验证）
- 实现：`Cache`（内存+磁盘二级缓存，TTL）存 bootscript/资产；`Node.Heartbeat()` 每 30s 向上游 POST `/api/v1/relay/nodes`；`relay_nodes` 表注册。设计文档见报告 §四。
- 验证：双进程模拟"主控+代理"：代理注册→主控 `/api/v1/relay/nodes` 查到 relay-sh；心跳更新 last_seen；缓存命中测试通过（单测）。
- 边界：单机双进程模拟，未做真实跨机房/跨网段部署验证。

### 5. ARM64 引导资产 — PASS（HTTP 层验证；QEMU aarch64 不可用如实标注）
- 实现：下载 Debian bookworm **官方 netboot arm64**（`linux` 32MB + `initrd.gz` 39MB，file 确认为 ARM64 kernel + gzip），放 `assets/debian-bookworm-arm64/`；DHCP 新增 `BootFileARM64="bootaa64.efi"`（option 93 0x000B，文档原话）；bootscript 增加 `arch` 参数（`?arch=aarch64` → `/ipxe/arm64/linux` + `console=ttyAMA0`）；新增 `/ipxe/arm64/{linux,initrd.gz}` 路由。
- 验证：真机 e2e：`bootscript?arch=aarch64` 渲染出 arm64 路径；两个资产 URL 返回 206（Range 请求，70MB 文件未全量下载验证内容）；`file` 命令确认内核格式。
- 边界：本机无 `qemu-system-aarch64`（`which` 确认缺失），PXE→内核启动的软模拟**未做**，如实标注。

### 6. 固件升级 `internal/firmware` — 部分 PASS（detect 完整；upgrade 默认关闭未验证）
- 实现：`Detect()`：lspci 枚举 PCI 设备→按厂商 ID 分类（1000/1001/1077/10df/15b3 等）→型号→7 路径映射（9361/storcli、PM8060/arcconf、3008/sas3flash、QLogic/qaucli、Emulex+ LPe1250/hbacmd、Mellanox/mlxfwmanager），全自己写的代码。`UpgradePlan()` 构造刷写命令。**双门控**：API 要求 `confirm=true`；agent 要求环境变量 `DEPLOS_ALLOW_FW_UPGRADE=1`，缺一个就拒绝并明示变砖风险。
- 验证：单测 7 路径分发全过（含 vendor 后缀陷阱修复：`MegaRAID_SAS_9361_8i` 按原厂 `_([^_]+)$` 会取到 "8i" 而误判，我们改用全型号关键词匹配并单测锁定）；真机 e2e：`firmware.detect` 在沙箱如实失败（无 lspci）；`firmware.upgrade` 无环境变量时被 agent 明确拒绝。
- 边界：无真卡，**任何 upgrade 命令从未真实执行**，误刷变砖风险已在代码/API/Web 三处明示。

### 7. Windows 装机链路（WinPE）— 流程代码完成，未验证（需授权镜像）
- 实现：`assets/winpe/boot.ipxe.tmpl`（iPXE→wimboot→BCD/boot.sdi/boot.wim→SMB→winpeshl.ini→unattend，全链路注释写清）；`/winpe/boot.ipxe?mac=` 按主机渲染；`/winpe/{file}` 静态服务；`assets/winpe/README.md` 明确列出所需授权文件清单。
- 验证：模板渲染单测+真机 e2e（boot.ipxe 返回 200，含 wimboot 链）；`/winpe/boot.wim` 返回 404 且正文写"needs licensed Windows image"。
- 边界：**目录故意留空**；任何 Windows 镜像（boot.wim/install.wim）需要客户授权，我们不下载、不内置、不测试实际 WinPE 启动。

### 8. RAID 配置 `internal/raid` — PASS（单测+mock；无真卡未验证）
- 实现：storcli JSON 解析（`ParseShowAll`：控制器/VD/PD 状态机）；`CreateVD`（0/1/5/10，拒绝其他级别，缺盘数校验）；`DeleteVD` 要求 confirm；`LocatePD`。agent 新增 `raid.query` 任务；API `/api/v1/raid/{query,create}`（create 要求 confirm=true）；Web `/raid` 页面。
- 验证：单测（storcli JSON 解析、非法级别拒绝、命令构造）；真机 e2e：`raid.query` 在沙箱如实失败（no RAID CLI found）。
- 边界：无真卡，创建/删除从未真实执行。

## 三、附带交付（M4 搭车）
- **ProxyDHCP 模式**：`--dhcp-proxy`，DISCOVER 回 OFFER 仅带引导信息（yiaddr=0.0.0.0），不消耗租约——文档原话"不动 DHCP，从旁路切入"。单测覆盖。
- **平台深健康**：`collectors/deephealth.go`（EDAC ce/ue、PCIe AER、MCE 存在性、NVMe avail_spare），agent 上报 `Snapshot.Deep`，服务端 `evaluateDeep` 按阈值（UE>0 / CE>1000 / AER fatal>0 / spare<10）发 `platform.deep` 告警；e2e 验证 UE=2 触发告警。
- **DB 迁移**：`cfg_snapshots`、`topo_links`、`relay_nodes` 三表（SQLite/PG 双驱动兼容，upsert 语法）。
- **Web 页面**：点灯/拓扑/固件/RAID/存档/代理 6 个新页面，导航栏已挂载；登录+RBAC 复用既有。

## 四、部署说明（代理节点）
```
# 机房 B 的代理节点（示例）
./deplos-relay -name relay-sh -upstream https://主控:9100 \
  -subnets 10.20.1.0/24 -cache-dir /var/lib/deplos/relay
# 代理提供：本地 /api/v1/ipxe/bootscript 中继、资产缓存、
# 本网段 DHCP/PXE（-dhcp-proxy 旁路模式）；心跳 30s 上报主控。
```

## 五、代码统计
- M4 新增：6 个包 + deephealth + db/store 扩展 ≈ **1816 行**（含单测）
- M4 API/Web/模板/agent 接线 ≈ **1200 行**
- M1→M4 总计：**≈7380 行** Go（不含测试）；测试 ≈1500 行

## 六、诚实边界总览
| 项 | 状态 | 未验证点 |
|---|---|---|
| 点灯 | 代码+单测 PASS | 真 LED 效果（无真盘） |
| 配置存档 | 全链路 PASS | — |
| 拓扑 | 单测+mock PASS | 真交换机 FDB（无） |
| 代理节点 | 双进程模拟 PASS | 真实跨机房部署 |
| ARM64 | HTTP 层 PASS | QEMU aarch64 软模拟（本机无 qemu-system-aarch64） |
| 固件升级 | detect PASS / upgrade 双门控 | 真卡刷写（永不做无授权测试） |
| WinPE | 流程代码 PASS | 需授权镜像（目录留空） |
| RAID | 单测+mock PASS | 真卡执行 |

`go build ./... && go vet ./... && go test ./...` 全绿（2026-10-19 10:15）。
