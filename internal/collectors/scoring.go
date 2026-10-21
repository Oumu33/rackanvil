package collectors

// Vendor-aligned disk health scoring (M7, 2026-10-21).
//
// Source: the vendor server binary embeds its production scoring rulebook
// as JSON — module "disk_dual_track", version "3.1_ml_integrated",
// updated_at 2026-03-16 — recovered verbatim in
// ~/workspace/deplos-reversing/m7-vendor-disk-rules.json. The thresholds
// and per-condition scores below are that factory data, adopted as-is.
// Our message strings are paraphrases, not the vendor's text.
//
// The rulebook is a "dual track" design:
//   - scoring track: 6 graded dimensions (NVMe critical_warning /
//     percentage_used / media_errors, SAS grown_defects /
//     uncorrected_errors, composite temperature), each 0-100;
//   - instant track: 5 dmesg log-pattern rules that flag Critical
//     immediately (SATA fatal/transport, NVMe controller, EXT4 I/O,
//     generic buffer I/O) — see ScanDmesgCritical.
//
// M8 correction: the JSON rulebook is only the vendor's STATIC track
// (loaded by hwfault.LoadRuleConfig("disk.json") into EvaluateMetrics).
// The vendor code ALSO has full SATA attribute scoring tracks
// (applyHDDRules / applySSDRules) in its history/trend engine — see
// scoring_trend.go. Our ATA AnalyzeSmart keeps its Backblaze-derived
// policy for its own instant verdicts; the vendor-faithful score is
// ScoreDiskTrend.
//
// Aggregation (recovered in M8, decompiled hwfault): the vendor's
// per-disk score is the TREND score — 100 minus fixed deductions per
// rule hit (scoring_trend.go) — not a minimum over the static dimension
// grades below. ScoreDiskHealth remains the static-track grader; its
// minimum-over-dimensions overall is our own summary of that track,
// not the vendor's displayed number.

import (
	"fmt"
	"regexp"
)

// DimensionScore is one graded health dimension.
type DimensionScore struct {
	ID       string // vendor rule id
	Score    int    // 0..100
	Critical bool   // vendor is_critical flag
	Message  string
}

// HealthInput carries the raw values the vendor rules consume. A nil field
// means "not reported" and its dimension is skipped (never scored as 0).
type HealthInput struct {
	NVMeCriticalWarning *int64
	NVMePercentageUsed  *int64
	NVMeMediaErrors     *int64
	SASGrownDefects     *int64
	SASUncorrected      *int64
	TemperatureC        *int // composite temperature, already sanity-checked (see NvmeTempCelsius)
}

// cond is one vendor condition: the first matching condition (in listed
// order, highest threshold first) sets the score.
type cond struct {
	op    string // ">" or ">="
	value int64
	score int
	msg   string
}

// vendorRules mirrors m7-vendor-disk-rules.json "rules" exactly
// (thresholds + scores); messages are our paraphrases.
var vendorRules = []struct {
	id       string
	critical bool
	get      func(*HealthInput) *int64
	conds    []cond
}{
	{"critical_warning", true, func(i *HealthInput) *int64 { return i.NVMeCriticalWarning }, []cond{
		{">", 0, 0, "NVMe critical warning flags set (media/readonly/controller fault) — replace the drive now"},
	}},
	{"percentage_used", false, func(i *HealthInput) *int64 { return i.NVMePercentageUsed }, []cond{
		{">=", 100, 0, "NVMe endurance exhausted (100% used) — may drop to read-only at any time"},
		{">=", 98, 20, "NVMe endurance nearly exhausted — schedule replacement immediately"},
		{">=", 90, 60, "NVMe endurance in end-of-life range"},
		{">=", 80, 80, "NVMe endurance heavily consumed"},
	}},
	{"media_errors", true, func(i *HealthInput) *int64 { return i.NVMeMediaErrors }, []cond{
		{">=", 1000, 0, "NVMe media errors extremely frequent — NAND massively damaged"},
		{">=", 100, 30, "NVMe media errors numerous — serious drop risk"},
		{">=", 10, 60, "NVMe media errors present — monitor closely"},
		{">=", 1, 90, "NVMe media errors: a few recorded"},
	}},
	{"grown_defects", true, func(i *HealthInput) *int64 { return i.SASGrownDefects }, []cond{
		{">=", 1000, 0, "SAS grown defect list huge — surface severely degraded"},
		{">=", 500, 40, "SAS grown defect list large"},
		{">=", 100, 70, "SAS grown defect list notable"},
		{">", 0, 95, "SAS grown defect list non-empty"},
	}},
	{"uncorrected_errors", true, func(i *HealthInput) *int64 { return i.SASUncorrected }, []cond{
		{">=", 50, 0, "SAS uncorrected errors frequent — data loss likely"},
		{">=", 10, 40, "SAS uncorrected errors repeated"},
		{">", 0, 65, "SAS uncorrected errors recorded"},
	}},
	{"temperature", false, func(i *HealthInput) *int64 {
		if i.TemperatureC == nil {
			return nil
		}
		v := int64(*i.TemperatureC)
		return &v
	}, []cond{
		{">", 75, 0, "composite temperature above 75C — cooling failure risk"},
		{">", 65, 40, "composite temperature above 65C"},
		{">", 55, 75, "composite temperature above 55C"},
	}},
}

