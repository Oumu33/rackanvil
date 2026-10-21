# M11 验收报告：「优化」——原厂短板清单 + 本轮落地优化

日期：2026-10-21。口径（用户亲定）：这是**产品优化轮**，不是挖矿轮——把
M1–M10 已实锤的原厂 bug/短板逐条对回我们的产品，能修的全修，再扫一遍
我们自己的优化点。汇报重心：**我们的东西变好了什么**。

验证基线：`go build ./...`、`go vet ./...`、`go test ./... -count=1`
全绿，**21 个测试包**（基线 19 + 新增 `internal/signreq` 包与 agent 包首批测试）；
`go test -race ./internal/collectors/ ./internal/api/ ./internal/agent/`
干净。

---

## 1. 原厂短板清单（12 条，逐条溯源 + 我方处置）

| # | 原厂问题 | 证据出处 | 我方处置 |
|---|---|---|---|
| 1 | 硬编码后门凭据多组：PG 密码 `HrnrKit_2025_Secure!` 编进安装器与 server DSN；`HrnrKit_Super_Secret_Backdoor_2025` 躺在 server rodata；Web 默认账密 admin/hrnrkit 写在官方 README | deplos-reversing/REPORT.md §凭据表（行 96–102） | ✅ **已避**：Web admin 首启随机生成、只打一次日志（`internal/web/web.go` EnsureAdmin）；装机 root 口令每次随机（`internal/orchestrator/orchestrator.go` runInstall）；agent token 每主机随机（`internal/store` newToken）；**本轮再加固**：签名密钥同样每主机随机、运行时下发（见 §2-1） |
| 2 | NVMe 温度解析 bug：VMware 虚拟盘在厂商演示里报 **1739°C**（缺失传感器被当数值展示） | 厂商文档截图；`docs/M7-REPORT.md:70` | ✅ **已修**：`collectors.NvmeTempCelsius` 对缺失/0/超出 −43..127°C 的读数返回"未知"，禁止编造数字（解析器硬化轮，含专项测试） |
| 3 | 固件分发型号后缀误判：原厂按 `_([^_]+)$` 取后缀，`MegaRAID_SAS_9361_8i` 取到 "8i"，分发路径选错 | `docs/M4-REPORT.md:48` | ✅ **已修**：`internal/firmware` 全型号关键词匹配（Contains "9361" 等）+ 单测锁定该陷阱 |
| 4 | preseed late_command 没 sync 就发 install-done：装机完成信号触发断电时页缓存未落盘，agent 二进制变 0 字节、unit 被 systemd 判 masked | `docs/M6-REPORT.md`（D 项实锤）；根因注释在 `assets/preseed/debian.cfg.tmpl` | ✅ **已修**：模板 `sync ; sleep 2` 后才发 install-done，顺序断言单测 `internal/api/m6_test.go`；**本轮再加固**：校验和门控（见 §2-3） |
| 5 | `collectors_checksums.txt` 与三个分发渠道的实物 sha **全对不上**（陈旧工件） | `docs/M10-REPORT.md:46` | ✅ **本轮修复**：校验和改为从实物实时计算（见 §2-3），结构上不存在"陈旧" |
| 6 | 双构建同标 1.3.5.1：安装包 server 是 commit 4ab8b303（2026-07-15），更新包里却是更旧的 7b1eb25d（2026-05-06），版本号看不出新旧 | `docs/M10-REPORT.md:31-32` | ✅ **已避（分发模型不同）**：我方单一源码构建、一个构建一个产物，不存在"更新包静默装旧构建"的环节；无两个产物共用一个发布身份 |
| 7 | initrd 内置 **root SSH 密钥对**，随镜像分发到每一台装机对象 | `docs/M9-REPORT.md:38`（仅记录存在性，未复制） | ✅ **已避**：我方使用发行版官方 netboot initrd，不注入任何密钥；一切凭据运行时注册下发、用后可轮换 |
| 8 | Windows autounattend 模板内 **明文 RootPassword**；所谓加密传输（decryptor 解密应答文件）只护传输不护模板本身 | `docs/M8-REPORT.md:13-38` | 🔶 **部分处置**：我方装机口令每台随机（短板 #1 同项）、preseed 按 **per-host token 路径**分发而非全网可拉、我方 autounattend 模板不写明文密码片段（M8 红线决定，维持）。残余：PXE 局域网内口令经 HTTP 明文传输是 d-i/无人值守生态通例，改造需自研装机协议，超出本轮（见 §4） |
| 9 | `assets/agent/.manifest.txt` 与出货不符：引用不存在的 `collector_vmware_{amd64,arm64}` 与旧名 `hrnrOS-agent-*` | deplos-reversing/COMPLETENESS-LEDGER.md:20 | ✅ **已避 + 本轮强化**：我方从无手工维护的清单；本轮 `/checksums.sha256` 由实物生成（§2-3），清单与出货不符在结构上不可能发生 |
| 10 | 评分规则文案与实际阈值不符：NVMe percentage_used 文案写"超过 95%/90%"，代码实际在 **96/91** 触发 | `docs/M8-REPORT.md`（评分规则正文）；我方 `scoring_trend.go` 旧注释逐字记录了该不一致 | ✅ **本轮修复**：在我方代码里发现了**同款复制**（key 文案沿用原厂错误数字），已改为文案由阈值常量生成 + 一致性单测（见 §2-4） |
| 11 | 厂商名实不符：GetDriver 注册表全集里**没有 Raisecom、没有 Ruijie**，宣传面提到的厂商实际走 Generic 兜底 | `docs/M8-REPORT.md:84-108`（三方互证定性） | ✅ **已处置**：`internal/switchdrv` 按实锤全集实现；Ruijie 明确标注为"我方超集扩展"，Raisecom 明确标注"不支持"，不冒充原厂能力（`docs/M7-REPORT.md`、`docs/M8-REPORT.md`） |
| 12 | proxmox 镜像识别定义以 `.json.bak` **停用状态**混在出货定义集里，靠改名停用、无显式标记 | `docs/M9-REPORT.md`（definitions 全转储）；`internal/osimage/definitions/proxmox.json.bak` | ✅ **本轮收口**：此前我方"为保真"把它加载**且参与识别**——这与原厂实际行为（停用）不符；本轮加显式 `Disabled` 标记、`Identify` 跳过、回归测试锁定（见 §2-5） |

