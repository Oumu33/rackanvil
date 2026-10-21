package switchdrv

// M7 vendor expansion (2026-10-21), aligned to the vendor server binary's
// own switchdriver package (deplOS/pkg/switchdriver/vendors, go1.26.3
// build, symbols recovered via strings):
//
//   - The vendor ships drivers for Arista/Cisco/H3C/Huawei/Inspur/
//     Juniper/Maipu/TPLink/ZTE + Generic. A ruijie.go source file exists
//     but exports NO driver symbols in this build (Ruijie is presumably
//     served by the GenericDriver). "Raisecom" appears NOWHERE in the
//     binary (no symbols, no log tags, no enterprise OID 8886) — not
//     implemented here; inventing it would be fabrication.
//   - CLI literals recovered verbatim from the binary and used below:
//     ZTE "show mac address-table" ([ZTEDriver] log context);
//     Junos "show ethernet-switching table", session init
//     "set cli screen-width 0" / "set cli terminal vt100", and the config
//     template "set interfaces %s unit 0 family ethernet-switching vlan
//     members %d"; Huawei session init "screen-length 0 temporary" and
//     VRP port commands "port default vlan %d", "port trunk permit
//     vlan %d", "port trunk allow-pass vlan %d", "undo port hybrid
//     vlan all", rate limit "qos lr inbound/outbound cir %d".
//   - For Huawei/H3C the vendor's FDB collection is SNMP-based
//     (BRIDGE-MIB dot1dTpFdbPort 1.3.6.1.2.1.17.4.3.1.2 and
//     dot1dBasePortIfIndex 1.3.6.1.2.1.17.1.4.1.2 are in the binary; the
//     literal "display mac-address" is NOT). Our CLI drivers keep the
//     NOS-standard CLI equivalents — labeled as such, not as vendor
//     verbatim.
//   - Remaining command sets (Arista/Ruijie/Maipu/ZTE port & VLAN queries)
//     follow each NOS's canonical public syntax; sample outputs in tests
//     are SYNTHETIC constructions in those formats.
//
// Session paging: drivers may implement SessionInitializer; the vendor's
// per-session init strings are binary-verbatim where noted.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// SessionInitializer is an optional interface: commands to run once after
// login to disable paging etc.
type SessionInitializer interface {
	InitCommands() []string
}

// SessionInit returns the driver's session-init commands, if any.
func SessionInit(d Driver) []string {
	if si, ok := d.(SessionInitializer); ok {
		return si.InitCommands()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Huawei VRP
// ---------------------------------------------------------------------------

type huaweiDriver struct{}

func (huaweiDriver) Vendor() Vendor            { return VendorHuawei }
func (huaweiDriver) CmdShowMACTable() string   { return "display mac-address" }
func (huaweiDriver) CmdShowPortStatus() string { return "display interface brief" }
func (huaweiDriver) CmdShowVLANs() string      { return "display vlan" }

// InitCommands: "screen-length 0 temporary" is binary-verbatim (VRP).
func (huaweiDriver) InitCommands() []string { return []string{"screen-length 0 temporary"} }

var huaweiMACRe = regexp.MustCompile(`^([0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4})\s+(\d+)(?:/\S+)?\s+(\S+)\s+(\S+)\s*$`)

func (huaweiDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := huaweiMACRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		vlan, _ := strconv.Atoi(m[2])
		entries = append(entries, FDBEntry{MAC: normalizeMAC(m[1]), VLAN: vlan, Port: m[3], Type: strings.ToUpper(m[4])})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/huawei: no MAC entries parsed")
	}
	return entries, nil
}

var huaweiIfBriefRe = regexp.MustCompile(`^(\S+)\s+(up|down|\*down)\s+(up|down)\s`)

func (huaweiDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	var ports []PortStatus
	for _, line := range strings.Split(out, "\n") {
		m := huaweiIfBriefRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		ports = append(ports, PortStatus{Name: m[1], Status: m[2]})
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("switchdrv/huawei: no port status parsed")
	}
	return ports, nil
}

var (
	huaweiVLANRe  = regexp.MustCompile(`^(\d+)\s+(enable|disable)\s+\S+\s+\S+\s+\S+\s*(.*?)\s*$`)
	huaweiPortTag = regexp.MustCompile(`^(TG|UT|TP|UP):\s*(.+)$`)
)

func (huaweiDriver) ParseVLANs(out string) ([]VLAN, error) {
	var vlans []VLAN
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := huaweiVLANRe.FindStringSubmatch(trimmed); m != nil {
			id, _ := strconv.Atoi(m[1])
			vlans = append(vlans, VLAN{ID: id, Name: m[3]})
			continue
		}
		if m := huaweiPortTag.FindStringSubmatch(trimmed); m != nil && len(vlans) > 0 {
			vlans[len(vlans)-1].Ports = append(vlans[len(vlans)-1].Ports, splitPortList(m[2])...)
		}
	}
	if len(vlans) == 0 {
		return nil, fmt.Errorf("switchdrv/huawei: no VLANs parsed")
	}
	return vlans, nil
}

