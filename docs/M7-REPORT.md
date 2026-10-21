# M7 报告：「原厂数据对齐」

用户指令（2026-10-21）：物理环境无法接触真机，但「可以去看人家官方给出来的数据配合，或者抓 deplos 里面已经验证过的数据」。本轮把原厂 DeplOS v1.3.5.1 包里生产验证过的数据挖出来，逐项对齐我们的物理层实现。

**口径（不变）**：这是数据级对齐。物理项的状态从「代码就绪」升级为「原厂生产数据对齐」，**不等于真机验证**——固件从未真刷、交换机从未真连、BMC 从未真调、WinPE 从未真启动。凡挖不到的写「未挖到」，不编造。

矿源：
- 原厂 server 二进制 `deplos-reversing/pkg/deplos_install_v1.3.5.1/bin/amd64/deplOS`（Go 1.26.3 构建，符号完整；strings + 符号表挖掘）
- 原厂 fw_agent（Ghidra 反编译：`fw_main.c`、`fw_upgradeDevice.c` + 本轮追加反编译 `main.upgrade*` 8 个 handler 与 `main.detectDevices`）
- 原厂 collectors_amd64（内嵌 storcli 帮助文本等）
- 原厂规则 JSON：server 二进制内嵌的 `disk_dual_track` 评分规则全量提取 → `deplos-reversing/m7-vendor-disk-rules.json`（6171 字节，证据文件）
- 厂商公众号文档/截图（`vendor-docs/HrnrOS/`）

## 1. 固件设备表 ↔ internal/firmware

来源：fw_agent 反编译（`main.upgradeDevice`，fw_agent/main.go:192；8 个 handler 本轮逐个反编译确认）。

原厂事实：
- CLI：`detect` 走 detectDevices（探针见下）；`upgrade` 读环境变量 `DEVICE_JSON`（DeviceInfo{Model, CurrentFW, TargetFW, ControllerID}）+ 固件文件 + force。
- TargetFW = 固件文件名经正则 `_([^_]+)$` 取末段；`!force && CurrentFW==TargetFW && CurrentFW!="N/A"` 时跳过。
- 分发按 Model 字符串精确匹配：`3008IR/3008IT`→3008 路径、`PM8060`→PM8060、`9271-8i/SAS3108`→Simple、`9361-8i`→9361、`LPE1250`→LPE1250、`MCX455A`→Mellanox、`QLE2670/QLE2690`→QLogic。
- 工具一律 `filepath.Join(toolsDir, 名)`，固件文件经 findFileByPattern 在固件包目录按正则找（9361 主图 `MR_4MB\.rom`、interim `.*interim.*\.rom`；Simple `\.rom$`；PM8060 `\.ufi$`；3008 `\.bin$`+`mptsas3\.rom`+`mpt3x64\.rom`；QLogic/Mellanox `\.bin$`）。

| 型号 | 原厂命令模板（逐字对齐） | 成功判据 |
|---|---|---|
| 9361-8i | `storcli64 /c{cid} download file={interim} resetnow`（仅当 versionToInt(CurrentFW) < **0x72038**），再 `storcli64 /c{cid} download file={main} noverchk` | 输出含 `status = success` |
| 9271-8i / SAS3108 | `storcli64 /c{cid} download file={rom} noverchk` | 输出含 `status = success` |
| PM8060 | `arcconf romupdate {cid} {ufi}`，stdin 喂 `y\n`（原厂用 exec.Command 直跑） | 输出含 `success` |
| 3008IR/IT | `sas3ircu {cid} display` 取 SAS 地址（正则 `SAS Address\s+:\s+([0-9a-fA-F]{16})`）→ `sas3flash -o -f {bin} -c {cid}` → `-o -b mptsas3.rom` → `-o -b mpt3x64.rom` → `-o -sasadd {addr} -c {cid}` 写回 | 退出码 |
| QLE2670/2690 | `qaucli -b all {bin}` | 输出含 `update successful` |
| LPE1250 | `elxflash /f /auto /q`（argv 里没有固件文件，靠 /auto 扫当前目录） | 退出码 |
| MCX455A | `mlxfwmanager -u -f -d {MST设备路径} -i {bin}` | 退出码 |

detectDevices 探针（原厂不用 lspci，直接探工具）：storcli64 `show` + `/c{id} show all`（正则 `FW Version\s+:\s+`、`Product Name\s+:\s+`，型号含 9361-8i/3108/9271-8i 映射）；arcconf `list`+`getconfig {id} ad`（`Firmware\s+:\s+`，含 PM80→PM8060）；sas3ircu `list`（正则含 `(1000h)\s+([0-9a-fA-F]{4})` 设备号）+ `{id} display`（`Firmware version\s+:\s+`）；elxflash `/q`（`Firmware=([\d\.]+)`）；qaucli `-i`（按 `HBA Model` 切段，正则 `:\s*(QLE26\d{2})`、`BIOS Version\s*:\s*`）；mst `start`/`status` + flint `-d {dev} q`（`FW Version:\s*`、设备正则 `^(\/dev\/mst\/mt\d+.*_pciconf0)`）。

