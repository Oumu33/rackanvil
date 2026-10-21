// Package api implements the rackanvil management server HTTP API.
//
// The route paths intentionally mirror the functional surface observed in the
// reference product's reverse-engineered route table
// (deplos-reversing/api-routes.md), because operators and tooling expect those
// paths. Every handler, DTO and behavior below is written from scratch; only
// the path strings (functional interface) are aligned.
package api

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Oumu33/rackanvil/internal/collectors"
	"github.com/Oumu33/rackanvil/internal/signreq"
	"github.com/Oumu33/rackanvil/internal/store"
)

// ---- DTOs ----

type InitializeRequest struct {
	Hostname string   `json:"hostname"`
	MACs     []string `json:"macs"`
	IPs      []string `json:"ips"`
	CPUModel string   `json:"cpu_model"`
	MemoryMB int64    `json:"memory_mb"`
	// M11: agent advertises request-signing support; the server then
	// issues a per-host HMAC key in the response and requires signed
	// checkin/report/metrics from this host from then on.
	WantSignKey bool `json:"want_sign_key,omitempty"`
	// M11 first-boot telemetry: whether the agent's network/server
	// readiness gate passed before enrolling, and how long it waited.
	NetworkReady   *bool `json:"network_ready,omitempty"`
	NetworkWaitSec int   `json:"network_wait_sec,omitempty"`
}

type InitializeResponse struct {
	HostID             string `json:"host_id"`
	AgentToken         string `json:"agent_token"`
	SignKey            string `json:"sign_key,omitempty"` // M11, only when requested
	CheckinIntervalSec int    `json:"checkin_interval_sec"`
	ServerURL          string `json:"server_url"`
}

type CheckinRequest struct {
	HostID     string `json:"host_id"`
	AgentToken string `json:"agent_token"`
}

type APITask struct {
	TaskID     string `json:"task_id"`
	Type       string `json:"type"`
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

type CheckinResponse struct {
	ServerTime string    `json:"server_time"`
	Tasks      []APITask `json:"tasks"`
}

type ReportRequest struct {
	HostID     string `json:"host_id"`
	AgentToken string `json:"agent_token"`
	TaskID     string `json:"task_id"`
	Status     string `json:"status"` // done | failed
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error"`
}

// ---- Server ----

// Store is the persistence surface the API needs. Both the M1 in-memory
// store and the M2 database (internal/db) implement it, so swapping the
// backend requires zero handler changes.
type Store interface {
	RegisterHost(hostname string, macs, ips []string, cpuModel string, memoryMB int64) *store.Host
	Authenticate(token string) (string, error)
	Checkin(hostID string) ([]*store.Task, error)
	EnqueueTask(hostID, command string, timeoutSec int) (*store.Task, error)
	// EnqueueTypedTask adds a non-shell task (M4: locator/raid/firmware/...).
	EnqueueTypedTask(hostID, typ, command string, timeoutSec int) (*store.Task, error)
	ReportTask(taskID string, r store.TaskReport) error
	GetTask(taskID string) (*store.Task, error)
	Hosts() []*store.Host
	Tasks() []*store.Task
	// M3 additions (bootscript install mode).
	GetHostByMAC(mac string) (*store.Host, error)
	InstallPending(hostID string) bool
	// M11 additions (agent request signing).
	IssueSignKey(hostID string) (string, error)
	HostSignKey(hostID string) (string, bool)
}

type Server struct {
	store     Store
	mstore    MetricsStore // nil when persistence has no metrics surface (M1 in-memory)
	serverURL string       // externally reachable base URL, used in bootscript
	assetDir  string       // boot assets root (./assets); empty = not provisioned
	mux       *http.ServeMux
	logger    *log.Logger
	m3        *M3Deps // nil until AttachM3
	reqLog    bool    // M6: full request logging (firmware/GRUB forensics)
	// M11: replay cache for signed agent requests (hostID -> nonce -> ts).
	nonceMu sync.Mutex
	nonces  map[string]map[string]int64
	// M11: sha256 cache for distributed files, keyed by absolute path,
	// validated against (modtime, size) on every hit.
	sumMu    sync.Mutex
	sumCache map[string]sumEntry
	// M11: parsed preseed template cache (was re-parsed from disk on
	// every /preseed/ request), invalidated by (modtime, size).
	tmplMu    sync.Mutex
	tmplCache map[string]tmplEntry
}

// MetricsStore is the persistence surface for metrics/SMART/alarms.
// *db.DB implements it; the M1 in-memory store does not (pass nil).
type MetricsStore interface {
	SaveMetrics(store.MetricSample) error
	LatestMetrics(hostID string) (*store.MetricSample, error)
	SaveSmartSample(store.SmartSample) error
	SmartHistory(hostID, device string, limit int) ([]store.SmartSample, error)
	RaiseAlarm(hostID, kind, severity, message string) error
	ResolveAlarm(hostID, kind string) error
	ActiveAlarms(hostID string) ([]store.Alarm, error)
	// M3: dashboard history.
	MetricsHistory(hostID string, limit int) ([]store.MetricSample, error)
}

func New(s Store, ms MetricsStore, serverURL string, logger *log.Logger) *Server {
	srv := &Server{store: s, mstore: ms, serverURL: strings.TrimSuffix(serverURL, "/"), mux: http.NewServeMux(), logger: logger}
	srv.routes()
	return srv
}

// SetRequestLog toggles full request logging (M6): every request is logged
// with its RAW RequestURI (path exactly as sent, including double slashes
// and query string), the response status and body size. Built for boot-chain
// forensics — e.g. learning which path a netboot GRUB actually asks for.
func (s *Server) SetRequestLog(on bool) { s.reqLog = on }

// statusWriter captures status + byte count for request logging while
// preserving the optional interfaces the wrapped handlers rely on
// (WebSSH needs Hijacker; SSE/streaming needs Flusher).
type statusWriter struct {
	http.ResponseWriter
	status int
	n      int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.n += n
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking not supported")
	}
	return h.Hijack()
}

