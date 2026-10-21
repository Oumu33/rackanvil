// M3 web console pages: dashboard (SVG charts), discovery, orchestration,
// license, terminal. Server-rendered, no external JS/CSS dependencies.
package web

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/Oumu33/rackanvil/internal/discovery"
	"github.com/Oumu33/rackanvil/internal/license"
	"github.com/Oumu33/rackanvil/internal/store"
)

// RegisterM3 mounts the M3 pages. licPub may be nil (dev mode).
func (w *Web) RegisterM3(licPub ed25519.PublicKey) {
	w.licPub = licPub
	w.mux.HandleFunc("/dashboard", w.requireLogin(w.handleDashboard))
	w.mux.HandleFunc("/discovery", w.requireLogin(w.handleDiscovery))
	w.mux.HandleFunc("/orch", w.requireLogin(w.handleOrch))
	w.mux.HandleFunc("/orch/", w.requireLogin(w.handleOrchDetail))
	w.mux.HandleFunc("/license", w.requireLogin(w.handleLicense))
	w.mux.HandleFunc("/terminal", w.requireLogin(w.handleTerminal))
	w.mux.HandleFunc("/ws/ssh", w.requireLogin(w.handleWS))
}

// ---- dashboard: SVG history charts ----

func (w *Web) handleDashboard(rw http.ResponseWriter, r *http.Request) {
	hosts := w.tasks.Hosts()
	hostID := r.URL.Query().Get("host")
	if hostID == "" && len(hosts) > 0 {
		hostID = hosts[0].ID
	}
	var chart template.HTML
	var name string
	if hostID != "" {
		for _, h := range hosts {
			if h.ID == hostID {
				name = h.Hostname
			}
		}
		if hist, err := w.db.MetricsHistory(hostID, 120); err == nil && len(hist) > 0 {
			chart = template.HTML(svgHistoryChart(hist))
		}
	}
	w.render(rw, "dashboard", map[string]any{
		"User": w.userOf(r), "Hosts": hosts, "HostID": hostID,
		"HostName": name, "Chart": chart,
	})
}

// svgHistoryChart renders CPU% (blue) and memory-used% (green) polylines.
// Pure SVG, no JS dependency.
func svgHistoryChart(samples []store.MetricSample) string {
	const W, H, pad = 900, 240, 30
	n := len(samples)
	if n == 0 {
		return ""
	}
	x := func(i int) float64 {
		if n == 1 {
			return pad
		}
		return pad + float64(W-2*pad)*float64(i)/float64(n-1)
	}
	y := func(pct float64) float64 {
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		return float64(H-pad) - float64(H-2*pad)*pct/100
	}
	var cpuPts, memPts strings.Builder
	for i, s := range samples {
		memPct := 0.0
		if s.MemTotalMB > 0 {
			memPct = float64(s.MemUsedMB) / float64(s.MemTotalMB) * 100
		}
		fmt.Fprintf(&cpuPts, "%.1f,%.1f ", x(i), y(s.CPUPct))
		fmt.Fprintf(&memPts, "%.1f,%.1f ", x(i), y(memPct))
	}
	first := samples[0].TS.Format("15:04")
	last := samples[n-1].TS.Format("15:04")
	return fmt.Sprintf(`<svg width="%d" height="%d" style="background:#fff;border-radius:8px">
<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#ddd"/>
<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#ddd"/>
<text x="%d" y="14" font-size="11" fill="#888">100%%</text>
<text x="%d" y="%d" font-size="11" fill="#888">0%%</text>
<polyline points="%s" fill="none" stroke="#2563eb" stroke-width="2"/>
<polyline points="%s" fill="none" stroke="#16a34a" stroke-width="2"/>
<text x="%d" y="%d" font-size="11" fill="#888">%s</text>
<text x="%d" y="%d" font-size="11" fill="#888">%s</text>
<text x="%d" y="20" font-size="12"><tspan fill="#2563eb">■</tspan> CPU%% <tspan fill="#16a34a">■</tspan> 内存使用率%%</text>
<text x="%d" y="%d" font-size="12" fill="#555">%d 个采样点</text>
</svg>`,
		W, H,
		pad, pad, W-pad, pad,
		pad, H-pad, W-pad, H-pad,
		4,
		4, H-pad,
		cpuPts.String(), memPts.String(),
		pad, H-8, first,
		W-pad-40, H-8, last,
		W-220,
		pad, H-8, n)
}

// ---- discovery ----