func (huaweiDriver) AccessVLANCommands(port string, vlan int) []string {
	// "port default vlan %d" is binary-verbatim VRP; "port link-type
	// access" is the canonical VRP mode command.
	return []string{
		"system-view",
		"interface " + port,
		"port link-type access",
		fmt.Sprintf("port default vlan %d", vlan),
		"quit",
		"quit",
	}
}

// ---------------------------------------------------------------------------
// Arista EOS — output layouts match Cisco IOS; the vendor ships a dedicated
// AristaDriver and the binary contains "show mac address-table".
// ---------------------------------------------------------------------------

type aristaDriver struct{ cisco ciscoDriver }

func (aristaDriver) Vendor() Vendor            { return VendorArista }
func (aristaDriver) CmdShowMACTable() string   { return "show mac address-table" }
func (aristaDriver) CmdShowPortStatus() string { return "show interfaces status" }
func (aristaDriver) CmdShowVLANs() string      { return "show vlan brief" }
func (a aristaDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	return a.cisco.ParseMACTable(out)
}
func (a aristaDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	return a.cisco.ParsePortStatus(out)
}
func (a aristaDriver) ParseVLANs(out string) ([]VLAN, error) { return a.cisco.ParseVLANs(out) }
func (aristaDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"configure terminal",
		"interface " + port,
		"switchport mode access",
		fmt.Sprintf("switchport access vlan %d", vlan),
		"end",
	}
}

// ---------------------------------------------------------------------------
// Ruijie RGOS — no dedicated driver symbols in the vendor binary (see file
// doc); commands follow RGOS canonical syntax ("show mac-address-table"
// with hyphen is the RGOS form).
// ---------------------------------------------------------------------------

type ruijieDriver struct{ cisco ciscoDriver }

func (ruijieDriver) Vendor() Vendor            { return VendorRuijie }
func (ruijieDriver) CmdShowMACTable() string   { return "show mac-address-table" }
func (ruijieDriver) CmdShowPortStatus() string { return "show interfaces status" }
func (ruijieDriver) CmdShowVLANs() string      { return "show vlan" }

var ruijieMACRe = regexp.MustCompile(`^(\d+)\s+([0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4})\s+(\S+)\s+(.+?)\s*$`)

func (ruijieDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := ruijieMACRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		vlan, _ := strconv.Atoi(m[1])
		entries = append(entries, FDBEntry{MAC: normalizeMAC(m[2]), VLAN: vlan, Type: strings.ToUpper(m[3]), Port: m[4]})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/ruijie: no MAC entries parsed")
	}
	return entries, nil
}
func (r ruijieDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	return r.cisco.ParsePortStatus(out)
}
func (r ruijieDriver) ParseVLANs(out string) ([]VLAN, error) { return r.cisco.ParseVLANs(out) }
func (ruijieDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"configure terminal",
		"interface " + port,
		"switchport mode access",
		fmt.Sprintf("switchport access vlan %d", vlan),
		"end",
	}
}

// ---------------------------------------------------------------------------
// Maipu MyPower OS — M8 update: the vendor's MaipuDriver reads are
// SNMP-based, not CLI: CollectMAC walks Q-BRIDGE dot1qTpFdbPort
// (1.3.6.1.2.1.17.7.1.2.2.1.2), CollectInterfaces / GetPortStatus use
// IF-MIB + IFX-MIB OIDs, and vendor verification checks the enterprise
// OID 1.3.6.1.4.1.5651 (sysObjectID / model strings "maipu",
// "mypoweros", "mp-"). The CLI commands below for reads are the NOS's
// canonical forms (kept for our CLI-shaped interface); the CONFIG
// templates are now binary-verbatim (M8 strings extraction of
// MaipuDriver.ConfigurePortVLAN / ConfigureVLAN): "interface %s",
// "switchport mode %s", "switchport access vlan %d",
// "switchport trunk native vlan %d",
// "switchport trunk allowed vlan add %d", "vlan %d", "name %s",
// wrapped in "configure terminal" ... "write memory"; VLAN IDs are
// validated 1-4094 ("invalid vlan id: %d (must be 1-4094)").
// ---------------------------------------------------------------------------

