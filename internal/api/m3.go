// M3 API surface: orchestration, discovery, license, preseed, install-done,
// agent binary download, metrics history.
//
// All cross-package needs are expressed as narrow interfaces (same pattern
// as MetricsStore): *db.DB implements them; the M1 in-memory store does not.
package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/Oumu33/rackanvil/internal/discovery"
	"github.com/Oumu33/rackanvil/internal/license"
	"github.com/Oumu33/rackanvil/internal/store"
)

// JobStore is the persistence surface for orchestration jobs.
type JobStore interface {
	CreateJob(name, hostID string, steps []store.StepDef) (*store.Job, error)
	GetJob(id string) (*store.Job, []*store.JobStep, error)
	ListJobs(limit int) ([]*store.Job, error)
}

// InstallStore is the persistence surface for the install arming flag.
type InstallStore interface {
	SetInstallPending(hostID string, pending bool) error
	InstallPending(hostID string) bool
	SetInstallContext(hostID, rootPW string) error
	GetInstallContext(hostID string) (string, bool)
	ClearInstallContext(hostID string) error
}

// DiscoveryStore persists discovery scans.
type DiscoveryStore interface {
	SaveScan(cidr, foundJSON, diffJSON string) (*store.ScanRecord, error)
	LatestScan() (*store.ScanRecord, error)
}

// LicenseStore persists imported licenses.
type LicenseStore interface {
	SaveLicense(customer string, maxHosts int, expiresAt time.Time, licenseText string) (*store.LicenseRecord, error)
	LatestLicense() (*store.LicenseRecord, error)
}

// M3Deps wires the optional M3 backends. Any nil backend disables its routes
// with 501 (honest, not silent).
type M3Deps struct {
	Jobs      JobStore
	Install   InstallStore
	Discovery DiscoveryStore
	Licenses  LicenseStore
	// LicensePub, when set, verifies imported licenses (our own Ed25519 key).
	LicensePub ed25519.PublicKey
	// AgentBinPath serves the agent binary for installer late_command.
	AgentBinPath string
	// PreseedDir holds debian.cfg.tmpl.
	PreseedDir string
	// MirrorHost/MirrorDir override the Debian mirror in rendered preseed
	// (default deb.debian.org /debian). Points at a local mirror ("弹药库")
	// in proxied/offline environments.
	MirrorHost string
	MirrorDir  string
}

// AttachM3 registers M3 routes and stores deps on the server.
func (s *Server) AttachM3(d M3Deps) {
	s.m3 = &d
	mux := s.mux
	mux.HandleFunc("/api/v1/client/install-done", s.handleInstallDone)
	mux.HandleFunc("/preseed/", s.handlePreseed)
	mux.HandleFunc("/agent/download", s.handleAgentDownload)
	// M11: integrity for distributed files — checksums are computed from
	// the files actually served, never maintained by hand (the vendor
	// shipped a checksums file that matched none of its own binaries).
	mux.HandleFunc("/agent/download.sha256", s.handleAgentDownloadSHA256)
	mux.HandleFunc("/checksums.sha256", s.handleChecksumsManifest)
	mux.HandleFunc("/api/v1/orch/jobs", s.handleOrchJobs)
	mux.HandleFunc("/api/v1/orch/jobs/", s.handleOrchJobDetail)
	mux.HandleFunc("/api/v1/discovery/scan", s.handleDiscoveryScan)
	mux.HandleFunc("/api/v1/discovery/latest", s.handleDiscoveryLatest)
	mux.HandleFunc("/api/v1/license/status", s.handleLicenseStatus)
}

// ---- install-done: the installer's callback after preseed finishes ----

type installDoneRequest struct {
	HostID     string `json:"host_id"`
	AgentToken string `json:"agent_token"`
}

func (s *Server) handleInstallDone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req installDoneRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.authenticate(w, req.HostID, req.AgentToken) {
		return
	}
	if s.m3 == nil || s.m3.Install == nil {
		writeError(w, http.StatusNotImplemented, "install backend not configured")
		return
	}
	if err := s.m3.Install.SetInstallPending(req.HostID, false); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.m3.Install.ClearInstallContext(req.HostID)
	s.logger.Printf("install-done: host=%s disarmed (preseed install finished)", req.HostID)
	writeJSON(w, http.StatusOK, map[string]bool{"ack": true})
}

// ---- preseed config: rendered per host, fetched by debian-installer ----

type preseedData struct {
	Hostname   string
	ServerURL  string
	HostID     string
	Token      string
	RootPW     string
	MirrorHost string
	MirrorDir  string
}

// tmplEntry is one cached parsed template, validated by (modtime, size).
type tmplEntry struct {
	modTime time.Time
	size    int64
	tmpl    *template.Template
}

