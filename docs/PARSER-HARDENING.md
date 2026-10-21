# 解析器硬化报告（PARSER-HARDENING）

日期：2026-10-20
目标：用公开渠道的真实命令输出样本，把硬件解析器练到"见过真格式"。

## 一、样本清单（14 个：8 真实 / 6 合成）

### 真实样本（verbatim，文件头注明来源 URL）

| # | 文件 | 命令 | 来源 |
|---|---|---|---|
| 1 | `internal/collectors/testdata/smartctl-sata-hdd-failing.txt` | `smartctl -a`（SATA HDD） | https://blog.toadjaune.eu/posts/2021/06-14_btrfs_and_data_corruption/ — Seagate ST500LM021-1KJ152 真坏盘：187=59352、197/198=16，但 overall-health 显示 PASSED（绝佳回归用例） |
| 2 | `internal/collectors/testdata/nvme-smart-log.txt` | `nvme smart-log`（文本） | https://docs.hetzner.com/robot/dedicated-server/maintenance/nvme/ — Hetzner 官方文档真机输出 |
| 3 | `internal/collectors/testdata/ipmitool-sensor.txt` | `ipmitool sensor` | https://forums.servethehome.com/index.php?goto/post&id=99882 — 含阈值列与 `na` 缺失值 |
| 4 | `internal/collectors/testdata/ipmitool-sdr.txt` | `ipmitool sdr elist full` | https://docs.oracle.com/cd/E19121-01/sf.x4100/819-3284-17/819-3284-17IMPI.html — Oracle 官方 ILOM 文档 |
| 5 | `internal/raid/testdata/storcli-show-all.json` | `storcli /cALL show all J` | https://github.com/prometheus-community/node-exporter-textfile-collector-scripts/tree/master/mock/fixtures — 真机采集的 fixture（PRAID EP420i） |
| 6 | `internal/raid/testdata/storcli-eall-sall-show-all.json` | `storcli /cALL/eALL/sALL show all J` | 同上 |
| 7 | `internal/raid/testdata/storcli-c0-show.txt` | `storcli64 /c0 show`（文本） | https://www.h3c.com/en/d_202310/1955075_294551_0.htm — H3C 官方文档（AVAGO MegaRAID 9361-8i，SN SK64909030） |
| 8 | `internal/raid/testdata/storcli-vall-show.txt` | `storcli64 /c0/dall show all` VD LIST（文本） | https://aerospike.com/docs/database/8.0.0/manage/planning/ssd/lsi-megacli — Aerospike 官方文档 |

### 合成样本（文件头大字标注 `SYNTHETIC - 格式仿真，非真实采集`）

| # | 文件 | 说明 |
|---|---|---|
| 9 | `internal/collectors/testdata/smartctl-sata-hdd.json` | smartmontools 官方 `--json` schema（稳定格式），取值派生自样本 1 的真实文本 |
| 10 | `internal/collectors/testdata/smartctl-nvme.json` | smartmontools NVMe `--json` schema，温度为 Kelvin（302K=29°C），取值派生自样本 2 |
| 11 | `internal/collectors/testdata/nvme-cli-smart-log.json` | nvme-cli 官方 `-o json` schema（键名与 smartctl 不同：`percent_used` 等） |
| 12 | `internal/collectors/testdata/smartctl-health-pass.txt` / `smartctl-health-fail.txt` | `smartctl -H` 文档化格式；FAILED 版用于"必须报 critical"回归 |
| 13 | `internal/firmware/testdata/lspci-nn.txt` | `lspci -nn -D` 文档化格式；PCI ID 均为公开真实 ID（1000:005d=9361-8i 等，见 H3C 样本 7 的 Vendor/Device Id 字段佐证） |

未找到独立真实样本、故用合成的项：`smartctl --json`（schema 稳定、文档化）、`nvme-cli -o json`（同上）、`smartctl -H` FAILED（格式仅 1 行、文档化）、`lspci -nn`（输出未在公开渠道找到含 MegaRAID 的完整粘贴，用公开 PCI ID 数据库拼装并明确标注）。

## 二、修掉的解析 bug（7 个）

1. **`raid.Controllers()` 解析了一个不存在的 JSON 结构**（最严重）。
   旧代码假设 `storcli show J` 的 `Response Data` 按 controller id 分键（`{"0":{"Controller":"0","Model":"..."}}`）。
   真实 `storcli /cALL show all J` 的结构是 `Response Data.Basics={Controller,Model}` + `VD LIST`/`PD LIST` 数组。
   修法：新增 `ParseShowAll` 按真实结构解析；`Controllers()`/`ListVDs()` 改调 `/cALL show all J`；新增 `ListPDs()`（`PhysicalDrive` 结构体之前存在但从未被填充）。
2. **`smartctlJSON.Raw.Value int64` 写死**：真实 smartctl JSON 的 raw 可能是字符串或缺失，旧结构体直接 unmarshal 失败/丢数据。
   修法：`smartRawValue` 兼容 number/string/absent 三种形态；`ParseSmartctlJSON` 独立可测。
