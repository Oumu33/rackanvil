# M5-G 进度：拿本云主机当被管对象真跑全套（2026-10-20）

脚本：`scripts/m5g-host-e2e.sh`（真实 server 进程 + 真实 agent 进程，端口 19102，
全新 SQLite 库 `.m5g/deplos.db`，日志 `.m5g/logs/{server,agent}.log`）。

## 结果（全部为本机真实执行，无 mock）

| 项 | 结果 | 证据 |
|---|---|---|
| agent initialize 注册 | ✅ PASS | host=host-3ae147f8641c3349，Hostname=htch-runtime，MAC/IP 为本机真实值（72:dc:7c:34:2f:fd / 198.19.0.2）。注：initialize 载荷按设计只带 hostname/MAC/IP，CPU 型号/内存在注册记录里为空，硬件数值走 collectors（见下行） |
| heartbeat（checkin 2s 周期） | ✅ PASS | 后续全部任务均经 checkin 领取并 report 落库，LastCheckin 持续更新 |
| shell 任务下发→执行→回传 | ✅ PASS | task-8e185d7d7c65ee5f status=done exit=0；Stdout 为本机真实输出：`Linux htch-runtime 7.0.0-39-generic... x86_64`、`nproc=2`、`MemTotal: 8125588 kB` |
| collectors 全量采集（真实 /proc、/sys） | ✅ PASS | `/api/v1/hosts/{id}/health` 的 latest_metrics：CPUPct=81.55（采样瞬间真实值）、MemUsedMB=7071 / MemTotalMB=7935；DiskJSON 来自 /sys/block：vda 7.97GB、vdb 8.05GB、vdc 1.08GB（rotational=true）、vdd 107.37GB（rotational=false）；NetJSON={}（本机采集器未产出网络明细，如实为空，未编造） |
| firmware.detect 真实执行 | ✅ 真执行、结果为明确失败 | task status=failed，Error=`lspci: exec: "lspci": executable file not found in $PATH`。本机无 lspci 二进制、无任何 RAID/HBA 卡；错误信息明确，未伪造"检测成功"。若 lspci 在场而无目标卡，Detect 返回空设备列表（PCI ID 解析已在解析器硬化轮用真实样本回归） |
| raid.query 真实执行 | ✅ 真执行、结果为明确失败 | task status=failed，Error=`no RAID CLI found (storcli/perccli)`。未伪造成功 |
| locator disk.scan 真实执行 | ✅ PASS（真实负结果） | status=done，输出枚举 /dev/vda–vdd 四块 virtio 盘，tool="none"、slot=-1（无 SES/LED 工具可用，如实标注）；disk.locate 走同一工具链（ledctl→sg_ses→storcli→nvme 全缺失），会以明确错误失败，不点亮任何东西、不报假成功 |
| SMART 采集 | ⚪ 如实无数据 | 本机无 smartctl 二进制，且 vda–vdd 为 virtio 盘本身无 SMART；collectors 按设计跳过并记录，未产生任何 SMART 样本，未编造数值 |

## 结论
云主机能执行的全部真实执行完毕：注册/心跳/任务/采集闭环 PASS；
firmware/raid/locator 在无硬件环境下均返回明确错误或真实负结果，零伪造。
物理层项（点灯 LED、RAID 真卡、固件真刷、真 BMC、GPU、WinPE 镜像）在
M5-REPORT 中按新口径列。