// ScoreDiskHealth grades one disk with the vendor rule table. overall is
// the minimum over scored dimensions (see file doc), or -1 when no
// dimension could be scored.
func ScoreDiskHealth(in HealthInput) (overall int, dims []DimensionScore) {
	overall = -1
	for _, r := range vendorRules {
		v := r.get(&in)
		if v == nil {
			continue
		}
		score := 100
		msg := ""
		for _, c := range r.conds {
			hit := (c.op == ">" && *v > c.value) || (c.op == ">=" && *v >= c.value)
			if hit {
				score = c.score
				msg = c.msg
				break
			}
		}
		dims = append(dims, DimensionScore{ID: r.id, Score: score, Critical: r.critical, Message: msg})
		if overall == -1 || score < overall {
			overall = score
		}
	}
	return overall, dims
}

// ScoreFromSmart builds a HealthInput from the data our collectors already
// parse: ATA/SAS attribute maps (smartctl) and NVMe health-log maps.
// SAS keys come from ParseSmartctlText's SAS extraction
// ("grown_defect_list", "sas_uncorrected_errors").
func ScoreFromSmart(attrs map[string]int64, nvmeLog map[string]int64, tempC *int) (int, []DimensionScore) {
	var in HealthInput
	ptr := func(m map[string]int64, key string) *int64 {
		if m == nil {
			return nil
		}
		if v, ok := m[key]; ok {
			vv := v
			return &vv
		}
		return nil
	}
	in.NVMeCriticalWarning = ptr(nvmeLog, "critical_warning")
	in.NVMePercentageUsed = ptr(nvmeLog, "percentage_used")
	in.NVMeMediaErrors = ptr(nvmeLog, "media_errors")
	in.SASGrownDefects = ptr(attrs, "grown_defect_list")
	in.SASUncorrected = ptr(attrs, "sas_uncorrected_errors")
	in.TemperatureC = tempC
	return ScoreDiskHealth(in)
}

// ---------------------------------------------------------------------------
// Instant track: the vendor's 5 dmesg rules (patterns verbatim from the
// extracted rulebook; severity Critical in the vendor config).
// ---------------------------------------------------------------------------

type instantRule struct {
	id      string
	pattern string
	message string
}

var vendorInstantRules = []instantRule{
	{"dmesg_sata_fatal", `(\[sda\]|ata\d+).*?(Device offlined|DID_BAD_TARGET|Unrecovered read error|DID_NO_CONNECT|Medium Error|reset failed, giving up|rejecting I/O to dead device|rejecting I/O to offline device|rejecting I/O to device being removed|failed command|hard resetting link|softreset failed)`,
		"kernel log: SATA system disk fatal fault — host-down risk"},
	{"dmesg_sata_transport", `DID_NO_CONNECT|DID_BUS_BUSY|DID_TIME_OUT|DID_BAD_TARGET|DID_ABORT|DID_PARITY|DID_ERROR|DID_RESET|DID_BAD_INTR|DID_PASSTHROUGH|DID_SOFT_ERROR|DID_IMM_RETRY|DID_REQUEUE|DID_TRANSPORT_DISRUPTED|DID_TRANSPORT_FAILFAST|DID_TARGET_FAILURE|DID_NEXUS_FAILURE`,
		"kernel log: SCSI transport-layer fault (DID error)"},
	{"dmesg_nvme_controller", `Identify Controller failed|IO queues not created|Device not ready|Identify namespace failed|controller is down|timeout, completion polled|reset controller|Disabling device|unable to allocate dma|Abort status|frozen state error|failure state error|Firmware bug`,
		"kernel log: NVMe controller serious fault (reset/timeout/disable)"},
	{"ext4_phys_error", `EXT4-fs \((sd[a-z]+|nvme\w+)\):.*(I/O error|error count|remounting ro|Medium Error)`,
		"kernel log: EXT4 on a physical disk reports I/O errors"},
	{"hdd_io_error_generic", `Buffer I/O error on device|I/O error`,
		"kernel log: disk I/O error"},
}

var instantRes = func() []struct {
	rule instantRule
	re   *regexp.Regexp
} {
	var out []struct {
		rule instantRule
		re   *regexp.Regexp
	}
	for _, r := range vendorInstantRules {
		out = append(out, struct {
			rule instantRule
			re   *regexp.Regexp
		}{r, regexp.MustCompile(r.pattern)})
	}
	return out
}()

// ScanDmesgCritical applies the vendor instant track to kernel log text;
// each matched rule yields one critical Finding for the device.
func ScanDmesgCritical(device, logText string) []Finding {
	var out []Finding
	for _, ir := range instantRes {
		if ir.re.MatchString(logText) {
			out = append(out, Finding{
				Device:   device,
				Severity: "critical",
				Message:  fmt.Sprintf("%s [%s]", ir.rule.message, ir.rule.id),
			})
		}
	}
	return out
}
