# M6 进展记录（E/F/D 收尾）

> 规则：QEMU 串行；边做边记；结论以日志为准。沙箱约 30-45 分钟重启一次，长任务留可续跑脚本+状态文件。

## 环境
- 2026-10-20 11:41 UTC 沙箱刚重启（up 2min），/usr 里 qemu 被清；已用 /var/cache/apt/archives 的 deb 整批 dpkg 装回：qemu 8.2.2 + AAVMF + ovmf + ipxe roms 全部就绪（此恢复步骤重启后需重做，一条命令：`dpkg -i /var/cache/apt/archives/*.deb`）。
- Go 工具链 ~/workspace/toolchain/go 完好。

## E. ARM PXE
- 待办：全量请求日志 → 重跑 shim/GRUB 链 → 定位 grub.cfg 路径 → 内核启动。

## F. 自研 DHCP live
- 待办：option175/77/60 综合识别 + 单测 → 双 VM 抓包 DORA → REQUEST/ACK/chain。

## D. preseed 装后闭环
- 待办：快照续跑脚本 scripts/qemu-preseed-snapshot.sh（savevm 每 ~12min，可 -loadvm 续跑）。

## E 迭代 1（2026-10-20 11:47 UTC）
- 代码改动已落地：① server 全量请求日志（-log-requests，记录原始 RequestURI+状态+字节，含双斜杠原样）+ 路径双斜杠归一化（消灭 shim 怕的 307）；② grub.cfg 改为按 serverURL 动态渲染（M5 的静态文件写死了 :19112 代理端口——拉到了也是废配置，这是隐藏坑）；③ 补 4 条 GRUB 可能探的 cfg 路由。api/pxe 测试全绿。
- grub 工具链：apt 被自愈进程占锁 → 从 aliyun Debian 池下 grub-common + grub-efi-arm64-bin 2.06-13+deb12u2（与资产 GRUB 同版本），dpkg -x 到 ~/workspace/toolchain/grub/x，grub-mkimage 可用。
- 首跑 FAIL：grub-mkimage -d 要传平台目录（.../grub/arm64-efi），已修脚本重跑。

## F 准备（2026-10-20 11:52 UTC，趁 E 在跑先备好）
- dhcp.go 已改：isIPXEClient（opt175 存在 / opt77 含 ipxe / opt60 含 ipxe 三路冗余）+ 单测 4 个（含 opt175-only、opt77-only、file 字段）；buildReply 补 BOOTP file 字段（经典 PXE ROM 只认这个）；Serve 改为单一 :67 套接字回包（SO_BROADCAST+REUSEADDR，源端口恒 67——此前回包走临时端口，是 M5"OFFER 被吞"的头号嫌疑）；handlePacket 日志补全客户端指纹（arch/opt60/opt77/opt175 原文）。
- 抓包证人 dhcpsnoop.go（AF_PACKET 全量解 DHCP 帧，双向）+ vesinit2（先拉 snoop 再 exec server）已构建进 vmA2.cpio（81MB）；运行脚本 .f-test/run-full2.sh 就绪，等 E 完串行跑。

## E 结论：✅ PASS（2026-10-20 11:51 UTC）
打法：不用 shim/Debian GRUB 的配置发现——grub-mkimage 自制 grubaa64.efi，配置（net_bootp → linux/initrd 走 HTTP → boot）直接嵌入镜像；AAVMF HTTP Boot 从我方 server 直拉该镜像（SB 关）。
原始证据（.m6-armtest/logs/）：
- server reqlog：`11:51:23 HEAD/GET /ipxe/arm64/grubaa64.efi -> 200 684032B`（自制 GRUB）→ `11:51:27 GET /ipxe/arm64/linux -> 200 32956352B` → `11:51:30 GET /ipxe/arm64/initrd.gz -> 200 40147331B`
- serial：`[    0.000000] Linux version 6.1.0-50-arm64 (debian-kernel@lists.debian.org) (gcc-12 ...) #1 SMP Debian 6.1.176-1`
- 旁证：GRUB 自带 HTTP 栈拉 33MB 内核只用 ~3 秒，EDK2 NBP 当初传不完的问题不在 GRUB 这条路。
脚本：scripts/qemu-arm64-grub-test.sh（可重复跑，RESULT.txt 落盘）。

