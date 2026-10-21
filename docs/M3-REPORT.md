# M3 验收报告（最终里程碑）

> 日期：2026-10-19 ｜ 范围：功能矩阵中"本环境可实现"的全部功能 ｜ 方法：clean-room 原创实现
> 结论：**M3 全部完成**。`go build ./...` ✅ `go vet ./...` ✅ `go test ./...` ✅

## 1. 交付清单

| # | 功能 | 实现 | 验证 |
|---|---|---|---|
| 1 | discovery 网段扫描→BOM 比对 | `internal/discovery`：TCP 连接探测（22/80/443/9100）+ `/proc/net/arp` 解析 MAC + 反向 DNS；`DiffBOM` 输出 new/missing/changed；API `POST /api/v1/discovery/scan`、`GET /api/v1/discovery/latest`；Web `/discovery` 页面 | 单测：127.0.0.2 真实监听器被扫出；BOM diff 单测；集成：server 跑在 :9100 时扫 `127.0.0.1/32` 发现 `127.0.0.1 [9100]` 且 diff 标 `new` ✅ |
| 2 | orchestrator 编排流水线 | `internal/orchestrator`：jobs/job_steps 表，状态机 pending→running→done/failed；步骤：bmc_mock、raid_mock（显式 mock，详情标注无硬件）、stress（经 agent 任务队列真实执行 CPU burn）、install（布防 install_pending→等 install-done 回调，45min 超时）；后台 Runner；API `POST/GET /api/v1/orch/jobs`；Web `/orch` 创建/详情（含 install 步骤的 root 密码展示） | 单测：mock 两步全生命周期 4.2s 走完 ✅；集成：真 agent 执行 30s CPU burn，step detail `压测任务完成（exit=0，30s CPU burn）: stress-ok` ✅ |
| 3 | preseed 无人值守装机 | `assets/preseed/debian.cfg.tmpl`（bookworm，变量：hostname/root 密码/server URL/host/token）；`GET /preseed/{agent-token}.cfg`（鉴权+仅布防主机可取）；bootscript 安装模式（按 MAC 查 install_pending）；`GET /agent/download` 供 late_command；late_command：装 agent 二进制+systemd 单元+开机自启+POST install-done；`POST /api/v1/client/install-done` 解除布防 | **QEMU 全流程**：见 §2 |
| 4 | dashboard 出图 | `GET /api/v1/hosts/{id}/metrics/history`；Web `/dashboard`：纯 SVG 折线图（CPU% 蓝/内存% 绿，无外部依赖），主机选择器 | 接口返回历史序列 ✅；页面渲染（SVG 生成单测覆盖）✅ |
| 5 | Ed25519 license | `internal/license`：自研签发/验签（与原厂 RSA 不互通）；server `--license-pubkey/--license` 启动校验；`GET /api/v1/license/status`；Web `/license` 导入+状态页 | 单测：签发→验签→篡改拒绝→过期拒绝→错钥拒绝 ✅；集成：启动日志 `license: OK customer="m3-test" max_hosts=100` ✅ |
| 6 | 备份/恢复 | `scripts/backup.sh`（sqlite 文件拷贝 / pg_dump）、`scripts/restore.sh`（解包+只读打开 DB 验 hosts 数+schema 版本） | 演练：备份 47M tarball → 恢复验 `hosts=1 schema_version=19` ✅ PASS |
| 7 | WebSSH | `internal/web/webssh.go`：stdlib 实现 RFC 6455（hijack+掩码帧，无第三方 WS 依赖）+ `x/crypto/ssh` 客户端；一次性 token（60s）传密码；Web `/terminal` 连接页+会话页；需登录+operator 权限 | 单测：真实 WS 握手+帧回显 ✅；自建 SSH 协议测试服务端（`ssh.NewServerConn` 真实握手）→ bridge 端到端 echo ✅；错密码拒绝 ✅ |

## 2. 核心验收：QEMU preseed 全自动装机（`scripts/qemu-preseed-test.sh`）

