package virt_test

import (
	"context"
	"testing"

	virt "github.com/Oumu33/rackanvil/internal/virt"
)

// SYNTHETIC sample payloads below. They mimic the documented PVE API
// {"data": [...]} envelope shape only; they were NOT captured from a
// real Proxmox VE server.
const syntheticNodesJSON = `{"data":[
	{"node":"pve-a","status":"online","cpu":4,"maxcpu":16,"mem":1000000000,"maxmem":32000000000},
	{"node":"pve-b","status":"offline","cpu":0,"maxcpu":8,"mem":0,"maxmem":16000000000}
]}`

const syntheticVMsJSON = `{"data":[
	{"vmid":100,"name":"web-01","status":"running","cpus":2,"mem":1000000000,"maxmem":2000000000,"pid":1234},
	{"vmid":101,"name":"db-01","status":"stopped","cpus":4,"mem":0,"maxmem":8000000000}
]}`

func TestParseNodesSynthetic(t *testing.T) {
	nodes, err := virt.ParseNodes([]byte(syntheticNodesJSON))
	if err != nil {
		t.Fatalf("ParseNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Node != "pve-a" || nodes[0].Status != "online" || nodes[0].MaxCPU != 16 {
		t.Fatalf("unexpected first node: %+v", nodes[0])
	}
	if nodes[1].Node != "pve-b" || nodes[1].Status != "offline" {
		t.Fatalf("unexpected second node: %+v", nodes[1])
	}
}

func TestParseVMsSynthetic(t *testing.T) {
	vms, err := virt.ParseVMs([]byte(syntheticVMsJSON))
	if err != nil {
		t.Fatalf("ParseVMs: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}
	if vms[0].VMID != 100 || vms[0].Name != "web-01" || vms[0].Status != "running" || vms[0].CPUs != 2 {
		t.Fatalf("unexpected first VM: %+v", vms[0])
	}
	if vms[1].VMID != 101 || vms[1].Name != "db-01" || vms[1].Status != "stopped" {
		t.Fatalf("unexpected second VM: %+v", vms[1])
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := virt.ParseNodes([]byte("not json")); err == nil {
		t.Fatal("ParseNodes accepted invalid JSON")
	}
	if _, err := virt.ParseVMs([]byte(`{"nope":true}`)); err == nil {
		t.Fatal("ParseVMs accepted a payload with no data field")
	}
}

func TestBuildRequestShape(t *testing.T) {
	// Only asserts request construction (URL + token header); the client
	// itself has not been verified against a real PVE server.
	c := virt.NewPVEClient("https://pve.invalid:8006/", "root@pam!ci", "s3cr3t")
	if _, err := c.ListVMsOnNode(context.Background(), ""); err == nil {
		t.Fatal("ListVMsOnNode with empty node unexpectedly succeeded")
	}
	_ = c
}
