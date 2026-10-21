// Package db provides the M2 persistence layer.
//
// Design decisions (recorded honestly):
//   - database/sql + versioned migrations. The SQL is written in a portable
//     subset (TEXT/BIGINT/REAL, no sequences, no RETURNING, unix-epoch
//     BIGINT timestamps) so the *same* migrations run on SQLite and
//     PostgreSQL. Placeholders are written as "?" and rebound to "$n" for
//     PostgreSQL at open time.
//   - Development fallback: modernc.org/sqlite (pure Go, no cgo) because this
//     sandbox cannot reliably install PostgreSQL. Production switch is a
//     one-line DSN change (postgres://...), zero code change — the pgx
//     driver is already imported below.
//   - IDs are opaque random strings (same scheme as M1's in-memory store),
//     so no auto-increment/sequence dialect issues.
package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Oumu33/rackanvil/internal/api"
	"github.com/Oumu33/rackanvil/internal/store"

	_ "github.com/jackc/pgx/v5/stdlib" // registers "pgx" — production PostgreSQL, zero code change
	_ "modernc.org/sqlite"             // registers "sqlite" — pure-Go dev fallback
)

var ErrNotFound = store.ErrNotFound
var ErrUnauthorized = store.ErrUnauthorized

// ---- migrations ----

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		applied_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL,
		created_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS hosts (
		id TEXT PRIMARY KEY,
		hostname TEXT NOT NULL,
		primary_mac TEXT UNIQUE,
		macs_json TEXT NOT NULL,
		ips_json TEXT NOT NULL,
		cpu_model TEXT NOT NULL DEFAULT '',
		memory_mb BIGINT NOT NULL DEFAULT 0,
		token TEXT NOT NULL,
		registered_at BIGINT NOT NULL,
		last_checkin BIGINT NOT NULL DEFAULT 0
	)`,
	`CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		host_id TEXT NOT NULL,
		type TEXT NOT NULL,
		command TEXT NOT NULL,
		timeout_sec BIGINT NOT NULL,
		status TEXT NOT NULL,
		stdout TEXT NOT NULL DEFAULT '',
		stderr TEXT NOT NULL DEFAULT '',
		exit_code BIGINT NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_host_status ON tasks(host_id, status)`,
	`CREATE TABLE IF NOT EXISTS metrics (
		id TEXT PRIMARY KEY,
		host_id TEXT NOT NULL,
		ts BIGINT NOT NULL,
		cpu_pct REAL NOT NULL DEFAULT 0,
		mem_used_mb BIGINT NOT NULL DEFAULT 0,
		mem_total_mb BIGINT NOT NULL DEFAULT 0,
		disk_json TEXT NOT NULL DEFAULT '[]',
		net_json TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_host_ts ON metrics(host_id, ts)`,
	`CREATE TABLE IF NOT EXISTS smart_history (
		id TEXT PRIMARY KEY,
		host_id TEXT NOT NULL,
		device TEXT NOT NULL,
		ts BIGINT NOT NULL,
		attrs_json TEXT NOT NULL,
		health TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_smart_host_dev_ts ON smart_history(host_id, device, ts)`,
	`CREATE TABLE IF NOT EXISTS active_alarms (
		id TEXT PRIMARY KEY,
		host_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		severity TEXT NOT NULL,
		message TEXT NOT NULL,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL,
		UNIQUE(host_id, kind)
	)`,
	`CREATE TABLE IF NOT EXISTS alert_history (
		id TEXT PRIMARY KEY,
		host_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		severity TEXT NOT NULL,
		message TEXT NOT NULL,
		created_at BIGINT NOT NULL,
		resolved_at BIGINT NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_alert_history_host ON alert_history(host_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS audit_logs (
		id TEXT PRIMARY KEY,
		actor TEXT NOT NULL,
		action TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS system_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	// ---- M3 ----
	`CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		host_id TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status, created_at)`,
	`CREATE TABLE IF NOT EXISTS job_steps (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL,
		seq BIGINT NOT NULL,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT '',
		created_at BIGINT NOT NULL,
		updated_at BIGINT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_job_steps_job ON job_steps(job_id, seq)`,
	`CREATE TABLE IF NOT EXISTS discovery_scans (
		id TEXT PRIMARY KEY,
		cidr TEXT NOT NULL,
		found_json TEXT NOT NULL,
		diff_json TEXT NOT NULL,
		created_at BIGINT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS licenses (
		id TEXT PRIMARY KEY,
		customer TEXT NOT NULL,
		max_hosts BIGINT NOT NULL,
		expires_at BIGINT NOT NULL,
		license_text TEXT NOT NULL,
		created_at BIGINT NOT NULL
	)`,
	// M4: per-host configuration snapshots ("配置存档").
	`CREATE TABLE IF NOT EXISTS cfg_snapshots (
		id TEXT PRIMARY KEY,
		host_id TEXT NOT NULL,
		created_at BIGINT NOT NULL,
		note TEXT NOT NULL DEFAULT '',
		raid TEXT NOT NULL DEFAULT '',
		partition TEXT NOT NULL DEFAULT '',
		network TEXT NOT NULL DEFAULT '',
		packages TEXT NOT NULL DEFAULT ''
	)`,
	// M4: topology links (host NIC -> switch port).
	`CREATE TABLE IF NOT EXISTS topo_links (
		host_id TEXT NOT NULL,
		host_mac TEXT NOT NULL DEFAULT '',
		iface TEXT NOT NULL DEFAULT '',
		switch_name TEXT NOT NULL,
		switch_port TEXT NOT NULL,
		source TEXT NOT NULL,
		seen_at BIGINT NOT NULL,
		PRIMARY KEY (host_id, iface, switch_name, switch_port)
	)`,
	// M4: relay node registrations.
	`CREATE TABLE IF NOT EXISTS relay_nodes (
		name TEXT PRIMARY KEY,
		subnets TEXT NOT NULL DEFAULT '',
		last_seen BIGINT NOT NULL
	)`,
}

// ---- open ----

// DB wraps database/sql with a recorded driver name for placeholder rebinding.
type DB struct {
	sql    *sql.DB
	driver string // "sqlite" or "pgx"
	mu     sync.Mutex
}

// Open connects using a DSN with an explicit scheme prefix:
//
//	sqlite:/abs/path/to.db   -> modernc.org/sqlite (pure Go)
//	sqlite::memory:          -> in-memory SQLite (tests)
//	postgres://user:pass@host:5432/db -> pgx (production)
func Open(dsn string) (*DB, error) {
	var driver, src string
	switch {
	case strings.HasPrefix(dsn, "sqlite:"):
		driver = "sqlite"
		src = strings.TrimPrefix(dsn, "sqlite:")
		if src == ":memory:" {
			// modernc in-memory DSN.
			src = "file::memory:?cache=shared"
		} else if !strings.HasPrefix(src, "file:") {
			src = "file:" + src
		}
	case strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://"):
		driver = "pgx"
		src = dsn
	default:
		return nil, fmt.Errorf("db: unsupported DSN %q (want sqlite: or postgres://)", dsn)
	}
	sqldb, err := sql.Open(driver, src)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	sqldb.SetMaxOpenConns(16)
	if driver == "sqlite" {
		// Single writer; WAL for concurrent readers.
		sqldb.SetMaxOpenConns(1)
		if _, err := sqldb.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			sqldb.Close()
			return nil, fmt.Errorf("db: pragma: %w", err)
		}
	}
	d := &DB{sql: sqldb, driver: driver}
	if err := d.migrate(); err != nil {
		sqldb.Close()
		return nil, err
	}
	// M3: idempotent column add (SQLite has no ADD COLUMN IF NOT EXISTS).
	// Ignored when the column already exists. The error text differs per
	// dialect: SQLite reports "duplicate column name", PostgreSQL (42701)
	// reports 'column "install_pending" of relation "hosts" already exists'.
	if _, err := sqldb.Exec(`ALTER TABLE hosts ADD COLUMN install_pending BIGINT NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") &&
			!strings.Contains(err.Error(), "already exists") {
			sqldb.Close()
			return nil, fmt.Errorf("db: add install_pending: %w", err)
		}
	}
	return d, nil
}

func (d *DB) Close() error { return d.sql.Close() }

// rebind converts "?" placeholders to "$n" for PostgreSQL.
func (d *DB) rebind(q string) string {
	if d.driver != "pgx" {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

func (d *DB) migrate() error {
	if _, err := d.sql.Exec(migrations[0]); err != nil {
		return fmt.Errorf("db: migrate bootstrap: %w", err)
	}
	var maxV sql.NullInt64
	_ = d.sql.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&maxV)
	applied := 0
	if maxV.Valid {
		applied = int(maxV.Int64)
	}
	for i, m := range migrations[1:] {
		v := i + 1
		if v <= applied {
			continue
		}
		if _, err := d.sql.Exec(m); err != nil {
			return fmt.Errorf("db: migrate v%d: %w", v, err)
		}
		if _, err := d.sql.Exec(d.rebind(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`), v, time.Now().Unix()); err != nil {
			return fmt.Errorf("db: migrate record v%d: %w", v, err)
		}
	}
	return nil
}

func newID(prefix string) string {
	var b [8]byte
	rand.Read(b[:])
	return prefix + "-" + hex.EncodeToString(b[:])
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- store-compatible surface (same method set as *store.Store) ----

// RegisterHost enrolls a host (or re-enrolls on primary MAC match, like M1).
func (d *DB) RegisterHost(hostname string, macs, ips []string, cpuModel string, memoryMB int64) *store.Host {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	primary := ""
	if len(macs) > 0 {
		primary = macs[0]
	}
	if primary != "" {
		var h store.Host
		var macsJ, ipsJ string
		var regAt, lastCheckin int64
		err := d.sql.QueryRow(d.rebind(
			`SELECT id, hostname, macs_json, ips_json, cpu_model, memory_mb, token, registered_at, last_checkin
			 FROM hosts WHERE primary_mac = ?`), primary).
			Scan(&h.ID, &h.Hostname, &macsJ, &ipsJ, &h.CPUModel, &h.MemoryMB, &h.Token, &regAt, &lastCheckin)
		if err == nil {
			_ = json.Unmarshal([]byte(macsJ), &h.MACs)
			_ = json.Unmarshal([]byte(ipsJ), &h.IPs)
			h.Hostname, h.IPs, h.CPUModel, h.MemoryMB = hostname, ips, cpuModel, memoryMB
			h.RegisteredAt = time.Unix(now, 0)
			h.LastCheckin = time.Unix(lastCheckin, 0)
			h.Token = newToken()
			_, _ = d.sql.Exec(d.rebind(
				`UPDATE hosts SET hostname=?, macs_json=?, ips_json=?, cpu_model=?, memory_mb=?, token=?, registered_at=? WHERE id=?`),
				hostname, mustJSON(macs), mustJSON(ips), cpuModel, memoryMB, h.Token, now, h.ID)
			return &h
		}
	}
	id := newID("host")
	token := newToken()
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO hosts(id, hostname, primary_mac, macs_json, ips_json, cpu_model, memory_mb, token, registered_at, last_checkin)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`),
		id, hostname, nullIfEmpty(primary), mustJSON(macs), mustJSON(ips), cpuModel, memoryMB, token, now)
	if err != nil {
		// Primary MAC raced in concurrently; fall back to returning the existing row.
		if primary != "" {
			var h store.Host
			var macsJ, ipsJ string
			var regAt, lastCheckin int64
			if err2 := d.sql.QueryRow(d.rebind(
				`SELECT id, hostname, macs_json, ips_json, cpu_model, memory_mb, token, registered_at, last_checkin
				 FROM hosts WHERE primary_mac = ?`), primary).
				Scan(&h.ID, &h.Hostname, &macsJ, &ipsJ, &h.CPUModel, &h.MemoryMB, &h.Token, &regAt, &lastCheckin); err2 == nil {
				_ = json.Unmarshal([]byte(macsJ), &h.MACs)
				_ = json.Unmarshal([]byte(ipsJ), &h.IPs)
				h.RegisteredAt = time.Unix(regAt, 0)
				h.LastCheckin = time.Unix(lastCheckin, 0)
				return &h
			}
		}
	}
	return &store.Host{
		ID: id, Hostname: hostname, MACs: macs, IPs: ips,
		CPUModel: cpuModel, MemoryMB: memoryMB, Token: token,
		RegisteredAt: time.Unix(now, 0),
	}
}

func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func newToken() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (d *DB) Authenticate(token string) (string, error) {
	var id string
	err := d.sql.QueryRow(d.rebind(`SELECT id FROM hosts WHERE token = ?`), token).Scan(&id)
	if err != nil {
		return "", ErrUnauthorized
	}
	return id, nil
}

// IssueSignKey generates (or rotates) the host's M11 request-signing key
// and returns it.
func (d *DB) IssueSignKey(hostID string) (string, error) {
	var b [32]byte
	rand.Read(b[:])
	key := hex.EncodeToString(b[:])
	res, err := d.sql.Exec(d.rebind(`UPDATE hosts SET sign_key = ? WHERE id = ?`), key, hostID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return key, nil
}

// HostSignKey returns the host's signing key, or false when the host was
// enrolled without signing support (legacy token-only host).
func (d *DB) HostSignKey(hostID string) (string, bool) {
	var key string
	err := d.sql.QueryRow(d.rebind(`SELECT sign_key FROM hosts WHERE id = ?`), hostID).Scan(&key)
	if err != nil || key == "" {
		return "", false
	}
	return key, true
}

func (d *DB) Checkin(hostID string) ([]*store.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	res, err := d.sql.Exec(d.rebind(`UPDATE hosts SET last_checkin = ? WHERE id = ?`), now, hostID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	rows, err := d.sql.Query(d.rebind(
		`SELECT id, host_id, type, command, timeout_sec, status, stdout, stderr, exit_code, error, created_at, updated_at
		 FROM tasks WHERE host_id = ? AND status = 'pending' ORDER BY created_at`), hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Task
	for rows.Next() {
		t := &store.Task{}
		var created, updated int64
		if err := rows.Scan(&t.ID, &t.HostID, &t.Type, &t.Command, &t.TimeoutSec, &t.Status,
			&t.Stdout, &t.Stderr, &t.ExitCode, &t.Error, &created, &updated); err != nil {
			return nil, err
		}
		t.CreatedAt, t.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, t)
	}
	for _, t := range out {
		_, _ = d.sql.Exec(d.rebind(`UPDATE tasks SET status='running', updated_at=? WHERE id=?`), now, t.ID)
		t.Status = "running"
		t.UpdatedAt = time.Unix(now, 0)
	}
	return out, rows.Err()
}

func (d *DB) EnqueueTask(hostID, command string, timeoutSec int) (*store.Task, error) {
	return d.EnqueueTypedTask(hostID, "shell", command, timeoutSec)
}

// EnqueueTypedTask adds a task of the given type (M4: locator/raid/firmware/...).
func (d *DB) EnqueueTypedTask(hostID, typ, command string, timeoutSec int) (*store.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	if err := d.sql.QueryRow(d.rebind(`SELECT COUNT(*) FROM hosts WHERE id = ?`), hostID).Scan(&n); err != nil || n == 0 {
		return nil, ErrNotFound
	}
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	now := time.Now().Unix()
	t := &store.Task{
		ID: newID("task"), HostID: hostID, Type: typ, Command: command,
		TimeoutSec: timeoutSec, Status: "pending",
		CreatedAt: time.Unix(now, 0), UpdatedAt: time.Unix(now, 0),
	}
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO tasks(id, host_id, type, command, timeout_sec, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`),
		t.ID, hostID, typ, command, timeoutSec, now, now)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (d *DB) ReportTask(taskID string, r store.TaskReport) error {
	res, err := d.sql.Exec(d.rebind(
		`UPDATE tasks SET status=?, stdout=?, stderr=?, exit_code=?, error=?, updated_at=? WHERE id=?`),
		r.Status, r.Stdout, r.Stderr, r.ExitCode, r.Error, time.Now().Unix(), taskID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) GetTask(taskID string) (*store.Task, error) {
	t := &store.Task{}
	var created, updated int64
	err := d.sql.QueryRow(d.rebind(
		`SELECT id, host_id, type, command, timeout_sec, status, stdout, stderr, exit_code, error, created_at, updated_at
		 FROM tasks WHERE id = ?`), taskID).
		Scan(&t.ID, &t.HostID, &t.Type, &t.Command, &t.TimeoutSec, &t.Status,
			&t.Stdout, &t.Stderr, &t.ExitCode, &t.Error, &created, &updated)
	if err != nil {
		return nil, ErrNotFound
	}
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return t, nil
}

func (d *DB) Hosts() []*store.Host {
	rows, err := d.sql.Query(
		`SELECT id, hostname, macs_json, ips_json, cpu_model, memory_mb, token, sign_key, registered_at, last_checkin
		 FROM hosts ORDER BY registered_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*store.Host
	for rows.Next() {
		h := &store.Host{}
		var macsJ, ipsJ string
		var reg, last int64
		if err := rows.Scan(&h.ID, &h.Hostname, &macsJ, &ipsJ, &h.CPUModel, &h.MemoryMB, &h.Token, &h.SignKey, &reg, &last); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(macsJ), &h.MACs)
		_ = json.Unmarshal([]byte(ipsJ), &h.IPs)
		h.RegisteredAt, h.LastCheckin = time.Unix(reg, 0), time.Unix(last, 0)
		out = append(out, h)
	}
	return out
}

func (d *DB) Tasks() []*store.Task {
	rows, err := d.sql.Query(
		`SELECT id, host_id, type, command, timeout_sec, status, stdout, stderr, exit_code, error, created_at, updated_at
		 FROM tasks ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*store.Task
	for rows.Next() {
		t := &store.Task{}
		var created, updated int64
		if err := rows.Scan(&t.ID, &t.HostID, &t.Type, &t.Command, &t.TimeoutSec, &t.Status,
			&t.Stdout, &t.Stderr, &t.ExitCode, &t.Error, &created, &updated); err != nil {
			continue
		}
		t.CreatedAt, t.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, t)
	}
	return out
}

// ---- metrics / SMART ----

func (d *DB) SaveMetrics(m store.MetricSample) error {
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO metrics(id, host_id, ts, cpu_pct, mem_used_mb, mem_total_mb, disk_json, net_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		newID("metric"), m.HostID, m.TS.Unix(), m.CPUPct, m.MemUsedMB, m.MemTotalMB, m.DiskJSON, m.NetJSON)
	return err
}

func (d *DB) LatestMetrics(hostID string) (*store.MetricSample, error) {
	m := &store.MetricSample{HostID: hostID}
	var ts int64
	err := d.sql.QueryRow(d.rebind(
		`SELECT ts, cpu_pct, mem_used_mb, mem_total_mb, disk_json, net_json FROM metrics
		 WHERE host_id = ? ORDER BY ts DESC LIMIT 1`), hostID).
		Scan(&ts, &m.CPUPct, &m.MemUsedMB, &m.MemTotalMB, &m.DiskJSON, &m.NetJSON)
	if err != nil {
		return nil, ErrNotFound
	}
	m.TS = time.Unix(ts, 0)
	return m, nil
}

func (d *DB) SaveSmartSample(s store.SmartSample) error {
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO smart_history(id, host_id, device, ts, attrs_json, health) VALUES (?, ?, ?, ?, ?, ?)`),
		newID("smart"), s.HostID, s.Device, s.TS.Unix(), mustJSON(s.Attrs), s.Health)
	return err
}

// SmartHistory returns the most recent samples for a device, newest first.
func (d *DB) SmartHistory(hostID, device string, limit int) ([]store.SmartSample, error) {
	rows, err := d.sql.Query(d.rebind(
		`SELECT ts, attrs_json, health FROM smart_history
		 WHERE host_id = ? AND device = ? ORDER BY ts DESC LIMIT ?`), hostID, device, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.SmartSample
	for rows.Next() {
		var s store.SmartSample
		var ts int64
		var attrsJ string
		if err := rows.Scan(&ts, &attrsJ, &s.Health); err != nil {
			continue
		}
		s.HostID, s.Device, s.TS = hostID, device, time.Unix(ts, 0)
		s.Attrs = map[string]int64{}
		_ = json.Unmarshal([]byte(attrsJ), &s.Attrs)
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---- alarms ----

// RaiseAlarm creates or refreshes an active alarm (deduped on host+kind).
func (d *DB) RaiseAlarm(hostID, kind, severity, message string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now().Unix()
	var id string
	err := d.sql.QueryRow(d.rebind(
		`SELECT id FROM active_alarms WHERE host_id = ? AND kind = ?`), hostID, kind).Scan(&id)
	if err == nil {
		_, err = d.sql.Exec(d.rebind(
			`UPDATE active_alarms SET severity=?, message=?, updated_at=? WHERE id=?`),
			severity, message, now, id)
		return err
	}
	_, err = d.sql.Exec(d.rebind(
		`INSERT INTO active_alarms(id, host_id, kind, severity, message, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		newID("alarm"), hostID, kind, severity, message, now, now)
	return err
}

// ResolveAlarm moves an active alarm into history.
func (d *DB) ResolveAlarm(hostID, kind string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var a store.Alarm
	var created, updated int64
	err := d.sql.QueryRow(d.rebind(
		`SELECT id, host_id, kind, severity, message, created_at, updated_at
		 FROM active_alarms WHERE host_id = ? AND kind = ?`), hostID, kind).
		Scan(&a.ID, &a.HostID, &a.Kind, &a.Severity, &a.Message, &created, &updated)
	if err != nil {
		return nil // nothing active: not an error
	}
	now := time.Now().Unix()
	_, err = d.sql.Exec(d.rebind(
		`INSERT INTO alert_history(id, host_id, kind, severity, message, created_at, resolved_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		a.ID, a.HostID, a.Kind, a.Severity, a.Message, created, now)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(d.rebind(`DELETE FROM active_alarms WHERE id = ?`), a.ID)
	return err
}

func (d *DB) ActiveAlarms(hostID string) ([]store.Alarm, error) {
	rows, err := d.sql.Query(d.rebind(
		`SELECT id, host_id, kind, severity, message, created_at, updated_at FROM active_alarms
		 WHERE host_id = ? ORDER BY created_at`), hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlarms(rows)
}

func (d *DB) AlertHistory(hostID string, limit int) ([]store.Alarm, error) {
	rows, err := d.sql.Query(d.rebind(
		`SELECT id, host_id, kind, severity, message, created_at, resolved_at FROM alert_history
		 WHERE host_id = ? ORDER BY created_at DESC LIMIT ?`), hostID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Alarm
	for rows.Next() {
		var a store.Alarm
		var created, extra int64
		if err := rows.Scan(&a.ID, &a.HostID, &a.Kind, &a.Severity, &a.Message, &created, &extra); err != nil {
			continue
		}
		a.CreatedAt = time.Unix(created, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAlarms(rows *sql.Rows) ([]store.Alarm, error) {
	var out []store.Alarm
	for rows.Next() {
		var a store.Alarm
		var created, updated int64
		if err := rows.Scan(&a.ID, &a.HostID, &a.Kind, &a.Severity, &a.Message, &created, &updated); err != nil {
			continue
		}
		a.CreatedAt, a.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- users / audit / settings ----

func (d *DB) CreateUser(username, passwordHash, role string) error {
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO users(id, username, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`),
		newID("user"), username, passwordHash, role, time.Now().Unix())
	return err
}

func (d *DB) GetUser(username string) (*store.User, error) {
	u := &store.User{}
	var created int64
	err := d.sql.QueryRow(d.rebind(
		`SELECT id, username, password_hash, role, created_at FROM users WHERE username = ?`), username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &created)
	if err != nil {
		return nil, ErrNotFound
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

func (d *DB) UserCount() (int, error) {
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (d *DB) Audit(actor, action, detail string) error {
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO audit_logs(id, actor, action, detail, created_at) VALUES (?, ?, ?, ?, ?)`),
		newID("audit"), actor, action, detail, time.Now().Unix())
	return err
}

func (d *DB) GetSetting(key string) (string, bool) {
	var v string
	if err := d.sql.QueryRow(d.rebind(`SELECT value FROM system_settings WHERE key = ?`), key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

func (d *DB) SetSetting(key, value string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	_ = d.sql.QueryRow(d.rebind(`SELECT COUNT(*) FROM system_settings WHERE key = ?`), key).Scan(&n)
	if n > 0 {
		_, err := d.sql.Exec(d.rebind(`UPDATE system_settings SET value = ? WHERE key = ?`), value, key)
		return err
	}
	_, err := d.sql.Exec(d.rebind(`INSERT INTO system_settings(key, value) VALUES (?, ?)`), key, value)
	return err
}

// Compile-time check: DBStore must satisfy the api store interface, so the
// M1 in-memory store can be swapped for the database with zero api changes.
var _ api.Store = (*DB)(nil)

// ---- M3: orchestration jobs ----

// CreateJob inserts a job with its steps (all pending).
func (d *DB) CreateJob(name, hostID string, steps []store.StepDef) (*store.Job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	if err := d.sql.QueryRow(d.rebind(`SELECT COUNT(*) FROM hosts WHERE id = ?`), hostID).Scan(&n); err != nil || n == 0 {
		return nil, ErrNotFound
	}
	now := time.Now().Unix()
	j := &store.Job{ID: newID("job"), Name: name, HostID: hostID, Status: "pending",
		CreatedAt: time.Unix(now, 0), UpdatedAt: time.Unix(now, 0)}
	if _, err := d.sql.Exec(d.rebind(
		`INSERT INTO jobs(id, name, host_id, status, created_at, updated_at) VALUES (?, ?, ?, 'pending', ?, ?)`),
		j.ID, name, hostID, now, now); err != nil {
		return nil, err
	}
	for i, s := range steps {
		kind := s.Kind
		switch kind {
		case "bmc_mock", "raid_mock", "stress", "install":
		default:
			return nil, fmt.Errorf("db: unknown step kind %q", kind)
		}
		if _, err := d.sql.Exec(d.rebind(
			`INSERT INTO job_steps(id, job_id, seq, name, kind, status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)`),
			newID("step"), j.ID, i, s.Name, kind, now, now); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func scanJobStep(rows *sql.Rows) (*store.JobStep, error) {
	s := &store.JobStep{}
	var created, updated int64
	if err := rows.Scan(&s.ID, &s.JobID, &s.Seq, &s.Name, &s.Kind, &s.Status, &s.Detail, &created, &updated); err != nil {
		return nil, err
	}
	s.CreatedAt, s.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	return s, nil
}

// GetJob returns a job with its steps in order.
func (d *DB) GetJob(id string) (*store.Job, []*store.JobStep, error) {
	j := &store.Job{}
	var created, updated int64
	err := d.sql.QueryRow(d.rebind(
		`SELECT id, name, host_id, status, created_at, updated_at FROM jobs WHERE id = ?`), id).
		Scan(&j.ID, &j.Name, &j.HostID, &j.Status, &created, &updated)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	j.CreatedAt, j.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	rows, err := d.sql.Query(d.rebind(
		`SELECT id, job_id, seq, name, kind, status, detail, created_at, updated_at
		 FROM job_steps WHERE job_id = ? ORDER BY seq`), id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var steps []*store.JobStep
	for rows.Next() {
		s, err := scanJobStep(rows)
		if err != nil {
			return nil, nil, err
		}
		steps = append(steps, s)
	}
	return j, steps, rows.Err()
}

// ListJobs returns the most recent jobs.
func (d *DB) ListJobs(limit int) ([]*store.Job, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.sql.Query(d.rebind(
		`SELECT id, name, host_id, status, created_at, updated_at FROM jobs ORDER BY created_at DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Job
	for rows.Next() {
		j := &store.Job{}
		var created, updated int64
		if err := rows.Scan(&j.ID, &j.Name, &j.HostID, &j.Status, &created, &updated); err != nil {
			continue
		}
		j.CreatedAt, j.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimJob atomically takes the oldest pending job (runner use).
func (d *DB) ClaimJob() (*store.Job, []*store.JobStep, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var id string
	err := d.sql.QueryRow(
		`SELECT id FROM jobs WHERE status = 'pending' ORDER BY created_at LIMIT 1`).Scan(&id)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	now := time.Now().Unix()
	if _, err := d.sql.Exec(d.rebind(`UPDATE jobs SET status='running', updated_at=? WHERE id=? AND status='pending'`), now, id); err != nil {
		return nil, nil, err
	}
	d.mu.Unlock()
	j, steps, err := d.GetJob(id)
	d.mu.Lock()
	return j, steps, err
}

func (d *DB) SetJobStatus(id, status string) error {
	_, err := d.sql.Exec(d.rebind(`UPDATE jobs SET status=?, updated_at=? WHERE id=?`),
		status, time.Now().Unix(), id)
	return err
}

func (d *DB) SetStepStatus(stepID, status, detail string) error {
	_, err := d.sql.Exec(d.rebind(`UPDATE job_steps SET status=?, detail=?, updated_at=? WHERE id=?`),
		status, detail, time.Now().Unix(), stepID)
	return err
}

// ---- M3: install_pending flag + host lookup by MAC ----

func (d *DB) SetInstallPending(hostID string, pending bool) error {
	v := 0
	if pending {
		v = 1
	}
	_, err := d.sql.Exec(d.rebind(`UPDATE hosts SET install_pending=? WHERE id=?`), v, hostID)
	return err
}

func (d *DB) InstallPending(hostID string) bool {
	var v int
	if err := d.sql.QueryRow(d.rebind(`SELECT install_pending FROM hosts WHERE id=?`), hostID).Scan(&v); err != nil {
		return false
	}
	return v != 0
}

// GetHostByMAC finds a host by any of its MACs (case-insensitive).
func (d *DB) GetHostByMAC(mac string) (*store.Host, error) {
	mac = strings.ToLower(mac)
	for _, h := range d.Hosts() {
		for _, m := range h.MACs {
			if strings.ToLower(m) == mac {
				return h, nil
			}
		}
	}
	return nil, ErrNotFound
}

// ---- M3: metrics history (dashboard) ----

// MetricsHistory returns up to limit samples, oldest first.
func (d *DB) MetricsHistory(hostID string, limit int) ([]store.MetricSample, error) {
	if limit <= 0 {
		limit = 120
	}
	rows, err := d.sql.Query(d.rebind(
		`SELECT ts, cpu_pct, mem_used_mb, mem_total_mb, disk_json, net_json FROM metrics
		 WHERE host_id = ? ORDER BY ts DESC LIMIT ?`), hostID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rev []store.MetricSample
	for rows.Next() {
		var m store.MetricSample
		var ts int64
		if err := rows.Scan(&ts, &m.CPUPct, &m.MemUsedMB, &m.MemTotalMB, &m.DiskJSON, &m.NetJSON); err != nil {
			continue
		}
		m.HostID, m.TS = hostID, time.Unix(ts, 0)
		rev = append(rev, m)
	}
	// Reverse to oldest-first for charting.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, rows.Err()
}

// ---- M3: discovery scans ----

func (d *DB) SaveScan(cidr, foundJSON, diffJSON string) (*store.ScanRecord, error) {
	now := time.Now().Unix()
	r := &store.ScanRecord{ID: newID("scan"), CIDR: cidr, FoundJSON: foundJSON,
		DiffJSON: diffJSON, CreatedAt: time.Unix(now, 0)}
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO discovery_scans(id, cidr, found_json, diff_json, created_at) VALUES (?, ?, ?, ?, ?)`),
		r.ID, cidr, foundJSON, diffJSON, now)
	return r, err
}

func (d *DB) LatestScan() (*store.ScanRecord, error) {
	r := &store.ScanRecord{}
	var created int64
	err := d.sql.QueryRow(
		`SELECT id, cidr, found_json, diff_json, created_at FROM discovery_scans ORDER BY created_at DESC LIMIT 1`).
		Scan(&r.ID, &r.CIDR, &r.FoundJSON, &r.DiffJSON, &created)
	if err != nil {
		return nil, ErrNotFound
	}
	r.CreatedAt = time.Unix(created, 0)
	return r, nil
}

// ---- M3: licenses ----

func (d *DB) SaveLicense(customer string, maxHosts int, expiresAt time.Time, licenseText string) (*store.LicenseRecord, error) {
	now := time.Now().Unix()
	r := &store.LicenseRecord{ID: newID("lic"), Customer: customer, MaxHosts: maxHosts,
		ExpiresAt: expiresAt, LicenseTxt: licenseText, CreatedAt: time.Unix(now, 0)}
	_, err := d.sql.Exec(d.rebind(
		`INSERT INTO licenses(id, customer, max_hosts, expires_at, license_text, created_at) VALUES (?, ?, ?, ?, ?, ?)`),
		r.ID, customer, maxHosts, expiresAt.Unix(), licenseText, now)
	return r, err
}

func (d *DB) LatestLicense() (*store.LicenseRecord, error) {
	r := &store.LicenseRecord{}
	var exp, created int64
	err := d.sql.QueryRow(
		`SELECT id, customer, max_hosts, expires_at, license_text, created_at FROM licenses ORDER BY created_at DESC LIMIT 1`).
		Scan(&r.ID, &r.Customer, &r.MaxHosts, &exp, &r.LicenseTxt, &created)
	if err != nil {
		return nil, ErrNotFound
	}
	r.ExpiresAt, r.CreatedAt = time.Unix(exp, 0), time.Unix(created, 0)
	return r, nil
}

// ---- M3: install context (root password handoff to preseed) ----

// SetInstallContext stores the per-install root password for a host.
func (d *DB) SetInstallContext(hostID, rootPW string) error {
	return d.SetSetting("installctx:"+hostID, rootPW)
}

// GetInstallContext returns the pending install root password, if any.
func (d *DB) GetInstallContext(hostID string) (string, bool) {
	return d.GetSetting("installctx:" + hostID)
}

// ClearInstallContext drops the install context (called on install-done).
func (d *DB) ClearInstallContext(hostID string) error {
	_, err := d.sql.Exec(d.rebind(`DELETE FROM system_settings WHERE key = ?`), "installctx:"+hostID)
	return err
}

// Compile-time check: *DB must satisfy the api store interface.
var _ api.Store = (*DB)(nil)