type maipuDriver struct{ cisco ciscoDriver }

func (maipuDriver) Vendor() Vendor            { return VendorMaipu }
func (maipuDriver) CmdShowMACTable() string   { return "show mac-address-table" }
func (maipuDriver) CmdShowPortStatus() string { return "show interface brief" }
func (maipuDriver) CmdShowVLANs() string      { return "show vlan" }

var maipuMACRe = regexp.MustCompile(`^(\d+)\s+([0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}|[0-9a-f]{2}(?::[0-9a-f]{2}){5})\s+(\S+)\s+(\S+)\s*$`)

func (maipuDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := maipuMACRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		vlan, _ := strconv.Atoi(m[1])
		entries = append(entries, FDBEntry{MAC: normalizeMAC(m[2]), VLAN: vlan, Type: strings.ToUpper(m[3]), Port: m[4]})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/maipu: no MAC entries parsed")
	}
	return entries, nil
}
func (maipuDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	return parseBriefPortStatus(out, "maipu")
}
func (m maipuDriver) ParseVLANs(out string) ([]VLAN, error) { return m.cisco.ParseVLANs(out) }
func (maipuDriver) AccessVLANCommands(port string, vlan int) []string {
	// Binary-verbatim Maipu ConfigurePortVLAN sequence (M8).
	return []string{
		"configure terminal",
		"interface " + port,
		"switchport mode access",
		fmt.Sprintf("switchport access vlan %d", vlan),
		"write memory",
	}
}

// TrunkVLANCommands renders the vendor's verbatim Maipu trunk
// templates: "switchport mode trunk", "switchport trunk native
// vlan %d", "switchport trunk allowed vlan add %d" (M8).
func (maipuDriver) TrunkVLANCommands(port string, native int, allowed []int) []string {
	cmds := []string{
		"configure terminal",
		"interface " + port,
		"switchport mode trunk",
		fmt.Sprintf("switchport trunk native vlan %d", native),
	}
	for _, v := range allowed {
		cmds = append(cmds, fmt.Sprintf("switchport trunk allowed vlan add %d", v))
	}
	return append(cmds, "write memory")
}

// CreateVLANCommands renders the vendor's verbatim Maipu ConfigureVLAN
// templates: "vlan %d" + "name %s" (M8).
func (maipuDriver) CreateVLANCommands(vlan int, name string) []string {
	return []string{
		"configure terminal",
		fmt.Sprintf("vlan %d", vlan),
		fmt.Sprintf("name %s", name),
		"write memory",
	}
}

// parseBriefPortStatus tolerantly parses "brief" interface listings whose
// exact column sets vary: first field is the interface name and the first
// standalone up/down token is the link status.
func parseBriefPortStatus(out, vendor string) ([]PortStatus, error) {
	var ports []PortStatus
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		status := ""
		for _, f := range fields[1:] {
			lf := strings.ToLower(f)
			if lf == "up" || lf == "down" {
				status = lf
				break
			}
		}
		if status == "" || !looksLikeInterface(fields[0]) {
			continue
		}
		ports = append(ports, PortStatus{Name: fields[0], Status: status})
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("switchdrv/%s: no port status parsed", vendor)
	}
	return ports, nil
}

func looksLikeInterface(s string) bool {
	return strings.Contains(s, "/") || strings.Contains(s, "-") && (strings.HasPrefix(s, "ge") || strings.HasPrefix(s, "xe") || strings.HasPrefix(s, "et"))
}

// ---------------------------------------------------------------------------
// ZTE ZXR10 — "show mac address-table" is binary-verbatim with the
// [ZTEDriver] log tag in the vendor binary.
// ---------------------------------------------------------------------------

type zteDriver struct{}

func (zteDriver) Vendor() Vendor            { return VendorZTE }
func (zteDriver) CmdShowMACTable() string   { return "show mac address-table" }
func (zteDriver) CmdShowPortStatus() string { return "show interface brief" }
func (zteDriver) CmdShowVLANs() string      { return "show vlan" }

var zteMACRe = regexp.MustCompile(`^([0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}|[0-9a-f]{2}(?::[0-9a-f]{2}){5})\s+(\d+)\s+(\S+)\s+(\S+)\s*$`)

func (zteDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := zteMACRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		vlan, _ := strconv.Atoi(m[2])
		entries = append(entries, FDBEntry{MAC: normalizeMAC(m[1]), VLAN: vlan, Port: m[3], Type: strings.ToUpper(m[4])})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/zte: no MAC entries parsed")
	}
	return entries, nil
}
func (zteDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	return parseBriefPortStatus(out, "zte")
}

