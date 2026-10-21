// Package agent implements the rackanvil node agent.
//
// The agent enrolls itself with the server (initialize), heartbeats
// (checkin), executes dispatched shell tasks and reports results. The wire
// protocol (paths + JSON shapes) mirrors the functional surface documented in
// deplos-reversing/api-routes.md; all code here is written from scratch.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Oumu33/rackanvil/internal/cfgarchive"
	"github.com/Oumu33/rackanvil/internal/collectors"
	"github.com/Oumu33/rackanvil/internal/firmware"
	"github.com/Oumu33/rackanvil/internal/locator"
	"github.com/Oumu33/rackanvil/internal/networkconfig"
	"github.com/Oumu33/rackanvil/internal/raid"
	"github.com/Oumu33/rackanvil/internal/signreq"
	"github.com/Oumu33/rackanvil/internal/topology"
)

// wholeDiskRe restricts disk.wipe targets to whole-disk device names.
var wholeDiskRe = regexp.MustCompile(`^/dev/(sd[a-z]+|hd[a-z]+|vd[a-z]+|nvme\d+n\d+|mmcblk\d+)$`)

// task mirrors api.APITask (kept local to avoid an import cycle in M1;
// the DTO will move to a shared package in M2).
type task struct {
	TaskID     string `json:"task_id"`
	Type       string `json:"type"`
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec"`
}

// Agent is a single managed node.
type Agent struct {
	serverURL       string
	interval        time.Duration
	metricsInterval time.Duration // 0 disables the collector loop
	client          *http.Client
	hostID          string
	token           string
	signKey         string // M11: per-host HMAC key from initialize ("" = unsigned)
	log             *log.Logger
	// M11 first-boot readiness gate (vendor pxe-first-boot alignment):
	// poll the server-time endpoint for up to readyTimeout before
	// enrolling; the outcome is reported in the initialize request.
	readyTimeout  time.Duration
	readyInterval time.Duration
	networkReady  bool
	networkWait   time.Duration
}

// New creates an agent talking to serverURL (e.g. "http://10.0.0.1:9100").
func New(serverURL string, interval time.Duration, logger *log.Logger) *Agent {
	if logger == nil {
		logger = log.Default()
	}
	return &Agent{
		serverURL:     strings.TrimSuffix(serverURL, "/"),
		interval:      interval,
		client:        &http.Client{Timeout: 30 * time.Second},
		log:           logger,
		readyTimeout:  120 * time.Second, // vendor first-boot bound
		readyInterval: 2 * time.Second,
	}
}

// WithMetricsInterval enables the collector loop (M2): every d the agent
// collects a snapshot and POSTs it to /api/v1/metrics.
func (a *Agent) WithMetricsInterval(d time.Duration) *Agent {
	a.metricsInterval = d
	return a
}

// reportMetrics collects one snapshot and uploads it.
func (a *Agent) reportMetrics() error {
	snap := collectors.Collect()
	snap.Deep = collectors.CollectDeep("/sys")
	var ack map[string]bool
	err := a.post("/api/v1/metrics", map[string]any{
		"host_id": a.hostID, "agent_token": a.token, "snapshot": snap,
	}, &ack)
	if err != nil {
		return fmt.Errorf("metrics: %w", err)
	}
	a.log.Printf("metrics ok: cpu=%.1f%% mem=%d/%dMB disks=%d smart=%d nvme=%d skipped=%v",
		snap.CPUPct, snap.MemUsedMB, snap.MemTotalMB,
		len(snap.Disks), len(snap.Smart), len(snap.Nvme), snap.Skipped)
	return nil
}

func (a *Agent) post(path string, reqBody, respBody any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(reqBody); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, a.serverURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// M11: sign the request when enrollment issued a signing key.
	// initialize itself is necessarily unsigned (it issues the key).
	if a.signKey != "" {
		nonce, err := signreq.NewNonce()
		if err != nil {
			return err
		}
		ts := time.Now().Unix()
		sig, err := signreq.Sign(a.signKey, a.hostID, http.MethodPost, path, ts, nonce, buf.Bytes())
		if err != nil {
			return err
		}
		req.Header.Set("X-Rackanvil-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Rackanvil-Nonce", nonce)
		req.Header.Set("X-Rackanvil-Signature", sig)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: status %d", path, resp.StatusCode)
	}
	if respBody != nil {
		return json.NewDecoder(resp.Body).Decode(respBody)
	}
	return nil
}

