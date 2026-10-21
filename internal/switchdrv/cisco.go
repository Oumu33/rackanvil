package switchdrv

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Cisco IOS driver. Command output formats follow Cisco IOS/IOS-XE public
// documentation (`show mac address-table`, `show interfaces status`,
// `show vlan brief`).

type ciscoDriver struct{}

func (ciscoDriver) Vendor() Vendor            { return VendorCisco }
func (ciscoDriver) CmdShowMACTable() string   { return "show mac address-table" }
func (ciscoDriver) CmdShowPortStatus() string { return "show interfaces status" }
func (ciscoDriver) CmdShowVLANs() string      { return "show vlan brief" }

var ciscoMACRe = regexp.MustCompile(`(?i)^\s*(all|\d+)\s+([0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4})\s+(\S+)\s+(\S+)\s*$`)

func (ciscoDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := ciscoMACRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		vlan := -1
		if m[1] != "All" {
			vlan, _ = strconv.Atoi(m[1])
		}
		entries = append(entries, FDBEntry{
			MAC:  normalizeMAC(m[2]),
			VLAN: vlan,
			Type: strings.ToUpper(m[3]),
			Port: m[4],
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/cisco: no MAC entries parsed")
	}
	return entries, nil
}

// show interfaces status: Port [Name] Status Vlan Duplex Speed Type —
// the Name column is optional and free-form, so anchor on the status token.
var ciscoIfStatusRe = regexp.MustCompile(`^(\S+)\s+(?:(.*?)\s+)?(connected|notconnect|disabled|err-disabled|monitoring|suspended)\s+(trunk|routed|\d+)\s+(\S+)\s+(\S+)\s+(.+?)\s*$`)

func (ciscoDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	var ports []PortStatus
	for _, line := range strings.Split(out, "\n") {
		m := ciscoIfStatusRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ports = append(ports, PortStatus{
			Name:   m[1],
			Status: m[3],
			VLAN:   m[4],
			Duplex: m[5],
			Speed:  m[6],
		})
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("switchdrv/cisco: no port status parsed")
	}
	return ports, nil
}

var ciscoVLANRe = regexp.MustCompile(`^(\d+)\s+(\S+)\s+(active|act/unsup|suspended|act/lshut|sus/lshut)\s*(.*)$`)

func (ciscoDriver) ParseVLANs(out string) ([]VLAN, error) {
	var vlans []VLAN
	for _, line := range strings.Split(out, "\n") {
		if m := ciscoVLANRe.FindStringSubmatch(line); m != nil {
			id, _ := strconv.Atoi(m[1])
			v := VLAN{ID: id, Name: m[2]}
			v.Ports = append(v.Ports, splitPortList(m[4])...)
			vlans = append(vlans, v)
			continue
		}
		// Wrapped port list continuation lines are indented and comma-led.
		trimmed := strings.TrimSpace(line)
		if len(vlans) > 0 && (strings.HasPrefix(trimmed, "Gi") || strings.HasPrefix(trimmed, "Fa") || strings.HasPrefix(trimmed, "Te") || strings.HasPrefix(trimmed, "Po")) {
			vlans[len(vlans)-1].Ports = append(vlans[len(vlans)-1].Ports, splitPortList(trimmed)...)
		}
	}
	if len(vlans) == 0 {
		return nil, fmt.Errorf("switchdrv/cisco: no VLANs parsed")
	}
	return vlans, nil
}

func splitPortList(s string) []string {
	var ports []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			ports = append(ports, p)
		}
	}
	return ports
}

func (ciscoDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"configure terminal",
		"interface " + port,
		"switchport mode access",
		fmt.Sprintf("switchport access vlan %d", vlan),
		"end",
	}
}
