package networkconfig

import (
	"context"
	"strings"
	"testing"
)

func TestRenderEthernetFull(t *testing.T) {
	got, err := Render(Config{GroupType: "ethernets", Name: "eth0",
		CIDR: "10.0.0.5/24", Gateway: "10.0.0.1", DNS: []string{"223.5.5.5", "8.8.8.8"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `network:
  version: 2
  ethernets:
    eth0:
      dhcp4: false
      dhcp6: false
      addresses:
        - 10.0.0.5/24
      routes:
        - to: default
          via: 10.0.0.1
      nameservers:
        addresses: [223.5.5.5, 8.8.8.8]
`
	if got != want {
		t.Fatalf("render mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderBondMinimal(t *testing.T) {
	got, err := Render(Config{GroupType: "bonds", Name: "bond0", CIDR: "10.0.0.6/24"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "routes:") || strings.Contains(got, "nameservers:") {
		t.Fatalf("optional blocks must be omitted:\n%s", got)
	}
	if !strings.Contains(got, "bonds:\n    bond0:") {
		t.Fatalf("bond group missing:\n%s", got)
	}
}

func TestRenderValidation(t *testing.T) {
	if _, err := Render(Config{GroupType: "vlans", Name: "x", CIDR: "10.0.0.1/24"}); err == nil {
		t.Fatal("bad group type must fail")
	}
	if _, err := Render(Config{GroupType: "ethernets", CIDR: "10.0.0.1/24"}); err == nil {
		t.Fatal("missing name must fail")
	}
}

type fakeRunner struct {
	wrotePath, wroteBody string
	ranApply             bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	if name == "netplan" && len(args) == 1 && args[0] == "apply" {
		f.ranApply = true
	}
	return "", "", nil
}

func (f *fakeRunner) WriteFile(_ context.Context, path, content string) error {
	f.wrotePath, f.wroteBody = path, content
	return nil
}

func TestApplyWritesVendorPath(t *testing.T) {
	fr := &fakeRunner{}
	body, err := Apply(context.Background(), fr,
		Config{GroupType: "ethernets", Name: "eno1", CIDR: "192.168.1.10/24"})
	if err != nil {
		t.Fatal(err)
	}
	if fr.wrotePath != "/etc/netplan/99-rackanvil-eno1.yaml" {
		t.Fatalf("path: %s", fr.wrotePath)
	}
	if fr.wroteBody != body || !fr.ranApply {
		t.Fatalf("body/apply mismatch: ranApply=%v", fr.ranApply)
	}
}