---

## 2. 本轮新落地的优化（5 项）

### 2-1. agent 协议签名（防伪造注册后流量）

**问题**：此前 agent 的 checkin/report/metrics 只靠 bearer token——token
一旦泄漏或被猜中，即可伪造任意主机的心跳、任务回执和指标。原厂 PXE
阶段 heartbeat（M10 恢复）反而有 runtime_salt + nonce + 签名信封，原厂
装机后 agent 却同样是明文 token。学其长、补其短。

**设计**（我方自有，不复刻原厂 Rust 签名）：

- 新包 `internal/signreq`：HMAC-SHA256，规范串
  `hostID \n method \n path \n unix时间戳 \n nonce \n sha256(body)`；
- 密钥：32 随机字节，server 在 initialize 响应中**一次性下发**
  （agent 请求带 `want_sign_key: true`），per-host 独立、可轮换、
  只存服务端 DB（`hosts.sign_key`，本轮迁移）与 agent 内存，**不落任何
  镜像/文件**；
- 强制点：server 对**有密钥的主机**在 checkin/report/metrics 三端点
  验签——时间窗 ±120 秒、nonce 防重放缓存、HMAC 常量时间比较；
- 兼容：未升级的旧 agent（无密钥主机）继续 token-only，不被锁死；
- 豁免：install-done 不签名（装机前环境只有 wget 可用、尚无 agent 本体；
  该端点仍要求有效 token 且只清除 install 标记，风险面有限，理由在此明示）。

**前后差异**：token 从"唯一凭据"降级为"身份标识 + 引导凭据"；签名主机
的流量无法被离线伪造、无法重放。

**测试**：signreq 签名/篡改/错钥 2 个；api 强制验签、重放拒绝、旧主机
兼容 3 个；agent 端到端（假 server 验签）1 个；db 密钥持久化/轮换 1 个。