type hostInfo struct {
	Hostname string   `json:"hostname"`
	MACs     []string `json:"macs"`
	IPs      []string `json:"ips"`
	CPUModel string   `json:"cpu_model"`
	MemoryMB int64    `json:"memory_mb"`
	// M11: signing capability advertisement + first-boot gate outcome.
	WantSignKey    bool  `json:"want_sign_key"`
	NetworkReady   *bool `json:"network_ready,omitempty"`
	NetworkWaitSec int   `json:"network_wait_sec,omitempty"`
}

// collectInfo gathers local identity: hostname, MACs and IPs of up
// non-loopback interfaces. (CPU/memory enrichment lands in M2 with collectors.)
func collectInfo() hostInfo {
	hi := hostInfo{}
	hi.Hostname, _ = os.Hostname()
	ifaces, err := net.Interfaces()
	if err != nil {
		return hi
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(ifi.HardwareAddr) > 0 {
			hi.MACs = append(hi.MACs, ifi.HardwareAddr.String())
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			hi.IPs = append(hi.IPs, ip.String())
		}
	}
	return hi
}

// initialize enrolls this node with the server and stores the returned token.
func (a *Agent) initialize() error {
	var resp struct {
		HostID             string `json:"host_id"`
		AgentToken         string `json:"agent_token"`
		SignKey            string `json:"sign_key"`
		CheckinIntervalSec int    `json:"checkin_interval_sec"`
		ServerURL          string `json:"server_url"`
	}
	info := collectInfo()
	info.WantSignKey = true
	ready := a.networkReady
	info.NetworkReady = &ready
	info.NetworkWaitSec = int(a.networkWait.Seconds())
	if err := a.post("/api/v1/client/initialize", info, &resp); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	a.hostID = resp.HostID
	a.token = resp.AgentToken
	a.signKey = resp.SignKey
	a.log.Printf("initialize: host_id=%s hostname resolved, token acquired (signing %s)",
		a.hostID, map[bool]string{true: "enabled", false: "unavailable"}[a.signKey != ""])
	return nil
}

// waitForServer is the first-boot readiness gate (vendor pxe-first-boot
// alignment, recovered as an M10 delta in the vendor's newer build):
// poll the server-time endpoint until it answers or the timeout
// elapses. The vendor script polls for up to 120s and reports a
// "network_ready" failure when it never comes up; we record the same
// signal into the initialize request instead of exiting — the agent's
// initialize retry loop + the service manager's restart policy cover
// the still-down case.
func (a *Agent) waitForServer(ctx context.Context) (bool, time.Duration) {
	start := time.Now()
	for {
		resp, err := a.client.Get(a.serverURL + "/api/v1/ipxe/server-time")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true, time.Since(start)
			}
		}
		if time.Since(start) >= a.readyTimeout {
			return false, time.Since(start)
		}
		select {
		case <-ctx.Done():
			return false, time.Since(start)
		case <-time.After(a.readyInterval):
		}
	}
}

// checkin heartbeats and fetches pending tasks.
func (a *Agent) checkin() ([]task, error) {
	var resp struct {
		ServerTime string `json:"server_time"`
		Tasks      []task `json:"tasks"`
	}
	err := a.post("/api/v1/client/checkin", map[string]string{
		"host_id": a.hostID, "agent_token": a.token,
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("checkin: %w", err)
	}
	return resp.Tasks, nil
}

// report sends a task execution result back to the server.
func (a *Agent) report(t task, status, stdout, stderr string, exitCode int, errMsg string) error {
	req := map[string]any{
		"host_id": a.hostID, "agent_token": a.token,
		"task_id": t.TaskID, "status": status,
		"stdout": stdout, "stderr": stderr,
		"exit_code": exitCode, "error": errMsg,
	}
	var ack map[string]bool
	if err := a.post("/api/v1/client/report", req, &ack); err != nil {
		return fmt.Errorf("report: %w", err)
	}
	return nil
}

// executeShell runs command via sh -c with a timeout, capturing outputs.
func executeShell(command string, timeoutSec int) (stdout, stderr string, exitCode int, errMsg string) {
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	stdout, stderr = so.String(), se.String()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout, stderr, 124, "timeout"
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return stdout, stderr, ee.ExitCode(), ""
		}
		return stdout, stderr, 1, err.Error()
	}
	return stdout, stderr, 0, ""
}

