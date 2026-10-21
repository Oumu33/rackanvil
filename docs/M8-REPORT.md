# M8-REPORT — 深挖定向反编译 + 全量验证数据盘点（2026-10-21）

方法：Ghidra 12.1.4 headless 定向反编译具体函数（server 工程
`ghidra-proj/server_proj`，fw 工程 `fw_agent_proj`）+ 逐函数字符串
提取（`func_strings.py` 按 LEA/长度寄存器配对）+ 二进制区域整份
转储。证据根：`~/workspace/deplos-reversing/m8-evidence/`
（decompiled/ 31 个函数、resolved/ 20 个常量回填版、strings/ 115 个
函数、regions/ 6 份区域转储、EVIDENCE-NOTES.md 解码笔记）。
总目录：`~/workspace/deplos-reversing/VALIDATED-DATA-CATALOG.md`。

6 个深挖点逐项结论如下（每条指回证据）。

## 1. winpeshl.ini / autounattend 模板正文 + SMB 共享名 ✅ 挖出

- **winpeshl.ini 正文实锤**：全文仅两行 `[LaunchApp]` /
  `AppPath = %SYSTEMDRIVE%\Windows\System32\install.bat`（注意单数
  [LaunchApp]——这正是 M7 按 "[LaunchApps]" 搜索零命中的原因）。
  证据：regions/winpeshl_ini_region.txt（字符串池区域转储，
  vaddr 0x23c9995）；handler `(*InstallationManager).handleWinPESHL`
  @0x190cc20 以 text/plain 伺服（decompiled/ipxeinstall.handleWinPESHL.c）。
  已对齐：`assets/winpe/winpeshl.ini.tmpl` 逐字替换（原标准形态作废）。
- **autounattend.xml 模板全文**：内嵌 `templates/windows_autounattend.xml.tmpl`
  三变体整份提取（13067/16476/19495B，245/276/332 行，含
  RunSynchronous 步骤上报的增量差异）。证据：regions/unattend_blob1/2/3.xml.tmpl。
  关键片段：语言判定（.Asset.Name 含 en-us/en_us/EN-US/English →
  en-US，否则 zh-CN）、`$bootMode` 默认 "uefi"、WillWipeDisk、
  {{.Task.WindowsEditionIndex}}、{{.Task.RootPassword}}（明文）、
  TimeZone "China Standard Time"、FirstLogonCommands（关防火墙 /
  winrm quickconfig / 下载 `{{.BaseURL}}/api/v1/ipxe/win/post-install.ps1?mac=&asset_id=` 执行）。
  已对齐：我们的 autounattend.xml.tmpl 加入语言判定/BootMode/时区/
  FirstLogonCommands 片段 + handler 补 `contains` FuncMap 与
  AssetName/BootMode/BaseURL/MachineID 字段；**明文密码片段按红线
  不复刻**（分歧已写入模板注释）。
- **install.bat 是 JIT 生成**（handleWinInstallBat @0x190cda0，
  日志前缀 [WinPE-JIT]）：等网络 → `net use Z: "%s" "%s" /user:%s >nul`
  （UNC `\\%s\%s`）→ 定位 Z:\...\setup.exe → diskpart 擦盘
  （X:\wipe_disk.txt）→ `decryptor.exe -url "%s" -key "%s" -out
  "X:\Windows\System32\autounattend.xml"`（应答文件 AES 加密传输，
  ipxeinstall 包内 DeriveKey/EncryptAESCFB/CBC 佐证）→ 配置 URL
  `/api/v1/ipxe/config?mac=&asset_id=&boot_mode=`。
  证据：strings/…handleWinInstallBat.txt（40 条）。
- **SMB 共享**：无固定共享名——`SambaManager.EnsureShare` @0x193ff00
  按资产把共享块写入 /etc/samba/smb.conf（逐字模板见目录 4.4：
  只读、valid users、create mask 0755、force user root、
  `### BEGIN/END SHARE %s ###` 包裹），systemctl 重启/重载；
  用户 pxeuser/PxeUser。证据：strings/…SambaManager.EnsureShare.txt。