**代码**：`internal/signreq/`、`internal/agent/agent.go`（post 签名）、
`internal/api/server.go`（agentAuth/verifySignature/claimNonce）、
`internal/store/store.go`、`internal/db/db.go` + `internal/db/m11_migrations.go`。

### 2-2. first-boot 等网络就绪对齐

**依据**：M10 实锤原厂新构建的 pxe-first-boot 增量——轮询 server-time
最长 120 秒、失败上报 network_ready、下载 3 次重试。这是原厂拿现场故障
换来的改进，我们对齐进自己的装机链：

- **agent 首启就绪门**：`Agent.Run` 先以 2 秒间隔轮询
  `GET /api/v1/ipxe/server-time`，上限 120 秒（原厂同值）；结果
  （`network_ready` + 等待秒数）随 initialize 上报，server 落日志。
  与原厂的差异如实说明：原厂脚本失败即退出上报，我方记录信号后继续走
  initialize 无限重试——配合 systemd `Restart=always`，等效且不丢注册；
- **preseed 下载重试**：late_command 的 agent 下载与校验和下载均改为
  `wget --tries=3 --waitretry=5`（原厂增量的 3 次重试口径）。

**测试**：agent 就绪门 up/down 双场景（`TestWaitForServerGate`）；
模板渲染断言含重试参数（m6 顺序测试扩展）。

### 2-3. 资产完整性校验（治原厂短板 #5/#9）

- `GET /agent/download.sha256`：agent 二进制的 sha256（sha256sum 格式）；
- `GET /checksums.sha256`：**全部在分发文件**的清单（agent 二进制 +
  boot 资产目录递归），请求时从实物生成；哈希按 (路径, mtime, size)
  缓存，文件一换立即失效——**清单永远等于实物**；
- **装机链强制验证**：late_command 下载后 `sha256sum -c` 验签，验过才
  写 `deplos-agent.verified` 标记；sync 之后、发 install-done 之前用
  `test -f` 门控。顺带修掉我方一个旧毛病：此前下载失败（服务器没配
  --agent-bin 等）也会照常发 install-done 假报成功；现在验不过 =
  不发完成信号 = orchestrator 超时判失败，**装不上就明说装不上**。

**测试**：校验和端点正确性 + 换文件后刷新（`TestAgentChecksumEndpoints`）；
late_command 顺序断言扩展为 下载 < 取校验和 < 验证 < sync < 门控 <
install-done（`internal/api/m6_test.go`）。

### 2-4. 评分文案一致性（治原厂短板 #10，且先治自己）

审计发现我方 `scoring_trend.go` 的 NVMe 趋势 key 把原厂错误文案
（"超过95%"/"超过90%"）逐字抄了进来，而实际阈值是 96/91——**原厂的
毛病在我们代码里活着**。本轮：

- 阈值收成命名常量 `nvmePctUsedHigh=96` / `nvmePctUsedWarn=91`，
  finding key 文案用 `fmt.Sprintf` **由常量生成**（`Percentage_Used≥96%`
  / `≥91%`），文案与阈值从此不可能漂移；
- 新增一致性单测 `internal/collectors/scoring_consistency_test.go`：
  ① 静态规则表全量扫描——每条 cond 文案里出现的 %/°C 数字必须等于该
  cond 的阈值常量；② 趋势规则边界——96/95/91/90 四个边界值的触发与
  文案逐一断言；③ 建议文案边界——197 属性"超过10个"文案只在值 >10
  时出现（10 时不出）、温度 key 在 50 不触发/51 触发；
- 全表复核结论：其余文案（温度三档 75/65/55、percentage_used 100%、
  HDD 建议文案等）与阈值一致；AnalyzeSmart/AnalyzeNvme 的文案只引用
  实测值、不声称阈值，无此风险。

### 2-5. proxmox 定义显式停用（治原厂短板 #12）

