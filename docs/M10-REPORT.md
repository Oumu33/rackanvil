# M10 报告 —「扫尾全盘点」（台账清零）

日期：2026-10-21。触发：用户直觉「感觉没有扒完所有验证过的数据」。
本轮先建文件级覆盖台账把「所有」定义清楚，再按台账逐项清零。

## 1. 台账验收（本轮核心交付）

`~/workspace/deplos-reversing/COMPLETENESS-LEDGER.md`（生成器
`m10-evidence/gen_ledger.py` + 状态覆写表 `status_overrides.tsv`，
可复跑）：枚举全部数据源每一个文件——安装 tar 全量解压 38 文件、
双架构 .hup 解密包各 19 文件、双架构 initrd rootfs 全清单
（amd64 18,415 + arm64 23,583 条目）、server 双架构 go:embed 全集、
GitHub 仓库全文件、厂商文档包 23 篇+74 图。

**终版统计：合计 42,830 项 — ✅1,036 / ⛔41,794 / 🔶0 / 👁0。**

⛔ 原因分类：上游发行版文件（Rocky 8.10 / openEuler 24.03 的库、
locale、固件、Python 库等，约 4.1 万）｜第三方工具实体（govc/
ipmitool/rclone/shellcheck/PG/Redis/VMRC，版本已盘点）｜第三方
运行库与样本数据（CUDA 库、GeoIP、字体等）｜红线项（root SSH
私钥/公钥仅记存在性、明文密码模板不复刻）｜定性不可挖
（client_* garble 压测族静态不可恢复、sys_roles_v 无定义、
deplosData 全源不存在）。

## 2. 本轮新爆出的关键数据（精选）

1. **.hup 增量有且仅有一条**：四个 server 构建的 619 个内嵌载荷
   presence 逐字验证，仅 `templates/pxe-first-boot.sh.tmpl` 不同
   ——新构建新增「等待网络就绪 ≤120 秒（server-time 探测、失败
   上报 network_ready 并终止）」整段 + 配置脚本下载改 3 次重试。
2. **server 双构建同标 1.3.5.1**：安装包 = commit 4ab8b303
   （2026-07-15），更新包 = commit 7b1eb25d（2026-05-06）；
   更正 M9「same build」说法（HUP-FORMAT.md 已加更正注）。
   新构建另多 `clientinit.normalizeMAC` 函数。
3. **47aa574d 是 garble 构建家族而非独立 protector**（推翻 M9
   定性）：72B 头变体、entryOff 改写、nameOff/functab 完好；
   函数表可 100% 恢复——loader 12,346/12,346、network-config
   双架构 8 件全恢复，方法已固化为脚本。loader 职责定性为
   PXE initrd 一次性核心初始化（oneshot/600s/Before=heart）。
4. **heartbeat 是第二套独立协议**：SignedHeartbeat 14 字段、
   runtime_salt 64 字符 hex 熵校验、Rust core_secure 签名、
   配置走 /proc/cmdline（app.server/BOOT_MAC）；与装机后 agent
   协议互不相干；心跳不含性能指标。
5. **collectors 不带规则**：disk.json 在双架构 collectors 中
   0/93 行命中——评分规则只在 server 侧；另其校验文件
   `collectors_checksums.txt` 的 sha 在三个分发中全对不上实物
   （陈旧工件）。
6. **deplosData 不存在**：全源（含 1.3G initrd 全文、双架构包、
   GitHub、厂商文档）三关键词零命中；驱动为运维侧另行分发，
   产品包只留 /pxedata 运行时挂载点。M4/M9 的「驱动目录待挖」
   就此定性结清。
7. **initrd 双基底**：amd64 = Rocky 8.10（deplos-release 还写着
   1.3.3），arm64 = openEuler 24.03 SP3 全面改标 DeplOS 1.3.5；
   arm64 无 pxe-agent/sysinfo/OMSA/ssacli（厂商工具生态缺口），
   heart/loader 服务单元双架构逐字相同。
8. **内核选型重构**：双架构无 IKCONFIG；ext4/xfs=y、btrfs 是
   唯一 FS 差异（amd64=m / arm64=y）；NVIDIA 580.126.09 树外
   模块双架构同版；rclone 双架构版本不一致（v1.73.2/v1.73.3）。

总目录 VALIDATED-DATA-CATALOG.md 终版 **109 条**（M9 时 89 条，
M10 新增 §9 共 20 条）。

## 3. 新增代码对齐（pxe-agent 缺口，bounded）

按 PXE-AGENT-DATA.md 对齐表（原厂有我们无的 8 项）评估后落地 4 项：

- `internal/collectors/pxealign.go`（原创实现，厂商命令/输出格式
  已实锤）：`ParseLsblkJSON`（lsblk -J 分区树）、
  `ParseStorcliDrives`（storcli 文本物理盘表 EID:Slt/DID/State，
  含 DG=- 的 UBad 行）、`ParseSas3ircuDisplay`（sas3ircu DISPLAY
  硬盘段，跳过 enclosure/volume 段）、`WipeSteps`（厂商擦盘
  序列构建器：dd 盘头 100MB+盘尾、wipefs、swapoff、vgchange
  -an、mdadm --stop --scan、partprobe，顺序逐字对齐）。
- agent 新增 `disk.wipe` 任务：confirm=true 门控 + 整盘设备名
  白名单（拒绝分区与任意路径），按 WipeSteps 顺序执行。
- 单测：3 份 SYNTHETIC fixture（按厂商已验证输出格式建模）+
  4 个测试函数，collectors 包全绿。

未落地（记为差异，不硬凑）：raidconfig 执行通道（退出码 67
语义；我们的 raid 包走 storcli 直调，M7 已定性）、titan_tools
回退路径（厂商私有工具目录）、sysinfo 中文速览脚本（已逐字
存档于证据，属运维便利脚本非数据契约）、装机前健康门禁
（我们有 deephealth 等价面，判定模型不同：原厂为三态字符串、
我们为评分制——按 M7/M8 已对齐的评分轨为准，不回退）。

pxe-first-boot 模板增量（9.8）未做代码对齐：我们的装机链没有
first-boot 脚本工件，该阶段由 agent 注册 + net.config 任务
承担；增量数据本身已入总目录存档。

## 4. 终验

- 台账：🔶0 / 👁0（见 §1）。
- `go build ./... && go vet ./... && go test -count=1 ./...`：
  19 个测试包全绿（本轮终验重跑）。
- FEATURE_MATRIX.md、README 已同步 M10 状态。

## 5. 遗留定性项（非欠账，均为已定性关闭）

- client_* 压测族 8 件：garble 全保护，静态不可恢复；服务端
  点火/参数/结果通道已在总目录 1.10/1.11 与本轮 9.5 对齐。
- loader/heartbeat 字面量：garble 加密未静态提取；loader 职责
  已定性、heartbeat 已动态恢复协议全貌。
- sys_roles_v：全二进制零定义，不可还原（M9 定性，M10 台账
  复核维持）。
- 固件真刷/真 BMC/真交换机：纯物理项，美国云主机环境限制，
  按用户 2026-10-20 口径视为该环境下完成。
