// Package db — live PostgreSQL test. Gated on RACKANVIL_TEST_PG_DSN so the
// normal suite (SQLite) is unaffected. Used to prove the *same* migrations
// run on real PostgreSQL in M5.
package db

import (
	"os"
	"testing"

	"github.com/Oumu33/rackanvil/internal/store"
)

// TestPostgresMigrationsAndRoundtrip runs a compact slice of the SQLite suite
// against a real PostgreSQL instance. Skipped unless RACKANVIL_TEST_PG_DSN is set,
// e.g. postgres://postgres@127.0.0.1:5433/rackanvil_test?sslmode=disable
func TestPostgresMigrationsAndRoundtrip(t *testing.T) {
	dsn := os.Getenv("RACKANVIL_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("RACKANVIL_TEST_PG_DSN not set; live PostgreSQL test skipped")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	// Host register + re-enroll + authenticate.
	h := d.RegisterHost("pgnode1", []string{"aa:bb:cc:dd:ee:99"}, []string{"10.0.0.9"}, "Xeon", 65536)
	if h.ID == "" || h.Token == "" {
		t.Fatalf("bad host: %+v", h)
	}
	h2 := d.RegisterHost("pgnode1b", []string{"aa:bb:cc:dd:ee:99"}, []string{"10.0.0.10"}, "Xeon", 65536)
	if h2.ID != h.ID || h2.Token == h.Token {
		t.Fatalf("re-enroll mismatch: %+v vs %+v", h, h2)
	}
	if id, err := d.Authenticate(h2.Token); err != nil || id != h.ID {
		t.Fatalf("authenticate: %v %s", err, id)
	}

	// Task lifecycle: enqueue -> checkin claim -> report -> get.
	task, err := d.EnqueueTypedTask(h.ID, "shell", "echo pg-ok", 60)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := d.Checkin(h.ID)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("checkin: %v %d", err, len(claimed))
	}
	found := false
	for _, ct := range claimed {
		if ct.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task %s not claimed", task.ID)
	}
	if err := d.ReportTask(task.ID, store.TaskReport{Status: "done", Stdout: "pg-ok\n", ExitCode: 0}); err != nil {
		t.Fatalf("report: %v", err)
	}
	got, err := d.GetTask(task.ID)
	if err != nil || got.Status != "done" || got.Stdout != "pg-ok\n" {
		t.Fatalf("get: %v %+v", err, got)
	}

	// Metrics + smart + alarms.
	if err := d.SaveMetrics(store.MetricSample{HostID: h.ID, CPUPct: 12.5}); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if err := d.SaveSmartSample(store.SmartSample{HostID: h.ID, Device: "/dev/sda", Health: "ok"}); err != nil {
		t.Fatalf("smart: %v", err)
	}
	if err := d.RaiseAlarm(h.ID, "smart.test", "critical", "pg alarm"); err != nil {
		t.Fatalf("raise: %v", err)
	}
	alarms, err := d.ActiveAlarms(h.ID)
	if err != nil || len(alarms) != 1 {
		t.Fatalf("active alarms: %v %d", err, len(alarms))
	}
	if err := d.ResolveAlarm(h.ID, "smart.test"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Users.
	if err := d.CreateUser("pgadmin", "hash", "admin"); err != nil {
		t.Fatalf("createuser: %v", err)
	}
	if u, err := d.GetUser("pgadmin"); err != nil || u.Role != "admin" {
		t.Fatalf("getuser: %v %+v", err, u)
	}

	// Settings roundtrip.
	if err := d.SetSetting("k1", "v1"); err != nil {
		t.Fatalf("setsetting: %v", err)
	}
	if v, ok := d.GetSetting("k1"); !ok || v != "v1" {
		t.Fatalf("getsetting: %v %v", v, ok)
	}
	t.Logf("live PostgreSQL roundtrip PASS (host=%s)", h.ID)
}