// preseedTemplate returns the parsed preseed template, re-parsing only
// when the file changed since the cached parse.
func (s *Server) preseedTemplate(path string) (*template.Template, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	s.tmplMu.Lock()
	if s.tmplCache == nil {
		s.tmplCache = map[string]tmplEntry{}
	}
	if e, ok := s.tmplCache[path]; ok && e.size == st.Size() && e.modTime.Equal(st.ModTime()) {
		s.tmplMu.Unlock()
		return e.tmpl, nil
	}
	s.tmplMu.Unlock()
	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return nil, err
	}
	s.tmplMu.Lock()
	s.tmplCache[path] = tmplEntry{modTime: st.ModTime(), size: st.Size(), tmpl: tmpl}
	s.tmplMu.Unlock()
	return tmpl, nil
}

func (s *Server) handlePreseed(w http.ResponseWriter, r *http.Request) {
	if s.m3 == nil || s.m3.Install == nil || s.m3.PreseedDir == "" {
		writeError(w, http.StatusNotImplemented, "preseed not configured")
		return
	}
	token := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/preseed/"), ".cfg")
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	hostID, err := s.store.Authenticate(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var host *store.Host
	for _, h := range s.store.Hosts() {
		if h.ID == hostID {
			host = h
			break
		}
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	rootpw, ok := s.m3.Install.GetInstallContext(hostID)
	if !ok {
		writeError(w, http.StatusForbidden, "no pending install for this host")
		return
	}
	tmpl, err := s.preseedTemplate(filepath.Join(s.m3.PreseedDir, "debian.cfg.tmpl"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "preseed template: "+err.Error())
		return
	}
	s.logger.Printf("preseed: serving config for host=%s", hostID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	mirrorHost, mirrorDir := "deb.debian.org", "/debian"
	if s.m3.MirrorHost != "" {
		mirrorHost = s.m3.MirrorHost
	}
	if s.m3.MirrorDir != "" {
		mirrorDir = s.m3.MirrorDir
	}
	_ = tmpl.Execute(w, preseedData{
		Hostname: host.Hostname, ServerURL: s.serverURL,
		HostID: hostID, Token: token, RootPW: rootpw,
		MirrorHost: mirrorHost, MirrorDir: mirrorDir,
	})
}

// ---- agent binary download (installer late_command) ----

func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if s.m3 == nil || s.m3.AgentBinPath == "" {
		writeError(w, http.StatusNotImplemented, "agent binary not provisioned (see --agent-bin)")
		return
	}
	f, err := os.Open(s.m3.AgentBinPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent binary missing")
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	s.logger.Printf("agent-download: serving %d bytes to %s", st.Size(), r.RemoteAddr)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	http.ServeContent(w, r, "rackanvil-agent", st.ModTime(), f)
}

// ---- M11: checksums for distributed files (auto-generated) ----

// sumEntry is one cached file hash, validated by (modtime, size).
type sumEntry struct {
	modTime time.Time
	size    int64
	hex     string
}

// fileSHA256 returns the sha256 hex of a file, using the server cache.
// The cache key is the absolute path; an entry is reused only while the
// file's (modtime, size) are unchanged, so replacing a file invalidates
// its checksum immediately — a stale manifest is impossible by design.
func (s *Server) fileSHA256(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	s.sumMu.Lock()
	if s.sumCache == nil {
		s.sumCache = map[string]sumEntry{}
	}
	if e, ok := s.sumCache[abs]; ok && e.size == st.Size() && e.modTime.Equal(st.ModTime()) {
		s.sumMu.Unlock()
		return e.hex, nil
	}
	s.sumMu.Unlock()
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	s.sumMu.Lock()
	s.sumCache[abs] = sumEntry{modTime: st.ModTime(), size: st.Size(), hex: sum}
	s.sumMu.Unlock()
	return sum, nil
}

// handleAgentDownloadSHA256 serves the agent binary's checksum in
// sha256sum format ("<hex>  rackanvil-agent"), so the installer's
// late_command can verify with a plain `sha256sum -c`.
func (s *Server) handleAgentDownloadSHA256(w http.ResponseWriter, r *http.Request) {
	if s.m3 == nil || s.m3.AgentBinPath == "" {
		writeError(w, http.StatusNotImplemented, "agent binary not provisioned (see --agent-bin)")
		return
	}
	sum, err := s.fileSHA256(s.m3.AgentBinPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent binary missing")
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(sum + "  rackanvil-agent\n"))
}

// handleChecksumsManifest serves a sha256sum-format manifest of every
// file this server distributes: the agent binary plus everything under
// the boot-assets directory. Generated from the live files on request
// (hashes cached per file), so it can never drift from reality the way
// a hand-maintained checksums file does.
func (s *Server) handleChecksumsManifest(w http.ResponseWriter, r *http.Request) {
	type line struct{ name, sum string }
	var lines []line
	if s.m3 != nil && s.m3.AgentBinPath != "" {
		if sum, err := s.fileSHA256(s.m3.AgentBinPath); err == nil {
			lines = append(lines, line{"rackanvil-agent", sum})
		}
	}
	if s.assetDir != "" {
		_ = filepath.Walk(s.assetDir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(s.assetDir, p)
			if err != nil {
				return nil
			}
			if sum, err := s.fileSHA256(p); err == nil {
				lines = append(lines, line{"assets/" + filepath.ToSlash(rel), sum})
			}
			return nil
		})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].name < lines[j].name })
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.sum + "  " + l.name + "\n")
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(b.String()))
}

