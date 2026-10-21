package store

import "time"

// CfgSnapshot is one archived host configuration ("配置存档").
type CfgSnapshot struct {
	ID        string
	HostID    string
	CreatedAt time.Time
	Note      string
	RAID      string
	Partition string
	Network   string
	Packages  string
}

// TopoLink is one resolved uplink: host NIC -> switch port.
type TopoLink struct {
	HostID     string
	HostMAC    string
	Iface      string
	SwitchName string
	SwitchPort string
	Source     string // "lldp" | "arp-fdb" | "manual"
	SeenAt     time.Time
}

// RelayNode is a registered cross-site relay node.
type RelayNode struct {
	Name     string
	Subnets  string // JSON array
	LastSeen time.Time
}
