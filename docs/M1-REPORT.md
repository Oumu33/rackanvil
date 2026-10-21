# M1 验收报告：最小可用闭环

日期：2026-10-18 ｜ 目标：server↔agent 真实联调 + PXE 链路验证

## 交付的代码（全部原创，clean-room）

| 文件 | 行数 | 说明 |
|---|---|---|
| `internal/store/store.go` | ~230 | 内存数据层：Host/Task 注册、心跳、任务队列、token 鉴权 |
| `internal/api/server.go` | ~250 | HTTP API：`/healthz`、`/api/v1/client/initialize`、`/api/v1/client/checkin`、`/api/v1/client/report`、`/api/v1/ipxe/bootscript`、`/ipxe/{vmlinuz,initrd.img}`（501 诚实占位）、`/debug/*` 测试钩 |
| `internal/pxe/dhcp.go` | ~430 | 自研 DHCP 服务器（RFC 2131）：DISCOVER→OFFER、REQUEST→ACK/NAK、租约池、option 93 按 BIOS/UEFI 选 bootfile、option 60 识别 iPXE 直接 chain HTTP bootscript、SO_BROADCAST 广播回复 |
| `internal/agent/agent.go` | ~220 | agent：initialize 注册（含 hostname/MAC/IP 采集）、checkin 心跳（带重试）、shell 任务执行（超时、stdout/stderr/退出码捕获）、report 回传 |
| `cmd/server/main.go` | ~60 | 入口：flags（-addr/-public-url/-dhcp/-dhcp-server-ip/-dhcp-pool-*） |
| `cmd/agent/main.go` | ~40 | 入口：flags（-server/-interval），信号优雅退出 |
| `internal/pxe/dhcp_test.go` | ~170 | 包级单测：BIOS/UEFI/iPXE 三种 bootfile 选择、REQUEST→ACK、错误 IP→NAK、地址池耗尽 |
| `internal/api/server_test.go` | ~150 | API 单测：initialize→enqueue→checkin 取任务→report→落库全链路、鉴权 401、bootscript 内容断言 |
| `scripts/m1-e2e.sh` | ~90 | 真实双进程联调脚本 |
| `scripts/qemu-pxe-test.sh` | ~90 | QEMU PXE 验证脚本（含环境预检） |

`go build ./...` ✅　`go vet ./...` ✅　`go test ./...` ✅（见下）

## 验证证据

### 1. server↔agent 真实进程联调 ✅ PASS

`scripts/m1-e2e.sh`：起 server（:19100）+ agent（真进程，2s 心跳）→ initialize → debug 下发 `echo hello-m1 && hostname` → agent 执行 → report → 校验落库。

关键日志（`--` 分隔 server/agent）：

```
server: initialize: host=htch-runtime id=host-0c4f5f836ca1d914 macs=[72:dc:7c:34:2f:fd] ips=[198.19.0.2 ...]
server: debug: enqueued task=task-e74505168c62d140 for host=host-0c4f5f836ca1d914 cmd="echo hello-m1 && hostname"
server: checkin: host=host-0c4f5f836ca1d914 tasks=1
server: report: host=host-0c4f5f836ca1d914 task=task-e74505168c62d140 status=done exit=0
---
agent: initialize: host_id=host-0c4f5f836ca1d914 hostname resolved, token acquired
agent: checkin ok: 1 task(s)
agent: task: executing task-e74505168c62d140 type=shell cmd="echo hello-m1 && hostname"
agent: task: task-e74505168c62d140 finished status=done exit=0
```

任务落库校验：`/debug/tasks` 返回 `status=done` 且 stdout 含 `hello-m1`。bootscript 接口同步验证通过（`#!ipxe` + 按 MAC 渲染 + kernel/initrd URL）。

途中修了一个真 bug：agent 启动时若 server 未就绪，initialize 只试一次就退出；已加 2s 退避重试（真实 PXE/initrd 场景同样需要）。

### 2. 单元测试 ✅ 全绿

```
ok  deplos-reimpl/internal/api
ok  deplos-reimpl/internal/pxe
```

