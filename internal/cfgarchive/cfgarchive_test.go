package cfgarchive

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
	switch name {
	case "sfdisk":
		return "label: gpt\n/dev/sda1 : start=2048, size=1048576, type=EF\n", "", nil
	case "ip":
		return `[{"ifname":"eth0","addr_info":[{"local":"10.0.0.5"}]}]`, "", nil
	case "sh":
		return "ii  bash  5.1\n", "", nil
	default:
		return "", "", errors.New("not found: " + name)
	}
}

func TestCollectSkipsMissingTools(t *testing.T) {
	m := &mockRunner{}
	s, err := Collect(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.Partition, "label: gpt") {
		t.Errorf("partition dump missing: %q", s.Partition)
	}
	if !strings.Contains(s.Note, "storcli: unavailable") {
		t.Errorf("missing-tool note not recorded: %q", s.Note)
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) == 0 {
		t.Fatal("snapshot not JSON-serializable")
	}
}

func TestRestoreRequiresConfirm(t *testing.T) {
	if _, _, err := RestorePlan(Snapshot{}, false); err == nil {
		t.Fatal("restore without confirm must fail")
	}
}

func TestRestorePlanEmitsSteps(t *testing.T) {
	s := Snapshot{Partition: "label: gpt\n", RAID: "VD LIST", Network: "{}"}
	steps, manual, err := RestorePlan(s, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Error("want sfdisk restore steps")
	}
	if len(manual) == 0 {
		t.Error("want RAID manual checklist")
	}
}
