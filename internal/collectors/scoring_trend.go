package collectors

// Vendor trend/history disk scoring (M8, 2026-10-21; M9 corrections
// 2026-10-21) — the aggregation the M7 rulebook JSON did not contain,
// recovered by Ghidra decompilation of the vendor server binary's
// hwfault package:
//
//	hwfault.AnalyzeDiskSmartHistory @0x19cc360 (+ closures func1/func2)
//	hwfault.applyHDDRules @0x19cd320, applySSDRules @0x19cdd20,
//	hwfault.applyNVMeRules @0x19cbf80, applyAbsoluteOrDelta @0x19ce600
//	hwfault.buildOutput @0x19c9e00 (grade bands)
//	M9: hwfault.applySnicSSDRules @0x19ce240,
//	    hwfault.applyHostSSDRules @0x19ce320 (full decompiles in
//	    deplos-reversing/m9-evidence/ghidra/)
//
// The vendor model is NOT a minimum over dimension grades:
//   - score starts at 100.0;
//   - a report() closure appends a finding and SUBTRACTS a fixed
//     deduction (only while score > 0); whitelisted disks are skipped
//     entirely;
//   - SMART FAILING_NOW subtracts 100; SMART overall-health "FAILED"
//     subtracts 100 (both tracks call it "硬盘健康状态异常");
//   - per-track rule tables (HDD / SSD / NVMe) subtract per hit using
//     current values AND deltas against the previous collection
//     (delta closure = cur[attr] - prev[attr]);
//   - display: with findings present the shown score is capped at 99;
//     grade: score >= 60 -> Warning (Info + shown 100 when no
//     findings), score < 60 -> Critical (buildOutput).
//
// M9 correction: the last numeric argument of the vendor report() call
// is the ADVICE STRING'S BYTE LENGTH (Go string header), not a
// severity code — verified against the recovered literals (e.g. the
// attr-187 advice is exactly 99 bytes). M8 stored those lengths in a
// field named Severity; the field is now Deduct, the vendor's actual
// per-finding float64 (read from the finding prototypes in .data:
// 10/20/40/60/80/100). Per-finding severity codes do not exist in the
// vendor call; grading is by total score only (buildOutput).
//
// SATA therefore DOES have full attribute scoring tracks in the vendor
// code (applyHDDRules/applySSDRules) — this corrects the M7 note that
// read the JSON rulebook as the whole story. The JSON (disk.json /
// "disk_dual_track") feeds the vendor's separate static metrics engine
// (LoadRuleConfig("disk.json") + EvaluateMetrics); ScoreDiskHealth in
// scoring.go keeps that static view.

import (
	"fmt"
	"strings"
)

// TrendFinding is one vendor-style scoring finding.
type TrendFinding struct {
	Key    string  // vendor rule key, e.g. "硬盘Reallocated_Sector_Ct出现增长"
	Detail string  // formatted measurement, e.g. "增幅3，当前值12"
	Advice string  // vendor advice text (verbatim where recovered)
	Deduct float64 // vendor per-finding score deduction
}

// TrendInput is one collection snapshot for trend scoring.
type TrendInput struct {
	Track         string           // "HDD" | "SSD" | "NVME" (vendor dispatch values)
	Model         string           // disk model; SSD brand dispatch ("Snic" prefix)
	Health        string           // SMART overall health; "FAILED" -> -100
	FailingNow    bool             // SMART status FAILING_NOW -> -100
	Attrs         map[string]int64 // current: ATA ids "5","183",... / NVMe keys
	Prev          map[string]int64 // previous collection; nil = no baseline
	Whitelisted   bool             // vendor isWhitelisted: skip all findings
	ReallocStreak int              // persisted consecutive-growth count for ATA 5
}

// TrendResult is the scored output.
type TrendResult struct {
	Score         float64
	Findings      []TrendFinding
	ReallocStreak int // updated streak to persist for the next collection
}

