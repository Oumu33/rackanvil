# M9-REPORT — deplos-reimpl M9「爆到底」验收报告（2026-10-21）

任务：清掉 M8 全部剩余项 + 啃两个硬核（.hup 解密、agent 字符串解密）。
验收口径（用户）：剩余清单逐项结清（成或定性不成），不许再留
「未逐份/未复刻」的尾巴。证据全存 `~/workspace/deplos-reversing/
m9-evidence/`，本报告每条结论指回证据。

**总结：第一梯队 7 项全部结清（6 项成、1 项成 + 子项定性）；第二
梯队 2 硬核：.hup 完整解密成功；agent 字符串静态全量恢复定性为
不可行（garble），但动态实跑恢复了完整协议面。总目录终版 89 条。
`go build/vet/test -count=1` 全绿（19 个测试包，基线 17 + 新增
networkconfig、osimage 两包）。**

## 第一梯队

### 1. definitions/*.json 全份转储 ✅
- 54 份 JSON 正文整份提取（M8 只有名单）：`m9-evidence/definitions/`
  + `DEFINITIONS-INDEX.md`（逐份 id/name/family/主版本/身份文件/正则）。
- 结构定性：OS 镜像识别定义（身份文件 + 内容正则 + validationFiles
  文件清单校验 → 家族/主版本 → 驱动模板选择）。proxmox 份在原厂构建
  中以 .bak 停用态出货，已如实记录。
- 代码对齐：新增 `internal/osimage`——按原厂算法实现 Identify（身份
  文件多选一 `|`、RE2 正则、validationFiles 全存在），54 份定义以
  embed 进代码；`/api/v1/ipxe/supported-os` 暴露定义集。单测覆盖
  Debian12/RHEL9 识别与残缺树拒绝。

### 2. initrd 脚本全量提取 ✅
- pkg2 initrd.img（441MB，LZMA 单层流）全量解压 → 1.35GB cpio、
  18,416 条目全盘点：`m9-evidence/initrd/`（initrd.cpio、
  decompress.log、INITRD-INVENTORY.md）。
- 288 个 shell 脚本逐份存档（rootfs_scripts/）——绝大多数是发行版
  stock（dracut/sysbench/pcp）；**原厂客户端逻辑不在脚本里，在三
  个二进制里**：pxe-agent（2.6MB，未混淆，Go 1.24.4，
  HrnrKitOS-Nexus，2,891 函数全盘点）、heartbeat（9.1MB，6,634
  函数全命名，heart.service 拉起）、loader（14.3MB，第三种
  pclntab 魔数 47aa574d，定性存档 8.8）。
- 附带盘点：rootfs 内 ssacli/OMSA/MFT 全套工具、/etc/deplos-release
  = DeplOS 1.3.3、root SSH 密钥对内置于镜像（仅记录存在性，未复
  制密钥材料）。
- 与我们 agent/firstboot 的对照结论写入 INITRD-INVENTORY.md（功能
  契约等价：报到+硬件扫描+任务执行；原厂三进程拆分不复刻）。

### 3. 前端整份提取 + 路由补齐 ✅
- 前端整份解出：591 个 embed 条目、499 文件（38 HTML 页 + 约 40
  应用 JS + noVNC 等）：`m9-evidence/frontend/`。应用 JS 为混淆
  构建，实调用 API 族由字符串片段重建（asset/clients/discovery/
  disk/gpu/locatord/mirror/orchestrator/org/roles/users/ipmi/vm/
  raid 等，清单见 ROUTE-DIFF.md）。
- 机械 diff：厂商 147 路由（132 唯一路径）vs 我们 55 条注册——
  125 条无同名注册，其中多数功能已有自有路径等价实现。
