// Package collectors implements the node metrics collector and the storage
// failure predictor.
//
// All logic is written from scratch. The prediction policy below is our own
// engineering policy, informed by publicly published research (Backblaze
// drive-stats reports on SMART 5/187/188/197/198 correlation with failure,
// Google's "Failure Trends in a Large Disk Drive Population"). We do not
// replicate any vendor's proprietary model — we never recovered one.
package collectors

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---- snapshot ----

// DiskInfo describes one physical disk.
type DiskInfo struct {
	Name     string  `json:"name"`
	Model    string  `json:"model"`
	SizeGB   float64 `json:"size_gb"`
	UsedPct  float64 `json:"used_pct"`
	Rotating bool    `json:"rotational"`
}

// Snapshot is one full collector pass.
type Snapshot struct {
	CPUPct     float64    `json:"cpu_pct"`
	MemUsedMB  int64      `json:"mem_used_mb"`
	MemTotalMB int64      `json:"mem_total_mb"`
	Disks      []DiskInfo `json:"disks"`
	// Smart maps device name ("sda") -> attribute id ("5") -> raw value.
	Smart map[string]map[string]int64 `json:"smart"`
	// SmartHealth maps device name -> SMART overall-health self-assessment
	// result (smartctl "smart_status.passed" or the text-mode health line).
	// Absent key = health unknown (e.g. NVMe, or smartctl without the field).
	SmartHealth map[string]bool `json:"smart_health,omitempty"`
	// Nvme maps device name ("nvme0") -> key ("percentage_used") -> value.
	Nvme map[string]map[string]int64 `json:"nvme"`
	// Deep carries beyond-SMART signals (EDAC/AER/MCE), M4 addition.
	// Pointer + omitempty: old agents simply omit it.
	Deep *DeepHealth `json:"deep,omitempty"`
	// Skipped records data sources that were unavailable (honest signal).
	Skipped []string `json:"skipped"`
}

// Collect gathers a snapshot from /proc, /sys and (optionally) smartctl.
//
// M11: the two external-probe collectors (SMART per disk, NVMe per
// controller) run concurrently — each spawns subprocesses with
// multi-second timeouts, so serial collection made the metrics period
// scale with device count. They write disjoint Snapshot fields and
// return their skip notes, which Collect appends in a fixed order.
func Collect() *Snapshot {
	s := &Snapshot{
		Smart:       map[string]map[string]int64{},
		SmartHealth: map[string]bool{},
		Nvme:        map[string]map[string]int64{},
	}
	s.CPUPct = cpuPct()
	s.MemUsedMB, s.MemTotalMB = memInfo()
	s.Disks = disks(s)
	var wg sync.WaitGroup
	var smartSkip, nvmeSkip []string
	wg.Add(2)
	go func() { defer wg.Done(); smartSkip = collectSmart(s) }()
	go func() { defer wg.Done(); nvmeSkip = collectNvme(s) }()
	wg.Wait()
	s.Skipped = append(s.Skipped, smartSkip...)
	s.Skipped = append(s.Skipped, nvmeSkip...)
	return s
}

// maxParallelProbes bounds concurrent smartctl/nvme-cli subprocesses
// during collection: enough to hide per-device latency, few enough to
// never stampede a storage stack with parallel health queries.
const maxParallelProbes = 8

// smartRawValue handles the real-world smartctl JSON quirk where
// "raw": {"value": ...} may be a JSON number, a JSON string, or absent.
func smartRawValue(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			return v, true
		}
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(v), true
		}
	}
	return 0, false
}