我们的改动：`internal/firmware` 重写——Device 加 ControllerID/FWVersionInt；新增 UpgradeSteps（完整步骤序列+阶段名）、dispatch 全表（精确表+我们 detect 名称的关键词回退到同一 handler）、TargetFWOf、VersionToInt、FindFileByPattern、ResolveFiles、DetectWithTools（原厂 6 探针逐字对齐）；Upgrade 改走步骤序列（双门控不变），PM8060 的 stdin 需求经 stdinRunner 接口落实、不能提供 stdin 时拒绝而不是降级 shell；agent 的 firmware.upgrade 任务加 fw_dir/controller_id/current_fw 透传。单测锁定全分发表、每 handler 的完整 argv/stdin/判据、9361 interim 阈值 0x72038。

与我们旧实现的差异（旧实现是错的，已改）：工具名 storcli→storcli64；控制器不再写死 /c0；旧 `sas3flash -f fw -b rom` 混合形态、旧 `qaucli -pr fc -u`、旧 `hbacmd Download`、旧 `mlxfwmanager --update` 均与原厂不符，全部替换。

未挖到/存疑：versionToInt 的具体打包方式未能从反编译完全确认（我们用 major<<16|minor<<8|patch 重构，常量 0x72038 与比较方向是逐字实锤，门限判定对真实版本串因此是近似）；SAS3008 的 IR/IT 区分字段未挖到（detect 默认 3008IT 并注明）；LPE1250 固件文件如何到达 elxflash 未确认（原厂 argv 确无文件参数）。

## 2. 交换机厂商补全 ↔ internal/switchdrv

来源：server 二进制符号表（包 `deplOS/pkg/switchdriver`）+ strings 字面量上下文。

原厂事实：驱动类型共 10 个——Arista、Cisco、Generic、H3C、Huawei、Inspur、Juniper、Maipu、TPLink、ZTE；方言变量 eosCmd/h3cCmd/iosCmd/junosCmd/maipuCmd/tplinkCmd/vrpCmd/zteCmd；方法集含 CollectMAC/CollectARP/CollectLLDP/CollectInterfaces/GetPortStatus/ConfigureVLAN/ConfigurePortVLAN/SetPortRateLimit/ApplyACL/BindStaticARP/BackupConfig 等。字面量实锤：ZTE `show mac address-table`（`[ZTEDriver]` 日志上下文）；Junos `show ethernet-switching table`、会话初始化 `set cli screen-width 0`/`set cli terminal vt100`、配置模板 `set interfaces %s unit 0 family ethernet-switching vlan members %d`；VRP 会话初始化 `screen-length 0 temporary`、端口命令 `port default vlan %d`、`port trunk permit vlan %d`、`port trunk allow-pass vlan %d`、`undo port hybrid vlan all`、限速 `qos lr inbound/outbound cir %d`；H3C 会话 `screen-length disable`、`port access vlan %d`（我们 M5 已对）；华为/H3C 的 FDB 采集原厂走 **SNMP**（BRIDGE-MIB `1.3.6.1.2.1.17.4.3.1.2`、`1.3.6.1.2.1.17.1.4.1.2`；LLDP-MIB `1.0.8802.1.1.2.1.4.1.*`），二进制里不存在 `display mac-address` 字面量。

我们的改动：switchdrv 从 2 家扩到 **8 家**——新增 huawei-vrp、arista-eos、ruijie-rgos、maipu、zte-zxr10、juniper-junos 驱动（FDB/端口/VLAN 解析 + access VLAN 模板）；加可选 SessionInitializer 接口（华为/Juniper/H3C 的会话初始化串为原厂逐字）。Arista 复用 IOS 形态解析；Ruijie 用 RGOS 规范命令。

诚实标注：① **Raisecom 未挖到**——全二进制零命中（无符号、无日志标签、企业 OID 8886 零出现），不补造，ForVendor("raisecom") 仍报错（有单测锁定）；② ruijie.go 源文件在原厂存在但无驱动符号，疑由 GenericDriver 覆盖，我们的 Ruijie 驱动按 RGOS 规范语法实现、非原厂逐字；③ Maipu 逐字模板未单独挖到（驱动与方言变量存在），按 Maipu 规范语法；④ 华为 CLI 的 display 命令为 NOS 规范等价（原厂该项走 SNMP），已在代码与本表中标注；⑤ 测试样本回显全部为按格式构造的 SYNTHETIC 样本；⑥ 原厂还有 Inspur/TPLink 驱动，我们本轮未补（不在用户清单内），如实记录。

