# M5-DEF 进展记录（三个 QEMU 实验：D / E / F）

> 规则：三个 QEMU 进程严格串行，任何时刻最多 1 个 QEMU。沙箱约每 30–45 分钟重启一次（清空 /usr、杀进程），长任务速战速决、边做边记。所有结论以日志为准，不许硬说 PASS。

## 总览（2026-10-20 本轮结论）

| 项 | 结论 | 一句话 |
|---|---|---|
| D | ❌ 未成 | 最小化模板改好、安装全自动跑到 partman（约 25–30%）时被沙箱重启掐断（12:33），install-done 未达 |
| E | 🔶 部分 | 固件从我方 server 拉走 arm64 内核（33MB）+ shim 拉走整套 GRUB 并在 guest 里跑起来；卡在 GRUB 找不到 grub.cfg、EDK2 传不完 33MB NBP，内核未启动 |
| F | 🔶 部分 | 预检 PASS（socket TCP 隧道双向通广播）；真 server -dhcp 在隧道里收到 DISCOVER 并 OFFER 192.168.100.100+bootfile；未走完 ACK、未识别 iPXE(opt60) |

## D. preseed 最后 10%（最小化安装集 → TCG 20 分钟内跑完 late_command + agent 重报到）

- 状态：❌ 未成（沙箱重启掐断，如实记录，无 PASS）
- 运行实况（2026-10-20 12:11–12:33 CST）：
  - 12:11 开跑（PID 7565），mirror-proxy 缓存 140M deb 预热
  - 12:13 `bootasset: serving vmlinuz` + `serving initrd.img`（guest PXE→installer 内核）
  - 12:15 `preseed: serving config for host=host-1cb87f03d16f269c`（installer 拉走我方 preseed，token 鉴权 OK）
  - 12:18 serial：`Network autoconfiguration has succeeded`
  - 12:31 serial：`Starting up the partitioner / Scanning disks / Setting up encryption`（partman 分区阶段，约 25–30%）
  - 12:33 沙箱重启（/usr 被清空、qemu/server/python3 进程全灭），安装中断于 partman 阶段
- 结论：install-done 未收到（0 次），late_command 真实执行未验证，agent 重报到未验证。与 M3 报告一致：非代码问题，是沙箱 30–45 分钟重启窗口 < TCG 全安装所需 ~40–60 分钟。模板最小化改动已保留在 assets/preseed/debian.cfg.tmpl（ntp 关、services-select 清空、popcon 关）。
- 模板改动（`assets/preseed/debian.cfg.tmpl`）：
  - `d-i base-installer/install-recommends boolean false`（已有）、docs/man/locale path-exclude early_command（已有）、tasksel 空（已有）、`pkgsel/include` 只留 openssh-server curl（已有）
  - 新增：`d-i apt-setup/services-select multiselect`（测试模板清空 security/updates，省一次 apt 元数据拉取；生产模板应加回 security）
  - 新增：`d-i clock-setup/ntp boolean false`（沙箱 guest 无真实 NTP，d-i 的 ntpdate 会白白等待超时）
  - 新增：`d-i popularity-contest/participate boolean false`
  - mirror 复用 `scripts/mirror-proxy.py`（脚本自带），缓存盘有 140M 已缓存 deb
- 运行：`scripts/qemu-preseed-test.sh`，超时默认 2700s（2026-10-20 12:11 CST 开跑，PID 7565）
- 进展日志（2026-10-20 12:11 CST 开跑，已被沙箱重启掐断，见上「状态」）：
  - 12:13 server 日志：`bootasset: serving vmlinuz` + `serving initrd.img`（guest 拿到安装内核）
  - 12:15 server 日志：`preseed: serving config for host=host-1cb87f03d16f269c`（installer 拉走我方 preseed）
  - 12:18 serial：`Network autoconfiguration has succeeded`，d-i 全自动推进中
  - mirror-proxy 的请求日志被脚本静默（log_message no-op），故 mirror.log 无动态，用 serial/server 日志判进度
- PASS 判据：脚本 exit 0 且 server 日志出现 `install-done` + 重启后 `initialize` ≥2 次

