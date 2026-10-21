package raid

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type mockRunner struct{ calls []string }

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	m.calls = append(m.calls, name+" "+strings.Join(args, " "))
	if name == "sh" {
		return "/usr/sbin/storcli", "", nil // command -v storcli
	}
	if name == "storcli" && len(args) >= 3 && args[0] == "/cALL" && args[1] == "show" && args[2] == "all" {
		return `{"Controllers":[{"Command Status":{"Status":"Success"},"Response Data":{"Basics":{"Controller":0,"Model":"MegaRAID SAS 9361-8i"},"VD LIST":[{"DG/VD":"0/0","TYPE":"RAID1","State":"Optl","Size":"1.089 TB"}],"PD LIST":[{"EID:Slt":"252:0","DID":5,"State":"Onln","Model":"TOSHIBA HDWE140 "}]}}]}`, "", nil
	}
	return "", "", errors.New("not found")
}

func TestControllers(t *testing.T) {
	m := &mockRunner{}
	ctrls, err := Controllers(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctrls) != 1 || ctrls[0].Model != "MegaRAID SAS 9361-8i" {
		t.Fatalf("unexpected controllers %+v", ctrls)
	}
}

func TestListVDs(t *testing.T) {
	m := &mockRunner{}
	vds, err := ListVDs(context.Background(), m, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(vds) != 1 || vds[0].RAID != "RAID1" || vds[0].State != "Optl" {
		t.Fatalf("unexpected vds %+v", vds)
	}
}

func TestCreateVDLevels(t *testing.T) {
	for _, lvl := range []string{"0", "1", "5", "10"} {
		cmd, err := CreateVD("0", lvl, []string{"252:0", "252:1"})
		if err != nil {
			t.Errorf("level %s: %v", lvl, err)
			continue
		}
		joined := strings.Join(cmd, " ")
		// M7: storcli grammar from the vendor-bundled storcli help:
		// "add vd r<level> drives=..." (not "type=raid<level>").
		if !strings.Contains(joined, "add vd r"+lvl+" ") {
			t.Errorf("level %s: bad command %v", lvl, cmd)
		}
	}
	if _, err := CreateVD("0", "6", []string{"252:0"}); err == nil {
		t.Error("RAID6 must be rejected (unsupported per vendor doc)")
	}
	if _, err := CreateVD("0", "1", nil); err == nil {
		t.Error("empty drive list must be rejected")
	}
}

func TestQueryJSON(t *testing.T) {
	m := &mockRunner{}
	out, err := QueryJSON(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	var v []map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 {
		t.Fatalf("want 1 controller in JSON, got %d", len(v))
	}
}