3. **`smart_status.passed` 解析了但从未使用**：overall-health FAILED 的盘不会被检出。
   修法：`Snapshot.SmartHealth[dev]` 落盘；`AnalyzeSmart(..., healthPassed)` 在 FAILED 时直接 critical；`server.go` 里"未知≠失败"（缺字段时不误报）。
4. **NVMe 温度"采而不用"+厂商 1739°C 同类坑**：smartctl JSON 的 Kelvin 温度只存不用；nvme-cli fallback 分支漏解析 temperature；缺失值无"未知"处理。
   修法：三路温度统一为 Kelvin 入库；新增 `NvmeTempCelsius` —— 缺失/0/物理不可能值（<230K 或 >400K）一律返回 `ok=false`，调用方必须显示"未知"，绝不编造四位数摄氏度。专项回归测试 `TestNvmeTempUnknown` 覆盖 missing/zero/2012K（即 1739°C 级别）/过低四种情形。
5. **缺失的文本模式解析器**：旧代码只有 `--json` 路径，老版本 smartctl / 无 JSON 时直接跳过。
   修法：新增 `ParseSmartctlText`（属性表：处理 `42 (Min/Max 36/52)`、`21892 (3 79 0)` 等后缀；health 行；Device Model/Serial Number）、`ParseNvmeText`（处理 `29 C` / `43°C (316 Kelvin)` / `35%` / `113,446` 四种真实变体）、`ParseNvmeJSON`（nvme-cli 键名重映射）、`ParseIpmitoolSensor`（`sensor` 10 列 / `sdr` 3 列 / `sdr elist full` 5 列三种布局，`na` 值原样保留不丢行）。
6. **storcli 文本模式无 fallback**：新增 `ParseStorcliShowText`（`Product Name =`/`Serial Number =`）与 `ParseStorcliVDListText`（VD LIST 表格，含末尾 Name 列可选的情况）。
7. **`firmware.classifyLine` 纯关键词匹配**：本地化/驱动改名即失效。
   修法：先匹配 PCI ID（`[1000:005d]` 等 13 个公开 ID 表），关键词降级为 fallback。

## 三、SMART 阈值复核结论（对照 Backblaze 公开博客）

Backblaze 公开分析（年度 Drive Stats +《What SMART Stats Tell Us》系列）确认 **5/187/188/197/198** 五个属性与故障强相关，但**从未发布硬阈值**（他们的方法是机群 AFR 相关性分析，不是单值门限）；Google 2007 年 FAST 论文结论方向一致。

| 属性 | 我们的阈值 | 结论 |
|---|---|---|
| 187 Reported_Uncorrect | >0 → critical | 保持。Backblaze 列为强预测因子；任何非零都值得 critical |
| 197 Current_Pending | ≥10 critical / >0 warning | 保持。分层合理 |
| 198 Offline_Uncorrectable | >0 → warning | 保持。单独出现时多为可恢复场景，warning + 趋势检测兜底 |
| 5 Reallocated_Sector_Ct | ≥50 critical / >0 warning | 保持。Backblaze 最强单因子；50 是保守的"换盘"线 |
| 188 Command_Timeout | >0 → warning | 保持。方向正确 |
| 趋势检测 | 连续 3 样本严格递增 → 预警 | 保持。这是 Backblaze 方法论在本机的近似实现 |

**结论：阈值不动。** Backblaze 没有发布可直接套用的硬阈值，我们的阈值方向与他们的相关性结论一致，且偏保守（宁可误报不漏报）。如实记录，未做"看起来更准"的微调。

## 四、ARM QEMU 结果：成功

- `apt-get install -y qemu-system-arm` 一次成功（之前超时是 apt 锁冲突，重试即好），QEMU 8.2.2，自带 `qemu-system-aarch64`。
- **bootscript arch 感知验证通过**（新增 `internal/api/bootscript_arch_test.go`）：`arch=aarch64` 返回 `/ipxe/arm64/linux` + `initrd.gz` + `ttyAMA0`；默认返回 x86_64 + `ttyS0`。中途发现测试断言写错（实际 URL 是 `/ipxe/arm64/linux` 而非 `/ipxe/debian-bookworm-arm64/linux`），已修正——代码本身是对的。
- **真机启动验证**：`qemu-system-aarch64 -M virt -cpu cortex-a72` 直接加载 `assets/debian-bookworm-arm64/linux` + `initrd.gz`，内核成功启动（`Booting Linux on physical CPU`，Debian 6.1.0-50-arm64）。arm64 netboot 资产在 aarch64 虚机上可工作。

## 五、验证状态

`go build ./...`、`go vet ./...`、`go test ./...` 全绿（17 个包）。
新增测试：`collectors/fixture_test.go`（10 个）、`raid/fixture_test.go`（4 个）、`firmware/fixture_test.go`（1 个）、`api/bootscript_arch_test.go`（1 个）。