// collapseSlashes folds runs of "/" in a URL path into one. Boot clients
// (shim especially) request paths like "/ipxe/arm64//grubaa64.efi"; the
// stock ServeMux answers those with a 307 redirect that such clients do
// not follow (M5 forensics) — normalizing before routing removes the
// redirect entirely.
func collapseSlashes(p string) string {
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	return p
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cp := collapseSlashes(r.URL.Path); cp != r.URL.Path {
			r.URL.Path = cp
		}
		if !s.reqLog {
			s.mux.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		s.mux.ServeHTTP(sw, r)
		st := sw.status
		if st == 0 {
			st = http.StatusOK
		}
		s.logger.Printf("reqlog: %s %s -> %d %dB from %s", r.Method, r.RequestURI, st, sw.n, r.RemoteAddr)
	})
}

// SetAssetDir points the server at provisioned boot assets
// (<dir>/debian-bookworm/{vmlinuz,initrd.img}).
func (s *Server) SetAssetDir(dir string) { s.assetDir = dir }

// Mount registers an additional handler on the server mux (the web console
// mounts on "/"; more specific patterns like "/api/..." keep precedence).
func (s *Server) Mount(pattern string, h http.Handler) { s.mux.Handle(pattern, h) }

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/api/v1/client/initialize", s.handleInitialize)
	s.mux.HandleFunc("/api/v1/client/checkin", s.handleCheckin)
	s.mux.HandleFunc("/api/v1/client/report", s.handleReport)
	s.mux.HandleFunc("/api/v1/ipxe/bootscript", s.handleBootScript)
	s.mux.HandleFunc("/ipxe/vmlinuz", s.handleBootAsset("debian-bookworm", "vmlinuz"))
	s.mux.HandleFunc("/ipxe/initrd.img", s.handleBootAsset("debian-bookworm", "initrd.img"))
	// M4: ARM64 boot assets (Debian bookworm netboot arm64).
	s.mux.HandleFunc("/ipxe/arm64/linux", s.handleBootAsset("debian-bookworm-arm64", "linux"))
	s.mux.HandleFunc("/ipxe/arm64/initrd.gz", s.handleBootAsset("debian-bookworm-arm64", "initrd.gz"))
	// M5-E: ARM64 UEFI netboot assets — EDK2 cannot use QEMU SLIRP TFTP, so
	// the firmware HTTP-boots GRUB, and GRUB fetches its config + kernel +
	// initrd from us over HTTP.
	s.mux.HandleFunc("/ipxe/arm64/bootnetaa64.efi", s.handleBootAsset("debian-bookworm-arm64", "bootnetaa64.efi"))
	s.mux.HandleFunc("/ipxe/arm64/grubaa64.efi", s.handleBootAsset("debian-bookworm-arm64", "grubaa64.efi"))
	// M5-E (round 2): the real d-i GRUB probes the Debian netboot layout.
	s.mux.HandleFunc("/debian-installer/arm64/grubaa64.efi", s.handleBootAsset("debian-bookworm-arm64", "grubaa64.efi"))
	s.mux.HandleFunc("/debian-installer/arm64/grub/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/debian-installer/arm64/linux", s.handleBootAsset("debian-bookworm-arm64", "linux"))
	s.mux.HandleFunc("/debian-installer/arm64/initrd.gz", s.handleBootAsset("debian-bookworm-arm64", "initrd.gz"))
	s.mux.HandleFunc("/ipxe/arm64/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/ipxe/arm64/grub/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/grub/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	// M6: further locations a netboot GRUB may probe for its config,
	// depending on how its prefix was derived (image dir, USB layout...).
	s.mux.HandleFunc("/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/EFI/BOOT/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/boot/grub/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/ipxe/grub.cfg", s.handleBootAsset("debian-bookworm-arm64", "grub.cfg"))
	s.mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/v1/hosts/", s.handleHostSub)
	// M1 test hooks (not part of the product API surface).
	s.mux.HandleFunc("/debug/enqueue-task", s.handleDebugEnqueue)
	s.mux.HandleFunc("/debug/hosts", s.handleDebugHosts)
	s.mux.HandleFunc("/debug/tasks", s.handleDebugTasks)
	// M4: locator/cfgarchive/topology/relay/firmware/raid/winpe.
	s.registerM4()
	s.registerM9()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// authenticate checks host_id + agent_token. M1 uses a simple opaque token;
// JWT+bcrypt lands with the web console milestone (per project README).
func (s *Server) authenticate(w http.ResponseWriter, hostID, token string) bool {
	id, err := s.store.Authenticate(token)
	if err != nil || id != hostID {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

// agentAuth is the shared front door of the agent endpoints (checkin,
// report, metrics): read the raw body once, authenticate the bearer
// token, then — for hosts enrolled with a signing key — verify the
// M11 request signature over that exact body. Returns the raw body and
// the authenticated host ID.
func (s *Server) agentAuth(w http.ResponseWriter, r *http.Request) ([]byte, string, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return nil, "", false
	}
	var probe struct {
		HostID     string `json:"host_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return nil, "", false
	}
	if !s.authenticate(w, probe.HostID, probe.AgentToken) {
		return nil, "", false
	}
	if !s.verifySignature(w, r, probe.HostID, raw) {
		return nil, "", false
	}
	return raw, probe.HostID, true
}

// verifySignature enforces M11 request signing for hosts that have a
// signing key. Legacy hosts (no key) pass with token auth alone.
// Checks, in order: headers present, timestamp inside ±MaxSkewSeconds,
// HMAC valid, nonce not replayed within the window.
func (s *Server) verifySignature(w http.ResponseWriter, r *http.Request, hostID string, raw []byte) bool {
	key, has := s.store.HostSignKey(hostID)
	if !has {
		return true
	}
	fail := func(msg string) bool {
		s.logger.Printf("signature: host=%s rejected: %s", hostID, msg)
		writeError(w, http.StatusUnauthorized, "signature: "+msg)
		return false
	}
	tsHdr := r.Header.Get("X-Rackanvil-Timestamp")
	nonce := r.Header.Get("X-Rackanvil-Nonce")
	sig := r.Header.Get("X-Rackanvil-Signature")
	if tsHdr == "" || nonce == "" || sig == "" {
		return fail("missing X-Rackanvil-Timestamp/Nonce/Signature headers")
	}
	ts, err := strconv.ParseInt(tsHdr, 10, 64)
	if err != nil {
		return fail("bad timestamp")
	}
	if d := time.Now().Unix() - ts; d > signreq.MaxSkewSeconds || d < -signreq.MaxSkewSeconds {
		return fail("timestamp outside acceptance window")
	}
	if !signreq.Verify(key, hostID, r.Method, r.URL.Path, ts, nonce, raw, sig) {
		return fail("mismatch")
	}
	if !s.claimNonce(hostID, nonce, ts) {
		return fail("nonce replay")
	}
	return true
}

// claimNonce records a nonce for a host; false when it was already used
// inside the acceptance window. Expired entries are swept lazily.
func (s *Server) claimNonce(hostID, nonce string, ts int64) bool {
	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	if s.nonces == nil {
		s.nonces = map[string]map[string]int64{}
	}
	m := s.nonces[hostID]
	if m == nil {
		m = map[string]int64{}
		s.nonces[hostID] = m
	}
	cutoff := time.Now().Unix() - signreq.MaxSkewSeconds
	for n, t := range m {
		if t < cutoff {
			delete(m, n)
		}
	}
	if _, seen := m[nonce]; seen {
		return false
	}
	m[nonce] = ts
	return true
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "component": "rackanvil-server"})
}

func (s *Server) handleInitialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req InitializeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Hostname == "" {
		writeError(w, http.StatusBadRequest, "hostname required")
		return
	}
	h := s.store.RegisterHost(req.Hostname, req.MACs, req.IPs, req.CPUModel, req.MemoryMB)
	resp := InitializeResponse{
		HostID:             h.ID,
		AgentToken:         h.Token,
		CheckinIntervalSec: 30,
		ServerURL:          s.serverURL,
	}
	// M11: issue/rotate the request-signing key for agents that
	// advertise support. Hosts that don't ask keep token-only auth.
	if req.WantSignKey {
		if key, err := s.store.IssueSignKey(h.ID); err != nil {
			s.logger.Printf("initialize: host=%s sign key issue failed: %v", h.ID, err)
		} else {
			resp.SignKey = key
		}
	}
	s.logger.Printf("initialize: host=%s id=%s macs=%v ips=%v", h.Hostname, h.ID, h.MACs, h.IPs)
	if req.NetworkReady != nil {
		s.logger.Printf("initialize: host=%s first-boot network_ready=%v wait=%ds", h.ID, *req.NetworkReady, req.NetworkWaitSec)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	raw, _, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var req CheckinRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	tasks, err := s.store.Checkin(req.HostID)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	out := make([]APITask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, APITask{TaskID: t.ID, Type: t.Type, Command: t.Command, TimeoutSec: t.TimeoutSec})
	}
	s.logger.Printf("checkin: host=%s tasks=%d", req.HostID, len(out))
	writeJSON(w, http.StatusOK, CheckinResponse{
		ServerTime: time.Now().UTC().Format(time.RFC3339),
		Tasks:      out,
	})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	raw, _, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var req ReportRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Status != "done" && req.Status != "failed" {
		writeError(w, http.StatusBadRequest, "status must be done|failed")
		return
	}
	if err := s.store.ReportTask(req.TaskID, store.TaskReport{
		Status: req.Status, Stdout: req.Stdout, Stderr: req.Stderr,
		ExitCode: req.ExitCode, Error: req.Error,
	}); err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	s.logger.Printf("report: host=%s task=%s status=%s exit=%d", req.HostID, req.TaskID, req.Status, req.ExitCode)
	writeJSON(w, http.StatusOK, map[string]bool{"ack": true})
}

// handleBootScript renders a per-host iPXE boot script. Two modes:
//   - normal: boot the provisioned kernel+initrd (M2).
//   - install: when the host is armed for preseed install (orchestrator
//     install step), boot the installer with a per-host preseed URL (M3).
func (s *Server) handleBootScript(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac query param required")
		return
	}
	// M4: architecture-aware boot assets. arch=aarch64 selects the ARM64
	// netboot pair; default is x86_64. (Doc basis: vendor 信创 article —
	// option 93 0x000B → ARM64.)
	arch := r.URL.Query().Get("arch")
	assetSubdir, kernel, initrd, console := "debian-bookworm", "vmlinuz", "initrd.img", "ttyS0,115200"
	if arch == "aarch64" || arch == "arm64" {
		assetSubdir, kernel, initrd, console = "debian-bookworm-arm64", "linux", "initrd.gz", "ttyAMA0,115200"
		arch = "aarch64"
	} else {
		arch = "x86_64"
	}
	_ = assetSubdir // resolved per-route by handleBootAsset; kept for clarity
	s.logger.Printf("bootscript: mac=%s arch=%s from=%s", mac, arch, r.RemoteAddr)
	w.Header().Set("Content-Type", "text/plain")

	if h, err := s.store.GetHostByMAC(mac); err == nil && s.store.InstallPending(h.ID) {
		s.logger.Printf("bootscript: mac=%s host=%s INSTALL MODE (preseed)", mac, h.ID)
		script := `#!ipxe
# rackanvil M3 bootscript — UNATTENDED INSTALL for ` + h.ID + `
echo [rackanvil] unattended install, fetching preseed
kernel ` + s.serverURL + `/ipxe/` + archPath(arch) + kernel + ` auto=true priority=critical netcfg/choose_interface=auto preseed/url=` + s.serverURL + `/preseed/` + h.Token + `.cfg console=` + console + `
initrd ` + s.serverURL + `/ipxe/` + archPath(arch) + initrd + `
boot
`
		w.Write([]byte(script))
		return
	}

	script := `#!ipxe
# rackanvil M1 bootscript (generated per-host)
echo [rackanvil] PXE boot for MAC ` + mac + ` arch ` + arch + `
echo [rackanvil] fetching kernel from ` + s.serverURL + `
kernel ` + s.serverURL + `/ipxe/` + archPath(arch) + kernel + ` initrd=` + initrd + ` console=` + console + `
initrd ` + s.serverURL + `/ipxe/` + archPath(arch) + initrd + `
boot
`
	w.Write([]byte(script))
}

// archPath returns the URL prefix for arch-specific boot assets.
func archPath(arch string) string {
	if arch == "aarch64" {
		return "arm64/"
	}
	return ""
}

// renderGRUBCfg builds the ARM64 netboot GRUB config on the fly, pointing
// at THIS server's public host:port. (The static asset file necessarily
// hardcodes one environment's port — M5's pointed at a throwaway logging
// proxy on :19112, which is exactly how a fetched-but-useless config
// happens. Rendering from serverURL removes that failure mode.)
func (s *Server) renderGRUBCfg() string {
	hostport := ""
	if u, err := url.Parse(s.serverURL); err == nil && u.Host != "" {
		hostport = u.Host
	}
	if hostport == "" {
		hostport = "10.0.2.2:9100"
	}
	return `# rackanvil arm64 netboot config (rendered; server ` + hostport + `)
set timeout=3
insmod http
if linux (http,` + hostport + `)/ipxe/arm64/linux console=ttyAMA0,115200 earlycon=pl011,0x9000000; then
  initrd (http,` + hostport + `)/ipxe/arm64/initrd.gz
  boot
fi
linux (tftp)/debian-installer/arm64/linux console=ttyAMA0,115200 earlycon=pl011,0x9000000
initrd (tftp)/debian-installer/arm64/initrd.gz
boot
`
}

func (s *Server) handleBootAsset(subdir, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if name == "grub.cfg" {
			body := s.renderGRUBCfg()
			s.logger.Printf("bootasset: serving grub.cfg (rendered, %d bytes) to %s", len(body), r.RemoteAddr)
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write([]byte(body))
			return
		}
		if s.assetDir == "" {
			s.bootAssetMissing(w, r)
			return
		}
		path := filepath.Join(s.assetDir, subdir, name)
		f, err := os.Open(path)
		if err != nil {
			s.bootAssetMissing(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			s.bootAssetMissing(w, r)
			return
		}
		s.logger.Printf("bootasset: serving %s (%d bytes) to %s", name, st.Size(), r.RemoteAddr)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, name, st.ModTime(), f)
	}
}

func (s *Server) bootAssetMissing(w http.ResponseWriter, r *http.Request) {
	s.logger.Printf("bootasset: %s requested, not provisioned", r.URL.Path)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusNotImplemented)
	w.Write([]byte("boot asset not provisioned (see --assets)\n"))
}

// ---- M2: metrics ingestion + storage failure prediction + health ----

// MetricsRequest is what the agent posts from its collector loop.
type MetricsRequest struct {
	HostID     string              `json:"host_id"`
	AgentToken string              `json:"agent_token"`
	Snapshot   collectors.Snapshot `json:"snapshot"`
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	raw, _, ok := s.agentAuth(w, r)
	if !ok {
		return
	}
	var req MetricsRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if s.mstore == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	now := time.Now()
	diskJSON, _ := json.Marshal(req.Snapshot.Disks)
	if err := s.mstore.SaveMetrics(store.MetricSample{
		HostID: req.HostID, TS: now,
		CPUPct: req.Snapshot.CPUPct, MemUsedMB: req.Snapshot.MemUsedMB, MemTotalMB: req.Snapshot.MemTotalMB,
		DiskJSON: string(diskJSON), NetJSON: "{}",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "save metrics: "+err.Error())
		return
	}
	s.evaluateStorage(req.HostID, &req.Snapshot, now)
	s.evaluateDeep(req.HostID, &req.Snapshot)
	s.logger.Printf("metrics: host=%s cpu=%.1f%% mem=%d/%dMB disks=%d smart=%d nvme=%d",
		req.HostID, req.Snapshot.CPUPct, req.Snapshot.MemUsedMB, req.Snapshot.MemTotalMB,
		len(req.Snapshot.Disks), len(req.Snapshot.Smart), len(req.Snapshot.Nvme))
	writeJSON(w, http.StatusOK, map[string]bool{"ack": true})
}

// evaluateStorage runs the failure predictor over SMART/NVMe readings:
// every sample is archived to smart_history, findings become (or clear)
// active alarms. The policy lives in internal/collectors (our own policy,
// documented there).
func (s *Server) evaluateStorage(hostID string, snap *collectors.Snapshot, now time.Time) {
	for dev, attrs := range snap.Smart {
		hist, _ := s.mstore.SmartHistory(hostID, dev, 5)
		hmaps := make([]map[string]int64, 0, len(hist))
		for _, h := range hist {
			hmaps = append(hmaps, h.Attrs)
		}
		// health unknown (absent) ≠ failed: only an explicit FAILED is critical.
		healthPassed := true
		if hp, known := snap.SmartHealth[dev]; known {
			healthPassed = hp
		}
		findings := collectors.AnalyzeSmart(dev, attrs, hmaps, healthPassed)
		s.applyFindings(hostID, "smart."+dev, findings)
		_ = s.mstore.SaveSmartSample(store.SmartSample{
			HostID: hostID, Device: dev, TS: now, Attrs: attrs,
			Health: collectors.WorstSeverity(findings),
		})
	}
	for dev, nlog := range snap.Nvme {
		findings := collectors.AnalyzeNvme(dev, nlog)
		// M4: NVMe spare-blocks signal (vendor doc "硬件故障感知").
		findings = append(findings, collectors.AnalyzeNvmeSpare(dev, nlog)...)
		s.applyFindings(hostID, "nvme."+dev, findings)
		_ = s.mstore.SaveSmartSample(store.SmartSample{
			HostID: hostID, Device: dev, TS: now, Attrs: nlog,
			Health: collectors.WorstSeverity(findings),
		})
	}
}

// evaluateDeep runs the beyond-SMART platform health check (M4):
// EDAC memory errors, PCIe AER, MCE availability.
func (s *Server) evaluateDeep(hostID string, snap *collectors.Snapshot) {
	if snap.Deep == nil {
		return // old agent without deep signals
	}
	s.applyFindings(hostID, "platform.deep", collectors.AnalyzeDeep(snap.Deep))
}

func (s *Server) applyFindings(hostID, kind string, findings []collectors.Finding) {
	worst := collectors.WorstSeverity(findings)
	if worst == "ok" {
		_ = s.mstore.ResolveAlarm(hostID, kind)
		return
	}
	var msgs []string
	for _, f := range findings {
		if f.Severity != "ok" {
			msgs = append(msgs, "["+f.Severity+"] "+f.Message)
		}
	}
	if err := s.mstore.RaiseAlarm(hostID, kind, worst, strings.Join(msgs, "; ")); err == nil {
		s.logger.Printf("alarm: host=%s kind=%s severity=%s", hostID, kind, worst)
	}
}

// handleHostSub serves /api/v1/hosts/{id}/health and
// /api/v1/hosts/{id}/metrics/history (M3 dashboard data).
func (s *Server) handleHostSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/hosts/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if s.mstore == nil {
		writeError(w, http.StatusServiceUnavailable, "metrics not configured")
		return
	}
	hostID := parts[0]
	switch {
	case parts[1] == "health" && len(parts) == 2:
		m, _ := s.mstore.LatestMetrics(hostID)
		alarms, _ := s.mstore.ActiveAlarms(hostID)
		writeJSON(w, http.StatusOK, map[string]any{
			"host_id": hostID, "latest_metrics": m, "active_alarms": alarms,
		})
	case parts[1] == "metrics" && len(parts) == 3 && parts[2] == "history":
		limit := 120
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
				limit = n
			}
		}
		hist, err := s.mstore.MetricsHistory(hostID, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, hist)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

type enqueueRequest struct {
	HostID     string `json:"host_id"`
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

// ---- M1 debug hooks ----

func (s *Server) handleDebugEnqueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req enqueueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	t, err := s.store.EnqueueTask(req.HostID, req.Command, req.TimeoutSec)
	if err != nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	s.logger.Printf("debug: enqueued task=%s for host=%s cmd=%q", t.ID, t.HostID, t.Command)
	writeJSON(w, http.StatusOK, map[string]string{"task_id": t.ID})
}

func (s *Server) handleDebugHosts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Hosts())
}

func (s *Server) handleDebugTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Tasks())
}
