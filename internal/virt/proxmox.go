// Proxmox VE API client skeleton for package virt.
//
// IMPORTANT — honest boundary: this file is a typed HTTP + parsing
// skeleton only. It has NOT been verified against a real Proxmox VE
// server ("未对真 PVE 验证"). Only the JSON response parsing is covered
// by unit tests, and those tests use SYNTHETIC sample payloads that mimic
// the documented PVE API response envelope ({"data": [...]}) — no real
// server was contacted.
//
// Live request helpers exist so that wiring this up later means adding
// integration coverage against a real cluster, not re-shaping the API.
package virt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PVEClient is a minimal Proxmox VE API client authenticated with an
// API token. See the header comment: not verified against a real server.
type PVEClient struct {
	baseURL string // e.g. https://pve.example.com:8006 (no trailing slash)
	tokenID string // e.g. user@pam!tokenid
	secret  string
	http    *http.Client
}

// NewPVEClient builds a PVEClient. It performs no network I/O.
func NewPVEClient(baseURL, tokenID, secret string) *PVEClient {
	baseURL = strings.TrimRight(baseURL, "/")
	return &PVEClient{
		baseURL: baseURL,
		tokenID: tokenID,
		secret:  secret,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// NewPVEClientWithHTTP is NewPVEClient with a caller-provided HTTP client
// (e.g. one whose transport skips TLS verification for lab use).
func NewPVEClientWithHTTP(baseURL, tokenID, secret string, hc *http.Client) *PVEClient {
	c := NewPVEClient(baseURL, tokenID, secret)
	if hc != nil {
		c.http = hc
	}
	return c
}

// PVENode is one entry of GET /api2/json/nodes.
type PVENode struct {
	Node   string `json:"node"`
	Status string `json:"status"`
	CPU    int    `json:"cpu"`
	MaxCPU int    `json:"maxcpu"`
	Mem    int64  `json:"mem"`
	MaxMem int64  `json:"maxmem"`
}

// PVEVM is one entry of GET /api2/json/nodes/{node}/qemu.
type PVEVM struct {
	VMID     int    `json:"vmid"`
	Name     string `json:"name"`
	Status   string `json:"status"` // e.g. "running", "stopped"
	CPUs     int    `json:"cpus"`
	Mem      int64  `json:"mem"`    // bytes
	MaxMem   int64  `json:"maxmem"` // bytes
	PID      int    `json:"pid"`
	Template int    `json:"template"` // 1 if template, 0/absent otherwise
}

// buildRequest constructs an authenticated PVE API GET request for an
// API-relative path such as "/api2/json/nodes".
func (c *PVEClient) buildRequest(ctx context.Context, path string) (*http.Request, error) {
	if c == nil || c.baseURL == "" {
		return nil, errors.New("virt: PVE base URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("virt: build PVE request: %w", err)
	}
	if c.tokenID != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+c.tokenID+"="+c.secret)
	}
	return req, nil
}

// get performs an authenticated GET against the PVE API and returns the
// raw response body. Not exercised against a real server (see header).
func (c *PVEClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := c.buildRequest(ctx, path)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("virt: PVE GET %s: %w", path, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("virt: read PVE response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("virt: PVE GET %s: HTTP %s", path, res.Status)
	}
	return body, nil
}

// ListNodes returns the cluster nodes reported by the PVE API.
func (c *PVEClient) ListNodes(ctx context.Context) ([]PVENode, error) {
	body, err := c.get(ctx, "/api2/json/nodes")
	if err != nil {
		return nil, err
	}
	return ParseNodes(body)
}

// ListVMsOnNode returns the QEMU guests on one node.
func (c *PVEClient) ListVMsOnNode(ctx context.Context, node string) ([]PVEVM, error) {
	if node == "" {
		return nil, errors.New("virt: PVE node name is required")
	}
	body, err := c.get(ctx, "/api2/json/nodes/"+node+"/qemu")
	if err != nil {
		return nil, err
	}
	return ParseVMs(body)
}

// pveEnvelope is the PVE API response envelope: {"data": <payload>}.
type pveEnvelope struct {
	Data json.RawMessage `json:"data"`
}

func unwrapData(body []byte) (json.RawMessage, error) {
	var env pveEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("virt: parse PVE envelope: %w", err)
	}
	if len(env.Data) == 0 {
		return nil, errors.New("virt: PVE response has no data field")
	}
	return env.Data, nil
}

// ParseNodes parses a PVE /nodes response body into PVENode entries.
// Parsing only; the sample used by tests is SYNTHETIC.
func ParseNodes(body []byte) ([]PVENode, error) {
	data, err := unwrapData(body)
	if err != nil {
		return nil, err
	}
	var nodes []PVENode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("virt: parse PVE nodes: %w", err)
	}
	return nodes, nil
}

// ParseVMs parses a PVE /nodes/{node}/qemu response body into PVEVM
// entries. Parsing only; the sample used by tests is SYNTHETIC.
func ParseVMs(body []byte) ([]PVEVM, error) {
	data, err := unwrapData(body)
	if err != nil {
		return nil, err
	}
	var vms []PVEVM
	if err := json.Unmarshal(data, &vms); err != nil {
		return nil, fmt.Errorf("virt: parse PVE VMs: %w", err)
	}
	return vms, nil
}