## E. ARM PXE 全链路（aarch64 virt 机 SLIRP → 我方 server → arm64 内核启动）

- 状态：🔶 部分（链路打通到 GRUB 在 guest 里跑起来；arm64 内核未在 serial 上完成启动）
- 最终证据汇总：
  - 固件：`qemu-efi-aarch64 2024.02-2ubuntu0.10` 装好，AAVMF pflash 正常启动 UEFI PXE/HTTP Boot 菜单链
  - 我方 server 的 arch 感知 bootscript 服务端自测 OK（`?arch=aarch64` 返回 /ipxe/arm64/linux + ttyAMA0）
  - **guest 固件真的从我方 server 拉走 arm64 内核**（尝试 3a 实录：`bootasset: serving linux (32956352 bytes)`×2）——但 EDK2 HTTP Boot 完成不了 33MB NBP 传输（`Could not retrieve NBP file size` / `Unexpected network error`），内核没被固件执行
  - shim→GRUB 链（经日志代理 :19112 归一化路径后）：guest 依次从我方 server 拉走 bootnetaa64.efi（1MB）、（代理答 404 容忍掉 shim 的 revocations/cert 可选文件）、grubaa64.efi（3.97MB，完整）——**Debian GRUB 2.06-13+deb12u2 在 arm64 guest 里真的跑起来了**（serial 实录 `Welcome to GRUB!`）
  - **最终卡点**：GRUB 起来后落在 `grub>` 空 prompt，没加载任何 grub.cfg（其内置 prefix 的配置路径在本轮时间内没探明——HTTP 侧没见到它请求配置，TFTP 侧已按 Debian 原生布局备了 tftp2 根目录+全量 .mod+内核/initrd 兜底，它仍未取用），故没有执行 linux/initrd 命令，serial 上没有 `Linux version`
  - 原任务设想的「iPXE 脚本 chain」在 aarch64 上需要 ipxe-arm64.efi（沙箱里 apt 被系统自愈进程长期占锁、dpkg 直装只补齐了 qemu 系包，交叉编译链没装上），全程没拿到 iPXE-arm64 二进制，所以 bootscript 端点在 guest 侧未被真实请求过（仅服务端自测）
