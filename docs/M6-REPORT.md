# M6 验收报告：M5 未闭环 D/E/F 三项收尾（2026-10-20）

结论：**E ✅ PASS / F ✅ PASS / D ✅ PASS**。三项全部闭环，每项附原始证据（串口行、抓包字段、server 日志行）。过程全记录在 `docs/M6-progress.md`（含翻车与根因迭代）。

---

## E. ARM PXE ✅ PASS

**断点（M5）**：AAVMF 固件经我方 server 拉走 arm64 内核+grubaa64.efi，Debian GRUB 2.06 在 guest 真跑起来，但落空 prompt 未加载 grub.cfg。

**根因**：① Debian GRUB 经 shim 链加载后 prefix 无从推导，零配置请求直接落空 prompt；② 当时能拉到的 grub.cfg 还是 M5 静态文件，写死了已死的 :19112 代理端口（隐藏坑）。

**打法**：放弃 shim+Debian GRUB 的配置发现链。用 grub-mkimage 自制 grubaa64.efi，把配置**嵌入镜像**（启动即 `net_bootp` → linux/initrd 走 HTTP 拉我方 arm64 资产 → boot）；AAVMF HTTP Boot 直拉（Secure Boot 关）。复跑脚本 `scripts/qemu-arm64-grub-test.sh`。

**服务端配套改动（进代码+测试）**：
- `-log-requests` 全量请求日志中间件（原始 RequestURI + 状态 + 字节数；statusWriter 保留 Hijack/Flush 供 WebSSH）；
- 路径双斜杠归一化（消灭 shim 不跟随的 307）；
- grub.cfg 改为按 serverURL **动态渲染**（`renderGRUBCfg`），不再写死端口；
- 补 4 条 GRUB 探测路由（/grub.cfg、/EFI/BOOT/grub.cfg、/boot/grub/grub.cfg、/ipxe/grub.cfg）。新增 `internal/api/m6_test.go`。

**原始证据**（server 请求日志 `.m6-armtest/logs/`，2026-10-20 11:51 UTC）：

```
11:51:23 HEAD/GET /ipxe/arm64/grubaa64.efi -> 200 684032B
11:51:27 GET /ipxe/arm64/linux -> 200 32956352B
11:51:30 GET /ipxe/arm64/initrd.gz -> 200 40147331B
```

guest 串口：

```
Linux version 6.1.0-50-arm64 (debian-kernel@lists.debian.org) ... #1 SMP Debian 6.1.176-1
```

## F. 自研 DHCP live ✅ PASS

**断点（M5）**：隧道网段已实录 DISCOVER→OFFER，但 VM-B 未发 REQUEST；且 selectBootFile 未命中 iPXE 分支。

**拓扑**：两台 QEMU 用 `-netdev socket` TCP listen/connect（127.0.0.1:41006）组网；VM-A 极简 initrd 跑真 server `-dhcp`（192.168.100.1，池 .100–.200，HTTP :19110）；VM-B 用 iPXE ROM（e1000_82540.rom）PXE 启动。抓包证人：VM-A 内 AF_PACKET 全量解 DHCP 帧的 snoop 程序先行启动、再 exec server（`.f-test/`，运行脚本 `.f-test/run-full2.sh`）。

**三层根因（逐层抓包定版，全部修死，进 `internal/pxe/dhcp.go` + 单测）**：
1. **iPXE 识别**：ROM 阶段 DISCOVER 的 opt60 = `"PXEClient:Arch:00000:UNDI:002001"`（不是字面 "iPXE"），但 opt77="iPXE"、opt175 存在。识别改三路冗余：opt175 存在 / opt77 含 ipxe / opt60 含 ipxe（`isIPXEClient`）；未命中的后果是 OFFER 错发 undionly.kpxe 而非 bootscript URL。
2. **回包地址**：旧逻辑把 OFFER 单播到 yiaddr，yiaddr 尚无 ARP 表项、被内核吞掉（线上 67→68 帧数=0 且无 write 报错）。改为 ciaddr 为零一律广播、仅续租单播（`replyAddr`）。同时 `buildReply` 补 BOOTP **file 字段**与 opt67 双写（老 PXE ROM 只认 file 字段）。
3. **广播发送路径**：广播 sendto 在无默认路由的极简 guest 里报 `network is unreachable`。回包改走 AF_PACKET 自构以太帧直发（按 ServerIP 选网卡、自算 IP/UDP 校验和），失败回退内核发送（`rawBroadcast`）。Serve 侧改单一 :67 套接字回包（ListenConfig 设 SO_BROADCAST+SO_REUSEADDR，源端口恒 67），handlePacket 日志补全客户端指纹（arch/opt60/opt77/opt175 原文）。

**原始证据**（`.f-test/vmA2-serial.log` / `vmB2-serial.log`，2026-10-20 12:01 UTC）：

- snoop 线上实录 OFFER：xid=`f6022e0d`，yi=`192.168.100.100`，file 字段与 opt67 双写 bootscript URL，opt54=`192.168.100.1`；
- server 日志：`12:01:15 dhcp: REQUEST → dhcp: ACK`（DORA 走完）；
- `12:01:16 bootscript: mac=52:54:00:12:34:b2 arch=x86_64 from=192.168.100.100:18587`（VM-B 拿到地址后经 HTTP 拉 bootscript）；
- VM-B 串口：`Linux version 6.1.0-50-amd64`（内核经自研 DHCP 链路启动）。