// osRunner adapts os/exec to the hardware packages' Runner interfaces
// (locator/cfgarchive/topology/firmware/raid all share the same shape).
type osRunner struct{}

func (osRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	var so, se bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

// netRunner adds direct file writes for networkconfig.Apply (the
// agent runs as root on the managed host).
type netRunner struct{ osRunner }

func (netRunner) WriteFile(_ context.Context, path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func (a *Agent) handleTask(t task) {
	a.log.Printf("task: executing %s type=%s cmd=%q", t.TaskID, t.Type, t.Command)
	var status, stdout, stderr, errMsg string
	var code int
	r := osRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(t.TimeoutSec)*time.Second)
	if t.TimeoutSec <= 0 {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), 120*time.Second)
	}
	defer cancel()
	runJSON := func(fn func(context.Context, osRunner, string) (string, error)) {
		out, err := fn(ctx, r, t.Command)
		if err != nil {
			status, errMsg, code = "failed", err.Error(), 1
			return
		}
		stdout, status, code = out, "done", 0
	}
	switch t.Type {
	case "shell":
		stdout, stderr, code, errMsg = executeShell(t.Command, t.TimeoutSec)
		if errMsg == "" && code == 0 {
			status = "done"
		} else {
			status = "failed"
		}
	case "disk.scan":
		runJSON(func(ctx context.Context, r osRunner, _ string) (string, error) {
			return locator.ScanJSON(ctx, r)
		})
	case "disk.locate":
		runJSON(func(ctx context.Context, r osRunner, payload string) (string, error) {
			return locator.LocateJSON(ctx, r, payload)
		})
	case "disk.wipe":
		// Vendor-verified whole-disk erase sequence (M10 pxe-agent
		// recovery; collectors.WipeSteps). Destructive: requires an
		// explicit confirm flag in the payload and a whole-disk
		// device name — partitions and arbitrary paths are refused.
		var p struct {
			Device      string `json:"device"`
			SizeSectors int64  `json:"size_sectors"`
			Confirm     bool   `json:"confirm"`
		}
		if err := json.Unmarshal([]byte(t.Command), &p); err != nil {
			status, errMsg, code = "failed", "bad payload: "+err.Error(), 1
			break
		}
		if !p.Confirm {
			status, errMsg, code = "failed", "disk.wipe requires confirm=true", 1
			break
		}
		if !wholeDiskRe.MatchString(p.Device) {
			status, errMsg, code = "failed", "refusing non whole-disk device: "+p.Device, 1
			break
		}
		steps := collectors.WipeSteps(p.Device, p.SizeSectors)
		ran := 0
		var werr error
		for _, st := range steps {
			if err := exec.CommandContext(ctx, st.Name, st.Args...).Run(); err != nil {
				werr = fmt.Errorf("%s: %w", st.Name, err)
				break
			}
			ran++
		}
		if werr != nil {
			status, errMsg, code = "failed", werr.Error(), 1
		} else {
			b, _ := json.Marshal(map[string]any{"device": p.Device, "steps_run": ran})
			stdout, status, code = string(b), "done", 0
		}
	case "net.config":
		runJSON(func(ctx context.Context, r osRunner, payload string) (string, error) {
			return networkconfig.ApplyJSON(ctx, netRunner{r}, payload)
		})
	case "cfg.snapshot":
		runJSON(func(ctx context.Context, r osRunner, _ string) (string, error) {
			return cfgarchive.CollectJSON(ctx, r)
		})
	case "raid.query":
		runJSON(func(ctx context.Context, r osRunner, _ string) (string, error) {
			return raid.QueryJSON(ctx, r)
		})
	case "topo.collect":
		runJSON(func(ctx context.Context, r osRunner, _ string) (string, error) {
			return topology.LinksJSON(ctx, r, a.hostID)
		})
	case "firmware.detect":
		runJSON(func(ctx context.Context, r osRunner, _ string) (string, error) {
			devs, err := firmware.Detect(ctx, r)
			if err != nil {
				return "", err
			}
			b, err := json.Marshal(devs)
			return string(b), err
		})
	case "firmware.upgrade":
		// Double-gated: payload must carry i_understand_brick_risk=true AND
		// the agent must run with RACKANVIL_ALLOW_FW_UPGRADE=1 in its env.
		var p struct {
			Model                string `json:"model"`
			PCIAddr              string `json:"pci_addr"`
			FwFile               string `json:"fw_file"`
			FwDir                string `json:"fw_dir"`
			ControllerID         string `json:"controller_id"`
			CurrentFW            string `json:"current_fw"`
			IUnderstandBrickRisk bool   `json:"i_understand_brick_risk"`
		}
		if err := json.Unmarshal([]byte(t.Command), &p); err != nil {
			status, errMsg, code = "failed", "bad payload: "+err.Error(), 1
			break
		}
		allow := p.IUnderstandBrickRisk && os.Getenv("RACKANVIL_ALLOW_FW_UPGRADE") == "1"
		// Vendor flow passes a firmware package directory (files resolved
		// by pattern) plus the detected ControllerID; a single file still
		// works for handlers whose sequence needs only the main image.
		fwPath := p.FwFile
		if p.FwDir != "" {
			fwPath = p.FwDir
		}
		dev := firmware.Device{Model: p.Model, PCIAddr: p.PCIAddr, ControllerID: p.ControllerID, FW: p.CurrentFW}
		if p.CurrentFW != "" {
			dev.FWVersionInt = firmware.VersionToInt(p.CurrentFW)
		}
		out, err := firmware.Upgrade(ctx, r, dev, fwPath, allow)
		if err != nil {
			status, errMsg, code = "failed", err.Error(), 1
		} else {
			stdout, status, code = out, "done", 0
		}
	default:
		status, errMsg, code = "failed", "unsupported task type: "+t.Type, 1
	}
	if err := a.report(t, status, stdout, stderr, code, errMsg); err != nil {
		a.log.Printf("task: report failed for %s: %v", t.TaskID, err)
		return
	}
	a.log.Printf("task: %s finished status=%s exit=%d", t.TaskID, status, code)
}

