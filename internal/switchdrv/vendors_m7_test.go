package switchdrv

// M7 vendor tests. ALL sample outputs in this file are SYNTHETIC: they were
// constructed in each NOS's canonical output format to exercise the parsers
// (command strings themselves are aligned per vendors_m7.go provenance).
// No real switch was involved.

import (
	"reflect"
	"testing"
)

// --- Huawei VRP ---

const huaweiMAC = `MAC Address    VLAN/VSI  Learned-From     Type
001e-90a1-b2c3 100/-     GE0/0/1          dynamic
aabb-ccdd-ee01 200/-     GE0/0/2          dynamic
`

const huaweiIfBrief = `PHY: Physical   *down: administratively down
Interface  PHY  Protocol  InUti  OutUti  inErrors  outErrors
GE0/0/1    up   up        0.01%  0.01%   0         0
GE0/0/2    down down      0%     0%      0         0
`

const huaweiVLAN = `VID  Status  Property  MAC-LRN  Statistics  Description
1    enable  default   enable   disable     VLAN 0001
100  enable  common    enable   disable     prod-net
`

func TestHuaweiDriver(t *testing.T) {
	d, err := ForVendor(VendorHuawei)
	if err != nil {
		t.Fatal(err)
	}
	if d.CmdShowMACTable() != "display mac-address" {
		t.Errorf("cmd = %q", d.CmdShowMACTable())
	}
	if got := SessionInit(d); !reflect.DeepEqual(got, []string{"screen-length 0 temporary"}) {
		t.Errorf("init = %v", got)
	}
	entries, err := d.ParseMACTable(huaweiMAC)
	if err != nil || len(entries) != 2 {
		t.Fatalf("mac: %v %+v", err, entries)
	}
	if entries[0].MAC != "00:1e:90:a1:b2:c3" || entries[0].VLAN != 100 || entries[0].Port != "GE0/0/1" {
		t.Errorf("entry0 = %+v", entries[0])
	}
	ports, err := d.ParsePortStatus(huaweiIfBrief)
	if err != nil || len(ports) != 2 || ports[0].Status != "up" || ports[1].Status != "down" {
		t.Errorf("ports: %v %+v", err, ports)
	}
	vlans, err := d.ParseVLANs(huaweiVLAN)
	if err != nil || len(vlans) != 2 || vlans[1].ID != 100 || vlans[1].Name != "prod-net" {
		t.Errorf("vlans: %v %+v", err, vlans)
	}
	cmds := d.AccessVLANCommands("GE0/0/1", 100)
	want := []string{"system-view", "interface GE0/0/1", "port link-type access", "port default vlan 100", "quit", "quit"}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("access cmds = %v", cmds)
	}
}

// --- Arista EOS (Cisco-shaped output) ---

func TestAristaDriver(t *testing.T) {
	d, err := ForVendor(VendorArista)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := d.ParseMACTable(ciscoMACTable)
	if err != nil || len(entries) != 5 {
		t.Errorf("arista mac: %v n=%d", err, len(entries))
	}
	if _, err := d.ParsePortStatus(ciscoIfStatus); err != nil {
		t.Errorf("arista ports: %v", err)
	}
	if _, err := d.ParseVLANs(ciscoVLANBrief); err != nil {
		t.Errorf("arista vlans: %v", err)
	}
}

// --- Ruijie RGOS ---

const ruijieMAC = `Vlan  MAC Address       Type     Interface
1     001a.2b3c.4d5e    DYNAMIC  GigabitEthernet 0/1
100   aabb.ccdd.ee01    DYNAMIC  GigabitEthernet 0/2
`

func TestRuijieDriver(t *testing.T) {
	d, err := ForVendor(VendorRuijie)
	if err != nil {
		t.Fatal(err)
	}
	if d.CmdShowMACTable() != "show mac-address-table" {
		t.Errorf("cmd = %q", d.CmdShowMACTable())
	}
	entries, err := d.ParseMACTable(ruijieMAC)
	if err != nil || len(entries) != 2 {
		t.Fatalf("mac: %v %+v", err, entries)
	}
	if entries[0].MAC != "00:1a:2b:3c:4d:5e" || entries[0].Port != "GigabitEthernet 0/1" || entries[1].VLAN != 100 {
		t.Errorf("entries = %+v", entries)
	}
}

// --- Maipu ---

const maipuMAC = `Vlan  Mac Address         Type     Port
100   00:1a:2b:3c:4d:5e   dynamic  ethernet0/1
100   001a.2b3c.4d5f      dynamic  ethernet0/2
`

const maipuIfBrief = `Interface    Link  Speed   Duplex
ethernet0/1  up    1000M   full
ethernet0/2  down  auto    auto
`