## F 迭代 2（2026-10-20 11:52 UTC）——抓包定版，双根因
dhcpsnoop 在 VM-A 全量解码，证据（.f-test/vmA2-serial.log）：
- DISCOVER 原文：`opt60="PXEClient:Arch:00000:UNDI:002001"`（ROM 阶段确实不是 "iPXE"，M5 猜测证实）、`opt77="iPXE"`、opt175 存在且含 iPXE 特性字节、opt93=0、opt94 UNDI、opt61 client-id。新识别生效：server 日志 `ipxe=true`，OFFER 已改发 bootscript URL（不再是 undionly.kpxe）。根因①（识别）已修死。
- 但 DORA 仍未推进：snoop 解到 4 轮 DISCOVER（1s/2s/4s 退避），**线上 `udp 67->68` 帧数 = 0**，server 无任何 write 报错 → OFFER 在 VM-A 内核里被吞：回包按旧 replyAddr 单播到 yiaddr=192.168.100.100，而客户端尚未绑定该地址、不回 ARP，内核 UDP 发不出去。根因②（回包地址）定版。
- 修复：replyAddr 改为 ciaddr 为零一律广播（RFC 2131 §4.1 许可、ISC/Windows DHCP 惯例），仅续租（ciaddr 已绑定）单播；新增单测 TestReplyAddrBroadcastUntilBound。连同迭代 1 的"回包源端口恒 67"+"file 字段"，OFFER 链路三处都已对齐真实 DHCP 服务器行为。

## F 迭代 3（2026-10-20 11:57 UTC）——第三层根因暴露
广播回包后：`dhcp: write 255.255.255.255:68: sendto: network is unreachable`。VM-A 极简 guest 无默认路由，内核对 255.255.255.255 的路由查找直接 ENETUNREACH（正常宿主机有默认路由所以从不暴露——这也是此前从未在真宿主测出的原因）。ISC dhcpd/dnsmasq 的标准解法：回包不走内核 UDP 路由，用 AF_PACKET 原始套接字在收包网卡上自构以太帧发出。已按此改 Serve：广播回包走 rawBroadcast（按 ServerIP 选网卡、广播 MAC、自算 IP/UDP 校验和），单播续租仍走内核路径。
- rawBroadcast 已实现并单测（帧结构/IP+UDP 校验和全验），Serve 广播回包优先 raw、失败回退内核发送。

## F 结论：✅ PASS（2026-10-20 12:01 UTC，迭代 4）
完整 DORA + chain + 内核启动，证据（.f-test/vmA2-serial.log / vmB2-serial.log）：
- snoop 线上实录 OFFER：`TX type=OFFER xid=f6022e0d yi=192.168.100.100 file="http://192.168.100.1:19110/api/v1/ipxe/bootscript?mac=52:54:00:12:34:b2" opts{53,54=192.168.100.1,51=3600,1,3,6,67=URL}` —— file 字段与 option 67 双写、字段齐全。
- `12:01:15 dhcp: REQUEST ... -> dhcp: ACK 52:54:00:12:34:b2 -> 192.168.100.100`（DORA 走完）
- `12:01:16 bootscript: mac=52:54:00:12:34:b2 arch=x86_64 from=192.168.100.100:18587`（iPXE chain 到我方 bootscript）
- VM-B serial：`Linux version 6.1.0-50-amd64 ... #1 SMP PREEMPT_DYNAMIC Debian 6.1.176-1`
三层根因全部修死：① opt60 识别（opt175/77 冗余判定）② 回包单播 yiaddr 被 ARP 吞（改广播）③ 广播 sendto 在无默认路由时 ENETUNREACH（AF_PACKET raw 直发）。自研 DHCP 至此 live 全链路闭环。

