package switchdrv

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// H3C Comware driver. Command output formats follow H3C Comware V7 public
// documentation (`display mac-address`, `display interface brief`,
// `display vlan all`).

type h3cDriver struct{}

func (h3cDriver) Vendor() Vendor            { return VendorH3C }
func (h3cDriver) CmdShowMACTable() string   { return "display mac-address" }
func (h3cDriver) CmdShowPortStatus() string { return "display interface brief" }
func (h3cDriver) CmdShowVLANs() string      { return "display vlan all" }

var h3cMACRe = regexp.MustCompile(`(?i)^([0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4})\s+(\d+)\s+(\S+)\s+(\S+)`)

func (h3cDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := h3cMACRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		vlan, _ := strconv.Atoi(m[2])
		entries = append(entries, FDBEntry{
			MAC:  normalizeMAC(m[1]),
			VLAN: vlan,
			Type: m[3],
			Port: m[4],
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/h3c: no MAC entries parsed")
	}
	return entries, nil
}

// display interface brief (Comware V7 route mode):
//
//	Interface            Link Speed     Duplex  Link-type  PVID Description
//	GE1/0/1              UP   1G        (a)     access     1
//	GE1/0/2              DOWN auto      auto    trunk      1
var h3cIfBriefRe = regexp.MustCompile(`^(\S+)\s+(UP|DOWN|ADM)\s+(\S+)\s+(\S+)\s+(access|trunk|hybrid|route)\s+(\d+)`)

func (h3cDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	var ports []PortStatus
	for _, line := range strings.Split(out, "\n") {
		m := h3cIfBriefRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		ports = append(ports, PortStatus{
			Name:   m[1],
			Status: m[2],
			Speed:  m[3],
			Duplex: m[4],
			VLAN:   m[6], // PVID
		})
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("switchdrv/h3c: no port status parsed")
	}
	return ports, nil
}

// display vlan all: blocks keyed by "VLAN ID:" with "Tagged ports:" /
// "Untagged ports:" sections.
func (h3cDriver) ParseVLANs(out string) ([]VLAN, error) {
	var vlans []VLAN
	var cur *VLAN
	var inPorts bool
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "VLAN ID:") {
			id, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(trimmed, "VLAN ID:")))
			vlans = append(vlans, VLAN{ID: id})
			cur = &vlans[len(vlans)-1]
			inPorts = false
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(trimmed, "Name:") {
			cur.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))
			continue
		}
		if strings.HasPrefix(trimmed, "Tagged ports:") || strings.HasPrefix(trimmed, "Untagged ports:") {
			inPorts = true
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Tagged ports:"))
			if strings.HasPrefix(trimmed, "Untagged") {
				rest = strings.TrimSpace(strings.TrimPrefix(trimmed, "Untagged ports:"))
			}
			if rest != "" && rest != "none" {
				cur.Ports = append(cur.Ports, strings.Fields(rest)...)
			}
			continue
		}
		if inPorts {
			if trimmed == "" || strings.Contains(trimmed, ":") {
				inPorts = false
				continue
			}
			cur.Ports = append(cur.Ports, strings.Fields(trimmed)...)
		}
	}
	if len(vlans) == 0 {
		return nil, fmt.Errorf("switchdrv/h3c: no VLANs parsed")
	}
	return vlans, nil
}

// InitCommands: "screen-length disable" is the vendor binary's verbatim
// Comware session-init string (M7).
func (h3cDriver) InitCommands() []string { return []string{"screen-length disable"} }

func (h3cDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"system-view",
		"interface " + port,
		"port link-type access",
		fmt.Sprintf("port access vlan %d", vlan),
		"quit",
	}
}
