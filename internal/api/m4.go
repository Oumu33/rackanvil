package api

// M4 routes: locator, cfgarchive, topology, relay nodes, firmware, raid,
// WinPE assets, task result polling.
//
// Auth model follows the existing agent-facing API: host token auth where a
// host acts on itself; operator-initiated dispatch goes through the Web
// console (login+RBAC) which enqueues typed tasks via the same store.
// The /debug/* hooks remain the unauthenticated escape hatch from M1.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/Oumu33/rackanvil/internal/cfgarchive"
	"github.com/Oumu33/rackanvil/internal/raid"
	"github.com/Oumu33/rackanvil/internal/store"
	"github.com/Oumu33/rackanvil/internal/topology"
)

// M4Store is the persistence surface for M4 features. *db.DB implements it;
// the M1 in-memory store does not (handlers return 503 then).
type M4Store interface {
	SaveCfgSnapshot(s store.CfgSnapshot) (*store.CfgSnapshot, error)
	ListCfgSnapshots(hostID string) ([]*store.CfgSnapshot, error)
	GetCfgSnapshot(id string) (*store.CfgSnapshot, error)
	SaveTopoLinks(links []store.TopoLink) error
	ListTopoLinks() ([]*store.TopoLink, error)
	RegisterRelayNode(name, subnets string) error
	ListRelayNodes() ([]*store.RelayNode, error)
}

func (s *Server) m4store() M4Store {
	ms, ok := s.mstore.(M4Store)
	if !ok {
		return nil
	}
	return ms
}

func (s *Server) requireM4(w http.ResponseWriter) M4Store {
	ms := s.m4store()
	if ms == nil {
		writeError(w, http.StatusServiceUnavailable, "M4 persistence not configured (needs DB backend)")
		return nil
	}
	return ms
}

func (s *Server) registerM4() {
	s.mux.HandleFunc("/api/v1/tasks/", s.handleTaskGet)
	s.mux.HandleFunc("/api/v1/locator/scan", s.handleLocatorScan)
	s.mux.HandleFunc("/api/v1/locator/locate", s.handleLocatorLocate)
	s.mux.HandleFunc("/api/v1/cfgarchive/snapshot", s.handleCfgSnapshot)
	s.mux.HandleFunc("/api/v1/cfgarchive/save", s.handleCfgSave)
	s.mux.HandleFunc("/api/v1/cfgarchive/list", s.handleCfgList)
	s.mux.HandleFunc("/api/v1/cfgarchive/restore", s.handleCfgRestore)
	s.mux.HandleFunc("/api/v1/topology/collect", s.handleTopoCollect)
	s.mux.HandleFunc("/api/v1/topology/links", s.handleTopoLinks)
	s.mux.HandleFunc("/api/v1/topology/fdb-import", s.handleTopoFDBImport)
	s.mux.HandleFunc("/api/v1/relay/nodes", s.handleRelayNodes)
	s.mux.HandleFunc("/api/v1/firmware/detect", s.handleFirmwareDetect)
	s.mux.HandleFunc("/api/v1/firmware/upgrade", s.handleFirmwareUpgrade)
	s.mux.HandleFunc("/api/v1/raid/query", s.handleRaidQuery)
	s.mux.HandleFunc("/api/v1/raid/create", s.handleRaidCreate)
	s.mux.HandleFunc("/winpe/boot.ipxe", s.handleWinPEBoot)
	// M7: vendor-verbatim route for winpeshl.ini (vendor server binary:
	// "/api/v1/ipxe/win/winpeshl.ini", handler handleWinPESHL).
	s.mux.HandleFunc("/api/v1/ipxe/win/winpeshl.ini", s.handleWinPESHL)
	// M9: JIT-generated install.bat (vendor route, handleWinInstallBat).
	s.mux.HandleFunc("/api/v1/ipxe/win/install.bat", s.handleWinInstallBat)
	s.mux.HandleFunc("/winpe/autounattend.xml", s.handleWinPEUnattend)
	s.mux.HandleFunc("/winpe/", s.handleWinPE)
}

