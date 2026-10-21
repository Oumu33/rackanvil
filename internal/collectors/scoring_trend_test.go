package collectors

// M8 trend-scoring tests. Expected values come straight from the
// decompiled vendor rule tables (see scoring_trend.go doc): base 100,
// fixed deductions, vendor thresholds (realloc tiers 1001/2801, NVMe
// percentage_used 91/96, temperature >50, absolute-or-delta 10000).

import "testing"

func TestTrendHealthyDisk(t *testing.T) {
	r := ScoreDiskTrend(TrendInput{Track: "HDD", Health: "PASSED",
		Attrs: map[string]int64{"5": 0, "197": 0, "198": 0},
		Prev:  map[string]int64{"5": 0, "197": 0, "198": 0}})
	if r.Score != 100 || len(r.Findings) != 0 {
		t.Fatalf("healthy = %+v", r)
	}
	if lvl, shown := TrendGrade(r.Score, len(r.Findings)); lvl != "Info" || shown != 100 {
		t.Fatalf("grade = %s %.0f", lvl, shown)
	}
}

func TestTrendFailedAndFailingNow(t *testing.T) {
	r := ScoreDiskTrend(TrendInput{Track: "HDD", Health: "FAILED", Attrs: map[string]int64{}})
	if r.Score != 0 || len(r.Findings) != 1 || r.Findings[0].Key != "硬盘健康状态异常" {
		t.Fatalf("FAILED = %+v", r)
	}
	r = ScoreDiskTrend(TrendInput{Track: "NVME", FailingNow: true, Attrs: map[string]int64{}})
	if r.Score != 0 {
		t.Fatalf("FAILING_NOW score = %v", r.Score)
	}
}

func TestTrendReallocTiers(t *testing.T) {
	cases := []struct {
		cur  int64
		want float64
	}{
		{2801, 5},   // -95 极度危险
		{5000, 5},   // still -95 (single highest tier)
		{1001, 20},  // -80 大量消耗
		{1000, 100}, // below tier, no growth (prev == cur) -> stock rule needs v>0 & delta<1: -8
	}
	for _, c := range cases {
		want := c.want
		if c.cur == 1000 {
			want = 92 // 存量 -8
		}
		r := ScoreDiskTrend(TrendInput{Track: "HDD", Health: "PASSED",
			Attrs: map[string]int64{"5": c.cur}, Prev: map[string]int64{"5": c.cur}})
		if r.Score != want {
			t.Errorf("realloc cur=%d score=%v want %v (findings %+v)", c.cur, r.Score, want, r.Findings)
		}
	}
}

func TestTrendReallocGrowthAndStreak(t *testing.T) {
	// First collection with existing stock: baseline rule (-8), no streak.
	r := ScoreDiskTrend(TrendInput{Track: "HDD", Health: "PASSED",
		Attrs: map[string]int64{"5": 10}})
	if r.Score != 92 || r.ReallocStreak != 0 {
		t.Fatalf("first = %+v", r)
	}
	// Growth of 2 with streak continuing: 1st growth -40, streak 1.
	r = ScoreDiskTrend(TrendInput{Track: "HDD", Health: "PASSED",
		Attrs: map[string]int64{"5": 12}, Prev: map[string]int64{"5": 10}, ReallocStreak: 0})
	if r.Score != 60 || r.ReallocStreak != 1 {
		t.Fatalf("growth1 = %+v", r)
	}
	// Third consecutive growth (streak 2 -> 3): -60.
	r = ScoreDiskTrend(TrendInput{Track: "HDD", Health: "PASSED",
		Attrs: map[string]int64{"5": 13}, Prev: map[string]int64{"5": 12}, ReallocStreak: 2})
	if r.Score != 40 || r.ReallocStreak != 3 {
		t.Fatalf("growth3 = %+v", r)
	}
	// Rapid growth (delta >= 4): -80 regardless of streak.
	r = ScoreDiskTrend(TrendInput{Track: "HDD", Health: "PASSED",
		Attrs: map[string]int64{"5": 20}, Prev: map[string]int64{"5": 12}, ReallocStreak: 0})
	if r.Score != 20 {
		t.Fatalf("rapid = %+v", r)
	}
}

func TestTrendDeltaRulesAndWhitelist(t *testing.T) {
	in := TrendInput{Track: "HDD", Health: "PASSED",
		Attrs: map[string]int64{"197": 3, "199": 50},
		Prev:  map[string]int64{"197": 3, "199": 40}}
	r := ScoreDiskTrend(in)
	// 197>0 -> -80 ; delta199 = 10 > 0 -> -20 ; total 0.
	if r.Score != 0 || len(r.Findings) != 2 {
		t.Fatalf("delta rules = %+v", r)
	}
	in.Whitelisted = true
	r = ScoreDiskTrend(in)
	if r.Score != 100 || len(r.Findings) != 0 {
		t.Fatalf("whitelisted = %+v", r)
	}
}