func (w *Web) handleDiscovery(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var result any
	if r.Method == http.MethodPost {
		if !w.canOperate(u) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		cidr := r.FormValue("cidr")
		found, err := discovery.ScanCIDR(cidr, nil, 800*time.Millisecond, 64)
		if err != nil {
			w.render(rw, "discovery", map[string]any{"User": u, "Error": err.Error()})
			return
		}
		var known []discovery.HostRef
		for _, h := range w.tasks.Hosts() {
			known = append(known, discovery.HostRef{ID: h.ID, Hostname: h.Hostname, MACs: h.MACs, IPs: h.IPs})
		}
		diff := discovery.DiffBOM(found, known)
		foundJ, _ := jsonMarshal(found)
		diffJ, _ := jsonMarshal(diff)
		rec, serr := w.db.SaveScan(cidr, foundJ, diffJ)
		if serr != nil {
			w.render(rw, "discovery", map[string]any{"User": u, "Error": serr.Error()})
			return
		}
		_ = w.db.Audit(u.Username, "discovery.scan", cidr)
		result = map[string]any{"ScanID": rec.ID, "Found": found, "Diff": diff}
	}
	latest, _ := w.db.LatestScan()
	w.render(rw, "discovery", map[string]any{"User": u, "Result": result, "Latest": latest})
}

// ---- orchestration ----

func (w *Web) handleOrch(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if r.Method == http.MethodPost {
		if !w.canOperate(u) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		hostID := r.FormValue("host_id")
		name := r.FormValue("name")
		if name == "" {
			name = "交付编排 " + time.Now().Format("15:04:05")
		}
		var steps []store.StepDef
		for _, k := range []string{"bmc_mock", "raid_mock", "stress", "install"} {
			if r.FormValue("step_"+k) == "on" {
				steps = append(steps, store.StepDef{Name: stepName(k), Kind: k})
			}
		}
		j, err := w.db.CreateJob(name, hostID, steps)
		if err != nil {
			w.render(rw, "orch", map[string]any{"User": u, "Error": err.Error(), "Hosts": w.tasks.Hosts()})
			return
		}
		_ = w.db.Audit(u.Username, "orch.create", j.ID+" "+hostID)
		http.Redirect(rw, r, "/orch/"+j.ID, http.StatusFound)
		return
	}
	jobs, _ := w.db.ListJobs(100)
	w.render(rw, "orch", map[string]any{"User": u, "Jobs": jobs, "Hosts": w.tasks.Hosts()})
}

func stepName(k string) string {
	switch k {
	case "bmc_mock":
		return "BMC 强制配置"
	case "raid_mock":
		return "RAID 组建"
	case "stress":
		return "压力测试"
	case "install":
		return "系统安装"
	}
	return k
}

func (w *Web) handleOrchDetail(rw http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/orch/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(rw, r)
		return
	}
	j, steps, err := w.db.GetJob(id)
	if err != nil {
		http.NotFound(rw, r)
		return
	}
	var hostname string
	for _, h := range w.tasks.Hosts() {
		if h.ID == j.HostID {
			hostname = h.Hostname
		}
	}
	w.render(rw, "orch_detail", map[string]any{"User": userOf(r), "Job": j, "Steps": steps, "Hostname": hostname})
}

// ---- license ----

func (w *Web) handleLicense(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	if r.Method == http.MethodPost {
		if !w.canOperate(u) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		if w.licPub == nil {
			w.render(rw, "license", map[string]any{"User": u, "Error": "server 未配置 license 公钥（dev 模式）"})
			return
		}
		text := strings.TrimSpace(r.FormValue("license"))
		lic, err := license.Verify(w.licPub, text)
		if err != nil {
			_ = w.db.Audit(u.Username, "license.import.failed", err.Error())
			w.render(rw, "license", map[string]any{"User": u, "Error": "license 无效: " + err.Error()})
			return
		}
		if _, err := w.db.SaveLicense(lic.Customer, lic.MaxHosts, time.Unix(lic.ExpiresAt, 0), text); err != nil {
			w.render(rw, "license", map[string]any{"User": u, "Error": err.Error()})
			return
		}
		_ = w.db.Audit(u.Username, "license.import", lic.Customer)
		http.Redirect(rw, r, "/license", http.StatusFound)
		return
	}
	info := map[string]any{"Licensed": false, "Mode": "dev 模式（未配置公钥）"}
	if w.licPub != nil {
		if rec, err := w.db.LatestLicense(); err == nil {
			if lic, verr := license.Verify(w.licPub, rec.LicenseTxt); verr == nil {
				info = map[string]any{"Licensed": true, "Customer": lic.Customer,
					"MaxHosts":  lic.MaxHosts,
					"ExpiresAt": time.Unix(lic.ExpiresAt, 0).Format("2006-01-02"),
					"Features":  lic.Features}
			} else {
				info = map[string]any{"Licensed": false, "Mode": "license 无效: " + verr.Error()}
			}
		} else {
			info = map[string]any{"Licensed": false, "Mode": "尚未导入 license"}
		}
	}
	w.render(rw, "license", map[string]any{"User": u, "Info": info})
}

// userOf is a tiny indirection so m3.go stays independent of web.go privates.
func (w *Web) userOf(r *http.Request) *store.User { return userOf(r) }

func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}