// POST {"host_id":...} -> enqueue disk.scan
func (s *Server) handleLocatorScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID string `json:"host_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.EnqueueTypedTask(req.HostID, "disk.scan", "{}", 120)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// POST {"host_id":...,"device":"/dev/sda","tool":"ledctl","on":true}
func (s *Server) handleLocatorLocate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID    string `json:"host_id"`
		Device    string `json:"device"`
		Enclosure string `json:"enclosure"`
		Tool      string `json:"tool"`
		On        bool   `json:"on"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"device": req.Device, "enclosure": req.Enclosure, "tool": req.Tool, "on": req.On,
	})
	t, err := s.store.EnqueueTypedTask(req.HostID, "disk.locate", string(payload), 120)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	s.logger.Printf("locator: host=%s device=%s on=%v", req.HostID, req.Device, req.On)
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// GET /api/v1/tasks/{id} — poll a task result (scan/query JSON lands in stdout).
func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "task id required")
		return
	}
	t, err := s.store.GetTask(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// POST {"host_id":...} — enqueue cfg.snapshot; the agent's stdout JSON is
// archived by POST /api/v1/cfgarchive/list polling + save (see below).
func (s *Server) handleCfgSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID string `json:"host_id"`
		Note   string `json:"note"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.EnqueueTypedTask(req.HostID, "cfg.snapshot", "{}", 180)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID, "note": "poll task stdout, then POST /api/v1/cfgarchive/save"})
}

// POST {"task_id":...,"note":...} — archive a completed cfg.snapshot result.
func (s *Server) handleCfgSave(w http.ResponseWriter, r *http.Request) {
	ms := s.requireM4(w)
	if ms == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		TaskID string `json:"task_id"`
		Note   string `json:"note"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.GetTask(req.TaskID)
	if err != nil || t.Status != "done" {
		writeError(w, http.StatusBadRequest, "task not done")
		return
	}
	var snap cfgarchive.Snapshot
	if err := json.Unmarshal([]byte(t.Stdout), &snap); err != nil {
		writeError(w, http.StatusBadRequest, "task stdout is not a snapshot: "+err.Error())
		return
	}
	saved, err := ms.SaveCfgSnapshot(store.CfgSnapshot{
		HostID: t.HostID, Note: req.Note,
		RAID: snap.RAID, Partition: snap.Partition, Network: snap.Network, Packages: snap.Packages,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"snapshot_id": saved.ID})
}

// GET /api/v1/cfgarchive/list?host_id=
func (s *Server) handleCfgList(w http.ResponseWriter, r *http.Request) {
	ms := s.requireM4(w)
	if ms == nil {
		return
	}
	snaps, err := ms.ListCfgSnapshots(r.URL.Query().Get("host_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snaps)
}

// POST {"snapshot_id":...,"confirm":true} — build restore plan, enqueue as shell.
func (s *Server) handleCfgRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	ms := s.requireM4(w)
	if ms == nil {
		return
	}
	var req struct {
		SnapshotID string `json:"snapshot_id"`
		Confirm    bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	snap, err := ms.GetCfgSnapshot(req.SnapshotID)
	if err != nil {
		writeError(w, http.StatusNotFound, "snapshot not found")
		return
	}
	steps, manual, err := cfgarchive.RestorePlan(cfgarchive.Snapshot{
		Partition: snap.Partition, RAID: snap.RAID, Network: snap.Network,
	}, req.Confirm)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.store.EnqueueTask(snap.HostID, "set -e\n"+strings.Join(steps, "\n"), 600)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	s.logger.Printf("cfgarchive: restore snapshot=%s host=%s task=%s", snap.ID, snap.HostID, t.ID)
	writeJSON(w, http.StatusOK, map[string]any{"task_id": t.ID, "manual": manual})
}

// POST {"host_id":...} -> enqueue topo.collect
func (s *Server) handleTopoCollect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID string `json:"host_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.EnqueueTypedTask(req.HostID, "topo.collect", "{}", 120)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// GET /api/v1/topology/links
func (s *Server) handleTopoLinks(w http.ResponseWriter, r *http.Request) {
	ms := s.requireM4(w)
	if ms == nil {
		return
	}
	links, err := ms.ListTopoLinks()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, links)
}

// POST {"host_id":...,"entries":[{"switch_name":...,"port":...,"mac":...,"vlan":...}]}
// — the "ARP 与 FDB 表联动" import path from the vendor doc.
func (s *Server) handleTopoFDBImport(w http.ResponseWriter, r *http.Request) {
	ms := s.requireM4(w)
	if ms == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID  string              `json:"host_id"`
		MACs    []string            `json:"macs"`
		Entries []topology.FDBEntry `json:"entries"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	got := topology.ResolveByFDB(req.HostID, req.MACs, req.Entries)
	var slinks []store.TopoLink
	for _, l := range got {
		slinks = append(slinks, store.TopoLink{
			HostID: l.HostID, HostMAC: l.HostMAC, Iface: l.Iface,
			SwitchName: l.SwitchName, SwitchPort: l.SwitchPort, Source: l.Source,
		})
	}
	if err := ms.SaveTopoLinks(slinks); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"resolved": len(slinks)})
}

