package switchdrv

// M8 vendor completion (2026-10-21): the vendor factory full set,
// recovered by Ghidra decompilation of switchdriver.GetDriver
// (0x14bf1c0) in the vendor server binary — qualitative evidence, not
// string-search absence:
//
//	GetDriver dispatches on the vendor string with exact compares:
//	  "H3C" -> H3CDriver        "ZTE"     -> ZTEDriver
//	  "Cisco" -> CiscoDriver    "Maipu"   -> MaipuDriver
//	  "Arista" -> AristaDriver  "Huawei"  -> HuaweiDriver
//	  "Inspur" -> InspurDriver  "Juniper" -> JuniperDriver
//	  "TP-Link" -> TPLinkDriver
//	  default  -> GenericDriver
//
// The binary's driver type set is exactly these 10 types (symbol
// table). "Ruijie" and "Raisecom" have NO driver type and NO dispatch
// branch: in the vendor product both fall through to GenericDriver,
// whose collection methods are stubs returning "not supported" style
// errors (GenericDriver.CollectMAC's recovered string prefix is
// "GenericDriver: CollectMAC "). Our ruijieDriver (vendors_m7.go) is
// therefore an extension beyond the vendor set, kept for RGOS shops;
// ForVendorName below mirrors the vendor dispatch exactly, including
// the Generic fallback for Ruijie/Raisecom/anything unknown.
//
// Per-driver verbatim evidence (M8 strings extraction):
//   - Inspur: CollectMAC and CollectInterfaces are SNMP-based
//     (Q-BRIDGE dot1qTpFdbPort 1.3.6.1.2.1.17.7.1.2.2.1.2; IF-MIB /
//     IFX-MIB 1.3.6.1.2.1.2.2.1.x, 1.3.6.1.2.1.31.1.1.1.x — the same
//     collection style as Maipu). SetPortVlan uses the CLI literals
//     "interface %s" + "switchport access vlan %d".
//   - TP-Link: ShowMACTable runs the CLI "show mac address-table"
//     (verbatim), SetVLAN uses "interface %s" +
//     "switchport access vlan %d" and a "copy ru..." save; GetVendor
//     returns "TP-LINK". Port status in the vendor driver is
//     SNMP-assisted (OID instance format "%s.%d").
//   - Generic: GetVendor returns "Generic"; Collect* are stubs.
//
// Our Driver interface is CLI-shaped, so the Inspur/TP-Link drivers
// below expose the vendor's verbatim config commands and the canonical
// CLI read commands for their NOS; the SNMP read paths are documented
// here rather than forced through the CLI interface.

import (
	"fmt"
)

// ---------------------------------------------------------------------------
// Inspur (ICNOS) — reads are SNMP in the vendor driver (see file doc);
// config literals below are binary-verbatim.
// ---------------------------------------------------------------------------

type inspurDriver struct{ cisco ciscoDriver }

func (inspurDriver) Vendor() Vendor            { return VendorInspur }
func (inspurDriver) CmdShowMACTable() string   { return "show mac-address-table" }
func (inspurDriver) CmdShowPortStatus() string { return "show interface brief" }
func (inspurDriver) CmdShowVLANs() string      { return "show vlan" }
func (i inspurDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	return i.cisco.ParseMACTable(out)
}
func (i inspurDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	return parseBriefPortStatus(out, "inspur")
}
func (i inspurDriver) ParseVLANs(out string) ([]VLAN, error) { return i.cisco.ParseVLANs(out) }

// AccessVLANCommands: "interface %s" and "switchport access vlan %d"
// are the vendor's binary-verbatim Inspur SetPortVlan literals; the
// wrapper lines follow the vendor's execCmds session shape.
func (inspurDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"configure terminal",
		fmt.Sprintf("interface %s", port),
		fmt.Sprintf("switchport access vlan %d", vlan),
		"end",
		"write",
	}
}

// ---------------------------------------------------------------------------
// TP-Link — "show mac address-table" is binary-verbatim (ShowMACTable).
// ---------------------------------------------------------------------------

type tplinkDriver struct{ cisco ciscoDriver }

func (tplinkDriver) Vendor() Vendor            { return VendorTPLink }
func (tplinkDriver) CmdShowMACTable() string   { return "show mac address-table" }
func (tplinkDriver) CmdShowPortStatus() string { return "show interface brief" }
func (tplinkDriver) CmdShowVLANs() string      { return "show vlan brief" }
func (t tplinkDriver) ParseMACTable(out string) ([]FDBEntry, error) {
	return t.cisco.ParseMACTable(out)
}
func (t tplinkDriver) ParsePortStatus(out string) ([]PortStatus, error) {
	return parseBriefPortStatus(out, "tplink")
}
func (t tplinkDriver) ParseVLANs(out string) ([]VLAN, error) { return t.cisco.ParseVLANs(out) }

// AccessVLANCommands: vendor SetVLAN literals "interface %s" +
// "switchport access vlan %d", saved with a "copy running-config
// startup-config" (recovered prefix "copy ru").
func (tplinkDriver) AccessVLANCommands(port string, vlan int) []string {
	return []string{
		"configure terminal",
		fmt.Sprintf("interface %s", port),
		fmt.Sprintf("switchport access vlan %d", vlan),
		"end",
		"copy running-config startup-config",
	}
}

// ---------------------------------------------------------------------------
// Generic — the vendor's fallback driver. Its collection methods are
// stubs; ours return explicit unsupported errors, mirroring that.
// ---------------------------------------------------------------------------

type genericDriver struct{}

func (genericDriver) Vendor() Vendor            { return VendorGeneric }
func (genericDriver) CmdShowMACTable() string   { return "" }
func (genericDriver) CmdShowPortStatus() string { return "" }
func (genericDriver) CmdShowVLANs() string      { return "" }
func (genericDriver) ParseMACTable(string) ([]FDBEntry, error) {
	return nil, fmt.Errorf("switchdrv/generic: CollectMAC not supported by the Generic driver")
}
func (genericDriver) ParsePortStatus(string) ([]PortStatus, error) {
	return nil, fmt.Errorf("switchdrv/generic: GetPortStatus not supported by the Generic driver")
}
func (genericDriver) ParseVLANs(string) ([]VLAN, error) {
	return nil, fmt.Errorf("switchdrv/generic: CollectVLAN not supported by the Generic driver")
}
func (genericDriver) AccessVLANCommands(string, int) []string { return nil }

// ForVendorName mirrors the vendor's GetDriver factory dispatch
// exactly (M8 decompile evidence, see file doc): the nine named vendor
// strings map to their drivers; EVERY other string — including
// "Ruijie", "Raisecom" and unknown vendors — falls through to the
// Generic driver. Matching is exact, as in the vendor binary.
func ForVendorName(name string) Driver {
	switch name {
	case "H3C":
		return h3cDriver{}
	case "ZTE":
		return zteDriver{}
	case "Cisco":
		return ciscoDriver{}
	case "Maipu":
		return maipuDriver{}
	case "Arista":
		return aristaDriver{}
	case "Huawei":
		return huaweiDriver{}
	case "Inspur":
		return inspurDriver{}
	case "Juniper":
		return juniperDriver{}
	case "TP-Link":
		return tplinkDriver{}
	}
	return genericDriver{}
}