var (
	zteVLANIDRe   = regexp.MustCompile(`(?i)^vlan\s*(?:id)?\s*[: ]\s*(\d+)\s*$`)
	zteVLANNameRe = regexp.MustCompile(`(?i)^name\s*[: ]\s*(\S+)`)
	ztePortsRe    = regexp.MustCompile(`(?i)^ports?\s*[: ]\s*(.+)$`)
)

func (zteDriver) ParseVLANs(out string) ([]VLAN, error) {
	var vlans []VLAN
	var cur *VLAN
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if m := zteVLANIDRe.FindStringSubmatch(trimmed); m != nil {
			id, _ := strconv.Atoi(m[1])
			vlans = append(vlans, VLAN{ID: id})
			cur = &vlans[len(vlans)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if m := zteVLANNameRe.FindStringSubmatch(trimmed); m != nil {
			cur.Name = m[1]
		}
		if m := ztePortsRe.FindStringSubmatch(trimmed); m != nil {
			for _, p := range strings.Fields(strings.NewReplacer("(U)", " ", "(T)", " ", ",", " ").Replace(m[1])) {
				cur.Ports = append(cur.Ports, p)
			}
		}
	}
	if len(vlans) == 0 {
		return nil, fmt.Errorf("switchdrv/zte: no VLANs parsed")
	}
	return vlans, nil
}
func (zteDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"configure terminal",
		"interface " + port,
		"switchport mode access",
		fmt.Sprintf("switchport access vlan %d", vlan),
		"end",
	}
}

// ---------------------------------------------------------------------------
// Juniper Junos — FDB command and config template are binary-verbatim.
// ---------------------------------------------------------------------------

type juniperDriver struct{}

func (juniperDriver) Vendor() Vendor            { return VendorJuniper }
func (juniperDriver) CmdShowMACTable() string   { return "show ethernet-switching table" }
func (juniperDriver) CmdShowPortStatus() string { return "show interfaces terse" }
func (juniperDriver) CmdShowVLANs() string      { return "show vlans" }

// InitCommands: both strings are binary-verbatim (Junos session setup).
func (juniperDriver) InitCommands() []string {
	return []string{"set cli screen-width 0", "set cli terminal vt100"}
}

var juniperMACRe = regexp.MustCompile(`^(\S+)\s+([0-9a-f]{2}(?::[0-9a-f]{2}){5})\s+(\S+)\s+\S+\s+(\S+)\s*$`)

func (juniperDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	var entries []FDBEntry
	for _, line := range strings.Split(out, "\n") {
		m := juniperMACRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		vlan := -1 // Junos reports the VLAN *name*; numeric names map to IDs
		if n, err := strconv.Atoi(m[1]); err == nil {
			vlan = n
		}
		entries = append(entries, FDBEntry{MAC: normalizeMAC(m[2]), VLAN: vlan, Type: strings.ToUpper(m[3]), Port: m[4]})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("switchdrv/juniper: no MAC entries parsed")
	}
	return entries, nil
}

var juniperTerseRe = regexp.MustCompile(`^(\S+)\s+(up|down)\s+(up|down)\b`)

func (juniperDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	var ports []PortStatus
	for _, line := range strings.Split(out, "\n") {
		m := juniperTerseRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || !strings.Contains(m[1], "-") {
			continue
		}
		ports = append(ports, PortStatus{Name: m[1], Status: m[3]})
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("switchdrv/juniper: no port status parsed")
	}
	return ports, nil
}

var juniperVLANRe = regexp.MustCompile(`^\S+\s+(\S+)\s+(\d+)\s+(.+?)\s*$`)

func (juniperDriver) ParseVLANs(out string) ([]VLAN, error) {
	var vlans []VLAN
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Routing instance") {
			continue
		}
		m := juniperVLANRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[2])
		v := VLAN{ID: id, Name: m[1]}
		for _, p := range strings.Fields(m[3]) {
			v.Ports = append(v.Ports, strings.TrimSuffix(p, "*"))
		}
		vlans = append(vlans, v)
	}
	if len(vlans) == 0 {
		return nil, fmt.Errorf("switchdrv/juniper: no VLANs parsed")
	}
	return vlans, nil
}

func (juniperDriver) AccessVLANCommands(port string, vlan int) []string {
	// The vlan-members line is the vendor's binary-verbatim Junos template:
	// "set interfaces %s unit 0 family ethernet-switching vlan members %d".
	return []string{
		fmt.Sprintf("set interfaces %s unit 0 family ethernet-switching interface-mode access", port),
		fmt.Sprintf("set interfaces %s unit 0 family ethernet-switching vlan members %d", port, vlan),
	}
}
