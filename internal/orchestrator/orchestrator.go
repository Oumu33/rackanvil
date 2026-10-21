// Package orchestrator implements delivery orchestration jobs.
//
// A job is an ordered list of steps (BMC config -> RAID build -> stress ->
// install). Steps run sequentially; hardware steps (BMC/RAID/firmware) have
// no real hardware in most environments, so they execute as explicit mocks —
// the mock is labeled as such in the step detail, never disguised as real
// work. The stress step dispatches a real shell task through the agent task
// queue; the install step arms the host for preseed install and waits for
// the install-done callback from the installer.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Oumu33/rackanvil/internal/db"
	"github.com/Oumu33/rackanvil/internal/store"
)

// Step kinds.
const (
	StepBMCMock  = "bmc_mock"
	StepRAIDMock = "raid_mock"
	StepStress   = "stress"
	StepInstall  = "install"
)

// DefaultPlan is the standard delivery pipeline.
var DefaultPlan = []store.StepDef{
	{Name: "BMC 强制配置", Kind: StepBMCMock},
	{Name: "RAID 组建", Kind: StepRAIDMock},
	{Name: "压力测试", Kind: StepStress},
	{Name: "系统安装", Kind: StepInstall},
}

// Runner executes jobs in the background.
type Runner struct {
	db     *db.DB
	log    *log.Logger
	poll   time.Duration
	stress time.Duration // how long the stress step burns
}

// New creates a runner over the database.
func New(database *db.DB, logger *log.Logger) *Runner {
	if logger == nil {
		logger = log.Default()
	}
	return &Runner{db: database, log: logger, poll: 5 * time.Second, stress: 30 * time.Second}
}

// CreateJob validates the host and inserts a pending job.
func (r *Runner) CreateJob(hostID, name string, steps []store.StepDef) (*store.Job, error) {
	if len(steps) == 0 {
		steps = DefaultPlan
	}
	return r.db.CreateJob(name, hostID, steps)
}

// Run processes pending jobs until ctx ends. One job at a time.
func (r *Runner) Run(ctx context.Context) {
	r.log.Printf("orchestrator: runner started")
	t := time.NewTicker(r.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			job, steps, err := r.db.ClaimJob()
			if err != nil {
				continue // nothing pending
			}
			r.execute(ctx, job, steps)
		}
	}
}

func (r *Runner) execute(ctx context.Context, job *store.Job, steps []*store.JobStep) {
	r.log.Printf("orchestrator: job %s (%s) started, %d steps", job.ID, job.Name, len(steps))
	failed := false
	for _, s := range steps {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := r.db.SetStepStatus(s.ID, "running", ""); err != nil {
			r.log.Printf("orchestrator: step status: %v", err)
		}
		detail, err := r.runStep(ctx, job, s)
		if err != nil {
			_ = r.db.SetStepStatus(s.ID, "failed", err.Error())
			r.log.Printf("orchestrator: job %s step %q failed: %v", job.ID, s.Name, err)
			failed = true
			break
		}
		_ = r.db.SetStepStatus(s.ID, "done", detail)
		r.log.Printf("orchestrator: job %s step %q done", job.ID, s.Name)
	}
	status := "done"
	if failed {
		status = "failed"
	}
	_ = r.db.SetJobStatus(job.ID, status)
	r.log.Printf("orchestrator: job %s finished: %s", job.ID, status)
}

func (r *Runner) runStep(ctx context.Context, job *store.Job, s *store.JobStep) (string, error) {
	switch s.Kind {
	case StepBMCMock:
		// No BMC hardware here: explicit mock. The request templates are
		// the vendor's production shapes (M7, see bmc.go); execution here
		// remains mock — no BMC was contacted.
		time.Sleep(2 * time.Second)
		return "mock: BMC 配置已跳过（无带外硬件）; " + BMCTemplateSummary("generic"), nil
	case StepRAIDMock:
		time.Sleep(2 * time.Second)
		return "mock: RAID 组建已跳过（无阵列卡；真机环境需接 storcli/perccli）", nil
	case StepStress:
		return r.runStress(ctx, job)
	case StepInstall:
		return r.runInstall(ctx, job, s)
	default:
		return "", fmt.Errorf("unknown step kind %q", s.Kind)
	}
}

// runStress dispatches a real CPU burn through the agent task queue and
// waits for the agent's report.
func (r *Runner) runStress(ctx context.Context, job *store.Job) (string, error) {
	secs := int(r.stress.Seconds())
	// Portable sh (no bashisms): busy loop bounded by wall clock.
	cmd := fmt.Sprintf(`end=$(($(date +%%s)+%d)); while [ $(date +%%s) -lt $end ]; do :; done; echo stress-ok; nproc`, secs)
	t, err := r.db.EnqueueTask(job.HostID, cmd, secs+120)
	if err != nil {
		return "", fmt.Errorf("stress: enqueue: %w", err)
	}
	deadline := time.Now().Add(time.Duration(secs+180) * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
		got, err := r.db.GetTask(t.ID)
		if err != nil {
			return "", err
		}
		switch got.Status {
		case "done":
			return fmt.Sprintf("压测任务完成（exit=%d，%ds CPU burn）: %s", got.ExitCode, secs, firstLine(got.Stdout)), nil
		case "failed":
			return "", fmt.Errorf("stress task failed: %s %s", got.Error, got.Stderr)
		}
	}
	return "", fmt.Errorf("stress: timed out waiting for agent report")
}

// runInstall arms the host for preseed install and waits for the installer's
// install-done callback (which clears the flag).
func (r *Runner) runInstall(ctx context.Context, job *store.Job, s *store.JobStep) (string, error) {
	// Random root password for the new system; shown on the job page.
	var pw [12]byte
	rand.Read(pw[:])
	rootpw := hex.EncodeToString(pw[:])[:16]
	detail, _ := json.Marshal(map[string]string{"root_password": rootpw})
	_ = r.db.SetStepStatus(s.ID, "running", string(detail))

	// Hand the root password to the preseed renderer (per-host, in DB).
	if err := r.db.SetInstallContext(job.HostID, rootpw); err != nil {
		return "", fmt.Errorf("install: store install context: %w", err)
	}
	if err := r.db.SetInstallPending(job.HostID, true); err != nil {
		return "", fmt.Errorf("install: arm host: %w", err)
	}
	r.log.Printf("orchestrator: job %s host %s armed for preseed install (reboot into PXE to begin)", job.ID, job.HostID)
	// Wait up to 45 minutes for install-done (full Debian install in emulation
	// is slow). The install-done callback clears install_pending.
	deadline := time.Now().Add(45 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(15 * time.Second):
		}
		if !r.db.InstallPending(job.HostID) {
			return fmt.Sprintf("系统安装完成（root 密码见本步骤详情）"), nil
		}
	}
	_ = r.db.SetInstallPending(job.HostID, false)
	return "", fmt.Errorf("install: timed out waiting for install-done callback (host never PXE-installed?)")
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
}