- **wimboot iPXE 脚本全文**（497B）已提取并对齐 boot.ipxe.tmpl
  （initrd 含 install.bat 与 decryptor.exe 两行、失败块 sleep 30 +
  reboot）。证据：regions/wimboot_script.ipxe.tmpl +
  strings/…handleIPXEBootScript.txt。

## 2. 评分聚合公式 ✅ 挖出（并推翻 M7 一条结论）

证据：resolved/hwfault.{AnalyzeDiskSmartHistory,func1,func2,
applyHDDRules,applySSDRules,applyNVMeRules,applyAbsoluteOrDelta,
buildOutput,EvaluateSmartMLAlgorithm}.c（常量已回填实读值）。

- 聚合 = **趋势分：100 起逐条扣分**。report 闭包（func1）：未白名单
  才记 finding 并扣分（分 >0 才扣）；delta 闭包（func2）= 当前 −
  上期。FAILING_NOW −100；health "FAILED" −100（HDD/SSD）。
- 三轨规则表全量提取（目录 §6 表）：HDD 属性 5 的分层（≥2801 扣 95 /
  1001–2800 扣 80 / 增长 Δ≥4 扣 80 / 连续增长 ≥3 次扣 60 / 增长
  扣 40 / 存量扣 8，含连续计数与首次基线语义）、197/198 扣 80、
  Δ184 扣 60、Δ187 扣 80、Δ199 扣 20；SSD 的 183/184 Δ 扣 60 等；
  NVMe critical_warning 扣 100、percentage_used ≥96 扣 80 / 91–95
  扣 40（文案写"超过95%/90%"，阈值实为 96/91）、temperature >50
  扣 40、media_errors 与 num_err_log_entries 走 applyAbsoluteOrDelta
  （当前 >10000 扣 100/60，否则 Δ>0 扣 80/40）。
- 等级（buildOutput）：无 findings → Info（显示 100）；分 ≥60 →
  Warning 且有 findings 时显示封顶 99；分 <60 → Critical。
- **SATA 定性（推翻 M7）**：SATA 有完整属性评分轨
  （applyHDDRules/applySSDRules，另有 Snic/Host 两个 SSD 子轨，
  以属性 "5"/NAND 为键）；M7 提取的 JSON 是静态轨配置
  （LoadRuleConfig("disk.json") → EvaluateMetrics），静态轨
  EvaluateSmartMLAlgorithm 按厂商/型号分发 EvaluateNVMe /
  EvaluateSAS / evaluateSATASSD / evaluateSeagate / evaluateFallback
  只产 findings。趋势分发只有 HDD/SSD/NVME 三分支——**SAS 无趋势
  专轨**（定性，见目录 6 末行）。
- 已对齐：新增 `internal/collectors/scoring_trend.go`
  （ScoreDiskTrend + TrendGrade，白名单/连续计数/首次基线语义齐）
  + `scoring_trend_test.go`（7 个用例组覆盖各层）；scoring.go 的
  M7 错误结论（"SATA 无评分轨""聚合未挖到"）已改写。

## 3. Maipu / Ruijie 逐字模板 ✅ 挖出 + 定性

- **Maipu**（strings/…MaipuDriver.*.txt）：读路径是 **SNMP 不是 CLI**——
  CollectMAC 走 Q-BRIDGE dot1qTpFdbPort 1.3.6.1.2.1.17.7.1.2.2.1.2，
  CollectInterfaces/GetPortStatus 走 IF-MIB/IFX-MIB，verifyVendor
  查企业 OID 1.3.6.1.4.1.5651 与型号串 maipu/mypoweros/mp-。
  配置逐字模板：`interface %s`、`switchport mode %s`、
  `switchport access vlan %d`、`switchport trunk native vlan %d`、
  `switchport trunk allowed vlan add %d`、`vlan %d`、`name %s`、
  `configure terminal` … `write memory`；VLAN 合法范围 1–4094。
  已对齐：maipuDriver.AccessVLANCommands 改逐字序（收尾
  write memory），新增 TrunkVLANCommands / CreateVLANCommands，
  文件注释纠正读路径为 SNMP。
