package switchdrv

// Validation: local mock SSH server (golang.org/x/crypto/ssh server side)
// serving canned outputs in the vendors' documented formats. Formats per
// Cisco IOS-XE and H3C Comware V7 public documentation; values are synthetic
// demo data. No real switch exists in this environment; live-device behaviour
// is NOT verified.

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

const ciscoMACTable = `          Mac Address Table
-------------------------------------------

Vlan    Mac Address       Type        Ports
----    -----------       --------    -----
 All    0100.0ccc.cccc    STATIC      CPU
   1    0011.2233.4401    DYNAMIC     Gi1/0/1
   1    0011.2233.4402    DYNAMIC     Gi1/0/2
  10    aabb.ccdd.ee01    DYNAMIC     Gi1/0/3
  10    aabb.ccdd.ee02    DYNAMIC     Gi1/0/3
Total Mac Addresses for this criterion: 5
`

const ciscoIfStatus = `Port      Name               Status       Vlan       Duplex  Speed Type
Gi1/0/1                      connected    1          a-full a-1000 10/100/1000BaseTX
Gi1/0/2                      notconnect   1            auto   auto 10/100/1000BaseTX
Gi1/0/3   uplink-to-core     connected    trunk      a-full a-1000 10/100/1000BaseTX
`

const ciscoVLANBrief = `VLAN Name                             Status    Ports
---- -------------------------------- --------- -------------------------------
1    default                          active    Gi1/0/1, Gi1/0/2
10   MGMT                             active    Gi1/0/3
`

const h3cMACTable = `MAC Address      VLAN ID    State            Port/NIC name       Aging
0001-0203-0405   1          Learned          GE1/0/1             Y
aabb-ccdd-ee01   10         Learned          GE1/0/2             Y
aabb-ccdd-ee02   10         Learned          GE1/0/2             Y
 ---  3 mac address(es) found ---
`

const h3cIfBrief = `Brief information on interfaces in route mode:
Link: ADM - administratively down; Stby - standby
Protocol: (s) - spoofing
Interface            Link Speed     Duplex  Link-type  PVID Description
GE1/0/1              UP   1G        (a)     access     1
GE1/0/2              DOWN auto      auto    trunk      1
`

const h3cVLANAll = ` VLAN ID: 1
 VLAN type: Static
 Route interface: not configured
 Description: "VLAN 0001"
 Name: VLAN 0001
 Tagged ports: none
 Untagged ports:
    GigabitEthernet1/0/1        GigabitEthernet1/0/2

 VLAN ID: 10
 VLAN type: Static
 Route interface: not configured
 Description: "VLAN 0010"
 Name: VLAN 0010
 Tagged ports:
    GigabitEthernet1/0/2
 Untagged ports: none
`

// startMockSSH serves cannedOutputs[command] over SSH exec requests.
func startMockSSH(t *testing.T, canned map[string]string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go handleMockConn(nc, cfg, canned)
		}
	}()
	return ln.Addr().String()
}

func handleMockConn(nc net.Conn, cfg *ssh.ServerConfig, canned map[string]string) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				ssh.Unmarshal(req.Payload, &payload)
				out := canned[payload.Command]
				io.WriteString(channel, out)
				channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
				req.Reply(true, nil)
				return
			}
		}()
	}
}

func TestCiscoEndToEnd(t *testing.T) {
	addr := startMockSSH(t, map[string]string{
		"show mac address-table": ciscoMACTable,
		"show interfaces status": ciscoIfStatus,
		"show vlan brief":        ciscoVLANBrief,
	})
	c, err := Dial(VendorCisco, addr, "admin", "x", ssh.InsecureIgnoreHostKey(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	macs, err := c.MACTable()
	if err != nil || len(macs) != 5 {
		t.Fatalf("macs: %v %d", err, len(macs))
	}
	if macs[1].MAC != "00:11:22:33:44:01" || macs[1].VLAN != 1 || macs[1].Port != "Gi1/0/1" {
		t.Fatalf("mac[1]: %+v", macs[1])
	}
	if macs[0].VLAN != -1 { // "All" row
		t.Fatalf("mac[0] vlan: %d", macs[0].VLAN)
	}

	ports, err := c.PortStatus()
	if err != nil || len(ports) != 3 {
		t.Fatalf("ports: %v %d", err, len(ports))
	}
	if ports[0].Status != "connected" || ports[0].VLAN != "1" {
		t.Fatalf("port[0]: %+v", ports[0])
	}
	if ports[2].VLAN != "trunk" {
		t.Fatalf("port[2]: %+v", ports[2])
	}

	vlans, err := c.VLANs()
	if err != nil || len(vlans) != 2 {
		t.Fatalf("vlans: %v %d", err, len(vlans))
	}
	if vlans[1].ID != 10 || vlans[1].Name != "MGMT" || len(vlans[1].Ports) != 1 {
		t.Fatalf("vlan[1]: %+v", vlans[1])
	}
}

func TestH3CEndToEnd(t *testing.T) {
	addr := startMockSSH(t, map[string]string{
		"display mac-address":     h3cMACTable,
		"display interface brief": h3cIfBrief,
		"display vlan all":        h3cVLANAll,
	})
	c, err := Dial(VendorH3C, addr, "admin", "x", ssh.InsecureIgnoreHostKey(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	macs, err := c.MACTable()
	if err != nil || len(macs) != 3 {
		t.Fatalf("macs: %v %d", err, len(macs))
	}
	if macs[0].MAC != "00:01:02:03:04:05" || macs[0].VLAN != 1 || macs[0].Port != "GE1/0/1" {
		t.Fatalf("mac[0]: %+v", macs[0])
	}

	ports, err := c.PortStatus()
	if err != nil || len(ports) != 2 {
		t.Fatalf("ports: %v %d", err, len(ports))
	}
	if ports[0].Name != "GE1/0/1" || ports[0].Status != "UP" || ports[0].VLAN != "1" {
		t.Fatalf("port[0]: %+v", ports[0])
	}

	vlans, err := c.VLANs()
	if err != nil || len(vlans) != 2 {
		t.Fatalf("vlans: %v %d", err, len(vlans))
	}
	if vlans[0].ID != 1 || len(vlans[0].Ports) != 2 {
		t.Fatalf("vlan[0]: %+v", vlans[0])
	}
	if len(vlans[1].Ports) != 1 || vlans[1].Ports[0] != "GigabitEthernet1/0/2" {
		t.Fatalf("vlan[1]: %+v", vlans[1])
	}
}

func TestAccessVLANTemplates(t *testing.T) {
	cisco, _ := ForVendor(VendorCisco)
	cmds := cisco.AccessVLANCommands("Gi1/0/5", 20)
	if cmds[1] != "interface Gi1/0/5" || cmds[3] != "switchport access vlan 20" {
		t.Fatalf("cisco template: %v", cmds)
	}
	h3c, _ := ForVendor(VendorH3C)
	hcmds := h3c.AccessVLANCommands("GE1/0/5", 20)
	if hcmds[1] != "interface GE1/0/5" || hcmds[3] != "port access vlan 20" {
		t.Fatalf("h3c template: %v", hcmds)
	}
	if _, err := ForVendor("juniper"); err == nil {
		t.Fatal("unsupported vendor accepted")
	}
}