- 产物：`internal/api/server.go` 新增 9 条 arm64 netboot 资产路由（/ipxe/arm64/ 下 bootnetaa64.efi、grubaa64.efi、grub.cfg、grub/grub.cfg，/grub/grub.cfg，/debian-installer/arm64/ 原生布局 4 条），`assets/debian-bookworm-arm64/` 补入 bootnetaa64.efi + grubaa64.efi + grub.cfg（脚本式：insmod http → (http) 拉我方 linux/initrd → (tftp) 兜底）；`go build` 通过、`go test ./internal/api/ -run "TestBoot|TestAarch"` 通过
- 尝试记录：
  - 尝试 1（脚本版路径 B，TFTP）：脚本 server 就绪竞速问题（sleep 1 太短）→ 已改成 healthz 轮询 20s
  - 尝试 2（路径 B，SLIRP TFTP 给 AAVMF）：UEFI PXEv4 成功从 SLIRP 拿到 DHCP，但取 bootnetaa64.efi 时 EDK2 报 `Unknown TFTP error, treating as file not found` / `Unable to fetch TFTP image: Not Found`。**卡点：EDK2 的 TFTP 客户端与 QEMU SLIRP TFTP 不兼容**（x86 实验一直用的是 iPXE ROM 的 TFTP 客户端，与 SLIRP 相容，所以没暴露过这个问题）
  - 尝试 3a（EDK2 HTTP Boot，bootfile=http://10.0.2.2:19107/ipxe/arm64/linux）：**我方 server 日志两次实录 `bootasset: serving linux (32956352 bytes)`——guest 固件真的从我方 server 拉了 arm64 内核**；但 EDK2 随后报 `Error: Could not retrieve NBP file size from HTTP server.` / `Unexpected network error.`，33MB 的 NBP 在 EDK2 HTTP Boot 里传不完，内核没起来
  - 尝试 3c（进行中）：HTTP Boot 只拉 1MB 的 bootnetaa64.efi（GRUB），GRUB 再用自己的 http 从我方 server 取 linux+initrd.gz（带 console=ttyAMA0）
    - 05:13:22 实录：我方 server 先后被拉走 `bootnetaa64.efi`（1072064B）→ `grub.cfg` ×2（218B）——**GRUB 启动了并在用 HTTP 拉我方配置**
    - 但 stub 退出报 HTTP Error；serial：`Unable to fetch HTTP image \\grubaa64.efi`。**新卡点：Debian 的 bootnetaa64.efi 只是个 stub，它启动后还要去 HTTP 拉真正的 `grubaa64.efi`**（我方无此路由→404），导致 start_image 失败、EDK2 fallback 也失败。server 日志未见 linux/initrd.gz 请求——GRUB 还没走到拉内核那步
    - 下一步：从 aliyun 拉到 netboot.tar.gz（含 bootnetaa64.efi + grub/arm64-efi/*.mod，但没有 grubaa64.efi）；考虑用 grub-mkimage 自制含嵌入配置的 grubaa64.efi（嵌入 `linux (http,10.0.2.2:19110)/ipxe/arm64/linux console=ttyAMA0` + initrd），或找 grub-efi-arm64-bin 的模块。正在进行。
    - 3c 第二轮（补了 grubaa64.efi 4MB 路由后）：shim 成功拉走 `grubaa64.efi` ✔；但真 GRUB 起来后没有任何请求命中原有 grub.cfg 路由；补了 Debian 原生布局 4 条路由（/debian-installer/arm64/...）后亦然
    - 3c 第三轮（前置日志代理抓全路径，决定性证据）：shim 的真实请求序列 = `HEAD/GET /ipxe/arm64/bootnetaa64.efi → 200` 后，接 `GET /ipxe/arm64/revocations_sku.efi → 302`、`revocations_sbat.efi → 302`、`shim_certificate_0.efi → 302`、`GET /ipxe/arm64//grubaa64.efi → 307`。**断点定版：① shim 需要同目录的吊销/证书文件，我方 mux 把这些未注册路径导向 Web 控制台的登录重定向（302）；② grubaa64.efi 请求带双斜杠 `//`，Go ServeMux 做路径清洗回 307；shim 不跟随任何重定向 → 链断。**
- 路径设计：主路径 A = 从源码交叉编译 ipxe-arm64.efi（嵌入 boot 脚本 chain 我方 bootscript?arch=aarch64）；兜底路径 B = Debian d-i 的 bootnetaa64.efi（GRUB netboot，经 mirror-proxy 拉取）→ grub.cfg 用 HTTP 从我方 server 取 /ipxe/arm64/linux + initrd.gz。报告写明实际走了哪条路
- 固件：`qemu-efi-aarch64`（apt candidate 2024.02-2ubuntu0.10；如 apt 不可靠改从 Debian pool 下 .deb 解包取 AAVMF_CODE.fd）
- 启动命令（待验证）：`qemu-system-aarch64 -M virt -cpu cortex-a72 -bios AAVMF_CODE.fd -netdev user,id=n0,tftp=<dir>,bootfile=boot.ipxe -device virtio-net-pci,netdev=n0 -boot order=n`
- server 已有 arch 感知（`?arch=aarch64` → /ipxe/arm64/linux + initrd.gz，console ttyAMA0）
- PASS 判据：server 日志出现 `arm64 bootscript` 请求（`bootscript: mac=... arch=aarch64`）+ serial 出现 `Linux version`

## F. 自研 DHCP live（socket TCP 隧道两 VM 组网，限时 40 分钟）

- 状态：🔶 部分（预检 PASS；全链路走到 OFFER，未完成 ACK/chain）
- 全链路实测（2026-10-20 13:08 CST，时序修正后）：VM-A serial 实录：
  - `[vesinit] eth0 configured, exec'ing deplos server (-dhcp)` → `dhcp: enabled` / `dhcp: listening on :67` / `server: listening on :19110`
  - `dhcp: DISCOVER mac=52:54:00:12:34:b2 xid=d8c85066`
  - `dhcp: OFFER 52:54:00:12:34:b2 -> 192.168.100.100 bootfile="undionly.kpxe"`（自研 DHCP 真的在两 VM 的 socket 网段里分了地址+下发 bootfile）
  - DISCOVER/OFFER 共重复 4 轮（05:08:16–05:08:23）后 VM-B 不再发包
- 未完成与卡点（按 F 的 40 分钟上限停在这里，不硬撑）：
  1. VM-B 未发 REQUEST → 无 ACK，租约未走完 DORA。原因未定版：OFFER 可能没被 iPXE 接受（4 轮同 xid 重发 DISCOVER 是典型征兆），或 iPXE 接受后去 TFTP 拉 undionly.kpxe 而 VM-A 无 TFTP 服务、回退重 DHCP；两种都说得通，未抓包定版。
  2. `selectBootFile` 未识别该客户端为 iPXE：OFFER 给的是 arch0 默认 `undionly.kpxe`（TFTP），不是 iPXE 的 bootscript URL——该 ROM 的 DISCOVER 里 opt60 大概率不是 "iPXE"（e1000_82540.rom 的 iPXE 在 ROM 阶段的 vendor class 待抓包证实），故未命中 `opt60 contains ipxe` 分支。
  3. 时序坑（已证实并修正）：客户端必须在 server `dhcp: listening` 之后再启动——iPXE 的 DHCP 重试窗口只有约 10 秒，先起客户端则 DISCOVER 全部打空（第一轮全链路失败就是这个原因，不是隧道问题）。
- 结论：**socket(TCP) netdev 组网 + 自研 DHCP 的地址分配/OFFER 已 live 打通；完整 DORA(ACK) + bootscript chain 还差 opt60 识别与 TFTP/chain 衔接，未在本轮 40 分钟内闭环。**
- 预检实测（2026-10-20 13:05 CST）：VM-A(fprobe=send, mac 52:54:00:12:34:a1) 发出 5 个 ARP 广播帧；VM-B(fprobe=recv, mac 52:54:00:12:34:b2) serial 实录：`RECV frame len=60 bcast=true ethertype=0806 src=52:54:00:12:34:a1` → `RESULT: broadcast ARP frame received -> L2 broadcast WORKS over socket netdev`。**结论：socket(TCP) netdev 转发二层广播帧，预检通过。**
- 踩坑记录：Debian netboot 内核的 e1000 驱动是模块（.ko），极简 initrd 需自带 e1000.ko（从官方 initrd.img 提取，无依赖）+ finit_module 加载 + ioctl 拉 IFF_UP，否则 guest 里根本没有 eth0。
- 全链路进行中：VM-A initrd（`.f-test/vmA.cpio`，含真 server 静态二进制 + assets）跑 `server -dhcp`（192.168.100.1，池 .100–.200）；VM-B 用 iPXE ROM PXE 启动（MAC ...B2）。证据看 VM-A serial 里的 `dhcp: OFFER/ACK ... bootfile=` 与 `bootscript:` 行。（结果见上「状态」。）
- 预检工具：`.f-test/fprobe.go`（纯 Go 静态二进制，作为 PID 1 init 放进自制 newc initrd `.f-test/fprobe.cpio`，role 由内核命令行 `fprobe=send/recv` 选）；用 AF_PACKET 原始套接字发/收 ARP 广播帧，不需要 IP 配置，专测 socket netdev 二层广播转发
- 预检命令（待跑）：两 VM 同用 assets/debian-bookworm/vmlinuz + fprobe.cpio，`-netdev socket,listen/connect=127.0.0.1:41001`，串行日志看 `RESULT: broadcast ARP frame received`
- 计划：VM-A（极简 initrd 跑 `server -dhcp`）↔ VM-B（PXE 客户端），用 `-netdev socket,listen|connect` TCP 模式组以太帧隧道
- 先做二层连通性预检（广播帧是否过 socket netdev）；预检不过即停
- PASS 判据：VM-B 拿到 VM-A 自研 DHCP 分配的地址 + bootfile