- **Ruijie**：见 §4 定性——原厂无 Ruijie 驱动，走 Generic。我们的
  RGOS 驱动保留为超集扩展并已标注（switchdrv.go 包注释、
  vendors_m8.go 文件头）。

## 4. Raisecom 定性 + 厂商注册表全集 ✅ 定性实锤

- `switchdriver.GetDriver` @0x14bf1c0 反编译 + 立即数逐条解码
  （EVIDENCE-NOTES.md 有完整解码表）：分发全集 = H3C、ZTE、Cisco、
  Maipu、Arista、Huawei、Inspur、Juniper、TP-Link 九具名 + 默认
  Generic。与 pcln 符号表 10 个驱动类型、各 GetVendor 返回值
  （Generic/Inspur/Maipu/TP-LINK）三方互证。
- **Raisecom 不在全集 ⇒ 原厂不支持（走 Generic 兜底）**，Ruijie
  同。此结论由注册/分发全集支撑，非"搜不到"。
- 已对齐：补齐 Inspur（SetPortVlan 逐字 `interface %s` +
  `switchport access vlan %d`；读为 SNMP 的事实入注释）、TP-Link
  （`show mac address-table` 逐字、SetVLAN + `copy ru…` 保存）、
  Generic（桩语义：Collect* 返回不支持错误）三驱动，
  新增 `ForVendorName` 复刻工厂分发（含 Ruijie/Raisecom→Generic
  的单测断言）。switchdrv 至此与原厂全集同构（9 具名 + Generic，
  另加我们标注的 Ruijie 扩展）。

## 5. SAS3008 IR/IT 区分字段 ✅ 挖出

- fw `main.detectDevices` @0x5109e0 反编译 + rodata 实读
  （EVIDENCE-NOTES.md）：判定字段 = `sas3ircu list` 输出经正则
  `(?m)^\s*(\d+)\s+SAS.*(1000h)\s+([0-9a-fA-F]{4})` 捕获的**设备 ID
  组**；与 "000b"/"0012"/"000B" 字节级比较，命中 → 模型
  "3008IR"（0x54c279），否则 "3008IT"（0x54c273）。非 lspci 子型号、
  非固件串。
- 已对齐：`firmware.SAS3008Model(devID)` + detect_tools.go 改用
  （删除原"0097→默认 IT"近似），单测含三 IR 值、0097→IT 与端到端
  IR 检出。附带确认：3108 模型串为 "SAS3108"（0x54c4ce 实读）。

## 6. LPE1250 文件传递机制 ✅ 挖出

- fw `main.upgradeHBALPE1250` @0x515380（main.go:403）反编译：
  工具 = Join(toolsDir,"elxflash")，argv ["/f","/auto","/q"]，
  **不引用固件文件参数、不 chdir、不拷文件**；`main.runCommand`
  @0x510640 反汇编确认不设 Cmd.Dir ⇒ 继承 fw_agent 的 CWD，
  elxflash /auto 扫 CWD。
- server `fwupgrade.StartUpgradeProcess` 字符串实锤到位方式：
  目标机上 `mkdir -p <dir> && chmod 700 <dir>` → `unzip -o <pkg>
  -d <dir>` → DEVICE_JSON 写 `<dir>/.device_env_<n>` →
  `DEVICE_JSON=$(cat …) <dir>/fw_agent --tools-dir=… --action=upgrade
  --d=<dir> <fwfile>` 在该目录上下文执行——固件文件随包解压落在
  CWD 里被 /auto 拾取。证据：strings/…StartUpgradeProcess.txt +
  EVIDENCE-NOTES.md。
- 已对齐：firmware.go LPE1250 步骤 Note 按证据改写（含完整命令
  模板）；我们的 Upgrade 同受双门控约束、从未真刷（环境限制）。

