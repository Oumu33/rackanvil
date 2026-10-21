// Package relay implements the cross-site relay node ("多网段跨机房").
//
// Doc basis (vendor article "介绍一款支持多网段跨机房的管理软件" + README):
//   - 接口标签 Tags: PXE网络 / 业务网络 / 通用网络；DHCP 两种模式
//     Standalone / External DHCP；多网卡各自独立服务栈，互不干扰。
//   - 跨机房：主控节点（总部）+ 代理节点（分部/IDC），代理提供本地镜像
//     缓存 + PXE 引导服务，统一 Web 管理。
//
// Our clean-room design: a Relay is an HTTP service that fronts the master
// for one site. It caches bootscripts (keyed by MAC, short TTL) and boot
// assets (content-addressed by path) locally, so a remote site's PXE storm
// never crosses the WAN twice. It heartbeats to the master, which lists
// relay nodes on the topology view.
//
// Multi-subnet on one box (vendor's "接口独立设计") is a deployment pattern:
// run one pxe.Server per NIC with its own Config (see docs/M4-REPORT.md);
// the relay adds the cross-site dimension on top.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Node is a relay node registration on the master.
type Node struct {
	Name     string    `json:"name"`
	Subnets  []string  `json:"subnets"`
	LastSeen time.Time `json:"last_seen"`
}

// Relay fronts Upstream for one site.
type Relay struct {
	NodeName  string
	Upstream  string // e.g. http://master:9100
	CacheDir  string
	ScriptTTL time.Duration

	client      *http.Client
	mu          sync.Mutex
	scriptCache map[string]cachedScript
}

type cachedScript struct {
	body []byte
	at   time.Time
}

// New creates a relay; CacheDir is created if missing.
func New(nodeName, upstream, cacheDir string) (*Relay, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	return &Relay{
		NodeName: nodeName, Upstream: strings.TrimSuffix(upstream, "/"),
		CacheDir: cacheDir, ScriptTTL: 60 * time.Second,
		client:      &http.Client{Timeout: 30 * time.Second},
		scriptCache: map[string]cachedScript{},
	}, nil
}

// Handler serves the relay endpoints.
func (rl *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ipxe/bootscript", rl.handleBootScript)
	mux.HandleFunc("/ipxe/", rl.handleAsset)
	mux.HandleFunc("/api/v1/relay/health", rl.handleHealth)
	return mux
}

func (rl *Relay) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"node": rl.NodeName, "upstream": rl.Upstream})
}

// handleBootScript proxies the master's bootscript, caching per MAC.
func (rl *Relay) handleBootScript(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	rl.mu.Lock()
	c, ok := rl.scriptCache[mac]
	if ok && time.Since(c.at) < rl.ScriptTTL {
		body := c.body
		rl.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Relay-Cache", "HIT")
		w.Write(body)
		return
	}
	rl.mu.Unlock()

	up := rl.Upstream + "/api/v1/ipxe/bootscript?" + r.URL.RawQuery
	resp, err := rl.client.Get(up)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		http.Error(w, "upstream status "+resp.Status, http.StatusBadGateway)
		return
	}
	rl.mu.Lock()
	rl.scriptCache[mac] = cachedScript{body: body, at: time.Now()}
	rl.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("X-Relay-Cache", "MISS")
	w.Write(body)
}

// handleAsset serves boot assets from the local cache, fetching from
// upstream on miss ("本地镜像缓存").
func (rl *Relay) handleAsset(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/ipxe/")
	if rel == "" || strings.Contains(rel, "..") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	local := filepath.Join(rl.CacheDir, rel)
	if _, err := os.Stat(local); err == nil {
		w.Header().Set("X-Relay-Cache", "HIT")
		http.ServeFile(w, r, local)
		return
	}
	up := rl.Upstream + "/ipxe/" + rel
	resp, err := rl.client.Get(up)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		http.Error(w, "upstream status "+resp.Status, http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	os.MkdirAll(filepath.Dir(local), 0o755)
	os.WriteFile(local, body, 0o644)
	w.Header().Set("X-Relay-Cache", "MISS")
	w.Write(body)
}

// Heartbeat registers this node on the master. Call on a ticker.
func (rl *Relay) Heartbeat(ctx context.Context, subnets []string) error {
	body, _ := json.Marshal(Node{Name: rl.NodeName, Subnets: subnets, LastSeen: time.Now()})
	req, err := http.NewRequestWithContext(ctx, "POST",
		rl.Upstream+"/api/v1/relay/nodes", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := rl.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("heartbeat status %s", resp.Status)
	}
	return nil
}
