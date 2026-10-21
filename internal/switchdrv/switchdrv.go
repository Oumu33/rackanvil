// Package switchdrv is the switch SSH driver framework.
//
// Scope (honest): read paths (MAC address table / FDB, port status, VLAN
// list) plus VLAN config *command templates* for Cisco IOS, H3C Comware
// (M5), and — M7/M8, aligned to the vendor server binary's own
// switchdriver package — Huawei VRP, Arista EOS, Maipu, ZTE ZXR10,
// Juniper Junos, Inspur and TP-Link, plus the Generic fallback driver
// (see vendors_m7.go / vendors_m8.go for per-vendor provenance). M8
// decompilation of the vendor's GetDriver factory proves the vendor
// registers exactly those drivers: there is NO Ruijie and NO Raisecom
// driver in the vendor product (both fall through to Generic there).
// Our Ruijie RGOS driver is an extension beyond the vendor set, kept
// and labeled as such. There is no real switch in this environment;
// validation is against a local mock SSH server serving canned outputs
// (see switchdrv_test.go, outputs marked with their provenance).
// Live-device behaviour is NOT verified.
package switchdrv

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Vendor identifies a switch NOS family.
type Vendor string

const (
	VendorCisco   Vendor = "cisco-ios"
	VendorH3C     Vendor = "h3c-comware"
	VendorHuawei  Vendor = "huawei-vrp"
	VendorArista  Vendor = "arista-eos"
	VendorRuijie  Vendor = "ruijie-rgos"
	VendorMaipu   Vendor = "maipu"
	VendorZTE     Vendor = "zte-zxr10"
	VendorJuniper Vendor = "juniper-junos"
	VendorInspur  Vendor = "inspur"
	VendorTPLink  Vendor = "tplink"
	VendorGeneric Vendor = "generic"
)

// FDBEntry is one MAC address table row.
type FDBEntry struct {
	MAC  string // normalized lowercase colon form aa:bb:cc:dd:ee:ff
	VLAN int    // -1 when the platform reports "All"
	Port string
	Type string // DYNAMIC | STATIC | Learned | ...
}

// PortStatus is one interface status row.
type PortStatus struct {
	Name   string
	Status string // connected | notconnect | UP | DOWN | ...
	VLAN   string // "1", "trunk", "routed", or PVID for H3C
	Duplex string
	Speed  string
}

// VLAN is one VLAN and its member ports.
type VLAN struct {
	ID    int
	Name  string
	Ports []string
}

// Driver is the per-vendor command set + parsers.
type Driver interface {
	Vendor() Vendor
	CmdShowMACTable() string
	CmdShowPortStatus() string
	CmdShowVLANs() string
	ParseMACTable(out string) ([]FDBEntry, error)
	ParsePortStatus(out string) ([]PortStatus, error)
	ParseVLANs(out string) ([]VLAN, error)
	// AccessVLANCommands renders (does not execute) the config template that
	// puts a port into an access VLAN.
	AccessVLANCommands(port string, vlan int) []string
}

// ForVendor returns the driver for a vendor.
func ForVendor(v Vendor) (Driver, error) {
	switch v {
	case VendorCisco:
		return ciscoDriver{}, nil
	case VendorH3C:
		return h3cDriver{}, nil
	case VendorHuawei:
		return huaweiDriver{}, nil
	case VendorArista:
		return aristaDriver{}, nil
	case VendorRuijie:
		return ruijieDriver{}, nil
	case VendorMaipu:
		return maipuDriver{}, nil
	case VendorZTE:
		return zteDriver{}, nil
	case VendorJuniper:
		return juniperDriver{}, nil
	case VendorInspur:
		return inspurDriver{}, nil
	case VendorTPLink:
		return tplinkDriver{}, nil
	case VendorGeneric:
		return genericDriver{}, nil
	}
	return nil, fmt.Errorf("switchdrv: unsupported vendor %q", v)
}

// Client is an SSH session to one switch.
type Client struct {
	d    Driver
	conn *ssh.Client
}

// Dial opens an SSH connection. hostKeyCallback is intentionally the
// caller's responsibility in production; tests pass ssh.InsecureIgnoreHostKey.
func Dial(v Vendor, addr, user, password string, hostKeyCallback ssh.HostKeyCallback, timeout time.Duration) (*Client, error) {
	d, err := ForVendor(v)
	if err != nil {
		return nil, err
	}
	if hostKeyCallback == nil {
		hostKeyCallback = ssh.InsecureIgnoreHostKey()
	}
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("switchdrv: dial %s: %w", addr, err)
	}
	return &Client{d: d, conn: conn}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Run executes one command and returns its combined output.
func (c *Client) Run(cmd string) (string, error) {
	sess, err := c.conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	b, err := sess.CombinedOutput(cmd)
	if err != nil {
		return string(b), fmt.Errorf("switchdrv: run %q: %w", cmd, err)
	}
	return string(b), nil
}

// MACTable queries and parses the FDB.
func (c *Client) MACTable() ([]FDBEntry, error) {
	out, err := c.Run(c.d.CmdShowMACTable())
	if err != nil {
		return nil, err
	}
	return c.d.ParseMACTable(out)
}

// PortStatus queries and parses interface status.
func (c *Client) PortStatus() ([]PortStatus, error) {
	out, err := c.Run(c.d.CmdShowPortStatus())
	if err != nil {
		return nil, err
	}
	return c.d.ParsePortStatus(out)
}

// VLANs queries and parses the VLAN list.
func (c *Client) VLANs() ([]VLAN, error) {
	out, err := c.Run(c.d.CmdShowVLANs())
	if err != nil {
		return nil, err
	}
	return c.d.ParseVLANs(out)
}

// normalizeMAC converts cisco dotted (aabb.ccdd.eeff), H3C dashed
// (aabb-ccdd-eeff), or colon forms to lowercase colon form.
func normalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(".", "", "-", "").Replace(s)
	if len(s) != 12 {
		return s
	}
	var parts []string
	for i := 0; i < 12; i += 2 {
		parts = append(parts, s[i:i+2])
	}
	return strings.Join(parts, ":")
}
