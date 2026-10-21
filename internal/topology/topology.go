// Package topology maps server NICs to their uplink switch ports
// ("智能网络拓扑寻址").
//
// Doc basis (vendor article "大规模数据中心裸金属自动化装机与纳管实践",
// design point 6): "依托网络探针模块…通过与核心交换机的 ARP 与 FDB 表联动，
// 自动精准标定服务器上联的交换机名称及具体的物理端口".
//
// Our clean-room design has two collection paths:
//  1. LLDP (preferred): `lldpctl -f json` on the host gives, per local
//     interface, the neighbor switch system-name + port-id. No switch
//     credentials needed.
//  2. ARP/FDB联动 (documented, switch-side): given a switch's ARP/FDB dump
//     (imported via API), we resolve host MAC -> switch port. This needs
//     switch access, so it is a documented manual-import path.
//
// Links are stored in the topo_links table and rendered on the Web topology
// view. Without a real switch/lldpd, collection is verified with mock data
// (honestly labeled in the report).
package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Runner executes commands.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// Link is one resolved uplink: host NIC -> switch port.
type Link struct {
	HostID     string    `json:"host_id"`
	HostMAC    string    `json:"host_mac"`
	Iface      string    `json:"iface"`       // local interface, e.g. eth0
	SwitchName string    `json:"switch_name"` // neighbor system name
	SwitchPort string    `json:"switch_port"` // neighbor port id, e.g. Gi1/0/1
	Source     string    `json:"source"`      // "lldp" | "arp-fdb" | "manual"
	SeenAt     time.Time `json:"seen_at"`
}

// lldpJSON is the subset of `lldpctl -f json` we parse.
type lldpJSON struct {
	LLDP struct {
		Interface map[string]struct {
			Chassis map[string]struct {
				Name struct {
					Value string `json:"value"`
				} `json:"name"`
			} `json:"chassis"`
			Port map[string]struct {
				Descr struct {
					Value string `json:"value"`
				} `json:"descr"`
			} `json:"port"`
		} `json:"interface"`
	} `json:"lldp"`
}

// CollectLLDP parses `lldpctl -f json` into links.
func CollectLLDP(ctx context.Context, r Runner, hostID string) ([]Link, error) {
	out, _, err := r.Run(ctx, "lldpctl", "-f", "json")
	if err != nil {
		return nil, fmt.Errorf("lldpctl: %w (is lldpd running?)", err)
	}
	var parsed lldpJSON
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("lldpctl parse: %w", err)
	}
	var links []Link
	for iface, data := range parsed.LLDP.Interface {
		for _, ch := range data.Chassis {
			for _, p := range data.Port {
				links = append(links, Link{
					HostID: hostID, Iface: iface,
					SwitchName: ch.Name.Value, SwitchPort: p.Descr.Value,
					Source: "lldp", SeenAt: time.Now(),
				})
			}
		}
	}
	return links, nil
}

// FDBEntry is one row of a switch MAC/FDB table import.
type FDBEntry struct {
	SwitchName string `json:"switch_name"`
	Port       string `json:"port"`
	MAC        string `json:"mac"`
	VLAN       int    `json:"vlan"`
}

// ResolveByFDB maps host MACs to switch ports from an imported FDB dump.
// This is the "ARP 与 FDB 表联动" path from the vendor doc.
func ResolveByFDB(hostID string, macs []string, fdb []FDBEntry) []Link {
	byMAC := map[string]FDBEntry{}
	for _, e := range fdb {
		byMAC[strings.ToLower(e.MAC)] = e
	}
	var links []Link
	for _, m := range macs {
		if e, ok := byMAC[strings.ToLower(m)]; ok {
			links = append(links, Link{
				HostID: hostID, HostMAC: m,
				SwitchName: e.SwitchName, SwitchPort: e.Port,
				Source: "arp-fdb", SeenAt: time.Now(),
			})
		}
	}
	return links
}

// LinksJSON is the agent task contract for `topo.collect`.
func LinksJSON(ctx context.Context, r Runner, hostID string) (string, error) {
	links, err := CollectLLDP(ctx, r, hostID)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(links)
	return string(b), err
}