新增单测：opt175-only / opt77-only 识别、file 字段、isIPXEClient 表驱动、ReplyAddr、广播帧结构/校验和。`go test ./internal/pxe/` 全绿。

## D. preseed 装后闭环 ✅ PASS

**根因（M3/M5 五次失败）**：TCG 全装 40–60+ 分钟 > 沙箱 30–45 分钟存活窗口，每次被掐断在 25–90%。

**新打法——快照续跑**（`scripts/qemu-preseed-snapshot.sh`）：qcow2 盘 + 1GB RAM + MTTCG，HMP 每 ~12 分钟 `savevm m6snap`；状态文件 `.snaptest/state.env` 跨沙箱重启持久；脚本可重复调用、自动 `-loadvm` 续跑；退出码 0 PASS / 1 真 FAIL / 2 有进度续跑 / 3 环境缺失或拒绝冒险。第一遍共 10 轮调用、扛过 3 次沙箱重启。

**过程中的两个真 bug（均已修+回归）**：
1. **脚本续跑判定竞态**（run 7）：`qemu-img snapshot -l` 在沙箱 /usr 恢复竞态中瞬时失败、stderr 被吞，脚本误判无快照 → 全新 PXE 启动重跑早期安装。及时全停（新装未到 partman、guest 零写盘，快照无损）。修复：判定 fail-closed（重试 5 次；已有进度但快照不可确认 → ABORT exit 3，绝不覆盖式新装）；take_snapshot 只有 HMP 输出无 traceback/error 才记 LAST_SNAP。
2. **装后 agent 永不回连（产品真 bug）**：第一遍装完后 phase 2 等不到重报到。串口 sendkey 以 root 登入装后系统实锤：`/usr/local/bin/deplos-agent` 与 unit 文件均为 **0 字节**（wants 软链在、server.url 空），systemd 把空 unit 报为 `masked`。根因：late_command 的写入只在 guest 页缓存（ext4 延迟分配），install-done POST 一到、测试脚本立即 SIGTERM 掉 QEMU，/target 未 sync 就断电，数据全丢（install-done 是网络 POST、不依赖落盘所以独活）。**生产侧同险**：orchestrator 收到 install-done 后会走 IPMI 断电重启。修复：① 模板 late_command 在 POST install-done **之前**加 `sync; sleep 2`（附顺序断言单测 `TestPreseedLateCommandSyncsBeforeInstallDone`）；② 脚本收到 install-done 后加 60s guest 优雅期再杀 QEMU。

**修复验证（run A-10，guest 内手工补装）**：补下 agent 本体（9,737,341B）+ 重写 unit + sync + start 后，`15:47:53 initialize: host=debian id=host-44825d5bcb1d83c2 ips=[10.0.2.15 ...]`——机制打通。

**干净重跑终验（run B，修复版代码，零手工干预）✅**：host=`host-912a30c133bf6e31`，job=`job-c120236a4d820076`，server 日志原文：

```
2026/10/20 15:50:39 initialize: host=qemu-preseed-snap id=host-912a30c133bf6e31 macs=[52:54:00:12:34:99] ips=[]
2026/10/20 15:50:53 bootscript: mac=52:54:00:12:34:99 host=host-912a30c133bf6e31 INSTALL MODE (preseed)
2026/10/20 15:52:02 preseed: serving config for host=host-912a30c133bf6e31
2026/10/20 16:49:13 agent-download: serving 9732741 bytes to 127.0.0.1:36016
2026/10/20 16:49:18 install-done: host=host-912a30c133bf6e31 disarmed (preseed install finished)
2026/10/20 16:51:31 initialize: host=debian id=host-912a30c133bf6e31 macs=[52:54:00:12:34:99] ips=[10.0.2.15 fec0::5054:ff:fe12:3499 fe80::5054:ff:fe12:3499]
```

脚本终判原文：`PASS: preseed unattended install completed (snapshot-resumed) + agent re-enrolled after reboot`。PXE→bootscript→preseed 全自动安装→重启→装后 agent 以同一 host 身份（同 MAC 匹配、hostname=debian）重报到，**装机全链路闭环**。

## 代码改动汇总（M6）

- `internal/pxe/dhcp.go`：iPXE 三路识别、file 字段双写、广播回包判定、AF_PACKET raw 广播、单套接字回包、指纹日志；配套单测。
- `internal/api/server.go` + `cmd/server/main.go`：`-log-requests` 中间件、双斜杠归一化、grub.cfg 动态渲染 + 4 条探测路由；`internal/api/m6_test.go`。
- `assets/preseed/debian.cfg.tmpl`：late_command 加 sync 前置（+ 顺序断言单测）。
- `scripts/qemu-preseed-snapshot.sh`（新）：快照续跑装机验证（fail-closed 续跑判定、install-done 优雅期、phase2 长窗口 + mon2 监视口）。
- `scripts/qemu-arm64-grub-test.sh`（新）：ARM64 PXE/grub 复跑脚本（自制 grubaa64.efi 嵌配置）。
- FEATURE_MATRIX（#1/#5/#22 + D/E/F 行）、README 里程碑已同步。

## 终验

`go build ./... && go vet ./... && go test ./... -count=1` 全绿（本报告落盘时执行，见仓库状态）。

## 环境限制（沿用用户 2026-10-20 口径，不变）

纯物理项（点灯 LED / RAID 真卡 / 固件真刷 / 真 BMC / GPU / WinPE 授权镜像 / SMART 真盘）云上无硬件，代码就绪即封顶，本轮未重复验。