func TestTrendSSDTrack(t *testing.T) {
	r := ScoreDiskTrend(TrendInput{Track: "SSD", Health: "PASSED",
		Attrs: map[string]int64{"183": 5, "197": 0},
		Prev:  map[string]int64{"183": 2, "197": 0}})
	if r.Score != 40 || len(r.Findings) != 1 || r.Findings[0].Key != "硬盘Runtime_Bad_Block增长" {
		t.Fatalf("ssd = %+v", r)
	}
}

func TestTrendNVMeTrack(t *testing.T) {
	// percentage_used 93 -> -40; temperature 55 -> -40; media_errors
	// delta>0 (<=10000) -> -80. Score floors via report-while-positive:
	// 100-40-40-80 -> last deduction applies while score>0 -> -60.
	r := ScoreDiskTrend(TrendInput{Track: "NVME",
		Attrs: map[string]int64{"percentage_used": 93, "temperature": 55, "media_errors": 3},
		Prev:  map[string]int64{"percentage_used": 93, "temperature": 55, "media_errors": 1}})
	if len(r.Findings) != 3 {
		t.Fatalf("nvme findings = %+v", r.Findings)
	}
	if r.Score != -60 {
		t.Fatalf("nvme score = %v", r.Score)
	}
	// percentage_used 96 -> -80 tier.
	r = ScoreDiskTrend(TrendInput{Track: "NVME", Attrs: map[string]int64{"percentage_used": 96}})
	if r.Score != 20 {
		t.Fatalf("nvme 96 score = %v", r.Score)
	}
	// media_errors absolute > 10000 -> -100.
	r = ScoreDiskTrend(TrendInput{Track: "NVME", Attrs: map[string]int64{"media_errors": 10001}})
	if r.Score != 0 {
		t.Fatalf("nvme media abs score = %v", r.Score)
	}
	// critical_warning -> -100.
	r = ScoreDiskTrend(TrendInput{Track: "NVME", Attrs: map[string]int64{"critical_warning": 1}})
	if r.Score != 0 {
		t.Fatalf("nvme cw score = %v", r.Score)
	}
	if lvl, shown := TrendGrade(20, 1); lvl != "Critical" || shown != 20 {
		t.Fatalf("grade = %s %v", lvl, shown)
	}
	if lvl, shown := TrendGrade(100, 2); lvl != "Warning" || shown != 99 {
		t.Fatalf("grade cap = %s %v", lvl, shown)
	}
}

func TestSSDSubTracksM9(t *testing.T) {
	// Snic brand: any non-zero realloc fires deduct 40 exactly once.
	r := ScoreDiskTrend(TrendInput{Track: "SSD", Model: "SnicSSD-2000", Health: "PASSED",
		Attrs: map[string]int64{"5": 3}})
	if len(r.Findings) != 1 || r.Findings[0].Deduct != 40 ||
		r.Findings[0].Key != "硬盘Reallocated_Sector_Ct非0" {
		t.Fatalf("snic: got %+v", r.Findings)
	}
	// Host brand: small growth deduct 10, streak increments.
	in := TrendInput{Track: "SSD", Model: "GenericSSD", Health: "PASSED",
		Attrs: map[string]int64{"5": 12}, Prev: map[string]int64{"5": 10}}
	r = ScoreDiskTrend(in)
	if len(r.Findings) != 1 || r.Findings[0].Deduct != 10 || r.ReallocStreak != 1 {
		t.Fatalf("host small growth: got %+v streak=%d", r.Findings, r.ReallocStreak)
	}
	// Host brand: third consecutive growth escalates to deduct 80.
	in.ReallocStreak = 2
	r = ScoreDiskTrend(in)
	if len(r.Findings) != 1 || r.Findings[0].Deduct != 80 {
		t.Fatalf("host sustained growth: got %+v", r.Findings)
	}
	// Host brand: large single delta deduct 100.
	in.ReallocStreak = 0
	in.Attrs["5"], in.Prev["5"] = 20, 10
	r = ScoreDiskTrend(in)
	if len(r.Findings) != 1 || r.Findings[0].Deduct != 100 {
		t.Fatalf("host large growth: got %+v", r.Findings)
	}
	// Host brand: absolute exhaustion tier deduct 100.
	r = ScoreDiskTrend(TrendInput{Track: "SSD", Model: "GenericSSD", Health: "PASSED",
		Attrs: map[string]int64{"5": 1001}})
	if len(r.Findings) != 1 || r.Findings[0].Deduct != 100 ||
		r.Findings[0].Key != "硬盘Reallocated_Sector_Ct备用块接近耗尽" {
		t.Fatalf("host exhausted: got %+v", r.Findings)
	}
	// SSD 197 advice is the SSD track's verbatim text.
	r = ScoreDiskTrend(TrendInput{Track: "SSD", Health: "PASSED",
		Attrs: map[string]int64{"197": 11}})
	found := false
	for _, f := range r.Findings {
		if f.Key == "硬盘Current_Pending_Sector非0" {
			found = true
			if f.Advice != "RAW_VALUE超过10，代表当前硬盘存在坏块，建议换盘" || f.Deduct != 80 {
				t.Fatalf("197 advice/deduct: %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("197 finding missing: %+v", r.Findings)
	}
}