- 本轮补 7 条（前端/PXE 实调用 + 有真实子系统 backing，拒绝凑
  数）：server-time、supported-os、network-config、dashboard/
  summary、raid/topology（任务日志聚合）、license/import、
  win/install.bat（第 4 项）。逐项处置（含 vm/* 会话层缺失、
  business-systems 凭据面不复刻等定性）见 ROUTE-DIFF.md。

### 4. install.bat JIT 生成器复刻 ✅
- `internal/api/winpe_jit.go`：按资产渲染 install.bat——等网络
  （ping 目标）、`net use Z:`（UNC `\\%s\%s`）、setup.exe 定位、
  diskpart 擦盘脚本（X:\wipe_disk.txt）、decryptor 链
  （`-url/-key/-out` + 配置 URL `%s://%s/api/v1/ipxe/config?...`）
  全部片段逐字（证据：m8 strings 存档），连接行明确标注为我方
  重构；路由 /api/v1/ipxe/win/install.bat 已注册，错误语义对齐
  原厂（MAC required 400 / Task not found 404）。
- 红线：decryptor 只复刻格式；密钥为我方逐次下发值（查询参数/
  设置），片段中本就不存在原厂密钥常量，未复刻任何原厂密钥。

### 5. SSD 子轨细则逐条 ✅（含一处 M8 纠正）
- applySnicSSDRules（224B）/ applyHostSSDRules（736B）/
  applySSDRules 全反编译 + 全部字符串常量与扣分原型值逐字回填：
  `m9-evidence/ghidra/hwfault.apply*.c`、`m9-evidence/ssd/SSD-RULES.md`。
- 细则：Snic = 型号前缀 4 字符 "Snic" 分发，Reallocated>0 即扣 40；
  Host = ≥1001 扣 100（备用块接近耗尽）；增长升级：Δ≥4 扣 100、
  Δ<4 且连续≥3 次扣 80、Δ<4 且连续<3 次扣 10，Δ<1 计数清零。
  SSD 197/198 建议文案两档逐字恢复（92B/115B/65B）。
- **M9 纠正 M8**：M8 表中的「严重度码」实为建议字符串的字节长度
  （Go 字符串头参数）——187 规则建议恰 99 字节、183 恰 36 字节，
  逐一吻合。原厂 finding 无独立严重度码。我们代码 TrendFinding.
  Severity 已更名 Deduct 并存真实扣分，SSD 文案换逐字版，Snic/
  Host 双子轨已实现（TrendInput 新增 Model 字段）+ 新增单测
  TestSSDSubTracksM9。

### 6. netplan 模板切换 ✅
- 查明前提：我们此前**没有** netplan 生成点（装机网络全走 preseed
  netcfg），故本项按「采用原厂模板新建生成」结清：新增
  `internal/networkconfig`——原厂 283B 模板逐字渲染（ethernets/
  bonds 双形态、routes/nameservers 条件块与原厂一致）、落盘
  `/etc/netplan/99-deplos-<name>.yaml`、`netplan apply`；保留我方
  变量注入方式（类型化 Config、DNS 列表 ", " 拼接）。
- agent 新增 `net.config` 任务类型接线（netRunner 写文件）；
  server 侧 /api/v1/ipxe/network-config 可按主机下发同一 Config
  （format=json|yaml）。渲染/落盘路径单测全绿。

### 7. DB 71 表还原 diff ✅（sys_roles_v 定性不可还原）
- 二进制内 85 处 CREATE TABLE 提取，按表取最长版得 **71 表字段级
  DDL**（含 hosts；db-tables.txt 71 名中 70 张有 DDL）：
  `m9-evidence/db/vendor_ddl.json` + `diff.md`。
- **sys_roles_v 定性**：全二进制零 CREATE TABLE、零 CREATE VIEW
  ——非嵌入迁移创建（外部/遗留路径），字段级不可还原，不编造。
- 65 张缺表全数补迁移（`internal/db/m9_migrations.go`）：原厂 DDL
  为 PG 方言，归一化到本项目可移植子集（SERIAL→BIGINT、JSONB→
  TEXT、NOW()/gen_random_uuid() 默认值归一、分区表去分区子句、
  TIMESTAMPTZ→TIMESTAMP；一处二进制内最长版字符串越界污染
  （custom_scripts）以括号配平截断修复）。sqlite 全量迁移+测试
  通过；PG 走同一可移植子集（M5 已证双引擎同迁移集）。

## 第二梯队（硬核）

### 8. .hup 更新包解密 ✅ 完整成功
- 入口定位：settings.DecryptFile @0x1419d00（Ghidra 反编译，
  `m9-evidence/ghidra/settings.DecryptFile.c`）——栈上拼出 32 字节
  密钥常量，后接标准 AES-GCM Open 结构（NonceSize/Overhead 虚表
  调用 + 长度下限检查 + 0640 落盘）；utils.Decrypt @0x7ecc60 为
  同构的配置解密路径。
- 格式：整包 = nonce(12B) ‖ AES-256-GCM 密文+tag，密钥为编译期
  常量（明文见证据文档 HUP-FORMAT.md——属原厂随包出货常量，仅作
  逆向证据记录，**未进我们代码**）。
- 实解验证：GCM tag 校验通过，明文 1,020,667,323B = gzip → tar
  （1,484,288,000B），28 条目 = 完整 v1.3.5.1 发行包（server 二
  进制/core 双架构/agent 全套/release_info.json）；release_info
  给出应用映射（assets/core→/pxedata/config/pxeiso、assets/agent
  →/pxedata/files）与完整更新日志。工件：`m9-evidence/hup/`
  （hup_decrypted.bin、hup_contents.txt、HUP-FORMAT.md）。

### 9. agent 字符串解密 🔶 静态定性 + 动态恢复（协议面已全量对齐）
- **protector 定性 = mvdan/garble**（`m9-evidence/agent/
  AGENT-ANALYSIS.md`）：pclntab 魔数被改 e8916f30 + 72B 缩头；
  函数名全量改名（含 stdlib，如 sync/atomic.AytmdTCmV）；字面量
  逐点加密——掩蔽 32 个内嵌 ELF（50.77MB）后自有 .rodata 内
  "/api" 零命中、无任何明文 URL；buildinf 版本被抹。nameOff 仅
  约 2,183/14,282 可解析为良构名。
- **为何静态全量恢复不可行**：garble 无中央密钥/解密例程，每条
  字面量在使用点内联解密（逐点密钥材料），且标识符改名是单向
  的（二进制内不存原始名/种子）。这不是「再努力一下」的量级，
  是 protector 的构造性质；同批 collectors 二进制未加保护（标
  准魔数、go1.26.3 明文）可作对照。
- **动态恢复（实跑）**：以假服务器实跑该二进制，恢复其运行时
  契约全集（日志 `m9-evidence/agent/agent_http.log` 等）：CLI
  -server/-uuid；单端点 POST / 信封 {uuid, server_sn, ip,
  timestamp, data_type, data, agent_version="v1.0.1"}；五类上报
  BASIC/GPU/NETWORK/STORAGE/LOGS 的完整字段集；周期 GPU 2h、
  其余 30m；SN 经 dmidecode /dev/mem；工具走 PATH。160 秒观察窗
  内无独立任务轮询请求（下行应随上报响应或更长周期）。

## 我们实现因此新增/修正

- 新增包：internal/osimage（识别引擎+54 定义）、internal/
  networkconfig（原厂 netplan 模板）。
- 新增路由 7 条（见第 3 项）；新增 agent 任务类型 net.config。
- 新增迁移 65 张表（internal/db/m9_migrations.go）。
- 修正：scoring_trend.go——Severity 误标纠正为 Deduct、SSD 197/
  198 文案逐字化、补 Snic/Host 子轨；总目录 §6 同步纠正。
- 新增/扩充测试：osimage、networkconfig、winpe_jit（渲染逐字+
  路由错误语义）、m9_routes、SSD 子轨、db 迁移（既有套件覆盖）。

## 总目录与矩阵

- VALIDATED-DATA-CATALOG.md 终版 **89 条**（§1–§5 编号 40 + §6
  规则 30 + §7 驱动 11 + §8 M9 新增 8），各 M8 剩余项状态已刷新
  （1.4/4.3/5.1 翻 ✅、1.6 翻 🔶、5.2/5.4/5.5 补全提取）。
- FEATURE_MATRIX.md、README.md 已同步 M9 状态。

## 遗留（全部已定性，非尾巴）

- vm/* 20 条路由未补：缺 server 侧虚拟化会话管理层（virt 为客户
  端库），补壳即凑数——定性记录于 ROUTE-DIFF.md。
- loader（initrd）第三种 pclntab 魔数未深挖：不在本轮两硬核之
  列，已定性存档（目录 8.8）。
- 物理执行项（真卡/真机/授权 Windows 镜像）维持用户既定「环境
  限制封顶」口径，本轮为数据级对齐，不冒充真机验证。
