package orchestrator

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oumu33/rackanvil/internal/db"
	"github.com/Oumu33/rackanvil/internal/store"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open("sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestJobLifecycleMockSteps(t *testing.T) {
	d := openTestDB(t)
	h := d.RegisterHost("testhost", []string{"aa:bb:cc:dd:ee:ff"}, []string{"10.0.0.9"}, "x", 1024)

	r := New(d, nil)
	r.poll = 50 * time.Millisecond
	job, err := r.CreateJob(h.ID, "test plan", []store.StepDef{
		{Name: "BMC", Kind: StepBMCMock},
		{Name: "RAID", Kind: StepRAIDMock},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	go r.Run(ctx)

	deadline := time.Now().Add(12 * time.Second)
	for {
		j, steps, err := d.GetJob(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == "done" {
			if len(steps) != 2 {
				t.Fatalf("want 2 steps, got %d", len(steps))
			}
			for _, s := range steps {
				if s.Status != "done" {
					t.Fatalf("step %q status=%s", s.Name, s.Status)
				}
				t.Logf("step %q detail: %s", s.Name, s.Detail)
			}
			return
		}
		if j.Status == "failed" {
			t.Fatalf("job failed: %+v", steps)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for job, status=%s", j.Status)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestCreateJobRejectsBadHostAndKind(t *testing.T) {
	d := openTestDB(t)
	r := New(d, nil)
	if _, err := r.CreateJob("nope", "x", nil); err == nil {
		t.Fatal("want error for unknown host")
	}
	h := d.RegisterHost("h", []string{"aa:bb:cc:dd:ee:fe"}, nil, "x", 1)
	if _, err := r.CreateJob(h.ID, "x", []store.StepDef{{Name: "bad", Kind: "nope"}}); err == nil {
		t.Fatal("want error for unknown step kind")
	}
}

func TestDefaultPlanHasFourSteps(t *testing.T) {
	if len(DefaultPlan) != 4 {
		t.Fatalf("want 4 steps, got %d", len(DefaultPlan))
	}
	_ = filepath.Join // keep import if unused in future edits
}