// ScoreDiskTrend scores one snapshot with the vendor's history rules.
func ScoreDiskTrend(in TrendInput) TrendResult {
	score := 100.0
	streak := in.ReallocStreak
	var findings []TrendFinding
	report := func(deduct float64, key, detail, advice string) {
		if in.Whitelisted {
			return
		}
		findings = append(findings, TrendFinding{Key: key, Detail: detail, Advice: advice, Deduct: deduct})
		if score > 0 {
			score -= deduct
		}
	}
	delta := func(attr string) int64 {
		if in.Prev == nil {
			return 0
		}
		return in.Attrs[attr] - in.Prev[attr]
	}
	cur := func(attr string) int64 { return in.Attrs[attr] }

	if in.FailingNow {
		// Vendor: finding text begins "SMART预警已失效，请..." (recovered
		// prefix), deduction 100.
		report(100, "硬盘FAILING_NOW", "", "SMART预警已失效，建议立即换盘")
	}

	switch in.Track {
	case "HDD", "SSD":
		if in.Health == "FAILED" {
			report(100, "硬盘健康状态异常", "FAILED", "SMART整体评估失败，建议立即换盘")
		}
	}
	if in.Track == "HDD" {
		streak = applyHDDTrend(cur, delta, in.Prev == nil, streak, report)
	} else if in.Track == "SSD" {
		streak = applySSDTrend(cur, delta, streak, in.Model, report)
	} else {
		streak = 0
	}
	if in.Track == "NVME" {
		applyNVMeTrend(cur, delta, report)
	}
	return TrendResult{Score: score, Findings: findings, ReallocStreak: streak}
}

type trendReport func(deduct float64, key, detail, advice string)

// NVMe percentage_used trend thresholds (vendor values, M8 recovery).
// The finding key text is generated from these constants — see the
// percentage_used branch in applyNVMeTrend for why.
const (
	nvmePctUsedHigh = 96 // >= 96 -> deduct 80
	nvmePctUsedWarn = 91 // >= 91 (vendor branch: v > 90) -> deduct 40
)

func applyHDDTrend(cur func(string) int64, delta func(string) int64, first bool, streak int, report trendReport) int {
	// --- ATA 5 Reallocated_Sector_Ct: absolute tiers + growth trend ---
	v := cur("5")
	d := delta("5")
	switch {
	case v >= 2801:
		report(95, "硬盘Reallocated_Sector_Ct极度危险", fmt.Sprintf("当前值%d", v),
			"重映射扇区已逼近3000大关，随时可能掉盘，建议立即停止写入并更换硬盘")
		streak = 0
	case v >= 1001:
		report(80, "硬盘Reallocated_Sector_Ct备用块大量消耗", fmt.Sprintf("当前值%d", v),
			"备用块已大量消耗，硬盘可靠性显著下降，建议尽快更换硬盘")
		streak = 0
	case d < 1:
		if v > 0 {
			report(8, "硬盘Reallocated_Sector_Ct存在存量", fmt.Sprintf("当前值%d（本周期无增长）", v),
				"存在历史坏块但当前稳定，属于老盘正常损耗，建议持续观察，重点关注是否出现新增")
		}
		streak = 0
	case first:
		report(8, "硬盘Reallocated_Sector_Ct存在存量(首次采集基线)", fmt.Sprintf("当前值%d（首次采集，建立基线，不计入增长趋势）", v),
			"首次采集发现历史坏块，已建立基线，后续将追踪增长趋势，建议持续观察")
		streak = 0
	default:
		streak++
		switch {
		case d >= 4:
			report(80, "硬盘Reallocated_Sector_Ct急速增长", fmt.Sprintf("增幅%d，当前值%d", d, v),
				"坏块急速扩散，预示硬盘即将失效，建议立即换盘")
		case streak >= 3:
			report(60, "硬盘Reallocated_Sector_Ct持续增长", fmt.Sprintf("增幅%d，当前值%d，已连续增长%d次", d, v, streak),
				"坏块持续扩散，劣化趋势明显，建议尽快换盘")
		default:
			report(40, "硬盘Reallocated_Sector_Ct出现增长", fmt.Sprintf("增幅%d，当前值%d", d, v),
				"出现新增坏块，建议持续关注，若下次采集仍有增长则尽快换盘")
		}
	}
	// --- ATA 197 / 198: absolute non-zero ---
	if v := cur("197"); v > 0 {
		advice := "代表当前硬盘存在待处理坏块，如出现文件系统只读或IO报错建议重新格式化"
		if v > 10 {
			advice = "待处理坏块超过10个，数据丢失风险较高，建议换盘"
		}
		report(80, "硬盘Current_Pending_Sector非0", fmt.Sprintf("%d", v), advice)
	}
	if v := cur("198"); v > 0 {
		advice := "存在无法纠正的离线扇区，如dmesg出现media error建议重新格式化"
		if v > 10 {
			advice = "离线不可纠正扇区超过10个，建议立即换盘"
		}
		report(80, "硬盘Offline_Uncorrectable非0", fmt.Sprintf("%d", v), advice)
	}
	// --- delta-only rules ---
	if d := delta("184"); d > 0 {
		report(60, "硬盘End-to-End_Error新增", fmt.Sprintf("增幅%d", d),
			"端到端错误增长，存在数据静默损坏风险，建议换盘")
	}
	if d := delta("187"); d > 0 {
		report(80, "硬盘Reported_Uncorrect增长", fmt.Sprintf("增幅%d", d),
			"确认近期是否存在增长，如出现增长则预示异常表现与坏块有关，建议换盘")
	}
	if d := delta("199"); d > 0 {
		report(20, "硬盘UDMA_CRC_Error_Count增长", fmt.Sprintf("增幅%d", d),
			"确认近期是否存在增长，预示异常与CRC有关，建议排查链路，插拔硬盘")
	}
	return streak
}

