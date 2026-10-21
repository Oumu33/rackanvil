// pxealign.go — parsers and action plans aligned with the vendor's
// initrd-side pxe-agent, whose command templates and output handling
// were recovered verbatim in M10 (see
// deplos-reversing/m10-evidence/pxe-agent/PXE-AGENT-DATA.md):
//
//   - inventory comes from `lsblk -J` (JSON block device tree);
//   - physical drives behind MegaRAID come from storcli text tables
//     (`/c<id> /eall/sall show all` — the "show all" form);
//   - physical drives behind SAS HBAs come from
//     `sas3ircu <id> DISPLAY` sections;
//   - whole-disk erase is a fixed command sequence (dd head 100 MB,
//     dd tail, wipefs, swapoff, vgchange -an, mdadm --stop --scan,
//     partprobe), gated only by caller confirmation.
//
// The vendor health model is a tri-state string (Passed/Failed/
// Unknown) with no numeric SMART thresholds on this path; the
// scoring model lives in scoring.go / scoring_trend.go.

package collectors

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ---------- lsblk -J ----------

// BlockDevice mirrors the fields the vendor consumes from
// `lsblk -J` output: partitions appear as Children.
type BlockDevice struct {
	Name       string        `json:"name"`
	Type       string        `json:"type"` // disk, part, lvm, raid…
	SizeBytes  int64         `json:"size_bytes,omitempty"`
	Model      string        `json:"model,omitempty"`
	Serial     string        `json:"serial,omitempty"`
	Mountpoint string        `json:"mountpoint,omitempty"`
	Children   []BlockDevice `json:"children,omitempty"`
}

type lsblkDev struct {
	Name       string          `json:"name"`
	Type       string          `json:"type"`
	Size       json.RawMessage `json:"size"`
	Model      *string         `json:"model"`
	Serial     *string         `json:"serial"`
	Mountpoint *string         `json:"mountpoint"`
	Children   []lsblkDev      `json:"children"`
}

type lsblkOut struct {
	Blockdevices []lsblkDev `json:"blockdevices"`
}

func lsblkSize(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n // numeric when lsblk runs with -b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// Human sizes like "7.3T" — keep a best-effort byte value.
		return parseHumanSize(s)
	}
	return 0
}

func parseHumanSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := map[byte]int64{'K': 1 << 10, 'M': 1 << 20, 'G': 1 << 30, 'T': 1 << 40, 'P': 1 << 50}
	last := s[len(s)-1]
	m := int64(1)
	if v, ok := mult[last]; ok {
		m = v
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(m))
}

func convertLsblk(devs []lsblkDev) []BlockDevice {
	out := make([]BlockDevice, 0, len(devs))
	for _, d := range devs {
		bd := BlockDevice{Name: d.Name, Type: d.Type, SizeBytes: lsblkSize(d.Size)}
		if d.Model != nil {
			bd.Model = strings.TrimSpace(*d.Model)
		}
		if d.Serial != nil {
			bd.Serial = strings.TrimSpace(*d.Serial)
		}
		if d.Mountpoint != nil {
			bd.Mountpoint = *d.Mountpoint
		}
		if len(d.Children) > 0 {
			bd.Children = convertLsblk(d.Children)
		}
		out = append(out, bd)
	}
	return out
}

// ParseLsblkJSON parses `lsblk -J` output into the block device tree.
func ParseLsblkJSON(data []byte) ([]BlockDevice, error) {
	var raw lsblkOut
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("lsblk json: %w", err)
	}
	return convertLsblk(raw.Blockdevices), nil
}

// ---------- storcli text drive table ----------

// PhysDrive is one row of the storcli physical-drive table.
type PhysDrive struct {
	EnclosureSlot string `json:"enclosure_slot"` // "252:0"
	DeviceID      int    `json:"device_id"`      // DID
	State         string `json:"state"`          // Onln, Offln, UBad, JBOD…
	Size          string `json:"size"`           // as printed, e.g. "7.276 TB"
	Interface     string `json:"interface"`      // SATA, SAS, NVMe
	Medium        string `json:"medium"`         // HDD, SSD
	Model         string `json:"model"`
}