## 3. SMART 健康评分 ↔ internal/collectors

来源：server 二进制内嵌规则 JSON 全量提取（证据文件 `m7-vendor-disk-rules.json`）：`module: disk_dual_track`、`version: 3.1_ml_integrated`、`updated_at: 2026-03-16`，全二进制仅此一个评分模块。双轨设计：

评分轨（缺省 100 分，命中首条条件定分）：
| 规则 | 条件→分数 |
|---|---|
| critical_warning (NVMe, critical) | >0 → 0 |
| percentage_used (NVMe) | ≥100→0, ≥98→20, ≥90→60, ≥80→80 |
| media_errors (NVMe, critical) | ≥1000→0, ≥100→30, ≥10→60, ≥1→90 |
| grown_defects (SAS, critical) | ≥1000→0, ≥500→40, ≥100→70, >0→95 |
| uncorrected_errors (SAS, critical) | ≥50→0, ≥10→40, >0→65 |
| temperature（复合温度） | >75→0, >65→40, >55→75 |

即时轨（dmesg，5 条，全部 severity=Critical / count 型）：dmesg_sata_fatal、dmesg_sata_transport（SCSI DID_* 码全集）、dmesg_nvme_controller、ext4_phys_error、hdd_io_error_generic，正则原文已提取入证据 JSON。

我们的改动：新增 `internal/collectors/scoring.go`——ScoreDiskHealth 按原厂规则表逐条实现（阈值/分数逐字采用，消息文案为我们自己的英文转述，不照抄厂商文案）；ScanDmesgCritical 实现即时轨 5 正则（逐字）；ParseSmartctlText 补 SAS 字段提取（`Elements in grown defect list`、Error counter log 三行末列合计 uncorrected）喂评分；AnalyzeNvme 的严重度阈值改为原厂分级映射（media_errors ≥100 critical / ≥1 warning；percentage_used ≥98 critical / ≥80 warning）。

未挖到/差异：① **SATA 专项 SMART 评分规则未挖到**——原厂双轨里 SATA 靠 dmesg 轨 + smartctl 总体健康覆盖，我们 ATA 的 AnalyzeSmart 保留原 Backblaze 策略并注明；② 聚合公式未挖到：厂商截图里主机「最差分数」95 与其温度维度异常（1739°C 解析 bug）并存，说明原厂总分不是对展示维度简单取 min，我们采用「已评分维度取 min」的保守口径并在此注明分歧；③ ext4 正则要求括号内为裸设备名 `(sda)`，分区名 `(sda1)` 不命中——原厂正则原样复刻、怪癖保留并在测试中注明。

## 4. RAID / 点灯 ↔ internal/raid、internal/locator、collectors

来源：collectors_amd64 内嵌的 storcli/perccli 厂商帮助文本、server strings 格式串。

- 点灯：原厂语法实锤 `%s /cx/ex/sx start locate` / `stop locate`、VD 形态 `/cx/vx start locate`、perccli 形态 `/sasx/ex/sx`；**ledctl 在全资产零命中、sg_ses 零命中**——原厂点灯只有 storcli/perccli 一条路。我们的改动：Disk 加 Ctrl/Enc 数值寻址，齐全时走结构化 argv `storcli64 /c{ctrl}/e{enc}/s{slot} start|stop locate`（无 shell）；旧自由串路径保留兼容；ledctl/sg_ses/nvme 分支保留为**非原厂补充**并在代码注释标明。
- 建 VD：**原厂自身格式串未挖到**（server 侧只有查询/解析）。证据退而求其次为原厂捆绑的 storcli 帮助语法 `/cx add vd r[0|1|5|6|00|10|50|60] drives=e:s|e:s-x`；我们旧的 `add vd type=raid<level>` 不是 storcli 语法，已改为 `add vd r<level> drives=<csv>`。
- MegaRAID 直通 SMART：server strings 实锤格式串 `smartctl -a -d megaraid,%s %s`。已加 MegaRaidSmartArgs + lspci 见 1000:005d/0094 时对 /dev/sda 试 `-d megaraid,0..15` 的探针（collectSmart 直读失败时）；我们多加 `--json` 复用自家解析器，与原厂纯 `-a` 文本模式的差异在此注明。

## 5. BMC ↔ internal/orchestrator

来源：server strings + 符号（原厂用 gofish 库：`redfish.ResetType`、`redfish.PowerState`、`BootSourceOverrideTarget/Enabled/Mode` 类型俱在）。