## D 迭代日志
- run1（12:03–12:29）：全新安装，bootscript INSTALL MODE 12:04、preseed 12:06 下发，d-i 推进；savevm 成功 3 次（12:04/12:16/12:28），预算到点 exit 2 干净退出。快照机制首验通过。
- run2（12:30 起）：-loadvm 续跑成功；12:34 沙箱重启全灭（本轮未及打新快照，最新快照仍为 run1 的 12:28 状态，约损失 4 分钟 guest 进度——设计内）。
- run3：重启后自愈 qemu（脚本内 dpkg 缓存恢复）+ 续跑。
- run3（12:36–13:03）：沙箱重启后自愈+续跑，savevm 2 次（12:41/12:53），exit 2。盘 474MB→1.18GB 持续增长（pkgsel 检索/解包阶段），未卡死、纯慢（宿主 load 常驻 6–10）。
- run4：续跑，累计 guest 安装时间约 50 分钟，预期本轮或下轮见 install-done。
- run4（13:07–13:23）：续跑，savevm 1 次（13:13）；13:23 沙箱二次重启全灭（无收尾快照，最新可用快照 13:13，损失约 10 分钟 guest 进度）。盘 1.53GB。
- run5：续跑。累计有效 guest 安装时间约 55 分钟（不计被重启吞掉的段）。
- run5（13:25–13:50）：续跑 exit 2；串口见 tasksel 完成/console-setup/Storing network settings（收尾阶段），盘 1.53→2.27GB（initramfs 生成+包配置写入高峰）。
- run6：续跑，预期见 late_command/install-done。
- run6（13:52–14:08）：续跑中被沙箱二次重启杀掉（本实例 13:23 启动、14:08 重启）。最后快照 14:02（VM 时钟 01:29:55）。观察：续跑窗口内 guest 写盘量明显变小（blockstats 增量约 20MB/16min），疑似进入 CPU 密集阶段（initramfs 生成/包配置，TCG+宿主高负载）或续跑后半死 TCP 连接拖住 apt——run7 用 QEMU 进程 CPU% 判别。
- run7（14:25–14:37）：**事故（已拦下）**——续跑判定 `qemu-img snapshot -l` 在沙箱 /usr 恢复竞态中瞬时失败（stderr 被 2>/dev/null 吞掉），脚本误判无快照 → 全新 PXE 启动，d-i 从早期阶段重跑（串口实锤 Checking mirror/Loading components）。发现后立即全停：全新安装未到 partman、guest 零磁盘写入（blockstats 实锤），14:01 的晚期快照完好无损。脚本已修：① 续跑判定 fail-closed（重试 5 次；RUN>1 且快照不可确认 → ABORT exit 3，绝不覆盖式新装）② take_snapshot 只有 HMP 输出无 traceback/error 才更新 LAST_SNAP（此前失败也记账，run7 实锤）。run6 的低写入之谜随之改判：run6 是真续跑（有 resuming 行），其低 I/O 是晚期阶段本身特征/半死连接，二者未定，run8 续看。
- run9（15:01–15:26）：**install-done 到达** `15:24:13 install-done: host=host-44825d5bcb1d83c2 disarmed` —— late_command 在 guest 内真执行+回调，装机阶段闭环。Phase 2 首窗口（400s）未等到重报到：装后系统 grub 无串口 console（原厂配置），串口只见 GRUB→Loading Linux→静默，无法判读；脚本 phase2 窗口已放宽到 1500s（PHASE2_TIMEOUT）并加 mon2 监视口（可 screendump 看 VGA 登录界面判进度），重跑 phase 2。
- **D 根因定版（run10，guest 内实锤）**：装后系统能启动（VGA login 界面），但 agent 永不回连。串口 sendkey 以 root 登入 guest 查得：`/usr/local/bin/deplos-agent` 与 `/etc/systemd/system/deplos-agent.service` 均为 **0 字节**（wants 软链在、server.url 空）→ systemd 把空 unit 报为 masked。原因：late_command 的写入只在 guest 页缓存里（ext4 延迟分配），install-done POST 一到、测试脚本立即 SIGTERM 掉 QEMU，/target 未 sync/umount 就断电，数据全丢；install-done 是网络 POST 不依赖落盘所以独活。产品侧同样有此风险（orchestrator 收到 install-done 后走 IPMI 断电重启）。修法：模板 late_command 在 POST install-done 前加 `sync`；测试脚本收到 install-done 后给 guest ≥45s 优雅期再杀。先在活着的 phase-2 guest 里手工修复验证链路，再改代码重跑干净全程。
- run10（phase2 续跑）：**修复验证 PASS**——guest 内手工补下 agent 本体（9,737,341B）+ 重写 unit + sync + start，server 15:47:53 收到 `initialize: host=debian id=host-44825d5bcb1d83c2 ips=[10.0.2.15 ...]`（同 MAC 匹配回原 host），脚本判定 `PASS: preseed unattended install completed (snapshot-resumed) + agent re-enrolled after reboot`。链路机制全通。
- 代码修复：① 模板 late_command 在 install-done POST 前加 `sync; sleep 2`（附顺序断言单测 TestPreseedLateCommandSyncsBeforeInstallDone，已绿）；② 快照脚本 install-done 后加 60s guest 优雅期再杀 QEMU。第一遍状态归档为 .snaptest-m6-runA，下面清零用修复版重跑干净全程（无手工干预）验收。
- **run B（干净重跑，修复版模板+脚本，零手工干预）✅ PASS**：新 host host-912a30c133bf6e31 / job job-c120236a4d820076；15:50:39 initialize#1 → 15:50:53 bootscript INSTALL MODE → 15:52:02 preseed served →（快照续跑 3 轮，盘 1.86G→2.91G→完成）→ 16:49:13 agent-download → **16:49:18 install-done** → 脚本 60s 优雅期 → phase2 从盘启动 → **16:51:31 initialize#2 host=debian id=host-912a30c133bf6e31 ips=[10.0.2.15 ...]** → 脚本终判 `PASS: preseed unattended install completed (snapshot-resumed) + agent re-enrolled after reboot`。**D 项闭环成立。**
