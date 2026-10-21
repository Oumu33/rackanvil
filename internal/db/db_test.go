package db

import (
	"testing"
	"time"

	"github.com/Oumu33/rackanvil/internal/store"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open("sqlite::memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestMigrationsAndHostRoundtrip(t *testing.T) {
	d := openTestDB(t)
	h := d.RegisterHost("node1", []string{"aa:bb:cc:dd:ee:01"}, []string{"10.0.0.2"}, "Xeon", 32768)
	if h.ID == "" || h.Token == "" {
		t.Fatalf("bad host: %+v", h)
	}
	// Re-enroll on same primary MAC returns same host with fresh token.
	h2 := d.RegisterHost("node1-renamed", []string{"aa:bb:cc:dd:ee:01"}, []string{"10.0.0.3"}, "Xeon", 32768)
	if h2.ID != h.ID {
		t.Fatalf("re-enroll created new host: %s vs %s", h.ID, h2.ID)
	}
	if h2.Token == h.Token {
		t.Fatalf("re-enroll did not rotate token")
	}
	if h2.Hostname != "node1-renamed" {
		t.Fatalf("hostname not updated: %s", h2.Hostname)
	}
	// Authenticate with the new token.
	id, err := d.Authenticate(h2.Token)
	if err != nil || id != h.ID {
		t.Fatalf("authenticate: %v %s", err, id)
	}
	if _, err := d.Authenticate("bogus"); err == nil {
		t.Fatalf("bogus token accepted")
	}
}

func TestTaskLifecycle(t *testing.T) {
	d := openTestDB(t)
	h := d.RegisterHost("node2", []string{"aa:bb:cc:dd:ee:02"}, nil, "", 0)
	tk, err := d.EnqueueTask(h.ID, "echo hi", 60)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := d.EnqueueTask("nope", "echo", 1); err == nil {
		t.Fatalf("enqueue to missing host accepted")
	}
	got, err := d.Checkin(h.ID)
	if err != nil || len(got) != 1 || got[0].ID != tk.ID || got[0].Status != "running" {
		t.Fatalf("checkin: %v %+v", err, got)
	}
	// Second checkin: no more pending.
	got, _ = d.Checkin(h.ID)
	if len(got) != 0 {
		t.Fatalf("second checkin returned tasks")
	}
	if err := d.ReportTask(tk.ID, store.TaskReport{Status: "done", Stdout: "hi\n", ExitCode: 0}); err != nil {
		t.Fatalf("report: %v", err)
	}
	rt, err := d.GetTask(tk.ID)
	if err != nil || rt.Status != "done" || rt.Stdout != "hi\n" {
		t.Fatalf("get task: %v %+v", err, rt)
	}
}

func TestMetricsAndAlarms(t *testing.T) {
	d := openTestDB(t)
	h := d.RegisterHost("node3", []string{"aa:bb:cc:dd:ee:03"}, nil, "", 0)
	m := store.MetricSample{HostID: h.ID, CPUPct: 12.5, MemUsedMB: 1024, MemTotalMB: 8192, DiskJSON: "[]", NetJSON: "{}"}
	m.TS = time.Now()
	if err := d.SaveMetrics(m); err != nil {
		t.Fatalf("save metrics: %v", err)
	}
	lm, err := d.LatestMetrics(h.ID)
	if err != nil || lm.CPUPct != 12.5 {
		t.Fatalf("latest metrics: %v %+v", err, lm)
	}
	if err := d.SaveSmartSample(store.SmartSample{HostID: h.ID, Device: "sda", TS: time.Now(), Attrs: map[string]int64{"5": 3}, Health: "warning"}); err != nil {
		t.Fatalf("save smart: %v", err)
	}
	hist, err := d.SmartHistory(h.ID, "sda", 5)
	if err != nil || len(hist) != 1 || hist[0].Attrs["5"] != 3 {
		t.Fatalf("smart history: %v %+v", err, hist)
	}
	// Alarm dedup: raise twice, still one active alarm.
	if err := d.RaiseAlarm(h.ID, "smart.sda", "warning", "reallocated sectors"); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if err := d.RaiseAlarm(h.ID, "smart.sda", "critical", "worse"); err != nil {
		t.Fatalf("raise2: %v", err)
	}
	aa, err := d.ActiveAlarms(h.ID)
	if err != nil || len(aa) != 1 || aa[0].Severity != "critical" {
		t.Fatalf("active alarms: %v %+v", err, aa)
	}
	if err := d.ResolveAlarm(h.ID, "smart.sda"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	aa, _ = d.ActiveAlarms(h.ID)
	if len(aa) != 0 {
		t.Fatalf("alarm not resolved")
	}
	ah, err := d.AlertHistory(h.ID, 10)
	if err != nil || len(ah) != 1 {
		t.Fatalf("alert history: %v %+v", err, ah)
	}
}

func TestUsersAndAudit(t *testing.T) {
	d := openTestDB(t)
	n, err := d.UserCount()
	if err != nil || n != 0 {
		t.Fatalf("user count: %v %d", err, n)
	}
	if err := d.CreateUser("admin", "bcrypt-hash-here", "admin"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := d.GetUser("admin")
	if err != nil || u.Role != "admin" {
		t.Fatalf("get user: %v %+v", err, u)
	}
	if err := d.Audit("admin", "login", "ok"); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if v, ok := d.GetSetting("nope"); ok || v != "" {
		t.Fatalf("missing setting returned value")
	}
	if err := d.SetSetting("k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, ok := d.GetSetting("k"); !ok || v != "v1" {
		t.Fatalf("get setting: %v %v", v, ok)
	}
	if err := d.SetSetting("k", "v2"); err != nil {
		t.Fatalf("set2: %v", err)
	}
	if v, _ := d.GetSetting("k"); v != "v2" {
		t.Fatalf("setting not updated: %s", v)
	}
}