`internal/osimage`：`Definition` 新增 `Disabled` 标记（来源文件名
`.bak` 后缀），`Definitions()` 照常加载（目录保真），`Identify()` 跳过
停用定义。回归测试 `TestProxmoxDefinitionDisabled`：proxmox 镜像树
不得再被识别为 proxmox-ve，且仅此一条是 Disabled。

---

## 3. 我们自己的优化扫描

| 扫描点 | 结论 |
|---|---|
| **preseed 模板渲染** | ✅ **已优化**：此前每次 `/preseed/` 请求都 `template.ParseFiles` 读盘解析；改为 (mtime, size) 校验的解析缓存（`Server.preseedTemplate`），改模板自动失效。测试 `TestPreseedTemplateCacheInvalidation` 双向锁定（命中新内容、编辑后刷新） |
| **采集并发** | ✅ **已优化**：`collectors.Collect` 此前全串行——每块盘一次 smartctl（MegaRAID 回退最坏串行 16 次探测）、每 NVMe 控制器串行两段工具调用，采集耗时随设备数线性涨。改为：SMART/NVMe 两路并发 + 各自逐设备有界并发（上限 8），结果按设备序合并、skip 备注由 Collect 统一按固定顺序汇总。`go test -race` 干净 |
| **DHCP 热路径** | 不动。审计：`handlePacket` 单互斥锁 + map 操作，`allocate` 对地址池线性扫描（/24 量级、租约命中走 map），每包一条完整指纹日志是 M6 ROM 排障的有意取证设计。无可测瓶颈，不为优化而优化 |
| **bootscript / GRUB 渲染** | 不动。每请求字符串拼接 + 按 serverURL 渲染，微秒级；加缓存反而引入失效风险 |
| **DB 查询** | 不动。Authenticate/Checkin 都是按 token/host 的单行查询；`handlePreseed` 的 Hosts() 线性查找只在装机期发生（低频），不值得为它加索引路径 |
| **cpuPct 500ms 采样窗** | 不动。CPU 率必须有采样间隔，这是测量语义不是性能问题 |

---

## 4. 没做的及理由

1. **install-done 签名**：装机前环境（d-i late_command）只有 wget，没有
   agent 本体可执行签名逻辑；该端点已有 token 鉴权且只清除 install
   标记。待装机链引入可执行验证器再议。
2. **iPXE 引导资产签名（imgverify）**：需要证书体系与引导 ROM 侧配置，
   成本与当前局域网 PXE 威胁模型不匹配；本轮提供 `/checksums.sha256`
   供运维侧核对，ROM 侧强制验证未做。
3. **非对称签名（Ed25519）替代 HMAC**：HMAC 密钥与 token 同通道一次性
   下发，服务端本就持有 token，"服务端泄漏即全盘伪造"的威胁模型下两者
   等价；非对称的收益（服务端只存公钥）需 agent 侧密钥生成与注册协议
   改造，本轮不做，取舍在此明示。
4. **preseed 口令加密传输**：d-i 无人值守生态限制（短板 #8 残余），
   不在本轮范围。

---

## 5. 改动面与验证

- 新增：`internal/signreq/`（签名包 + 测试）、`internal/db/m11_migrations.go`
  （hosts.sign_key 迁移）、`internal/db/m11_test.go`、
  `internal/api/m11_test.go`、`internal/agent/agent_test.go`、
  `internal/collectors/scoring_consistency_test.go`；
- 修改：`internal/api/server.go`（DTO/验签/nonce 缓存/校验和与模板缓存
  状态）、`internal/api/m3.go`（校验和端点/模板缓存）、
  `internal/agent/agent.go`（签名发送/就绪门）、
  `internal/store/store.go`、`internal/db/db.go`、
  `internal/collectors/collectors.go`（并发采集）、
  `internal/collectors/scoring_trend.go`（阈值常量/文案生成）、
  `internal/osimage/osimage.go`（Disabled）、
  `assets/preseed/debian.cfg.tmpl`（重试/验证/门控）；
- 验证：`go build/vet/test ./... -count=1` 全绿（21 包）；-race 三包干净；
  既有 19 包测试零回退（含 M6 late_command 顺序测试的扩展断言）。
