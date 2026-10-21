package switchdrv

// M8 tests: vendor factory full set (GetDriver decompile evidence) and
// the newly aligned Inspur / TP-Link / Maipu-verbatim command sets.

import (
	"strings"
	"testing"
)

func TestForVendorNameFactoryDispatch(t *testing.T) {
	named := map[string]Vendor{
		"H3C": VendorH3C, "ZTE": VendorZTE, "Cisco": VendorCisco,
		"Maipu": VendorMaipu, "Arista": VendorArista, "Huawei": VendorHuawei,
		"Inspur": VendorInspur, "Juniper": VendorJuniper, "TP-Link": VendorTPLink,
	}
	for name, want := range named {
		if got := ForVendorName(name).Vendor(); got != want {
			t.Errorf("ForVendorName(%q).Vendor() = %q, want %q", name, got, want)
		}
	}
	// The vendor factory has NO Ruijie and NO Raisecom branch: both —
	// and anything else — fall through to the Generic driver. This is
	// the M8 qualitative conclusion from the GetDriver decompile.
	for _, name := range []string{"Ruijie", "Raisecom", "ruijie", "Unknown", ""} {
		if got := ForVendorName(name).Vendor(); got != VendorGeneric {
			t.Errorf("ForVendorName(%q).Vendor() = %q, want generic fallback", name, got)
		}
	}
}

func TestGenericDriverStubs(t *testing.T) {
	d, err := ForVendor(VendorGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ParseMACTable("anything"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("generic ParseMACTable err = %v", err)
	}
	if cmds := d.AccessVLANCommands("eth0", 10); cmds != nil {
		t.Errorf("generic AccessVLANCommands = %v, want nil", cmds)
	}
}

func TestInspurTPLinkVerbatim(t *testing.T) {
	inspur, err := ForVendor(VendorInspur)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(inspur.AccessVLANCommands("ge1/0/1", 100), "\n")
	if !strings.Contains(joined, "interface ge1/0/1") || !strings.Contains(joined, "switchport access vlan 100") {
		t.Errorf("inspur access cmds:\n%s", joined)
	}

	tp, err := ForVendor(VendorTPLink)
	if err != nil {
		t.Fatal(err)
	}
	if tp.CmdShowMACTable() != "show mac address-table" {
		t.Errorf("tplink mac cmd = %q", tp.CmdShowMACTable())
	}
	joined = strings.Join(tp.AccessVLANCommands("Gi1/0/2", 20), "\n")
	if !strings.Contains(joined, "switchport access vlan 20") || !strings.Contains(joined, "copy running-config startup-config") {
		t.Errorf("tplink access cmds:\n%s", joined)
	}
}

func TestMaipuVerbatimTemplates(t *testing.T) {
	d, err := ForVendor(VendorMaipu)
	if err != nil {
		t.Fatal(err)
	}
	m := d.(maipuDriver)
	access := strings.Join(m.AccessVLANCommands("ge0/1", 30), "\n")
	for _, want := range []string{"configure terminal", "interface ge0/1", "switchport mode access", "switchport access vlan 30", "write memory"} {
		if !strings.Contains(access, want) {
			t.Errorf("maipu access missing %q:\n%s", want, access)
		}
	}
	trunk := strings.Join(m.TrunkVLANCommands("ge0/1", 1, []int{10, 20}), "\n")
	for _, want := range []string{"switchport mode trunk", "switchport trunk native vlan 1", "switchport trunk allowed vlan add 10", "switchport trunk allowed vlan add 20"} {
		if !strings.Contains(trunk, want) {
			t.Errorf("maipu trunk missing %q:\n%s", want, trunk)
		}
	}
	create := strings.Join(m.CreateVLANCommands(30, "prod"), "\n")
	if !strings.Contains(create, "vlan 30") || !strings.Contains(create, "name prod") {
		t.Errorf("maipu create vlan:\n%s", create)
	}
}