## 扩围盘点（全量验证数据目录）

`VALIDATED-DATA-CATALOG.md` 共 7 章 ≈ 60 条：命令模板 13 组
（drconfig 动作集、固件工具全集、netplan 全文模板 + 其余 4 个网络
驱动、网络引导脚本全文 1275B、IPMI 本地配置命令组、点灯、GPU/CPU
压测点火与参数、镜像源生成）、阈值常量（离线检测 ticker 5s、
评分全表、VLAN 1–4094、9361 门限等）、硬件字典（§3/§5 两项 +
PCI ID 表 + 固件图案 + 厂商 OID/型号路由）、模板正文（§1 全部 +
装机模板名全集 20+、firstboot 字段契约、Samba 块模板）、嵌入资源
（definitions 约 50 份 JSON 名单、templates 名单、rules/disk.json、
前端与 wimboot 子 FS 存在性）。每条带来源函数/地址、证据文件、
对齐状态。

## 对齐改动清单（代码）

| 文件 | 改动 |
|---|---|
| internal/collectors/scoring_trend.go（新） | 原厂趋势评分引擎（HDD/SSD/NVMe 三轨 + 等级带） |
| internal/collectors/scoring_trend_test.go（新） | 7 组用例（分层/连续计数/白名单/等级） |
| internal/collectors/scoring.go | 改写 M7 错误结论段（SATA 轨、聚合已 recovered） |
| internal/switchdrv/vendors_m8.go（新） | Inspur/TPLink/Generic 驱动 + ForVendorName 工厂复刻 |
| internal/switchdrv/vendors_m8_test.go（新） | 分发全集 + 逐字模板断言 |
| internal/switchdrv/vendors_m7.go | Maipu 逐字模板对齐 + SNMP 事实纠正 + Trunk/Create 方法 |
| internal/switchdrv/switchdrv.go | 新增 3 个 Vendor、ForVendor 注册、包注释定性更新 |
| internal/firmware/detect_tools.go | SAS3008Model（IR/IT 实锤规则）+ 注释 |
| internal/firmware/detect_tools_test.go | IR/IT 单测 + 端到端 IR 检出 |
| internal/firmware/firmware.go | LPE1250 步骤 Note 按传递机制证据改写 |
| assets/winpe/winpeshl.ini.tmpl | 逐字正文替换 |
| assets/winpe/boot.ipxe.tmpl | 按原厂脚本全文重写（initrd 集 + 失败块） |
| assets/winpe/autounattend.xml.tmpl | 语言判定/BootMode/时区/FirstLogonCommands 片段对齐 |
| assets/winpe/README.md | 链路说明按 M8 证据改写 |
| internal/api/m4.go | unattend handler 补 contains/新字段；winpeshl 注释更新 |
| internal/api/m4_test.go | winpeshl 断言改 M8 逐字正文 |

验证：`go build ./...`、`go vet ./...`、`go test -count=1 ./...`
全绿（17 个测试包 ok）。FEATURE_MATRIX.md 行 6/12/14/26 已追补 M8。

## 剩余项（如实）

- definitions/*.json 正文未逐份转储（名单全集已有；正文是各 OS
  安装参数表，与现有 pxe 生成逻辑重叠度高，未列为差异）。
- SSD 子轨 applySnicSSDRules / applyHostSSDRules 细则未逐条展开
  （存在性、触发品牌串 "Snic"、键属性 "5"/NAND 已实锤）。
- install.bat JIT 生成器未在我们代码中复刻（机制与逐字片段已
  存档；我们的 winpe 链以模板三件套交付，JIT 属服务端动态生成，
  列为已知差异）。
- 网络配置 netplan 等模板已存档未切换（我们的 agent 网络配置为
  自有实现；模板可直接采用，未在本轮动工）。
- 离线判定阈值为原厂配置结构字段（非字面量），无硬编码值可对齐。
- 全部物理执行项仍为环境限制（真交换机/真卡/授权 Windows 镜像），
  本轮为数据级对齐，不改变该状态。
