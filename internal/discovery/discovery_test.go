package discovery

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestScanFindsLocalListener(t *testing.T) {
	// 127.0.0.2 is loopback on Linux (whole 127/8), so this is a real scan
	// against a real listener without leaving the sandbox.
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.2: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	found, err := ScanCIDR("127.0.0.2/32", []int{port}, 300*time.Millisecond, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].IP != "127.0.0.2" {
		t.Fatalf("want [127.0.0.2], got %+v", found)
	}
	if len(found[0].Ports) != 1 || found[0].Ports[0] != port {
		t.Fatalf("want open port %d, got %v", port, found[0].Ports)
	}
}

func TestScanRejectsHugeCIDR(t *testing.T) {
	if _, err := ScanCIDR("10.0.0.0/8", nil, time.Millisecond, 4); err == nil {
		t.Fatal("want error for /8")
	}
}

func TestDiffBOM(t *testing.T) {
	known := []HostRef{
		{ID: "h1", Hostname: "web1", MACs: []string{"aa:bb:cc:dd:ee:01"}, IPs: []string{"10.0.0.1"}},
		{ID: "h2", Hostname: "db1", MACs: []string{"aa:bb:cc:dd:ee:02"}, IPs: []string{"10.0.0.2"}},
	}
	found := []FoundHost{
		{IP: "10.0.0.1", MAC: "aa:bb:cc:dd:ee:01"},                     // known, silent hostname -> no diff
		{IP: "10.0.0.9", MAC: "aa:bb:cc:dd:ee:09", Hostname: "newbox"}, // new
	}
	diff := DiffBOM(found, known)
	kinds := map[string]int{}
	for _, d := range diff {
		kinds[d.Kind]++
		t.Logf("%s: %s", d.Kind, d.Detail)
	}
	if kinds["new"] != 1 || kinds["missing"] != 1 {
		t.Fatalf("want 1 new + 1 missing, got %v", kinds)
	}
	var newDetail string
	for _, d := range diff {
		if d.Kind == "new" {
			newDetail = d.Detail
		}
	}
	if newDetail == "" || len(fmt.Sprint(diff)) == 0 {
		t.Fatal("empty diff detail")
	}
}