// ---- orchestration jobs ----

type createJobRequest struct {
	HostID string          `json:"host_id"`
	Name   string          `json:"name"`
	Steps  []store.StepDef `json:"steps"` // empty = default plan
}

func (s *Server) handleOrchJobs(w http.ResponseWriter, r *http.Request) {
	if s.m3 == nil || s.m3.Jobs == nil {
		writeError(w, http.StatusNotImplemented, "orchestrator not configured")
		return
	}
	switch r.Method {
	case http.MethodPost:
		var req createJobRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.HostID == "" {
			writeError(w, http.StatusBadRequest, "host_id required")
			return
		}
		j, err := s.m3.Jobs.CreateJob(req.Name, req.HostID, req.Steps)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.logger.Printf("orch: job %s created for host %s (%d steps)", j.ID, req.HostID, len(req.Steps))
		writeJSON(w, http.StatusOK, map[string]string{"job_id": j.ID})
	case http.MethodGet:
		jobs, err := s.m3.Jobs.ListJobs(100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, jobs)
	default:
		writeError(w, http.StatusMethodNotAllowed, "GET/POST only")
	}
}

func (s *Server) handleOrchJobDetail(w http.ResponseWriter, r *http.Request) {
	if s.m3 == nil || s.m3.Jobs == nil {
		writeError(w, http.StatusNotImplemented, "orchestrator not configured")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/orch/jobs/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	j, steps, err := s.m3.Jobs.GetJob(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "steps": steps})
}

// ---- discovery ----

type discoveryScanRequest struct {
	CIDR string `json:"cidr"`
}

func (s *Server) handleDiscoveryScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if s.m3 == nil || s.m3.Discovery == nil {
		writeError(w, http.StatusNotImplemented, "discovery not configured")
		return
	}
	var req discoveryScanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.CIDR == "" {
		writeError(w, http.StatusBadRequest, "cidr required")
		return
	}
	found, err := discovery.ScanCIDR(req.CIDR, nil, 800*time.Millisecond, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var known []discovery.HostRef
	for _, h := range s.store.Hosts() {
		known = append(known, discovery.HostRef{ID: h.ID, Hostname: h.Hostname, MACs: h.MACs, IPs: h.IPs})
	}
	diff := discovery.DiffBOM(found, known)
	foundJ, _ := json.Marshal(found)
	diffJ, _ := json.Marshal(diff)
	rec, err := s.m3.Discovery.SaveScan(req.CIDR, string(foundJ), string(diffJ))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Printf("discovery: scan %s found=%d diff=%d", req.CIDR, len(found), len(diff))
	writeJSON(w, http.StatusOK, map[string]any{
		"scan_id": rec.ID, "found": found, "diff": diff,
	})
}

func (s *Server) handleDiscoveryLatest(w http.ResponseWriter, r *http.Request) {
	if s.m3 == nil || s.m3.Discovery == nil {
		writeError(w, http.StatusNotImplemented, "discovery not configured")
		return
	}
	rec, err := s.m3.Discovery.LatestScan()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"scan": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scan": rec})
}

// ---- license status ----

func (s *Server) handleLicenseStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"licensed": false, "mode": "dev (no public key configured)"}
	if s.m3 != nil && s.m3.Licenses != nil && s.m3.LicensePub != nil {
		rec, err := s.m3.Licenses.LatestLicense()
		if err == nil {
			if lic, verr := license.Verify(s.m3.LicensePub, rec.LicenseTxt); verr == nil {
				status = map[string]any{
					"licensed": true, "customer": lic.Customer,
					"max_hosts":  lic.MaxHosts,
					"expires_at": time.Unix(lic.ExpiresAt, 0).UTC().Format(time.RFC3339),
					"features":   lic.Features,
				}
			} else {
				status = map[string]any{"licensed": false, "mode": "invalid license: " + verr.Error()}
			}
		} else {
			status = map[string]any{"licensed": false, "mode": "no license imported"}
		}
	}
	writeJSON(w, http.StatusOK, status)
}