func TestMaipuDriver(t *testing.T) {
	d, err := ForVendor(VendorMaipu)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := d.ParseMACTable(maipuMAC)
	if err != nil || len(entries) != 2 {
		t.Fatalf("mac: %v %+v", err, entries)
	}
	if entries[0].MAC != "00:1a:2b:3c:4d:5e" || entries[1].MAC != "00:1a:2b:3c:4d:5f" {
		t.Errorf("entries = %+v", entries)
	}
	ports, err := d.ParsePortStatus(maipuIfBrief)
	if err != nil || len(ports) != 2 || ports[0].Status != "up" {
		t.Errorf("ports: %v %+v", err, ports)
	}
}

// --- ZTE ZXR10 ---

const zteMAC = `Mac Address     Vlan  Port      Type
00d0.d012.3456  100   gei_0/1   dynamic
00d0.d012.3457  100   gei_0/2   static
`

const zteIfBrief = `Interface   AdminStatus  Link
gei_0/1     enabled      up
gei_0/2     enabled      down
`

const zteVLAN = `VLAN ID: 100
 Name: prod
 Ports: gei_0/1(U), gei_0/2(U)
VLAN ID: 200
 Name: mgmt
 Ports: gei_0/3(U)
`

func TestZTEDriver(t *testing.T) {
	d, err := ForVendor(VendorZTE)
	if err != nil {
		t.Fatal(err)
	}
	if d.CmdShowMACTable() != "show mac address-table" {
		t.Errorf("cmd = %q", d.CmdShowMACTable())
	}
	entries, err := d.ParseMACTable(zteMAC)
	if err != nil || len(entries) != 2 || entries[0].MAC != "00:d0:d0:12:34:56" || entries[0].Port != "gei_0/1" {
		t.Errorf("mac: %v %+v", err, entries)
	}
	ports, err := d.ParsePortStatus(zteIfBrief)
	if err != nil || len(ports) != 2 || ports[1].Status != "down" {
		t.Errorf("ports: %v %+v", err, ports)
	}
	vlans, err := d.ParseVLANs(zteVLAN)
	if err != nil || len(vlans) != 2 || vlans[0].Name != "prod" || len(vlans[0].Ports) != 2 {
		t.Errorf("vlans: %v %+v", err, vlans)
	}
}

// --- Juniper Junos ---

const juniperMAC = `Ethernet switching table :    2 entries,    2 learned
Routing instance : default-switch
    VLAN      MAC address       Type         Age  Interfaces
    default   00:11:22:33:44:55 Learn          0  ge-0/0/0.0
    v100      aa:bb:cc:dd:ee:ff Learn          0  ge-0/0/1.0
`

const juniperTerse = `Interface               Admin Link Proto    Local                Remote
ge-0/0/0                up    up
ge-0/0/1                up    down
ge-0/0/2                down  down
`

const juniperVLANs = `Routing instance        VLAN name         Tag          Interfaces
default-switch          v100              100          ge-0/0/0.0*
                                                         ge-0/0/1.0
default-switch          v200              200          ge-0/0/2.0*
`

func TestJuniperDriver(t *testing.T) {
	d, err := ForVendor(VendorJuniper)
	if err != nil {
		t.Fatal(err)
	}
	if d.CmdShowMACTable() != "show ethernet-switching table" {
		t.Errorf("cmd = %q", d.CmdShowMACTable())
	}
	if got := SessionInit(d); !reflect.DeepEqual(got, []string{"set cli screen-width 0", "set cli terminal vt100"}) {
		t.Errorf("init = %v", got)
	}
	entries, err := d.ParseMACTable(juniperMAC)
	if err != nil || len(entries) != 2 {
		t.Fatalf("mac: %v %+v", err, entries)
	}
	// Junos reports VLAN *names* in the switching table, not tags: both
	// rows stay VLAN=-1 (unknown ID) rather than guessing from the name.
	if entries[0].VLAN != -1 || entries[1].VLAN != -1 || entries[1].Port != "ge-0/0/1.0" || entries[0].MAC != "00:11:22:33:44:55" {
		t.Errorf("entries = %+v", entries)
	}
	ports, err := d.ParsePortStatus(juniperTerse)
	if err != nil || len(ports) != 3 || ports[0].Status != "up" || ports[1].Status != "down" {
		t.Errorf("ports: %v %+v", err, ports)
	}
	vlans, err := d.ParseVLANs(juniperVLANs)
	if err != nil || len(vlans) != 2 || vlans[0].ID != 100 || vlans[0].Name != "v100" {
		t.Errorf("vlans: %v %+v", err, vlans)
	}
	cmds := d.AccessVLANCommands("ge-0/0/0", 100)
	want := "set interfaces ge-0/0/0 unit 0 family ethernet-switching vlan members 100"
	if len(cmds) != 2 || cmds[1] != want {
		t.Errorf("access cmds = %v", cmds)
	}
}

// Raisecom must stay unsupported: the vendor binary contains no trace of
// it, and inventing a driver would fabricate factory alignment.
func TestRaisecomUnsupported(t *testing.T) {
	if _, err := ForVendor(Vendor("raisecom")); err == nil {
		t.Error("raisecom must not resolve to a driver")
	}
}