// /api/v1/relay/nodes — POST register, GET list.
func (s *Server) handleRelayNodes(w http.ResponseWriter, r *http.Request) {
	ms := s.requireM4(w)
	if ms == nil {
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req struct {
			Name    string   `json:"name"`
			Subnets []string `json:"subnets"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		sub, _ := json.Marshal(req.Subnets)
		if err := ms.RegisterRelayNode(req.Name, string(sub)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": req.Name})
	case http.MethodGet:
		nodes, err := ms.ListRelayNodes()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nodes)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET/POST only")
	}
}

// POST {"host_id":...} -> enqueue firmware.detect
func (s *Server) handleFirmwareDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID string `json:"host_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.EnqueueTypedTask(req.HostID, "firmware.detect", "{}", 120)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// POST {"host_id":...,"model":...,"pci_addr":...,"fw_file":...,"confirm":true}
// Upgrade is double-gated: confirm=true here AND RACKANVIL_ALLOW_FW_UPGRADE=1
// on the agent. Default: refused.
func (s *Server) handleFirmwareUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID  string `json:"host_id"`
		Model   string `json:"model"`
		PCIAddr string `json:"pci_addr"`
		FwFile  string `json:"fw_file"`
		Confirm bool   `json:"confirm"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "firmware upgrade requires confirm=true (bricking risk)")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"model": req.Model, "pci_addr": req.PCIAddr, "fw_file": req.FwFile,
		"i_understand_brick_risk": true,
	})
	t, err := s.store.EnqueueTypedTask(req.HostID, "firmware.upgrade", string(payload), 900)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	s.logger.Printf("firmware: UPGRADE queued host=%s model=%s (agent-side gate still applies)", req.HostID, req.Model)
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// POST {"host_id":...} -> enqueue raid.query
func (s *Server) handleRaidQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID string `json:"host_id"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.EnqueueTypedTask(req.HostID, "raid.query", "{}", 120)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// POST {"host_id":...,"controller":"0","level":"1","drives":["252:0","252:1"],"confirm":true}
func (s *Server) handleRaidCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		HostID     string   `json:"host_id"`
		Controller string   `json:"controller"`
		Level      string   `json:"level"`
		Drives     []string `json:"drives"`
		Confirm    bool     `json:"confirm"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Confirm {
		writeError(w, http.StatusBadRequest, "RAID creation is destructive: confirm=true required")
		return
	}
	cmd, err := raid.CreateVD(req.Controller, req.Level, req.Drives)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t, err := s.store.EnqueueTask(req.HostID, strings.Join(cmd, " "), 600)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	s.logger.Printf("raid: create VD host=%s ctrl=%s raid%s", req.HostID, req.Controller, req.Level)
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

// handleWinPEBoot renders the WinPE iPXE chain per host (?mac=).
// The boot.wim itself is never bundled (see assets/winpe/README.md).
func (s *Server) handleWinPEBoot(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	tmplPath := filepath.Join(s.assetDir, "winpe", "boot.ipxe.tmpl")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "winpe template not provisioned")
		return
	}
	s.logger.Printf("winpe: boot.ipxe mac=%s from=%s", mac, r.RemoteAddr)
	w.Header().Set("Content-Type", "text/plain")
	_ = tmpl.Execute(w, map[string]string{"MAC": mac, "Server": s.serverURL})
}

// handleWinPESHL renders winpeshl.ini (M7; body recovered verbatim in
// M8). Vendor chain: this file is served by the server and loaded by
// iPXE as an initrd so WinPE's winpeshl.exe consumes it at boot; its
// whole body is "[LaunchApp] / AppPath =
// %SYSTEMDRIVE%\Windows\System32\install.bat" — the SMB mapping
// (net use Z:) happens inside the JIT-generated install.bat, not here.
// The smb_server/smb_share query parameters are accepted for operator
// compatibility but no longer used by the template.
func (s *Server) handleWinPESHL(w http.ResponseWriter, r *http.Request) {
	tmplPath := filepath.Join(s.assetDir, "winpe", "winpeshl.ini.tmpl")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "winpeshl template not provisioned")
		return
	}
	q := r.URL.Query()
	smbServer := q.Get("smb_server")
	if smbServer == "" {
		smbServer = strings.TrimPrefix(strings.TrimPrefix(s.serverURL, "https://"), "http://")
		if i := strings.IndexAny(smbServer, ":/"); i >= 0 {
			smbServer = smbServer[:i]
		}
	}
	smbShare := q.Get("smb_share")
	if smbShare == "" {
		smbShare = "winpe"
	}
	s.logger.Printf("winpe: winpeshl.ini mac=%s from=%s", q.Get("mac"), r.RemoteAddr)
	w.Header().Set("Content-Type", "text/plain")
	_ = tmpl.Execute(w, map[string]string{
		"MAC": q.Get("mac"), "Server": s.serverURL,
		"SMBServer": smbServer, "SMBShare": smbShare,
	})
}

// handleWinPEUnattend renders the unattend answer file (M7; M8-aligned
// to the vendor's recovered template fragments). The vendor renders
// templates/windows_autounattend.xml.tmpl server-side with a `contains`
// template helper; in the vendor chain the file travels AES-encrypted
// and is decrypted inside WinPE to X:\Windows\System32\autounattend.xml
// by decryptor.exe (install.bat JIT flow).
func (s *Server) handleWinPEUnattend(w http.ResponseWriter, r *http.Request) {
	tmplPath := filepath.Join(s.assetDir, "winpe", "autounattend.xml.tmpl")
	tmpl, err := template.New("").Funcs(template.FuncMap{"contains": strings.Contains}).ParseFiles(tmplPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "unattend template not provisioned")
		return
	}
	q := r.URL.Query()
	hostname := q.Get("hostname")
	if hostname == "" {
		hostname = "RACKANVIL-PC"
	}
	imageIndex := q.Get("image_index")
	if imageIndex == "" {
		imageIndex = "1"
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = tmpl.ExecuteTemplate(w, "autounattend.xml.tmpl", map[string]string{
		"MAC": q.Get("mac"), "Hostname": hostname, "ImageIndex": imageIndex,
		"AssetName": q.Get("asset_name"), "BootMode": q.Get("boot_mode"),
		"BaseURL": s.serverURL, "MachineID": q.Get("asset_id"),
	})
}

// handleWinPE serves WinPE boot files. The directory ships EMPTY: Windows
// images need a customer license and are never bundled or downloaded here.
func (s *Server) handleWinPE(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/winpe/")
	if rel == "" || strings.Contains(rel, "..") {
		writeError(w, http.StatusBadRequest, "bad path")
		return
	}
	path := filepath.Join(s.assetDir, "winpe", rel)
	if s.assetDir == "" {
		writeError(w, http.StatusNotFound, "winpe assets not provisioned (needs licensed Windows image)")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "winpe asset not provisioned (needs licensed Windows image)")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, rel, st.ModTime(), f)
}