func applySSDTrend(cur func(string) int64, delta func(string) int64, streak int, model string, report trendReport) int {
	// Vendor applySSDRules: 197/198 absolute rules (advice texts are
	// the SSD track's own variants, recovered verbatim in M9), then
	// delta rules for 183/184/187/199, then the ATA-5 brand sub-track:
	// model prefix "Snic" (4-char compare in the vendor code) ->
	// applySnicSSDRules, otherwise applyHostSSDRules.
	if v := cur("197"); v > 0 {
		advice := "代表当前硬盘存在坏块，如出现文件系统只读或IO报错建议重新格式化"
		if v > 10 {
			advice = "RAW_VALUE超过10，代表当前硬盘存在坏块，建议换盘"
		}
		report(80, "硬盘Current_Pending_Sector非0", fmt.Sprintf("%d", v), advice)
	}
	if v := cur("198"); v > 0 {
		advice := "代表当前硬盘存在坏块，如dmesg日志出现文件系统只读或者media error报错建议重新格式化"
		if v > 10 {
			advice = "RAW_VALUE超过10，代表当前硬盘存在坏块，建议换盘"
		}
		report(80, "硬盘Offline_Uncorrectable非0", fmt.Sprintf("%d", v), advice)
	}
	if d := delta("183"); d > 0 {
		report(60, "硬盘Runtime_Bad_Block增长", fmt.Sprintf("增幅%d", d), "运行时坏块增加，建议换盘")
	}
	if d := delta("184"); d > 0 {
		report(60, "硬盘End-to-End_Error增长", fmt.Sprintf("增幅%d", d), "端到端错误增长，建议换盘")
	}
	if d := delta("187"); d > 0 {
		report(80, "硬盘Reported_Uncorrect增长", fmt.Sprintf("增幅%d", d),
			"确认近期是否存在增长，如出现增长则预示异常表现与坏块有关，建议换盘")
	}
	if d := delta("199"); d > 0 {
		report(20, "硬盘UDMA_CRC_Error_Count增长", fmt.Sprintf("增幅%d", d),
			"确认近期是否存在增长，预示异常与CRC有关，建议排查链路，插拔硬盘")
	}
	// --- ATA 5 brand sub-tracks (M9, fully decompiled) ---
	v := cur("5")
	if strings.HasPrefix(model, "Snic") {
		// applySnicSSDRules: any non-zero realloc count fires once.
		if v > 0 {
			report(40, "硬盘Reallocated_Sector_Ct非0", fmt.Sprintf("%d", v),
				"确认近期是否存在增长，如出现增长预示异常表现与坏块有关，建议换盘")
		}
		return streak
	}
	// applyHostSSDRules: absolute exhaustion tier, else growth
	// escalation driven by a persisted consecutive-growth counter.
	if v >= 1001 {
		report(100, "硬盘Reallocated_Sector_Ct备用块接近耗尽", fmt.Sprintf("%d", v),
			"备用块耗尽，有极大掉盘风险，建议立即换盘")
		return streak
	}
	d := delta("5")
	if d < 1 {
		return 0
	}
	streak++
	switch {
	case d >= 4:
		report(100, "硬盘Reallocated_Sector_Ct大幅增长，NAND闪存劣化", fmt.Sprintf("增幅%d", d),
			"NAND闪存急速劣化，建议换盘")
	case streak >= 3:
		report(80, "硬盘Reallocated_Sector_Ct持续增长，NAND闪存劣化", fmt.Sprintf("增幅%d", d),
			"NAND闪存持续劣化，坏块有扩散趋势，建议换盘")
	default:
		report(10, "硬盘Reallocated_Sector_Ct少量增长，NAND正常损耗", fmt.Sprintf("增幅%d", d),
			"少量增长属于正常损耗，建议持续观察")
	}
	return streak
}