// ParseSmartctlJSON parses `smartctl --json -a` output.
//
// Returns attribute id -> raw value, plus the overall-health self-assessment
// (smart_status.passed). When the passed field is absent (some devices / old
// smartctl), healthPassed is true — the attribute-based analysis still runs,
// and callers must not treat "absent" as "failed".
func ParseSmartctlJSON(data []byte) (attrs map[string]int64, healthPassed bool, err error) {
	attrs = map[string]int64{}
	healthPassed = true
	var parsed struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Attributes *struct {
			Table []struct {
				ID  int             `json:"id"`
				Raw json.RawMessage `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, false, fmt.Errorf("parse smartctl json: %w", err)
	}
	if parsed.SmartStatus != nil {
		healthPassed = parsed.SmartStatus.Passed
	}
	if parsed.Attributes == nil {
		return attrs, healthPassed, nil
	}
	for _, a := range parsed.Attributes.Table {
		// Real smartctl nests value under raw.value; tolerate a flat raw too.
		var nested struct {
			Value json.RawMessage `json:"value"`
		}
		var raw json.RawMessage
		if err := json.Unmarshal(a.Raw, &nested); err == nil && len(nested.Value) > 0 {
			raw = nested.Value
		} else {
			raw = a.Raw
		}
		if v, ok := smartRawValue(raw); ok {
			attrs[strconv.Itoa(a.ID)] = v
		}
	}
	return attrs, healthPassed, nil
}

// ParseSmartctlText parses text-mode `smartctl -a` output: the INFO section
// (model/serial), the overall-health line, and the attribute table.
//
// RAW_VALUE may carry parenthesised vendor suffixes ("42 (Min/Max 36/52)",
// "21892 (3 79 0)"); only the leading number is taken. Returns healthPassed
// false only when the health line explicitly says FAILED; when the line is
// absent, health is unknown and healthPassed is true (see ParseSmartctlJSON).
func ParseSmartctlText(data []byte) (attrs map[string]int64, healthPassed bool, model, serial string, err error) {
	attrs = map[string]int64{}
	healthPassed = true
	inTable := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if v, ok := cutSmartPrefix(t, "Device Model:"); ok {
			model = strings.TrimSpace(v)
		} else if v, ok := cutSmartPrefix(t, "Serial Number:"); ok {
			serial = strings.TrimSpace(v)
		} else if strings.HasPrefix(t, "SMART overall-health self-assessment test result:") {
			rest := strings.ToUpper(t)
			healthPassed = !strings.Contains(rest, "FAILED")
		} else if strings.HasPrefix(t, "ID# ATTRIBUTE_NAME") {
			inTable = true
			continue
		} else if v, ok := cutSmartPrefix(t, "Elements in grown defect list:"); ok {
			// SAS/SCSI health page (smartctl text mode on SAS disks).
			if n, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil {
				attrs["grown_defect_list"] = n
			}
		} else if strings.HasPrefix(t, "read:") || strings.HasPrefix(t, "write:") || strings.HasPrefix(t, "verify:") {
			// SAS Error counter log rows: the last column is the total
			// uncorrected error count for that operation class.
			f := strings.Fields(t)
			if len(f) >= 8 {
				if n, perr := strconv.ParseInt(f[len(f)-1], 10, 64); perr == nil {
					attrs["sas_uncorrected_errors"] += n
				}
			}
		} else if inTable {
			if t == "" {
				inTable = false
				continue
			}
			f := strings.Fields(t)
			// ID# NAME FLAG VALUE WORST THRESH TYPE UPDATED WHEN_FAILED RAW_VALUE...
			if len(f) < 10 {
				continue
			}
			id, ferr := strconv.Atoi(f[0])
			if ferr != nil {
				continue
			}
			raw, rerr := strconv.ParseInt(f[9], 10, 64)
			if rerr != nil {
				continue
			}
			attrs[strconv.Itoa(id)] = raw
		}
	}
	return attrs, healthPassed, model, serial, nil
}

func cutSmartPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return strings.TrimSpace(s[len(prefix):]), true
	}
	return "", false
}

// nvmeTextKey maps nvme-cli text-mode labels to our canonical keys.
var nvmeTextKey = map[string]string{
	"critical_warning":                    "critical_warning",
	"temperature":                         "temperature",
	"available_spare":                     "avail_spare",
	"available_spare_threshold":           "spare_thresh",
	"percentage_used":                     "percentage_used",
	"data_units_read":                     "data_units_read",
	"data_units_written":                  "data_units_written",
	"host_read_commands":                  "host_read_commands",
	"host_write_commands":                 "host_write_commands",
	"controller_busy_time":                "controller_busy_time",
	"power_cycles":                        "power_cycles",
	"power_on_hours":                      "power_on_hours",
	"unsafe_shutdowns":                    "unsafe_shutdowns",
	"media_errors":                        "media_errors",
	"num_err_log_entries":                 "num_err_log_entries",
	"warning_temperature_time":            "warning_temp_time",
	"critical_composite_temperature_time": "critical_comp_time",
}

// ParseNvmeText parses nvme-cli `nvme smart-log` text output.
//
// Real-world variants handled:
//   - "temperature : 29 C"              (Hetzner docs style)
//   - "temperature : 43°C (316 Kelvin)" (nvme-cli default style)
//   - "percentage_used : 35%" / "percentage_used : 35"
//   - thousand separators: "data_units_read : 113,446"
func ParseNvmeText(data []byte) map[string]int64 {
	out := map[string]int64{}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(parts[0]))
		label = strings.ReplaceAll(label, " ", "_")
		key, ok := nvmeTextKey[label]
		if !ok {
			continue
		}
		val := strings.TrimSpace(parts[1])
		if key == "temperature" {
			// Take the °C number: "43°C (316 Kelvin)" -> 43; "29 C" -> 29.
			// Stored as Celsius with a sentinel offset? No — store Kelvin
			// like the other sources do, so NvmeTempCelsius stays uniform.
			num := leadingNumber(val)
			if strings.Contains(val, "Kelvin") && !strings.Contains(val, "°") && !strings.Contains(strings.ToLower(val), " c") {
				// bare Kelvin number, e.g. "316 Kelvin"
				if v, err := strconv.ParseInt(num, 10, 64); err == nil {
					out[key] = v
				}
				continue
			}
			if v, err := strconv.ParseInt(num, 10, 64); err == nil {
				out[key] = v + 273 // °C -> K, uniform with smartctl/nvme-cli JSON
			}
			continue
		}
		num := strings.ReplaceAll(leadingNumber(val), ",", "")
		num = strings.TrimSuffix(num, "%")
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			out[key] = v
		}
	}
	return out
}

// leadingNumber returns the leading run of digits (and commas) in s.
func leadingNumber(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || r == ',' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			break
		}
	}
	return b.String()
}

// ParseNvmeJSON parses nvme-cli `nvme smart-log -o json` output, whose key
// names differ from smartctl's (percent_used vs percentage_used, etc.).
// Temperature is Kelvin, matching smartctl.
func ParseNvmeJSON(data []byte) (map[string]int64, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse nvme json: %w", err)
	}
	renames := map[string]string{
		"percent_used": "percentage_used",
		"avail_spare":  "avail_spare",
		"spare_thresh": "spare_thresh",
	}
	out := map[string]int64{}
	for k, v := range raw {
		if nk, ok := renames[k]; ok {
			k = nk
		}
		if n, ok := smartRawValue(v); ok {
			out[k] = n
		}
	}
	return out, nil
}

// NvmeTempCelsius converts the NVMe health-log temperature to Celsius.
//
// All our temperature sources store Kelvin (smartctl JSON, nvme-cli JSON,
// ParseNvmeText normalises °C to K). A missing, zero, or physically
// implausible reading returns ok=false: the caller must report "unknown"
// and MUST NOT display a fabricated number. This is the guard against the
// vendor-demo bug class where a missing sensor rendered as a four-digit
// "temperature" (e.g. 1739°C) instead of "unknown".
func NvmeTempCelsius(log map[string]int64) (celsius float64, ok bool) {
	k, present := log["temperature"]
	if !present || k == 0 {
		return 0, false
	}
	// Plausible NVMe operating range: -43°C .. +127°C  (230K .. 400K).
	if k < 230 || k > 400 {
		return 0, false
	}
	return float64(k) - 273.15, true
}

// ---- CPU ----

func readCPUStat() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0, false
	}
	fields := strings.Fields(sc.Text())[1:]
	if len(fields) < 8 {
		return 0, 0, false
	}
	vals := make([]uint64, len(fields))
	for i, fld := range fields {
		v, err := strconv.ParseUint(fld, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		vals[i] = v
		total += v
	}
	idle = vals[3] + vals[4] // idle + iowait
	return idle, total, true
}

func cpuPct() float64 {
	i1, t1, ok1 := readCPUStat()
	if !ok1 {
		return 0
	}
	time.Sleep(500 * time.Millisecond)
	i2, t2, ok2 := readCPUStat()
	if !ok2 || t2 <= t1 {
		return 0
	}
	idleD := float64(i2 - i1)
	totalD := float64(t2 - t1)
	pct := (1 - idleD/totalD) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// ---- memory ----

func memInfo() (usedMB, totalMB int64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var total, avail int64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		flds := strings.Fields(sc.Text())
		if len(flds) < 2 {
			continue
		}
		v, err := strconv.ParseInt(flds[1], 10, 64)
		if err != nil {
			continue
		}
		switch flds[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	if total == 0 {
		return 0, 0
	}
	return (total - avail) / 1024, total / 1024
}

// ---- disks ----

func isPhysicalDisk(name string) bool {
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") {
		return false
	}
	return strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "hd") ||
		strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "nvme") ||
		strings.HasPrefix(name, "mmcblk")
}

func disks(s *Snapshot) []DiskInfo {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		s.Skipped = append(s.Skipped, "disk: /sys/block unreadable")
		return nil
	}
	// Map device -> mountpoint usage via /proc/mounts.
	mountUsage := map[string]float64{}
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			flds := strings.Fields(line)
			if len(flds) < 2 {
				continue
			}
			dev, mp := flds[0], flds[1]
			var st syscall.Statfs_t
			if err := syscall.Statfs(mp, &st); err != nil {
				continue
			}
			if st.Blocks > 0 {
				used := float64(st.Blocks-st.Bfree) / float64(st.Blocks) * 100
				base := filepath.Base(dev)
				mountUsage[base] = used
			}
		}
	}
	var out []DiskInfo
	for _, e := range entries {
		name := e.Name()
		if !isPhysicalDisk(name) {
			continue
		}
		var sizeGB float64
		if data, err := os.ReadFile(filepath.Join("/sys/block", name, "size")); err == nil {
			if sectors, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
				sizeGB = float64(sectors*512) / 1e9
			}
		}
		model := ""
		if data, err := os.ReadFile(filepath.Join("/sys/block", name, "device", "model")); err == nil {
			model = strings.TrimSpace(string(data))
		}
		rot := false
		if data, err := os.ReadFile(filepath.Join("/sys/block", name, "queue", "rotational")); err == nil {
			rot = strings.TrimSpace(string(data)) == "1"
		}
		usedPct := 0.0
		// Match partitions (sda1 -> sda) too.
		for dev, u := range mountUsage {
			if dev == name || strings.HasPrefix(dev, name) {
				if u > usedPct {
					usedPct = u
				}
			}
		}
		out = append(out, DiskInfo{Name: name, Model: model, SizeGB: sizeGB, UsedPct: usedPct, Rotating: rot})
	}
	return out
}

// ---- SMART (ATA) ----

type smartctlJSON struct {
	SmartStatus struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	ATASmartAttributes struct {
		Table []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Raw  struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NvmeSmartLog struct {
		PercentUsed  int64 `json:"percent_used"`
		MediaErrors  int64 `json:"media_errors"`
		CriticalWarn int64 `json:"critical_warning"`
		Temperature  int64 `json:"temperature"`
	} `json:"nvme_smart_health_information_log"`
}

// MegaRaidSmartArgs builds the smartctl argument list for a disk behind a
// MegaRAID controller (M7): the vendor server's own format string is
// "smartctl -a -d megaraid,%s %s" — MegaRAID-passthrough is the factory
// way to read SMART through the RAID card. We insert --json to reuse our
// JSON parser (a deviation from the vendor's text "-a", documented in
// docs/M7-REPORT.md). diskIndex is the physical drive number (0..63).
func MegaRaidSmartArgs(diskIndex int, device string) []string {
	return []string{"--json", "-a", "-d", "megaraid," + strconv.Itoa(diskIndex), device}
}

// hasMegaRAIDController reports whether lspci output shows a MegaRAID
// storage controller (PCI IDs 1000:005d / 1000:0094, the same IDs the
// firmware package keys on).
func hasMegaRAIDController(lspci string) bool {
	l := strings.ToLower(lspci)
	return strings.Contains(l, "[1000:005d]") || strings.Contains(l, "[1000:0094]")
}

// probeSmartDisk reads one disk's SMART data. ok=false means no data
// (command or parse failure); ok=true carries whatever was parsed,
// including an attribute-less health result.
func probeSmartDisk(name string, megaraid bool) (attrs map[string]int64, healthPassed bool, ok bool) {
	args := []string{"--json", "-a", "/dev/" + name}
	out, err := exec.Command("smartctl", args...).Output()
	if err != nil && megaraid && name == "sda" {
		// Direct read failed on a MegaRAID-backed boot disk: probe the
		// vendor's passthrough form for physical drives 0..15.
		for idx := 0; idx < 16; idx++ {
			if o2, e2 := exec.Command("smartctl", MegaRaidSmartArgs(idx, "/dev/sda")...).Output(); e2 == nil {
				out, err = o2, nil
				break
			}
		}
	}
	if err != nil {
		return nil, false, false
	}
	attrs, healthPassed, err = ParseSmartctlJSON(out)
	if err != nil {
		return nil, false, false
	}
	return attrs, healthPassed, true
}

// collectSmart probes every non-NVMe disk concurrently (bounded) and
// merges results in disk order. Returns skip notes for Collect.
func collectSmart(s *Snapshot) []string {
	if _, err := exec.LookPath("smartctl"); err != nil {
		return []string{"smart: smartctl not installed"}
	}
	megaraid := false
	if out, err := exec.Command("lspci", "-nn").Output(); err == nil {
		megaraid = hasMegaRAIDController(string(out))
	}
	type result struct {
		attrs  map[string]int64
		health bool
		ok     bool
	}
	results := make([]result, len(s.Disks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallelProbes)
	for i, d := range s.Disks {
		if strings.HasPrefix(d.Name, "nvme") {
			continue // handled by collectNvme
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a, h, ok := probeSmartDisk(name, megaraid)
			results[i] = result{a, h, ok}
		}(i, d.Name)
	}
	wg.Wait()
	for i, d := range s.Disks {
		r := results[i]
		if !r.ok {
			continue
		}
		if len(r.attrs) > 0 {
			s.Smart[d.Name] = r.attrs
		}
		s.SmartHealth[d.Name] = r.health
	}
	if len(s.Smart) == 0 {
		return []string{"smart: no ATA attributes readable"}
	}
	return nil
}

// ---- NVMe ----

// probeNvme reads one NVMe controller's health log: smartctl first,
// nvme-cli as fallback. Returns the canonical-keyed log, or nil when
// neither tool could read the device.
func probeNvme(dev string, hasSmartctl, hasNvmeCli bool) map[string]int64 {
	ns := dev + "n1"
	log := map[string]int64{}
	got := false
	if hasSmartctl {
		if out, err := exec.Command("smartctl", "--json", "-a", "/dev/"+ns).Output(); err == nil {
			var sj struct {
				NvmeSmartLog map[string]json.RawMessage `json:"nvme_smart_health_information_log"`
			}
			if json.Unmarshal(out, &sj) == nil && sj.NvmeSmartLog != nil {
				for k, v := range sj.NvmeSmartLog {
					if n, ok := smartRawValue(v); ok {
						log[k] = n
					}
				}
				got = len(log) > 0
			}
		}
	}
	if !got && hasNvmeCli {
		// Fallback: nvme-cli JSON (different key names).
		if out, err := exec.Command("nvme", "smart-log", "/dev/"+dev, "-o", "json").Output(); err == nil {
			if m, err := ParseNvmeJSON(out); err == nil && len(m) > 0 {
				log = m
				got = true
			}
		}
	}
	if !got {
		return nil
	}
	out := map[string]int64{}
	for _, k := range []string{"percentage_used", "percent_used", "media_errors", "critical_warning", "temperature", "avail_spare", "available_spare", "power_on_hours"} {
		if v, ok := log[k]; ok {
			out[canonicalNvmeKey(k)] = v
		}
	}
	return out
}

// collectNvme probes every NVMe controller concurrently (bounded) and
// merges results in device order. Returns skip notes for Collect.
func collectNvme(s *Snapshot) []string {
	entries, err := os.ReadDir("/sys/class/nvme")
	if err != nil || len(entries) == 0 {
		return nil // no NVMe devices; not an error
	}
	_, smartErr := exec.LookPath("smartctl")
	_, nvmeErr := exec.LookPath("nvme")
	results := make([]map[string]int64, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallelProbes)
	for i, e := range entries {
		wg.Add(1)
		go func(i int, dev string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = probeNvme(dev, smartErr == nil, nvmeErr == nil)
		}(i, e.Name())
	}
	wg.Wait()
	for i, e := range entries {
		if results[i] != nil {
			s.Nvme[e.Name()] = results[i]
		}
	}
	if len(s.Nvme) == 0 {
		return []string{"nvme: devices present but health unreadable (need smartctl or nvme-cli)"}
	}
	return nil
}

func canonicalNvmeKey(k string) string {
	switch k {
	case "percent_used":
		return "percentage_used"
	case "available_spare":
		return "avail_spare"
	}
	return k
}

// ---- failure prediction ----

// Finding is one prediction verdict for a device.
type Finding struct {
	Device   string
	Severity string // ok | warning | critical
	Message  string
}

// Key SMART attributes per published research (Backblaze drive stats,
// Google disk failure study): these correlate with near-term failure.
var watchedAttrs = []string{"5", "187", "188", "197", "198"}

// AnalyzeSmart evaluates one ATA device's attributes plus its recent history.
// AnalyzeSmart applies Backblaze/Google-derived thresholds plus the
// overall-health self-assessment. healthPassed comes from
// smartctl smart_status.passed (JSON) or the text-mode health line; when the
// source does not report it, callers pass true (unknown ≠ failed).
// history is newest-first, as returned by db.SmartHistory.
func AnalyzeSmart(device string, attrs map[string]int64, history []map[string]int64, healthPassed bool) []Finding {
	var out []Finding
	add := func(sev, msg string) {
		out = append(out, Finding{Device: device, Severity: sev, Message: msg})
	}
	if !healthPassed {
		add("critical", "SMART overall-health self-assessment: FAILED — replace drive immediately")
	}
	get := func(id string) int64 { return attrs[id] }

	// Threshold rules (our policy; documented, tunable).
	if get("187") > 0 {
		add("critical", fmt.Sprintf("SMART 187 Reported_Uncorrectable_Errors=%d: uncorrectable sectors seen", get("187")))
	}
	if p := get("197"); p >= 10 {
		add("critical", fmt.Sprintf("SMART 197 Current_Pending_Sector=%d: many sectors awaiting remap", p))
	} else if p > 0 {
		add("warning", fmt.Sprintf("SMART 197 Current_Pending_Sector=%d: unstable sectors present", p))
	}
	if get("198") > 0 {
		add("warning", fmt.Sprintf("SMART 198 Offline_Uncorrectable=%d: offline uncorrectable sectors", get("198")))
	}
	if r := get("5"); r >= 50 {
		add("critical", fmt.Sprintf("SMART 5 Reallocated_Sector_Ct=%d: heavy remapping", r))
	} else if r > 0 {
		add("warning", fmt.Sprintf("SMART 5 Reallocated_Sector_Ct=%d: remapped sectors present", r))
	}
	if get("188") > 0 {
		add("warning", fmt.Sprintf("SMART 188 Command_Timeout=%d: command timeouts seen", get("188")))
	}

	// Trend rule: watched attributes strictly increasing over the last two
	// intervals => accelerating degradation.
	if len(history) >= 2 {
		for _, id := range watchedAttrs {
			cur := attrs[id]
			h1 := history[0][id]
			h2 := history[1][id]
			if cur > h1 && h1 > h2 {
				add("warning", fmt.Sprintf("SMART %s accelerating: %d -> %d -> %d (degrading fast)", id, h2, h1, cur))
			}
		}
	}
	if len(out) == 0 {
		out = append(out, Finding{Device: device, Severity: "ok", Message: "SMART attributes within policy"})
	}
	return out
}

// AnalyzeNvme evaluates one NVMe device's health log.
func AnalyzeNvme(device string, log map[string]int64) []Finding {
	var out []Finding
	add := func(sev, msg string) {
		out = append(out, Finding{Device: device, Severity: sev, Message: msg})
	}
	if log["critical_warning"] != 0 {
		add("critical", fmt.Sprintf("NVMe critical_warning=0x%x", log["critical_warning"]))
	}
	// Severity thresholds aligned to the vendor scoring rulebook (M7,
	// disk_dual_track v3.1): media_errors scores 90/60/30/0 at
	// >=1/10/100/1000; percentage_used scores 80/60/20/0 at >=80/90/98/100.
	switch me := log["media_errors"]; {
	case me >= 100:
		add("critical", fmt.Sprintf("NVMe media_errors=%d", me))
	case me >= 1:
		add("warning", fmt.Sprintf("NVMe media_errors=%d", me))
	}
	switch pu := log["percentage_used"]; {
	case pu >= 98:
		add("critical", fmt.Sprintf("NVMe percentage_used=%d%%: end of life near", pu))
	case pu >= 80:
		add("warning", fmt.Sprintf("NVMe percentage_used=%d%%: wearing out", pu))
	}
	if len(out) == 0 {
		out = append(out, Finding{Device: device, Severity: "ok", Message: "NVMe health log clean"})
	}
	return out
}

// WorstSeverity rolls findings up to a single verdict.
func WorstSeverity(findings []Finding) string {
	worst := "ok"
	for _, f := range findings {
		switch f.Severity {
		case "critical":
			return "critical"
		case "warning":
			worst = "warning"
		}
	}
	return worst
}