DHCP 包级：BIOS→`undionly.kpxe`、UEFI(arch 7)→`ipxe.efi`、iPXE(vendor class)→`http://.../api/v1/ipxe/bootscript?mac=...`、REQUEST 正确 IP→ACK、错误 IP→NAK、地址池耗尽报错。

### 3. QEMU PXE live 验证 ✅ PASS（2026-10-19 修复）

此前降级原因：本沙箱禁止一切 UDP 发包（`sendto` 直接 `EPERM`），自研 DHCP 基于 UDP 广播，物理上跑不起来。

**修复方案**：改用 QEMU 用户态网络（SLIRP）的内置 DHCP+TFTP——DHCP/TFTP 全在 QEMU 进程内部完成，不需要宿主机 UDP socket：
- QEMU 参数：`-netdev user,id=n0,tftp=<tftpdir>,bootfile=boot.ipxe -device e1000,netdev=n0,mac=52:54:00:12:34:56,romfile=/usr/lib/ipxe/e1000_82540.rom -boot order=n`（无 KVM，纯软模拟）
- `<tftpdir>/boot.ipxe`：`#!ipxe` + `chain http://10.0.2.2:9100/api/v1/ipxe/bootscript?mac=${mac}`（10.0.2.2 是 SLIRP 里宿主机的地址，QEMU 转发到宿主机 127.0.0.1:9100，loopback TCP 沙箱允许）

**验证证据**（`scripts/qemu-pxe-test.sh`，断言基于 server 真实日志，无伪造）：
```
2026/10/19 00:56:22 bootscript: mac=52:54:00:12:34:56 from=127.0.0.1:50416
PASS: QEMU guest PXE -> SLIRP DHCP/TFTP -> our bootscript (guest MAC 52:54:00:12:34:56)
```
链路实锤：guest iPXE ROM → SLIRP DHCP → SLIRP TFTP 取 boot.ipxe → HTTP chain 我方 bootscript（server 日志出现 guest MAC 的 bootscript 请求，`from=127.0.0.1` 即 SLIRP 转发）。纯软模拟 18 秒跑完全链路。

**诚实边界**：SLIRP 用的是 QEMU 内置 DHCP，自研 DHCP（`internal/pxe`）的 live 验证仍待真环境；其协议逻辑由包级单测全覆盖。bootscript 的 kernel 拉取仍 501（M2 provisioning boot 资产后继续）。

### 4. 诚实声明的降级项

1. DHCP live 验证：SLIRP 链路已 PASS（上）；自研 DHCP（`internal/pxe`）的 live 验证待真环境，协议逻辑由包级单测全覆盖。
2. `/ipxe/vmlinuz`、`/ipxe/initrd.img`：返回 501 + 明文说明（boot 资产 M2 provisioning）。
3. 数据层：内存 map（M2 接 PostgreSQL，表结构见 `deplos-reversing/db-tables.txt` 71 张表）。
4. 认证：M1 简单 token；JWT+bcrypt 随 Web 控制台里程碑。
5. agent 只实现 shell 任务类型；CPU/内存采集待 collectors（M2）。

## M2 建议（按优先级）

1. **PostgreSQL 接入**：`internal/db` migrations，按 db-tables.txt 建 `hosts`、`asset_hardware_info`、`orchestration_jobs` 等核心表；store 接口保持不变做切换。
2. **真环境验证自研 DHCP（`internal/pxe`）的 live 链路**（QEMU SLIRP 链路已 PASS，见上）。
3. **boot 资产 provisioning**：`scripts/` 下做最小 initrd（或复用发行版 netboot 内核），让 bootscript 的 kernel/initrd 真正可拉取；M1 的验收标准推进到"虚机 PXE 进内核"。
4. **collectors**：CPU/内存/磁盘/SMART 采集 + `server_monitor` 入库（FEATURE_MATRIX #6，含存储故障预测）。
5. **Web 控制台 + JWT/bcrypt + RBAC**（FEATURE_MATRIX #4）。