func applyNVMeTrend(cur func(string) int64, delta func(string) int64, report trendReport) {
	if v := cur("critical_warning"); v > 0 {
		report(100, "Critical_Warning非0", fmt.Sprintf("%d", v), "属于硬件故障，建议换盘")
	}
	// NVMe percentage_used tiers. The vendor's rule keys label these
	// "超过95%" / "超过90%" while its actual thresholds are 96 / 91 —
	// the vendor's own text disagrees with its code (M8 recovery). We
	// keep the vendor thresholds but generate our key text FROM the
	// constants, so our text can never drift from the enforced values;
	// TestScoringTextMatchesThresholds locks this in.
	if v := cur("percentage_used"); v >= nvmePctUsedHigh {
		report(80, fmt.Sprintf("Percentage_Used≥%d%%", nvmePctUsedHigh), fmt.Sprintf("%d%%", v), "属于接近寿命耗尽，建议尽快更换新盘")
	} else if v >= nvmePctUsedWarn {
		report(40, fmt.Sprintf("Percentage_Used≥%d%%", nvmePctUsedWarn), fmt.Sprintf("%d%%", v), "接近寿命预警阈值，建议持续关注")
	}
	if v := cur("temperature"); v > 50 {
		report(40, "temperature大于50C", fmt.Sprintf("%dC", v),
			"确认温度是最近一天都处在高位还是单次日志出现高温，并且检查其他硬盘及包括CPU是否存在高温现象，综合判断是单机风扇故障还是制冷异常")
	}
	// applyAbsoluteOrDelta(100, 80, media_errors, 10000) and
	// applyAbsoluteOrDelta(60, 40, num_err_log_entries, 10000).
	absoluteOrDelta(cur, delta, report, "media_errors", 100, 80, 10000,
		"硬盘Media_Errors增长", "确认最近一天是否存在增长，如出现增长则预示异常表现与坏块有关，建议换盘")
	absoluteOrDelta(cur, delta, report, "num_err_log_entries", 60, 40, 10000,
		"硬盘Error_Log_Entries增长", "确认最近一天是否存在增长，预示异常表现与硬盘有关，需进一步确认error类型")
}

// absoluteOrDelta mirrors the vendor helper: cur > threshold ->
// deductAbs with detail "%d"; else delta > 0 -> deductDelta with
// detail "增量%d, 当前%d".
func absoluteOrDelta(cur func(string) int64, delta func(string) int64, report trendReport,
	attr string, deductAbs, deductDelta float64, threshold int64, key, advice string) {
	v := cur(attr)
	if v > threshold {
		report(deductAbs, key, fmt.Sprintf("%d", v), advice)
	} else if d := delta(attr); d > 0 {
		report(deductDelta, key, fmt.Sprintf("增量%d, 当前%d", d, v), advice)
	}
}

// TrendGrade mirrors the vendor buildOutput grade bands: no findings ->
// ("Info", shown score 100); score >= 60 -> "Warning" (shown score
// capped at 99 when findings exist); score < 60 -> "Critical".
func TrendGrade(score float64, findings int) (level string, shown float64) {
	if findings == 0 {
		return "Info", 100
	}
	shown = score
	if shown >= 100 {
		shown = 99
	}
	if score >= 60 {
		return "Warning", shown
	}
	return "Critical", shown
}
