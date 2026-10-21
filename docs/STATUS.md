# RackAnvil（DeplOS clean-room 重实现）— 项目状态总账

> 改名注记（2026-10-21）：产品定名 **RackAnvil**，GitHub 仓库为 `Oumu33/rackanvil`（Go module `github.com/Oumu33/rackanvil`、二进制 rackanvil-server/rackanvil-agent）。开发期间的工作名 `deplos-reimpl` 已弃用；M1–M12 历史报告中出现的旧名是当时的史实记录，不回改。本地工作目录名保持不变（与 deplos-reversing 证据互引不断）。

**更新时间：2026-10-21 21:20 CST**（每次里程碑后刷新本文件）

## 一句话状态

功能 26/26 全部实现且云环境验证封顶；原厂验证数据已全量扒完（台账 42,830 项清零、总目录 109 条）；原厂 12 条实锤短板已全部对账（7 已修/已避、1 部分处置、4 于 M11 新修）；**M12 产品化完成：自有版本 v1.0.0 双架构发布包已构建并通过从零实测，部署文档与使用手册（8 张控制台实拍）已交付**；剩余只有纯物理项（等真实机房硬件）。

## 原厂基线

- 目标产品：hrnr27/DeplOS v1.3.5.1（数据中心裸金属自动化运维平台）。
- 2026-10-21 复查 GitHub：仓库自 2026-07-19 后只有 README 提交、无新 release/tag，**v1.3.5.1 仍是最新版，对齐基线无需变动**。
- 注意：原厂同标 1.3.5.1 存在双构建（安装包 commit 4ab8b303 / 更新包 commit 7b1eb25d），我们的数据对齐以两构建并集为准（M10 已核）。

## 里程碑全表

| 轮次 | 内容 | 状态 |
|---|---|---|
| M1 | server/agent/DHCP + QEMU PXE live | ✅ |
| M2 | DB 持久化、采集+存储故障预测、Web 控制台、Debian 真实引导资产 | ✅ |
| M3 | 发现扫描、编排、拓扑、Ed25519 授权、备份恢复、WebSSH、压测 | ✅ |
| M4 | 厂商 23 篇文章驱动补齐 8 项（点灯/配置存档恢复/拓扑/代理节点/ARM/固件/WinPE/RAID）+ ProxyDHCP | ✅ |
| 解析器硬化 | 14 个真机样本修 7 个解析 bug（含 storcli JSON 重写、NVMe 温度防护） | ✅ |
| M5 | PostgreSQL 真迁移、交换机驱动、虚拟化（vcsim 真连）、云主机全真 e2e | ✅ |
| M6 | ARM PXE、自研 DHCP live、preseed 装后闭环三项全 PASS；抓修 late_command 丢文件产品 bug | ✅ |
| M7 | 原厂数据对齐：SMART 评分规则 JSON 全量提取、固件 8 handler 对齐、交换机 8 家 | ✅ |
| M8 | 深挖翻案：winpeshl/unattend/SMB、评分聚合公式、交换机注册表全集 9+1、SAS3008 IR/IT、LPE1250 传递机制；总目录 60 条 | ✅ |
| M9 | definitions 54 份、initrd 全解、前端提取+补 7 路由、install.bat JIT、DB 71 表还原；.hup AES-256-GCM 整包解密成功；agent 定性 garble+动态恢复协议；总目录 89 条 | ✅ |
| M10 | 文件级完整度台账 42,830 项清零（✅1,036/⛔41,794/半成品 0）；loader 函数表 100% 恢复、heartbeat 第二套协议、rackanvilData 定性不存在、initrd 双基底；总目录 109 条 | ✅ |
| M11 | 优化轮：原厂短板清单 12 条全对账；落地 agent 协议签名、first-boot 网络就绪、资产 sha256 校验、评分文案一致性、proxmox 定义停用 5 项优化 | ✅ |
| M12 | 产品化：v1.0.0 双架构发布包（release.sh、SHA256SUMS 实物生成、install/upgrade 脚本）+ 从零实测全通 + DEPLOY.md/USER-GUIDE.md（8 张自有控制台实拍） | ✅ |

## 当前家底（2026-10-21 实测）

- 代码：`~/workspace/deplos-reimpl/`，Go 约 1.9 万行（含测试）；`go build/vet/test -count=1` **21 个测试包全绿**。
- 发布包：`dist/rackanvil_1.0.0_linux_amd64.tar.gz`（107 MB）与 `..._linux_arm64.tar.gz`（106 MB），由 `scripts/release.sh` 可复跑生成，`dist/SHA256SUMS` 对实物生成；从零实测日志 `docs/m12-evidence/e2e.log`，验收 `docs/M12-REPORT.md`。
- 功能矩阵：`~/workspace/deplos-reversing/FEATURE_MATRIX.md`，26/26。
- 验证数据总目录：`~/workspace/deplos-reversing/VALIDATED-DATA-CATALOG.md`，109 条，全部带来源函数/证据文件。
- 完整度台账：`~/workspace/deplos-reversing/COMPLETENESS-LEDGER.md`，42,830 项无半成品格。
- 证据库：`~/workspace/deplos-reversing/m7/m8/m9/m10-evidence/`。

## 剩余项（全部已定性，非缺口）

- **纯物理执行**（按用户定调「环境限制封顶」）：真 LED 点灯、RAID 真卡执行、固件真刷、真 BMC、GPU 真卡、WinPE 授权镜像、SMART 真盘校准——代码就绪，等真实机房环境。
- **按红线不做**：原厂硬编码凭据/密钥不进我们代码（仅存证据文档）；autounattend 明文密码不复刻；business-systems 凭据面不复刻；root SSH 私钥仅记存在性。
- **定性不可还原**：`sys_roles_v`（全二进制零定义）；client_* garble 压测族静态明文（动态协议已恢复）；loader 字面量（函数表已全恢复）。
- **M11 明确不做（附取舍）**：install-done 签名、iPXE imgverify、Ed25519 替代 HMAC、preseed 口令加密传输，理由见 docs/M11-REPORT.md §4。

## 下一步候选（待用户拍板）

1. 有真实机房/硬件时：物理项逐项实测销账。
2. 原厂出新版本时：按 M10 台账流程做增量 diff 对齐。
3. ~~产品化方向：打包发布、部署文档、使用手册~~ —— M12 已完成（v1.0.0）。后续可做：用户管理/改密码页面（v1.0.0 已知缺口）、SQLite→PG 数据搬迁工具。
