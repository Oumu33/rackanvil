package web

// pageTmpl holds all console templates. Deliberately plain HTML —
// function over form for the M2 MVP.
const pageTmpl = `
{{define "head"}}<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RackAnvil 控制台</title>
<style>
body{font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;max-width:1100px;margin:0 auto;padding:20px;color:#222;background:#f7f7f8}
nav{background:#1a1d24;color:#fff;padding:10px 16px;border-radius:8px;display:flex;gap:18px;align-items:center}
nav a{color:#9fd0ff;text-decoration:none}nav a:hover{text-decoration:underline}
nav .sp{flex:1}nav .u{color:#bbb;font-size:13px}
table{border-collapse:collapse;width:100%;background:#fff;border-radius:8px;overflow:hidden;margin-top:14px}
th,td{text-align:left;padding:9px 12px;border-bottom:1px solid #eee;font-size:14px}
th{background:#f0f2f5}tr:hover td{background:#fafcff}
.card{background:#fff;border-radius:8px;padding:16px;margin-top:14px}
h2{margin:6px 0 4px}.muted{color:#888;font-size:13px}
.badge{display:inline-block;padding:2px 8px;border-radius:10px;font-size:12px}
.ok{background:#e6f7e6;color:#1a7a1a}.warn{background:#fff3d6;color:#9a6b00}.crit{background:#ffe0e0;color:#b00000}
pre{background:#14161c;color:#d6deeb;padding:12px;border-radius:8px;overflow:auto;font-size:13px}
input,select,button{font-size:14px;padding:8px 10px;border-radius:6px;border:1px solid #ccc}
button{background:#2563eb;color:#fff;border:none;cursor:pointer}button:hover{background:#1d4fd7}
form.row{display:flex;gap:8px;margin-top:12px}form.row input[type=text]{flex:1}
.err{background:#ffe0e0;color:#b00000;padding:10px;border-radius:6px;margin:12px 0}
.login{max-width:380px;margin:80px auto}.login input{width:100%;box-sizing:border-box;margin:6px 0}
.login button{width:100%;margin-top:10px}
</style></head><body>{{end}}

{{define "nav"}}<nav><b>RackAnvil</b><a href="/hosts">资产</a><a href="/dashboard">监控</a><a href="/discovery">发现</a><a href="/orch">编排</a><a href="/terminal">终端</a><a href="/license">许可</a><a href="/locator">点灯</a><a href="/topology">拓扑</a><a href="/firmware">固件</a><a href="/raid">RAID</a><a href="/cfgarchive">存档</a><a href="/relay">代理</a><span class="sp"></span>
<span class="u">{{.User.Username}} ({{.User.Role}})</span><a href="/logout">退出</a></nav>{{end}}

{{define "login"}}{{template "head"}}<div class="login card">
<h2>RackAnvil 控制台</h2>{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<form method="post" action="/login">
<input type="text" name="username" placeholder="用户名" autocomplete="username">
<input type="password" name="password" placeholder="密码" autocomplete="current-password">
<button type="submit">登录</button></form></div></body></html>{{end}}

{{define "hosts"}}{{template "head"}}{{template "nav" .}}
<h2>资产列表</h2><div class="muted">共 {{len .Hosts}} 台主机</div>
<table><tr><th>主机名</th><th>ID</th><th>MAC</th><th>IP</th><th>CPU</th><th>内存</th><th>上次心跳</th></tr>
{{range .Hosts}}<tr>
<td><a href="/hosts/{{.ID}}">{{.Hostname}}</a></td>
<td class="muted">{{.ID}}</td>
<td class="muted">{{range .MACs}}{{.}} {{end}}</td>
<td class="muted">{{range .IPs}}{{.}} {{end}}</td>
<td>{{.CPUModel}}</td><td>{{.MemoryMB}} MB</td><td>{{.LastCheckinAgo}}</td>
</tr>{{end}}</table></body></html>{{end}}

{{define "host_detail"}}{{template "head"}}{{template "nav" .}}
<h2>{{.Host.Hostname}}</h2>
<div class="card">
<div><b>ID:</b> <span class="muted">{{.Host.ID}}</span></div>
<div><b>MAC:</b> {{range .Host.MACs}}{{.}} {{end}}</div>
<div><b>IP:</b> {{range .Host.IPs}}{{.}} {{end}}</div>
<div><b>CPU:</b> {{.Host.CPUModel}} ｜ <b>内存:</b> {{.Host.MemoryMB}} MB</div>
{{if .Metrics}}<div><b>最新采集:</b> CPU {{printf "%.1f" .Metrics.CPUPct}}% ｜ 内存 {{.Metrics.MemUsedMB}}/{{.Metrics.MemTotalMB}} MB</div>{{end}}
</div>
{{if .Alarms}}<div class="card"><h3>活跃告警</h3>
{{range .Alarms}}<div><span class="badge {{if eq .Severity "critical"}}crit{{else}}warn{{end}}">{{.Severity}}</span>
<b>{{.Kind}}</b> — {{.Message}}</div>{{end}}</div>{{end}}
<div class="card"><h3>下发任务</h3>
<form class="row" method="post" action="/tasks">
<input type="hidden" name="host_id" value="{{.Host.ID}}">
<input type="text" name="command" placeholder="shell 命令，如 uptime">
<button type="submit">下发</button></form></div>
<div class="card"><h3>任务记录</h3>
<table><tr><th>任务</th><th>命令</th><th>状态</th><th>退出码</th><th>时间</th></tr>
{{range .Tasks}}<tr><td><a href="/tasks/{{.ID}}">{{.ID}}</a></td><td><code>{{.Command}}</code></td>
<td><span class="badge {{if eq .Status "done"}}ok{{else if eq .Status "failed"}}crit{{else}}warn{{end}}">{{.Status}}</span></td>
<td>{{.ExitCode}}</td><td class="muted">{{.UpdatedAt.Format "15:04:05"}}</td></tr>{{end}}
</table></div></body></html>{{end}}

{{define "task_detail"}}{{template "head"}}{{template "nav" .}}
<h2>任务 {{.Task.ID}}</h2>
<div class="card">
<div><b>命令:</b> <code>{{.Task.Command}}</code></div>
<div><b>状态:</b> <span class="badge {{if eq .Task.Status "done"}}ok{{else if eq .Task.Status "failed"}}crit{{else}}warn{{end}}">{{.Task.Status}}</span>
<b>退出码:</b> {{.Task.ExitCode}}</div>
{{if .Task.Error}}<div><b>错误:</b> {{.Task.Error}}</div>{{end}}
<h3>stdout</h3><pre>{{.Task.Stdout}}</pre>
<h3>stderr</h3><pre>{{.Task.Stderr}}</pre>
</div></body></html>{{end}}

{{define "dashboard"}}{{template "head"}}{{template "nav" .}}
<h2>监控看板</h2>
<form class="row" method="get" action="/dashboard"><select name="host">
{{range .Hosts}}<option value="{{.ID}}" {{if eq .ID $.HostID}}selected{{end}}>{{.Hostname}}</option>{{end}}
</select><button type="submit">查看</button></form>
{{if .Chart}}<div class="card"><h3>{{.HostName}} — 最近120个采样</h3>{{.Chart}}</div>
{{else}}<div class="card muted">暂无采集数据（agent 上报后显示）</div>{{end}}
</body></html>{{end}}

{{define "discovery"}}{{template "head"}}{{template "nav" .}}
<h2>网段发现</h2>
<div class="card"><form class="row" method="post" action="/discovery">
<input type="text" name="cidr" placeholder="如 192.168.1.0/24（TCP 探测 + ARP 表，不用 ICMP）">
<button type="submit">开始扫描</button></form>
<div class="muted">原理：TCP 连接探测常见端口 + 内核 ARP 表解析 MAC；沙箱等受限环境仅回环网段可见。</div></div>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
{{if .Result}}<div class="card"><h3>扫描结果（{{.Result.ScanID}}）</h3>
<h4>存活主机（{{len .Result.Found}}）</h4>
<table><tr><th>IP</th><th>MAC</th><th>主机名</th><th>开放端口</th></tr>
{{range .Result.Found}}<tr><td>{{.IP}}</td><td class="muted">{{.MAC}}</td><td>{{.Hostname}}</td><td>{{range .Ports}}{{.}} {{end}}</td></tr>{{end}}</table>
<h4>BOM 比对（{{len .Result.Diff}}）</h4>
<table><tr><th>类型</th><th>详情</th></tr>
{{range .Result.Diff}}<tr><td><span class="badge {{if eq .Kind "new"}}warn{{else if eq .Kind "missing"}}crit{{else}}ok{{end}}">{{.Kind}}</span></td><td>{{.Detail}}</td></tr>{{end}}</table>
</div>{{end}}
{{if .Latest}}<div class="card muted">上次扫描：{{.Latest.CIDR}}（{{.Latest.ID}}）</div>{{end}}
</body></html>{{end}}

{{define "orch"}}{{template "head"}}{{template "nav" .}}
<h2>交付编排</h2>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="card"><h3>新建编排任务</h3>
<form method="post" action="/orch">
<div class="row"><input type="text" name="name" placeholder="任务名（可选）">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select></div>
<div style="margin-top:8px">
<label><input type="checkbox" name="step_bmc_mock" checked> BMC 强制配置（无硬件=mock）</label><br>
<label><input type="checkbox" name="step_raid_mock" checked> RAID 组建（无硬件=mock）</label><br>
<label><input type="checkbox" name="step_stress" checked> 压力测试（agent 真实执行）</label><br>
<label><input type="checkbox" name="step_install" checked> 系统安装（preseed，需目标机 PXE 重启）</label>
</div>
<button type="submit" style="margin-top:10px">创建并执行</button></form>
<div class="muted">BMC/RAID 步骤在无硬件环境走 mock（步骤详情会明确标注）；压力测试与系统安装是真实执行。</div></div>
<div class="card"><h3>任务列表</h3>
<table><tr><th>任务</th><th>名称</th><th>主机</th><th>状态</th><th>创建时间</th></tr>
{{range .Jobs}}<tr><td><a href="/orch/{{.ID}}">{{.ID}}</a></td><td>{{.Name}}</td><td class="muted">{{.HostID}}</td>
<td><span class="badge {{if eq .Status "done"}}ok{{else if eq .Status "failed"}}crit{{else}}warn{{end}}">{{.Status}}</span></td>
<td class="muted">{{.CreatedAt.Format "15:04:05"}}</td></tr>{{end}}</table></div>
</body></html>{{end}}

{{define "orch_detail"}}{{template "head"}}{{template "nav" .}}
<h2>编排任务 {{.Job.ID}}</h2>
<div class="card"><div><b>名称:</b> {{.Job.Name}} ｜ <b>主机:</b> {{.Hostname}} <span class="muted">{{.Job.HostID}}</span></div>
<div><b>状态:</b> <span class="badge {{if eq .Job.Status "done"}}ok{{else if eq .Job.Status "failed"}}crit{{else}}warn{{end}}">{{.Job.Status}}</span></div></div>
<div class="card"><h3>步骤</h3>
<table><tr><th>#</th><th>步骤</th><th>类型</th><th>状态</th><th>详情</th></tr>
{{range .Steps}}<tr><td>{{.Seq}}</td><td>{{.Name}}</td><td class="muted">{{.Kind}}</td>
<td><span class="badge {{if eq .Status "done"}}ok{{else if eq .Status "failed"}}crit{{else}}warn{{end}}">{{.Status}}</span></td>
<td class="muted">{{.Detail}}</td></tr>{{end}}</table></div>
</body></html>{{end}}

{{define "license"}}{{template "head"}}{{template "nav" .}}
<h2>许可管理</h2>
<div class="card">
{{if .Info.Licensed}}<div><span class="badge ok">已授权</span></div>
<div><b>客户:</b> {{.Info.Customer}} ｜ <b>最大主机数:</b> {{.Info.MaxHosts}} ｜ <b>到期:</b> {{.Info.ExpiresAt}}</div>
<div class="muted">功能：{{range .Info.Features}}{{.}} {{end}}</div>
{{else}}<div><span class="badge warn">未授权</span> <span class="muted">{{.Info.Mode}}</span></div>{{end}}
</div>
<div class="card"><h3>导入 license</h3>
<form method="post" action="/license">
<input type="text" name="license" placeholder="粘贴 license 文件内容（base64url.payload.base64url.sig）" style="width:100%">
<button type="submit" style="margin-top:8px">导入</button></form>
<div class="muted">自研 Ed25519 体系，与原厂 RSA 体系不互通；密钥对用 license 包自签发。</div></div>
</body></html>{{end}}

{{define "terminal"}}{{template "head"}}{{template "nav" .}}
<h2>WebSSH 终端</h2>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<div class="card"><form method="post" action="/terminal">
<div class="row"><input type="text" name="target" placeholder="目标 host:port，如 127.0.0.1:22">
<input type="text" name="user" placeholder="用户名"></div>
<div class="row"><input type="password" name="password" placeholder="密码（仅本次连接使用，不存储）" style="flex:1">
<button type="submit">连接</button></div></form>
<div class="muted">MVP 说明：主机密钥暂不校验（known_hosts 后续补）；密码经一次性 token 传递，60 秒过期，不落盘。</div></div>
</body></html>{{end}}

{{define "terminal_session"}}{{template "head"}}{{template "nav" .}}
<h2>终端 — {{.Target}} ({{.User2}})</h2>
<div class="card"><pre id="out" style="height:420px"></pre>
<form class="row" id="f"><input type="text" id="in" autocomplete="off" placeholder="输入命令回车发送" style="flex:1"><button type="submit">发送</button></form></div>
<script>
var ws = new WebSocket("ws://"+location.host+"/ws/ssh?token={{.Token}}");
var out = document.getElementById("out");
ws.onmessage = function(e){ out.textContent += e.data; out.scrollTop = out.scrollHeight; };
ws.onclose = function(){ out.textContent += "\r\n[连接已关闭]\r\n"; };
document.getElementById("f").onsubmit = function(e){
  e.preventDefault();
  var v = document.getElementById("in").value;
  ws.send(v+"\n"); document.getElementById("in").value="";
};
</script></body></html>{{end}}
`