// Run enrolls the agent and then heartbeats forever (or until ctx ends).
// initialize is retried with backoff because the server may not be up yet
// (e.g. agent started by initrd before the network/server is ready).
func (a *Agent) Run(ctx context.Context) error {
	// M11 first-boot gate: wait (bounded) for the server to be reachable
	// before enrolling, and carry the outcome into the enrollment.
	a.networkReady, a.networkWait = a.waitForServer(ctx)
	a.log.Printf("first-boot gate: server_ready=%v waited=%s (bound %s)",
		a.networkReady, a.networkWait.Round(time.Millisecond), a.readyTimeout)
	var err error
	for attempt := 1; ; attempt++ {
		err = a.initialize()
		if err == nil {
			break
		}
		a.log.Printf("initialize attempt %d failed: %v (retrying)", attempt, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	if a.metricsInterval > 0 {
		mticker := time.NewTicker(a.metricsInterval)
		defer mticker.Stop()
		go func() {
			// First upload soon after enroll so the dashboard isn't empty.
			time.Sleep(5 * time.Second)
			for {
				select {
				case <-ctx.Done():
					return
				case <-mticker.C:
					if err := a.reportMetrics(); err != nil {
						a.log.Printf("metrics error: %v", err)
					}
				}
			}
		}()
	}
	for {
		tasks, err := a.checkin()
		if err != nil {
			a.log.Printf("checkin error: %v", err)
		} else {
			a.log.Printf("checkin ok: %d task(s)", len(tasks))
			for _, t := range tasks {
				a.handleTask(t)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
