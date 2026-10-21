package api

// M9 vendor-route additions. The vendor frontend (extracted in full,
// deplos-reversing/m9-evidence/frontend/) exercises the vendor's
// 147-route surface; our M1–M6 server implements the same functions
// mostly under its own paths. The routes below are the missing ones
// that are (a) demonstrably called by the vendor frontend or the PXE
// client chain and (b) backed by a real subsystem in this codebase —
// no stubs. The full 125-route diff and its dispositions are recorded
// in deplos-reversing/m9-evidence/frontend/ROUTE-DIFF.md.

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Oumu33/rackanvil/internal/license"
	"github.com/Oumu33/rackanvil/internal/networkconfig"
	"github.com/Oumu33/rackanvil/internal/osimage"
)

func (s *Server) registerM9() {
	s.mux.HandleFunc("/api/v1/ipxe/server-time", s.handleServerTime)
	s.mux.HandleFunc("/api/v1/ipxe/supported-os", s.handleSupportedOS)
	s.mux.HandleFunc("/api/v1/ipxe/network-config", s.handleNetworkConfig)
	s.mux.HandleFunc("/api/v1/dashboard/summary", s.handleDashboardSummary)
	s.mux.HandleFunc("/api/v1/raid/topology", s.handleRaidTopology)
	s.mux.HandleFunc("/api/v1/license/import", s.handleLicenseImport)
}

// GET /api/v1/ipxe/server-time — PXE clients sync clocks before
// install (vendor: ipxeinstall server-time route).
func (s *Server) handleServerTime(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	writeJSON(w, http.StatusOK, map[string]any{
		"server_time": now.Format(time.RFC3339),
		"unix":        now.Unix(),
	})
}

// GET /api/v1/ipxe/supported-os — the OS definition set the install
// chain can classify and provision (M9: vendor definitions, see
// internal/osimage).
func (s *Server) handleSupportedOS(w http.ResponseWriter, r *http.Request) {
	defs, err := osimage.Definitions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"id": d.ID, "name": d.Name, "family": d.Family,
			"major_version": d.MajorVersion,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"os": out, "count": len(out)})
}

// GET /api/v1/ipxe/network-config?mac=&format=json — per-host network
// configuration for the post-install network-config tool (vendor URL
// shape recovered in M8, CATALOG §1.6). The config itself is the
// networkconfig.Config JSON stored per host under netcfg:<hostID>
// (written by the operator/install flow); 404 until one is stored.
func (s *Server) handleNetworkConfig(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "MAC required")
		return
	}
	h, err := s.store.GetHostByMAC(mac)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	raw := ""
	if gs, ok := any(s.store).(interface {
		GetSetting(string) (string, bool)
	}); ok {
		raw, _ = gs.GetSetting("netcfg:" + h.ID)
	}
	if raw == "" {
		writeError(w, http.StatusNotFound, "no network config stored for host")
		return
	}
	var cfg networkconfig.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "stored config invalid: "+err.Error())
		return
	}
	if r.URL.Query().Get("format") == "yaml" {
		body, err := networkconfig.Render(cfg)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte(body))
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// GET /api/v1/dashboard/summary — fleet aggregate for the web
// dashboard (vendor frontend's landing call).
func (s *Server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	hosts := s.store.Hosts()
	tasks := s.store.Tasks()
	now := time.Now()
	online := 0
	for _, h := range hosts {
		if now.Sub(h.LastCheckin) < 5*time.Minute && !h.LastCheckin.IsZero() {
			online++
		}
	}
	byStatus := map[string]int{}
	for _, t := range tasks {
		byStatus[t.Status]++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hosts_total":     len(hosts),
		"hosts_online":    online,
		"tasks_total":     len(tasks),
		"tasks_by_status": byStatus,
	})
}

// GET /api/v1/raid/topology — latest completed raid.query result per
// host, aggregated from the task log (the RAID console's data call in
// the vendor frontend).
func (s *Server) handleRaidTopology(w http.ResponseWriter, r *http.Request) {
	latest := map[string]any{}
	for _, t := range s.store.Tasks() { // newest first
		if t.Type != "raid.query" || t.Status != "done" {
			continue
		}
		if _, seen := latest[t.HostID]; seen {
			continue
		}
		var parsed any
		if err := json.Unmarshal([]byte(t.Stdout), &parsed); err == nil {
			latest[t.HostID] = parsed
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": latest})
}

// POST /api/v1/license/import {license: "<text>"} — verifies (when a
// public key is configured) and stores a license, complementing the
// existing /api/v1/license/status.
func (s *Server) handleLicenseImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if s.m3 == nil || s.m3.Licenses == nil {
		writeError(w, http.StatusNotImplemented, "license store not configured")
		return
	}
	var req struct {
		License string `json:"license"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.License == "" {
		writeError(w, http.StatusBadRequest, "want {\"license\": \"<license text>\"}")
		return
	}
	customer, maxHosts, expires := "", 0, time.Time{}
	if s.m3.LicensePub != nil {
		lic, err := license.Verify(s.m3.LicensePub, req.License)
		if err != nil {
			writeError(w, http.StatusBadRequest, "license verify failed: "+err.Error())
			return
		}
		customer, maxHosts = lic.Customer, lic.MaxHosts
		if lic.ExpiresAt > 0 {
			expires = time.Unix(lic.ExpiresAt, 0)
		}
	}
	rec, err := s.m3.Licenses.SaveLicense(customer, maxHosts, expires, req.License)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": rec.ID, "customer": customer, "max_hosts": maxHosts})
}