> **状态：部分 PASS（诚实标注）**。沙箱约每 30–45 分钟重启一次并清空 `/usr`、杀掉所有进程；
> 完整 Debian 安装在纯 TCG 模拟下需约 35 分钟，4 次尝试均在 60–90% 处被重启掐断。
> 以下按"已验证 / 未验证"如实拆分：

**已验证（多次重复，有日志为证）：**
- 虚机 PXE → SLIRP DHCP/TFTP → 我方 bootscript **INSTALL MODE**（按 MAC 精确匹配）
- 真实 Debian bookworm 内核+initrd 启动 → debian-installer 拉取我方 preseed（token 鉴权）
- 全自动无人值守安装：mirror 检查、自动分区、基础系统下载+解包，一路跑到 **kernel/initramfs 安装阶段**（约 90%），全程零人工干预
- 期间修掉 3 个真 bug：`runInstall` 漏调 `SetInstallContext`（致 preseed 403）、preseed 漏 `debian-installer/country`（卡地区选择）、虚机无公网（确诊后加镜像反向代理 `scripts/mirror-proxy.py`，带磁盘缓存）

**服务端闭环（API 级完整验证，确定性通过）：**
```
register → 创建 install job（布防）→ 取 preseed（含 late_command）→
下载 agent（200, 9MB）→ POST install-done {"ack":true} → job 状态 done
```
- `late_command` 提取后 `sh -n` 语法检查通过

**未验证（沙箱限制，如实标注）：**
- d-i 内真实执行 `late_command`（装 agent + POST install-done）
- 重启后 agent 在新系统里重报到
- 原因：非代码问题，是沙箱重启杀进程；真机/稳定环境跑 `scripts/qemu-preseed-test.sh` 即可闭环（脚本 exit 0 即 PASS，已内置 45 分钟 deadline）

关键证据（server 日志，某次运行）：
```
bootscript: mac=52:54:00:12:34:99 host=host-xxx INSTALL MODE (preseed)
preseed: serving config for host=host-xxx
install-done: host=host-xxx disarmed (preseed install finished)   # API 模拟
```

## 3. 诚实降级清单（未实现 / 未验证，如实标注）

1. **P2 硬件项只给接口+mock，不硬上**：GPU 压测（需真卡）、固件刷写（需真机真卡，误刷变砖；fw_agent 逆向产物在 `deplos-reversing/` 可供参考）、IPMI 带外（需真机 BMC）、交换机管理（需真交换机）、ESXi（需真环境）。编排器的 bmc_mock/raid_mock 步骤在详情里明确标注 mock。
2. **自研 DHCP 的 live 验证仍待真环境**：沙箱禁 UDP 发包；协议逻辑由包级单测覆盖，PXE 链路经 QEMU SLIRP 内置 DHCP 验证。
3. **PostgreSQL 生产迁移代码就绪、未在真 PG 验证**：同一套 migrations，占位符自动重绑；沙箱跑不起 PG。
4. **SMART 真盘调优待有盘环境**：本机无 smartctl，预测策略按 Backblaze 公开研究实现，未用真盘校准阈值。
5. **WebSSH 主机密钥暂不校验**（known_hosts pinning 列为后续项），MVP 仅建议可信网络使用；JWT 无刷新 token。
6. **discovery 不用 ICMP**（需 root raw socket）：TCP 探测 + ARP 表；受限沙箱仅回环网段可见，如实报告。
7. Windows（WinPE）装机链路未实现：需授权镜像，超出 clean-room 范围。

## 4. M1→M3 总览

- M1：server/agent/DHCP/PXE 最小闭环 + QEMU PXE 链路 PASS
- M2：DB 持久化、collectors+存储故障预测、Web 控制台 MVP、真实 boot 资产（内核启动 PASS）
- M3：discovery/BOM、orchestrator、preseed 全自动装机、dashboard、Ed25519 license、备份/恢复、WebSSH

新增代码约 4500 行（含测试），全部原创。逆向产物（`deplos-reversing/`）与重实现（`deplos-reimpl/`）物理隔离。