var storcliRowRe = regexp.MustCompile(`^(\d+):(\d+)\s+(\d+)\s+(\S+)\s+\S+\s+([\d.]+\s*[KMGT]B)\s+(\S+)\s+(\S+)\s+\S+\s+\S+\s+\S+\s+(\S+)`)

// ParseStorcliDrives extracts physical drives from storcli text
// output (`/cx/eall/sall show` table form). Lines that are not drive
// rows (headers, separators, VD tables) are skipped.
func ParseStorcliDrives(text string) []PhysDrive {
	var out []PhysDrive
	for _, line := range strings.Split(text, "\n") {
		m := storcliRowRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		did, _ := strconv.Atoi(m[3])
		out = append(out, PhysDrive{
			EnclosureSlot: m[1] + ":" + m[2],
			DeviceID:      did,
			State:         m[4],
			Size:          strings.Join(strings.Fields(m[5]), " "),
			Interface:     m[6],
			Medium:        m[7],
			Model:         m[8],
		})
	}
	return out
}

// ---------- sas3ircu DISPLAY ----------

// SASDrive is one device section of `sas3ircu <id> DISPLAY` output.
type SASDrive struct {
	Enclosure   string `json:"enclosure"`
	Slot        string `json:"slot"`
	State       string `json:"state"` // e.g. "Optimal (OPT)"
	SizeMB      int64  `json:"size_mb,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model       string `json:"model,omitempty"`
	Firmware    string `json:"firmware,omitempty"`
	Serial      string `json:"serial,omitempty"`
	Protocol    string `json:"protocol,omitempty"` // SAS, SATA
	DriveType   string `json:"drive_type,omitempty"`
}

// ParseSas3ircuDisplay extracts hard-disk sections from sas3ircu
// DISPLAY output. Enclosure devices and volume sections are skipped,
// matching the vendor's hard-disk-only inventory.
func ParseSas3ircuDisplay(text string) []SASDrive {
	var out []SASDrive
	var cur *SASDrive
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Device is a Hard disk") {
			flush()
			cur = &SASDrive{}
			continue
		}
		if strings.HasPrefix(t, "Device is a") {
			flush() // enclosure / volume / expander section: skip
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Enclosure #":
			cur.Enclosure = v
		case "Slot #":
			cur.Slot = v
		case "State":
			cur.State = v
		case "Size (in MB)/(in sectors)":
			mb := strings.SplitN(v, "/", 2)[0]
			n, _ := strconv.ParseInt(strings.TrimSpace(mb), 10, 64)
			cur.SizeMB = n
		case "Manufacturer":
			cur.Manufacturer = v
		case "Model Number":
			cur.Model = v
		case "Firmware Revision":
			cur.Firmware = v
		case "Serial No":
			cur.Serial = v
		case "Protocol":
			cur.Protocol = v
		case "Drive Type":
			cur.DriveType = v
		}
	}
	flush()
	return out
}

// ---------- whole-disk erase plan ----------

// WipeStep is one command of the vendor's whole-disk erase sequence.
type WipeStep struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// WipeSteps returns the vendor-verified erase sequence for a whole
// disk: zero the first 100 MB (204800×512 B sectors), zero the last
// 100 MB, wipefs, swapoff, deactivate volume groups, stop MD arrays,
// partprobe. totalSectors comes from `blockdev --getsz`; when it is
// unknown (<= 204800) the tail step is omitted rather than guessed.
func WipeSteps(device string, totalSectors int64) []WipeStep {
	const headSectors = 204800 // 100 MB at 512 B/sector
	steps := []WipeStep{
		{Name: "dd", Args: []string{"if=/dev/zero", "of=" + device, "bs=512", "count=204800"}},
	}
	if totalSectors > headSectors {
		steps = append(steps, WipeStep{Name: "dd", Args: []string{
			"if=/dev/zero", "of=" + device, "bs=512",
			"seek=" + strconv.FormatInt(totalSectors-headSectors, 10), "count=204800"}})
	}
	steps = append(steps,
		WipeStep{Name: "wipefs", Args: []string{"-a", device}},
		WipeStep{Name: "swapoff", Args: []string{device}},
		WipeStep{Name: "vgchange", Args: []string{"-an"}},
		WipeStep{Name: "mdadm", Args: []string{"--stop", "--scan"}},
		WipeStep{Name: "partprobe", Args: []string{device}},
	)
	return steps
}
