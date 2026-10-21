package db

import (
	"time"

	"github.com/Oumu33/rackanvil/internal/store"
)

// ---- M4: cfg snapshots ----

func (d *DB) SaveCfgSnapshot(s store.CfgSnapshot) (*store.CfgSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.ID == "" {
		s.ID = newID("cfgsnap")
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO cfg_snapshots(id, host_id, created_at, note, raid, partition, network, packages)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		s.ID, s.HostID, s.CreatedAt.Unix(), s.Note, s.RAID, s.Partition, s.Network, s.Packages)
	if err != nil {
		return nil, err
	}
	out := s
	return &out, nil
}

func (d *DB) ListCfgSnapshots(hostID string) ([]*store.CfgSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(d.rebind(
		`SELECT id, host_id, created_at, note, raid, partition, network, packages
		 FROM cfg_snapshots WHERE host_id = ? ORDER BY created_at DESC`), hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.CfgSnapshot
	for rows.Next() {
		var s store.CfgSnapshot
		var ts int64
		if err := rows.Scan(&s.ID, &s.HostID, &ts, &s.Note, &s.RAID, &s.Partition, &s.Network, &s.Packages); err != nil {
			return nil, err
		}
		s.CreatedAt = time.Unix(ts, 0)
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (d *DB) GetCfgSnapshot(id string) (*store.CfgSnapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var s store.CfgSnapshot
	var ts int64
	err := d.sql.QueryRow(d.rebind(
		`SELECT id, host_id, created_at, note, raid, partition, network, packages
		 FROM cfg_snapshots WHERE id = ?`), id).
		Scan(&s.ID, &s.HostID, &ts, &s.Note, &s.RAID, &s.Partition, &s.Network, &s.Packages)
	if err != nil {
		return nil, err
	}
	s.CreatedAt = time.Unix(ts, 0)
	return &s, nil
}

// ---- M4: topology links ----

func (d *DB) SaveTopoLinks(links []store.TopoLink) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	for _, l := range links {
		if _, err := d.sql.Exec(d.rebind(
			`INSERT INTO topo_links(host_id, host_mac, iface, switch_name, switch_port, source, seen_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(host_id, iface, switch_name, switch_port)
			 DO UPDATE SET host_mac = excluded.host_mac, source = excluded.source, seen_at = excluded.seen_at`),
			l.HostID, l.HostMAC, l.Iface, l.SwitchName, l.SwitchPort, l.Source, now); err != nil {
			return err
		}
	}
	return nil
}

// ListTopoLinks returns all known uplink mappings.
func (d *DB) ListTopoLinks() ([]*store.TopoLink, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(
		`SELECT host_id, host_mac, iface, switch_name, switch_port, source, seen_at
		 FROM topo_links ORDER BY switch_name, switch_port`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.TopoLink
	for rows.Next() {
		var l store.TopoLink
		var ts int64
		if err := rows.Scan(&l.HostID, &l.HostMAC, &l.Iface, &l.SwitchName, &l.SwitchPort, &l.Source, &ts); err != nil {
			return nil, err
		}
		l.SeenAt = time.Unix(ts, 0)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// ---- M4: relay nodes ----

func (d *DB) RegisterRelayNode(name, subnets string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO relay_nodes(name, subnets, last_seen) VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET subnets = excluded.subnets, last_seen = excluded.last_seen`),
		name, subnets, time.Now().Unix())
	return err
}

func (d *DB) ListRelayNodes() ([]*store.RelayNode, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(
		`SELECT name, subnets, last_seen FROM relay_nodes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.RelayNode
	for rows.Next() {
		var n store.RelayNode
		var ts int64
		if err := rows.Scan(&n.Name, &n.Subnets, &ts); err != nil {
			return nil, err
		}
		n.LastSeen = time.Unix(ts, 0)
		out = append(out, &n)
	}
	return out, rows.Err()
}
