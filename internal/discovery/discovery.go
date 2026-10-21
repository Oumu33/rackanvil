// Package discovery implements subnet discovery and BOM comparison.
//
// Method (honest): no raw ICMP sockets (needs root/CAP_NET_RAW), so "ping
// sweep" is a TCP connect sweep over a small port set plus MAC resolution
// from the kernel ARP table (/proc/net/arp). On a real deployment network
// this finds live hosts; in restricted sandboxes only loopback is visible,
// which the scanner reports truthfully.
package discovery

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// FoundHost is one live address seen during a scan.
type FoundHost struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Ports    []int  `json:"open_ports,omitempty"`
}

// DefaultPorts are probed per address: ssh/http/https/our agent console.
var DefaultPorts = []int{22, 80, 443, 9100}

// arpTable reads /proc/net/arp into ip -> mac.
func arpTable() map[string]string {
	out := map[string]string{}
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		// IP address | HW type | Flags | HW address | Mask | Device
		if len(fields) < 4 || fields[2] == "0x0" {
			continue
		}
		ip, mac := fields[0], fields[3]
		if mac != "00:00:00:00:00:00" {
			out[ip] = mac
		}
	}
	return out
}

// expandCIDR lists usable host addresses, capped for safety.
func expandCIDR(cidr string, maxAddrs int) ([]string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("discovery: bad cidr: %w", err)
	}
	ones, _ := ipnet.Mask.Size()
	var addrs []string
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incIP(ip) {
		dup := make(net.IP, len(ip))
		copy(dup, ip)
		addrs = append(addrs, dup.String())
		if len(addrs) > maxAddrs {
			return nil, fmt.Errorf("discovery: cidr too large (cap %d addresses)", maxAddrs)
		}
	}
	if ones < 31 && len(addrs) >= 2 {
		// Drop network (first) and broadcast (last) for normal subnets.
		// /31 and /32 have no broadcast address (RFC 3021).
		addrs = addrs[1 : len(addrs)-1]
	}
	return addrs, nil
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] != 0 {
			break
		}
	}
}

func probeHost(ip string, ports []int, timeout time.Duration, arp map[string]string) *FoundHost {
	found := &FoundHost{IP: ip}
	if mac, ok := arp[ip]; ok {
		found.MAC = mac
	}
	for _, p := range ports {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, fmt.Sprint(p)), timeout)
		if err == nil {
			c.Close()
			found.Ports = append(found.Ports, p)
		}
	}
	if len(found.Ports) == 0 && found.MAC == "" {
		return nil // nothing alive here
	}
	if names, err := net.LookupAddr(ip); err == nil && len(names) > 0 {
		found.Hostname = strings.TrimSuffix(names[0], ".")
	}
	return found
}

// ScanCIDR sweeps a CIDR block. Returns live hosts only.
func ScanCIDR(cidr string, ports []int, perHostTimeout time.Duration, parallelism int) ([]FoundHost, error) {
	if len(ports) == 0 {
		ports = DefaultPorts
	}
	if parallelism <= 0 {
		parallelism = 64
	}
	addrs, err := expandCIDR(cidr, 1024)
	if err != nil {
		return nil, err
	}
	arp := arpTable()
	var mu sync.Mutex
	var out []FoundHost
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if h := probeHost(ip, ports, perHostTimeout, arp); h != nil {
				mu.Lock()
				out = append(out, *h)
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	return out, nil
}

// ---- BOM diff ----

// HostRef is the inventory side of a comparison.
type HostRef struct {
	ID       string
	Hostname string
	MACs     []string
	IPs      []string
}

// DiffEntry is one BOM difference.
type DiffEntry struct {
	Kind     string `json:"kind"` // new | missing | changed
	IP       string `json:"ip,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Detail   string `json:"detail"`
}

// DiffBOM compares freshly scanned hosts against the enrolled inventory:
//   - new:     scanned IP/MAC not present in inventory
//   - missing: enrolled host whose IPs were all silent during the scan
//   - changed: enrolled host seen under a different hostname than recorded
func DiffBOM(found []FoundHost, known []HostRef) []DiffEntry {
	knownByIP := map[string]*HostRef{}
	knownByMAC := map[string]*HostRef{}
	for i := range known {
		for _, ip := range known[i].IPs {
			knownByIP[ip] = &known[i]
		}
		for _, m := range known[i].MACs {
			if m != "" {
				knownByMAC[strings.ToLower(m)] = &known[i]
			}
		}
	}
	seen := map[*HostRef]bool{}
	var diff []DiffEntry
	for _, f := range found {
		var ref *HostRef
		if r, ok := knownByIP[f.IP]; ok {
			ref = r
		} else if f.MAC != "" {
			if r, ok := knownByMAC[strings.ToLower(f.MAC)]; ok {
				ref = r
			}
		}
		if ref == nil {
			diff = append(diff, DiffEntry{Kind: "new", IP: f.IP, MAC: f.MAC, Hostname: f.Hostname,
				Detail: fmt.Sprintf("discovered %s (%s), not in inventory", f.IP, f.MAC)})
			continue
		}
		seen[ref] = true
		if f.Hostname != "" && ref.Hostname != "" && f.Hostname != ref.Hostname {
			diff = append(diff, DiffEntry{Kind: "changed", IP: f.IP, MAC: f.MAC, Hostname: f.Hostname,
				Detail: fmt.Sprintf("hostname changed: inventory=%q scanned=%q", ref.Hostname, f.Hostname)})
		}
	}
	for i := range known {
		if !seen[&known[i]] {
			diff = append(diff, DiffEntry{Kind: "missing", Hostname: known[i].Hostname,
				Detail: fmt.Sprintf("enrolled host %q (%s) not seen during scan", known[i].Hostname, strings.Join(known[i].IPs, ","))})
		}
	}
	return diff
}
