package web

// M4 console page templates (appended to pageTmpl at render time).
const m4Tmpl = `
{{define "locator"}}{{template "head"}}{{template "nav" .}}
<h2>硬盘定位（点灯）</h2>{{if .Msg}}<div class="card">{{.Msg}}</div>{{end}}
<div class="card"><h3>扫描磁盘</h3>
<form method="post" action="/locator"><input type="hidden" name="action" value="scan">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<button type="submit">扫描</button></form>
<div class="muted">agent 执行 disk.scan，结果见任务详情（JSON 磁盘列表）。</div></div>
<div class="card"><h3>点亮 / 熄灭</h3>
<form method="post" action="/locator">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<input type="text" name="device" placeholder="/dev/sda">
<input type="text" name="tool" placeholder="工具(ledctl/sg_ses/storcli，留空自动)">
<button type="submit" name="action" value="on">点亮</button>
<button type="submit" name="action" value="off">熄灭</button></form></div>
</body></html>{{end}}

{{define "topology"}}{{template "head"}}{{template "nav" .}}
<h2>网络拓扑（MAC → 交换机端口）</h2>{{if .Msg}}<div class="card">{{.Msg}}</div>{{end}}
<div class="card"><h3>采集 LLDP 邻居</h3>
<form method="post" action="/topology"><select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<button type="submit">采集</button></form>
<div class="muted">需主机运行 lldpd；或通过 API /api/v1/topology/fdb-import 导入交换机 FDB 表（ARP 联动）。</div></div>
<div class="card"><h3>上联映射</h3>
<table><tr><th>主机</th><th>网卡</th><th>交换机</th><th>端口</th><th>来源</th></tr>
{{range .Links}}<tr><td class="muted">{{.HostID}}</td><td>{{.Iface}}</td><td>{{.SwitchName}}</td><td>{{.SwitchPort}}</td><td class="muted">{{.Source}}</td></tr>{{end}}</table></div>
</body></html>{{end}}

{{define "firmware"}}{{template "head"}}{{template "nav" .}}
<h2>固件升级（RAID/HBA/网卡）</h2>{{if .Msg}}<div class="card">{{.Msg}}</div>{{end}}
<div class="err">高风险操作：刷错固件会变砖。升级默认被 agent 拒绝，需 agent 侧 RACKANVIL_ALLOW_FW_UPGRADE=1 且表单确认。</div>
<div class="card"><h3>检测设备</h3>
<form method="post" action="/firmware"><input type="hidden" name="action" value="detect">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<button type="submit">检测</button></form></div>
<div class="card"><h3>升级固件</h3>
<form method="post" action="/firmware"><input type="hidden" name="action" value="upgrade">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<input type="text" name="model" placeholder="型号(如MegaRAID_SAS_9361_8i)">
<input type="text" name="pci_addr" placeholder="PCI地址">
<input type="text" name="fw_file" placeholder="固件文件路径">
<label><input type="checkbox" name="confirm" value="yes"> 我确认风险</label>
<button type="submit">升级</button></form></div>
</body></html>{{end}}

{{define "raid"}}{{template "head"}}{{template "nav" .}}
<h2>RAID 管理</h2>{{if .Msg}}<div class="card">{{.Msg}}</div>{{end}}
<div class="card"><h3>查询控制器/VD</h3>
<form method="post" action="/raid"><input type="hidden" name="action" value="query">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<button type="submit">查询</button></form></div>
<div class="card"><h3>创建 VD（破坏性）</h3>
<form method="post" action="/raid"><input type="hidden" name="action" value="create">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}">{{.Hostname}}</option>{{end}}</select>
<input type="text" name="controller" placeholder="控制器ID(0)" size="6">
<select name="level"><option>0</option><option>1</option><option>5</option><option>10</option></select>
<input type="text" name="drives" placeholder="盘位(252:0,252:1)">
<button type="submit">创建</button></form></div>
</body></html>{{end}}

{{define "cfgarchive"}}{{template "head"}}{{template "nav" .}}
<h2>配置存档（一键恢复）</h2>{{if .Msg}}<div class="card">{{.Msg}}</div>{{end}}
<div class="card"><h3>新建快照</h3>
<form method="post" action="/cfgarchive"><input type="hidden" name="action" value="snapshot">
<select name="host_id">{{range .Hosts}}<option value="{{.ID}}"{{if eq .ID $.HostID}} selected{{end}}>{{.Hostname}}</option>{{end}}</select>
<button type="submit">快照</button></form>
<div class="muted">快照含 RAID 布局、分区表、网络配置；完成后用 API /api/v1/cfgarchive/save 归档任务结果。</div></div>
<div class="card"><h3>快照列表</h3>
<table><tr><th>ID</th><th>时间</th><th>备注</th></tr>
{{range .Snaps}}<tr><td class="muted">{{.ID}}</td><td class="muted">{{.CreatedAt}}</td><td>{{.Note}}</td></tr>{{end}}</table></div>
</body></html>{{end}}

{{define "relay"}}{{template "head"}}{{template "nav" .}}
<h2>跨机房代理节点</h2>
<div class="card"><div class="muted">代理节点提供本地 bootscript/镜像缓存 + PXE 服务，心跳上报主控。部署见 docs/M4-REPORT.md。</div>
<table><tr><th>节点</th><th>网段</th><th>上次心跳</th></tr>
{{range .Nodes}}<tr><td>{{.Name}}</td><td class="muted">{{.Subnets}}</td><td class="muted">{{.LastSeen}}</td></tr>{{end}}</table></div>
</body></html>{{end}}
`
