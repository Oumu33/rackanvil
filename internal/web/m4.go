package web

// M4 console pages: locator, topology, firmware, raid, cfgarchive, relay.

import (
	"encoding/json"
	"net/http"

	"github.com/Oumu33/rackanvil/internal/store"
)

// RegisterM4 mounts the M4 pages.
func (w *Web) RegisterM4() {
	w.mux.HandleFunc("/locator", w.requireLogin(w.handleLocator))
	w.mux.HandleFunc("/topology", w.requireLogin(w.handleTopology))
	w.mux.HandleFunc("/firmware", w.requireLogin(w.handleFirmware))
	w.mux.HandleFunc("/raid", w.requireLogin(w.handleRaid))
	w.mux.HandleFunc("/cfgarchive", w.requireLogin(w.handleCfg))
	w.mux.HandleFunc("/relay", w.requireLogin(w.handleRelay))
}

func (w *Web) hostsForSelect() []*store.Host { return w.tasks.Hosts() }

// ---- locator ----

func (w *Web) handleLocator(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var msg string
	if r.Method == http.MethodPost && w.canOperate(u) {
		hostID := r.FormValue("host_id")
		action := r.FormValue("action")
		switch action {
		case "scan":
			t, err := w.tasks.EnqueueTypedTask(hostID, "disk.scan", "{}", 120)
			msg = taskMsg(t, err, "扫描")
		case "on", "off":
			payload, _ := json.Marshal(map[string]any{
				"device": r.FormValue("device"), "tool": r.FormValue("tool"),
				"on": action == "on",
			})
			t, err := w.tasks.EnqueueTypedTask(hostID, "disk.locate", string(payload), 120)
			msg = taskMsg(t, err, "点灯")
		}
		_ = w.db.Audit(u.Username, "locator."+action, hostID)
	}
	w.render(rw, "locator", map[string]any{"User": u, "Hosts": w.hostsForSelect(), "Msg": msg})
}

func taskMsg(t *store.Task, err error, what string) string {
	if err != nil {
		return what + "下发失败: " + err.Error()
	}
	return what + "任务已下发: " + t.ID + "（agent 下次心跳执行，结果见任务详情）"
}

// ---- topology ----

func (w *Web) handleTopology(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var msg string
	if r.Method == http.MethodPost && w.canOperate(u) {
		t, err := w.tasks.EnqueueTypedTask(r.FormValue("host_id"), "topo.collect", "{}", 120)
		msg = taskMsg(t, err, "拓扑采集")
		_ = w.db.Audit(u.Username, "topology.collect", r.FormValue("host_id"))
	}
	links, _ := w.db.ListTopoLinks()
	w.render(rw, "topology", map[string]any{"User": u, "Hosts": w.hostsForSelect(), "Links": links, "Msg": msg})
}

// ---- firmware ----

func (w *Web) handleFirmware(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var msg string
	if r.Method == http.MethodPost && w.canOperate(u) {
		hostID := r.FormValue("host_id")
		if r.FormValue("action") == "detect" {
			t, err := w.tasks.EnqueueTypedTask(hostID, "firmware.detect", "{}", 120)
			msg = taskMsg(t, err, "固件检测")
		} else {
			payload, _ := json.Marshal(map[string]any{
				"model": r.FormValue("model"), "pci_addr": r.FormValue("pci_addr"),
				"fw_file": r.FormValue("fw_file"), "i_understand_brick_risk": r.FormValue("confirm") == "yes",
			})
			t, err := w.tasks.EnqueueTypedTask(hostID, "firmware.upgrade", string(payload), 900)
			msg = taskMsg(t, err, "固件升级") + "（agent 侧仍需 RACKANVIL_ALLOW_FW_UPGRADE=1，否则拒绝执行）"
		}
		_ = w.db.Audit(u.Username, "firmware."+r.FormValue("action"), hostID)
	}
	w.render(rw, "firmware", map[string]any{"User": u, "Hosts": w.hostsForSelect(), "Msg": msg})
}

// ---- raid ----

func (w *Web) handleRaid(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var msg string
	if r.Method == http.MethodPost && w.canOperate(u) {
		hostID := r.FormValue("host_id")
		if r.FormValue("action") == "query" {
			t, err := w.tasks.EnqueueTypedTask(hostID, "raid.query", "{}", 120)
			msg = taskMsg(t, err, "RAID 查询")
		} else {
			cmd := "storcli /c" + r.FormValue("controller") + " add vd type=raid" +
				r.FormValue("level") + " drives=" + r.FormValue("drives")
			t, err := w.tasks.EnqueueTask(hostID, cmd, 600)
			msg = taskMsg(t, err, "RAID 创建")
		}
		_ = w.db.Audit(u.Username, "raid."+r.FormValue("action"), hostID)
	}
	w.render(rw, "raid", map[string]any{"User": u, "Hosts": w.hostsForSelect(), "Msg": msg})
}

// ---- cfgarchive ----

func (w *Web) handleCfg(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	var msg string
	hostID := r.FormValue("host_id")
	if hostID == "" && len(w.hostsForSelect()) > 0 {
		hostID = w.hostsForSelect()[0].ID
	}
	if r.Method == http.MethodPost && w.canOperate(u) {
		switch r.FormValue("action") {
		case "snapshot":
			t, err := w.tasks.EnqueueTypedTask(hostID, "cfg.snapshot", "{}", 180)
			msg = taskMsg(t, err, "配置快照") + "（完成后在快照列表页归档）"
		}
		_ = w.db.Audit(u.Username, "cfgarchive.snapshot", hostID)
	}
	snaps, _ := w.db.ListCfgSnapshots(hostID)
	w.render(rw, "cfgarchive", map[string]any{
		"User": u, "Hosts": w.hostsForSelect(), "HostID": hostID, "Snaps": snaps, "Msg": msg,
	})
}

// ---- relay ----

func (w *Web) handleRelay(rw http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	nodes, _ := w.db.ListRelayNodes()
	w.render(rw, "relay", map[string]any{"User": u, "Nodes": nodes})
}
