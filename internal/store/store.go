// Package store provides the M1 in-memory data layer.
//
// M1 deliberately keeps everything in process memory (a single mutex-guarded
// struct). The types mirror the tables we plan to create in PostgreSQL in M2
// (see deplos-reversing/db-tables.txt: hosts, asset_hardware_info,
// orchestration_jobs, job_assets), so the M2 migration is mechanical.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// Host is a managed machine (physical or virtual) enrolled via initialize.
type Host struct {
	ID           string
	Hostname     string
	MACs         []string
	IPs          []string
	CPUModel     string
	MemoryMB     int64
	Token        string // agent auth token, issued at initialize
	SignKey      string // M11: per-host HMAC signing key ("" = legacy host, token-only)
	RegisteredAt time.Time
	LastCheckin  time.Time
}

// Task is a unit of work dispatched to an agent (M1: only "shell").
type Task struct {
	ID         string
	HostID     string
	Type       string // "shell"
	Command    string
	TimeoutSec int
	Status     string // pending | running | done | failed
	Stdout     string
	Stderr     string
	ExitCode   int
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// TaskReport is what the agent sends back after executing a task.
type TaskReport struct {
	Status   string
	Stdout   string
	Stderr   string
	ExitCode int
	Error    string
}

var ErrNotFound = errors.New("store: not found")
var ErrUnauthorized = errors.New("store: bad token")

// ---- M2 shared DTOs (leaf types: api and db both use these) ----

// MetricSample is one collector snapshot for a host.
type MetricSample struct {
	HostID     string
	TS         time.Time
	CPUPct     float64
	MemUsedMB  int64
	MemTotalMB int64
	DiskJSON   string
	NetJSON    string
}

// SmartSample is one SMART/NVMe health reading for a device.
type SmartSample struct {
	HostID string
	Device string
	TS     time.Time
	Attrs  map[string]int64 // SMART id ("5","187",...) or nvme key ("percentage_used",...) -> raw value
	Health string           // ok | warning | critical
}

// Alarm is an active or historical alert.
type Alarm struct {
	ID        string
	HostID    string
	Kind      string
	Severity  string // warning | critical
	Message   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// User is a web console account.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string // admin | operator | viewer
	CreatedAt    time.Time
}

// ---- M3 shared DTOs ----

// StepDef describes one orchestration step at job creation time.
type StepDef struct {
	Name string
	Kind string // bmc_mock | raid_mock | stress | install
}

// Job is a delivery orchestration job: BMC -> RAID -> stress -> install.
type Job struct {
	ID        string
	Name      string
	HostID    string
	Status    string // pending | running | done | failed
	CreatedAt time.Time
	UpdatedAt time.Time
}

// JobStep is one step inside a job.
type JobStep struct {
	ID        string
	JobID     string
	Seq       int
	Name      string
	Kind      string
	Status    string // pending | running | done | failed | skipped
	Detail    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScanRecord is one persisted discovery scan.
type ScanRecord struct {
	ID        string
	CIDR      string
	FoundJSON string
	DiffJSON  string
	CreatedAt time.Time
}

// LicenseRecord is one imported license.
type LicenseRecord struct {
	ID         string
	Customer   string
	MaxHosts   int
	ExpiresAt  time.Time
	LicenseTxt string
	CreatedAt  time.Time
}

// Store is a concurrency-safe in-memory registry of hosts and tasks.
type Store struct {
	mu      sync.Mutex
	hosts   map[string]*Host    // hostID -> Host
	tokens  map[string]string   // agent token -> hostID
	tasks   map[string]*Task    // taskID -> Task
	pending map[string][]string // hostID -> FIFO of pending taskIDs
	seq     uint64
}

func New() *Store {
	return &Store{
		hosts:   make(map[string]*Host),
		tokens:  make(map[string]string),
		tasks:   make(map[string]*Task),
		pending: make(map[string][]string),
	}
}

func newID(prefix string, seq uint64) string {
	return prefix + "-" + hex8(seq)
}

func hex8(seq uint64) string {
	var b [8]byte
	rand.Read(b[:])
	// Mix in the sequence number so IDs stay unique even if rand misbehaves.
	for i := 0; i < 8; i++ {
		b[i] ^= byte(seq >> (8 * i))
	}
	return hex.EncodeToString(b[:])
}

func newToken() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// RegisterHost enrolls a host (or re-enrolls it: same primary MAC returns the
// existing host with a fresh token).
func (s *Store) RegisterHost(hostname string, macs, ips []string, cpuModel string, memoryMB int64) *Host {
	s.mu.Lock()
	defer s.mu.Unlock()

	primary := ""
	if len(macs) > 0 {
		primary = macs[0]
	}
	for _, h := range s.hosts {
		for _, m := range h.MACs {
			if m != "" && m == primary {
				h.Hostname = hostname
				h.IPs = ips
				h.CPUModel = cpuModel
				h.MemoryMB = memoryMB
				h.RegisteredAt = time.Now()
				delete(s.tokens, h.Token)
				h.Token = newToken()
				s.tokens[h.Token] = h.ID
				return h
			}
		}
	}

	s.seq++
	h := &Host{
		ID:           newID("host", s.seq),
		Hostname:     hostname,
		MACs:         macs,
		IPs:          ips,
		CPUModel:     cpuModel,
		MemoryMB:     memoryMB,
		Token:        newToken(),
		RegisteredAt: time.Now(),
	}
	s.hosts[h.ID] = h
	s.tokens[h.Token] = h.ID
	return h
}

// Authenticate resolves an agent token to its host ID.
func (s *Store) Authenticate(token string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.tokens[token]
	if !ok {
		return "", ErrUnauthorized
	}
	return id, nil
}

// IssueSignKey generates (or rotates) the host's M11 request-signing key
// and returns it. Called once per enrollment when the agent advertises
// signing support; the agent keeps the key in memory only.
func (s *Store) IssueSignKey(hostID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[hostID]
	if !ok {
		return "", ErrNotFound
	}
	var b [32]byte
	rand.Read(b[:])
	h.SignKey = hex.EncodeToString(b[:])
	return h.SignKey, nil
}

// HostSignKey returns the host's signing key, or false when the host was
// enrolled without signing support (legacy token-only host).
func (s *Store) HostSignKey(hostID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[hostID]
	if !ok || h.SignKey == "" {
		return "", false
	}
	return h.SignKey, true
}

// Checkin records a heartbeat and returns pending tasks for the host.
func (s *Store) Checkin(hostID string) ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[hostID]
	if !ok {
		return nil, ErrNotFound
	}
	h.LastCheckin = time.Now()
	var out []*Task
	for _, tid := range s.pending[hostID] {
		if t, ok := s.tasks[tid]; ok && t.Status == "pending" {
			t.Status = "running"
			t.UpdatedAt = time.Now()
			out = append(out, t)
		}
	}
	s.pending[hostID] = nil
	return out, nil
}

// EnqueueTask adds a shell task for a host. Used by the orchestrator (M2) and
// the M1 debug endpoint.
func (s *Store) EnqueueTask(hostID, command string, timeoutSec int) (*Task, error) {
	return s.EnqueueTypedTask(hostID, "shell", command, timeoutSec)
}

// EnqueueTypedTask adds a task of the given type (M4: locator/raid/firmware/...).
func (s *Store) EnqueueTypedTask(hostID, typ, command string, timeoutSec int) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.hosts[hostID]; !ok {
		return nil, ErrNotFound
	}
	if timeoutSec <= 0 {
		timeoutSec = 120
	}
	s.seq++
	t := &Task{
		ID:         newID("task", s.seq),
		HostID:     hostID,
		Type:       typ,
		Command:    command,
		TimeoutSec: timeoutSec,
		Status:     "pending",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	s.tasks[t.ID] = t
	s.pending[hostID] = append(s.pending[hostID], t.ID)
	return t, nil
}

// ReportTask records an agent's execution result.
func (s *Store) ReportTask(taskID string, r TaskReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return ErrNotFound
	}
	t.Status = r.Status
	t.Stdout = r.Stdout
	t.Stderr = r.Stderr
	t.ExitCode = r.ExitCode
	t.Error = r.Error
	t.UpdatedAt = time.Now()
	return nil
}

func (s *Store) GetTask(taskID string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *Store) Hosts() []*Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		cp := *h
		out = append(out, &cp)
	}
	return out
}

func (s *Store) Tasks() []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		cp := *t
		out = append(out, &cp)
	}
	return out
}

// GetHostByMAC finds a host by MAC (M3: bootscript install mode).
func (s *Store) GetHostByMAC(mac string) (*Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		for _, m := range h.MACs {
			if strings.EqualFold(m, mac) {
				cp := *h
				return &cp, nil
			}
		}
	}
	return nil, ErrNotFound
}

// InstallPending is always false for the M1 in-memory store (M3 uses *db.DB).
func (s *Store) InstallPending(hostID string) bool { return false }
