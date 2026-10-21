package topology

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type mockRunner struct{ lldp string }

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "lldpctl" && m.lldp != "" {
		return m.lldp, "", nil
	}
	return "", "", errors.New("not found")
}

const fakeLLDP = `{"lldp":{"interface":{
 "eth0":{"chassis":{"chassis1":{"name":{"value":"SW-CORE-01"}}},
          "port":{"port1":{"descr":{"value":"GigabitEthernet1/0/1"}}}},
 "eth1":{"chassis":{"chassis1":{"name":{"value":"SW-CORE-02"}}},
          "port":{"port1":{"descr":{"value":"GigabitEthernet2/0/7"}}}}
}}}`

func TestCollectLLDP(t *testing.T) {
	m := &mockRunner{lldp: fakeLLDP}
	links, err := CollectLLDP(context.Background(), m, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("want 2 links, got %d", len(links))
	}
	if links[0].SwitchName == "" || links[0].SwitchPort == "" {
		t.Errorf("link fields empty: %+v", links[0])
	}
	b, _ := json.Marshal(links)
	if !strings.Contains(string(b), "GigabitEthernet1/0/1") {
		t.Error("expected port in JSON")
	}
}

func TestResolveByFDB(t *testing.T) {
	fdb := []FDBEntry{
		{SwitchName: "SW-A", Port: "Gi1/0/3", MAC: "AA:BB:CC:DD:EE:FF", VLAN: 100},
	}
	links := ResolveByFDB("host-9", []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}, fdb)
	if len(links) != 1 {
		t.Fatalf("want 1 resolved link, got %d", len(links))
	}
	if links[0].SwitchPort != "Gi1/0/3" || links[0].Source != "arp-fdb" {
		t.Errorf("unexpected link %+v", links[0])
	}
}

func TestCollectLLDPMissing(t *testing.T) {
	m := &mockRunner{}
	if _, err := CollectLLDP(context.Background(), m, "h"); err == nil {
		t.Fatal("want error when lldpd absent")
	}
}
