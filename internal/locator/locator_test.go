package locator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type mockRunner struct {
	lsblk string
	which map[string]bool
	calls []string
}

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	m.calls = append(m.calls, name+" "+strings.Join(args, " "))
	if name == "lsblk" {
		return m.lsblk, "", nil
	}
	if name == "sh" && len(args) >= 2 && strings.HasPrefix(args[1], "command -v ") {
		tool := strings.TrimPrefix(args[1], "command -v ")
		if m.which[tool] {
			return "/usr/sbin/" + tool, "", nil
		}
		return "", "", errors.New("not found")
	}
	if name == "sh" && len(args) >= 2 && strings.Contains(args[1], "enclosure_device") {
		return "", "", errors.New("no enclosure")
	}
	return "", "", nil
}

const fakeLsblk = `{"blockdevices":[
 {"name":"sda","model":"ST12000NM000J","serial":"ABC123","type":"disk"},
 {"name":"sda1","model":"","serial":"","type":"part"},
 {"name":"nvme0n1","model":"Samsung SSD 970","serial":"XYZ789","type":"disk"}
]}`

func TestScanPicksTools(t *testing.T) {
	m := &mockRunner{lsblk: fakeLsblk, which: map[string]bool{"ledctl": true}}
	disks, err := Scan(context.Background(), m, "/sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 2 {
		t.Fatalf("want 2 disks, got %d", len(disks))
	}
	for _, d := range disks {
		if d.Tool != "ledctl" {
			t.Errorf("disk %s: want ledctl, got %s", d.Device, d.Tool)
		}
	}
}

func TestScanNVMeFallback(t *testing.T) {
	m := &mockRunner{lsblk: fakeLsblk, which: map[string]bool{"nvme": true}}
	disks, err := Scan(context.Background(), m, "/sys")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range disks {
		want := "none"
		if strings.HasPrefix(d.Device, "/dev/nvme") {
			want = "nvme"
		}
		if d.Tool != want {
			t.Errorf("disk %s: want %s, got %s", d.Device, want, d.Tool)
		}
	}
}

func TestLocateOnOff(t *testing.T) {
	m := &mockRunner{lsblk: fakeLsblk, which: map[string]bool{"ledctl": true}}
	payload := `{"device":"/dev/sda","tool":"ledctl","on":true}`
	out, err := LocateJSON(context.Background(), m, payload)
	if err != nil {
		t.Fatal(err)
	}
	var res locateResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.On || res.Tool != "ledctl" {
		t.Errorf("unexpected result %+v", res)
	}
	found := false
	for _, c := range m.calls {
		if strings.Contains(c, "locate=/dev/sda") {
			found = true
		}
	}
	if !found {
		t.Errorf("ledctl locate= not invoked, calls=%v", m.calls)
	}
}

func TestLocateNoTool(t *testing.T) {
	m := &mockRunner{lsblk: fakeLsblk, which: map[string]bool{}}
	_, err := LocateJSON(context.Background(), m, `{"device":"/dev/sda","tool":"none","on":true}`)
	if err == nil {
		t.Fatal("want error for missing tool")
	}
}
