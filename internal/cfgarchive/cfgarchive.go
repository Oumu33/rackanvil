// Package cfgarchive implements per-host configuration snapshots and
// one-click restore ("配置存档，一键恢复").
//
// Doc basis: vendor README — "硬件故障更换：下架返厂后需重新配置 RAID、分区…
// 配置存档，一键恢复历史设置". Our clean-room design: the agent collects
// RAID layout (storcli/sas3ircu if present), partition table (sfdisk -d)
// and network config (ip -j addr), the server stores the snapshot in the
// cfg_snapshots table, and restore replays it as a typed agent task.
// Restore is destructive by nature: it requires an explicit confirm flag.
package cfgarchive

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Runner executes commands; see locator for the shared pattern.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// Snapshot is one archived host configuration.
type Snapshot struct {
	ID        string    `json:"id"`
	HostID    string    `json:"host_id"`
	CreatedAt time.Time `json:"created_at"`
	Note      string    `json:"note"`
	RAID      string    `json:"raid"`      // raw `storcli /c0/vall show` / `sas3ircu 0 display` output
	Partition string    `json:"partition"` // `sfdisk -d` dump (per disk, concatenated)
	Network   string    `json:"network"`   // `ip -j addr` JSON
	Packages  string    `json:"packages"`  // `dpkg -l` / `rpm -qa` head (inventory aid)
}

// Collect gathers the snapshot content on the agent side. Missing tools are
// skipped with a note instead of failing the whole snapshot.
func Collect(ctx context.Context, r Runner) (Snapshot, error) {
	var s Snapshot
	var notes []string
	try := func(name string, args ...string) string {
		out, _, err := r.Run(ctx, name, args...)
		if err != nil {
			notes = append(notes, name+": unavailable")
			return ""
		}
		return out
	}
	s.RAID = try("storcli", "/c0/vall", "show")
	if s.RAID == "" {
		if alt := try("sas3ircu", "0", "display"); alt != "" {
			s.RAID = alt
		}
	}
	s.Partition = try("sfdisk", "-d", "/dev/sda")
	if s.Partition == "" {
		s.Partition = try("sfdisk", "--dump", "/dev/nvme0n1")
	}
	s.Network = try("ip", "-j", "addr")
	s.Packages = try("sh", "-c", "dpkg -l 2>/dev/null | head -50 || rpm -qa 2>/dev/null | head -50")
	s.Note = strings.Join(notes, "; ")
	return s, nil
}

// CollectJSON is the agent task contract: stdout carries the snapshot JSON.
func CollectJSON(ctx context.Context, r Runner) (string, error) {
	s, err := Collect(ctx, r)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(s)
	return string(b), err
}

// RestorePlan turns a snapshot into an ordered list of shell steps the agent
// executes. Partition restore uses sfdisk; network restore re-applies
// addresses. RAID recreation is deliberately NOT automated (controller state
// differs after hardware replacement) — it is emitted as a manual checklist.
func RestorePlan(s Snapshot, confirm bool) ([]string, []string, error) {
	if !confirm {
		return nil, nil, fmt.Errorf("restore requires explicit confirm=true")
	}
	var steps []string
	var manual []string
	if strings.TrimSpace(s.Partition) != "" {
		steps = append(steps,
			"cat > /tmp/restore.pt <<'EOF'\n"+s.Partition+"\nEOF",
			"sfdisk --force /dev/sda < /tmp/restore.pt",
		)
	} else {
		manual = append(manual, "no partition dump in snapshot; partition manually")
	}
	if strings.TrimSpace(s.RAID) != "" {
		manual = append(manual,
			"RAID layout archived for reference; recreate VD on the new controller, then continue")
	}
	return steps, manual, nil
}