原厂实锤：动作 URL 形态 `%s/Actions/ComputerSystem.Reset`；路径 `/redfish/v1/Systems/1`、`/Systems/System.Embedded.1`（Dell）、`/Chassis/Self`、`/Managers/1`（iLO）、`/Managers/bmc`（OpenBMC）、`/Managers/iDRAC.Embedded.1` + `/Oem/Dell/Jobs` + `DellJobService.DeleteJobQueue`、`/JobService/Jobs`、`/UpdateService/Actions/UpdateService.StartUpdate`、`/Registries/BiosAttributeRegistry/BiosAttributeRegistry.json`、`/SessionService/Sessions`；IPMI（安装/开通脚本串）：`/usr/bin/ipmitool chassis bootdev pxe`、`ipmitool chassis bootparam get 5`、`ipmitool lan print`、`ipmitool user list 1`、`ipmitool user set password`、`ipmitool user set name`、`ipmitool user enable`、`ipmitool channel setaccess 1` 与 `8`。

我们的改动：新增 `internal/orchestrator/bmc.go`——RedfishPower（POST …/Actions/ComputerSystem.Reset + `{"ResetType":…}`）、RedfishBootPXE（PATCH Boot 三字段 Once/Pxe/UEFI）、SystemIDFor 厂商映射（dell→System.Embedded.1、generic/hpe→1、openbmc→system）、RedfishStartUpdate、IPMIBootPXE 逐字常量、IPMIPower、IPMIProvisionUser 命令组（密码为占位符、绝不入模板）；StepBMCMock 的详情改为渲染这些原厂模板摘要，**执行仍为 mock** 并在文案中明示。

## 6. WinPE 布局 ↔ M4 winpe 模板与 API

来源：server strings。

原厂实锤：路由 `/api/v1/ipxe/win/winpeshl.ini`（符号 handleWinPESHL、InstallationManager.wimbootSubFS、`static/wimboot/wimboot`）；iPXE 行 `initrd %s boot.wim || goto failed`、`initrd %s winpeshl.ini || goto failed`；模板名 `templates/windows_autounattend.xml.tmpl`；unattend 落点 `X:\Windows\System32\autounattend.xml`；启动行 `%SETUP_PATH% /unattend:X:\Windows\System32\autounattend.xml`；计划任务描述 "Report WinPE Init Success"；ISO 布局 `%s/unpacked/%s/sources/boot.wim`、`sources/install.wim|install.esd`；字段 `json:"winpe_iso"`。

我们的改动：新增 `assets/winpe/winpeshl.ini.tmpl`（[LaunchApps] 标准形态：wpeinit → net use Z: 挂共享 → 带 unattend 启动 setup）与 `assets/winpe/autounattend.xml.tmpl`（标准 unattend 骨架，主机名/镜像索引占位，无密钥无口令）；API 新增原厂逐字路由 `/api/v1/ipxe/win/winpeshl.ini` 渲染 ini、`/winpe/autounattend.xml` 渲染 unattend；boot.ipxe.tmpl 按原厂链路重写（每步 `|| goto failed`、winpeshl.ini 以 initrd 从原厂路由加载）；assets README 按原厂布局更新。

未挖到：winpeshl.ini 与 unattend 模板的**原厂正文**（在原厂模板文件/加密段中，未能提取）；SMB 共享名；autounattend.xml 进入 X: 的具体机制。我们的模板为标准形态重构，已逐一在文件头注释标注证据边界。

## 验证

`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿（19 个包）。新增/更新单测：firmware 分发全表 + 每 handler argv 锁定 + 9361 interim 门限 + DetectWithTools 六探针；switchdrv 6 个新驱动解析 + Raisecom 不支持锁定；collectors 评分表 24 组边界值 + dmesg 即时轨 + SAS 文本提取 + megaraid 参数；locator 结构化点灯；orchestrator BMC 模板；api WinPE 三路由渲染。

## 未挖到清单（汇总）

1. Raisecom 交换机驱动——原厂不存在（零命中），未补造。
2. versionToInt 打包方式——重构实现，常量与方向实锤。
3. SAS3008 IR/IT 区分字段；LPE1250 固件文件传递机制。
4. Maipu 逐字命令模板；Ruijie 原厂独立驱动（疑 Generic 覆盖）。
5. RAID 建 VD 的原厂自身格式串（用原厂捆绑的 storcli 帮助语法替代并注明）。
6. SATA 专项 SMART 评分规则（原厂无此轨）；评分聚合公式（采用 min 并注明分歧）。
7. winpeshl.ini / unattend 原厂模板正文；SMB 共享名；unattend 入 X: 机制。
8. 原厂 Inspur/TPLink 交换机驱动存在但本轮未补（不在清单内，如实记录）。
