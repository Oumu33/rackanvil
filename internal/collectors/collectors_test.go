package collectors

import "testing"

func TestAnalyzeSmartThresholds(t *testing.T) {
	// Clean disk: no findings above ok.
	f := AnalyzeSmart("sda", map[string]int64{"5": 0, "187": 0, "197": 0, "198": 0, "188": 0}, nil, true)
	if WorstSeverity(f) != "ok" {
		t.Fatalf("clean disk flagged: %+v", f)
	}
	// Uncorrectable errors -> critical.
	f = AnalyzeSmart("sdb", map[string]int64{"187": 4}, nil, true)
	if WorstSeverity(f) != "critical" {
		t.Fatalf("187 not critical: %+v", f)
	}
	// Pending sectors: warning then critical at >=10.
	f = AnalyzeSmart("sdc", map[string]int64{"197": 3}, nil, true)
	if WorstSeverity(f) != "warning" {
		t.Fatalf("197=3 not warning: %+v", f)
	}
	f = AnalyzeSmart("sdc", map[string]int64{"197": 12}, nil, true)
	if WorstSeverity(f) != "critical" {
		t.Fatalf("197=12 not critical: %+v", f)
	}
	// Reallocated: warning, critical at >=50.
	f = AnalyzeSmart("sdd", map[string]int64{"5": 60}, nil, true)
	if WorstSeverity(f) != "critical" {
		t.Fatalf("5=60 not critical: %+v", f)
	}
}

func TestAnalyzeSmartTrend(t *testing.T) {
	// Accelerating degradation: 5 -> 8 -> 12.
	hist := []map[string]int64{{"5": 8}, {"5": 5}}
	f := AnalyzeSmart("sde", map[string]int64{"5": 12}, hist, true)
	found := false
	for _, x := range f {
		if x.Severity == "warning" && len(x.Message) > 20 {
			found = true
		}
	}
	if !found {
		t.Fatalf("trend not detected: %+v", f)
	}
	// Flat history: no trend warning.
	hist = []map[string]int64{{"5": 8}, {"5": 8}}
	f = AnalyzeSmart("sde", map[string]int64{"5": 8}, hist, true)
	for _, x := range f {
		if x.Severity != "ok" && contains(x.Message, "accelerating") {
			t.Fatalf("false trend: %+v", x)
		}
	}
}

func TestAnalyzeNvme(t *testing.T) {
	f := AnalyzeNvme("nvme0", map[string]int64{"percentage_used": 10, "media_errors": 0, "critical_warning": 0})
	if WorstSeverity(f) != "ok" {
		t.Fatalf("healthy nvme flagged: %+v", f)
	}
	f = AnalyzeNvme("nvme0", map[string]int64{"percentage_used": 90, "media_errors": 0, "critical_warning": 0})
	if WorstSeverity(f) != "warning" {
		t.Fatalf("90%% used not warning: %+v", f)
	}
	// M7 vendor alignment: media_errors=7 scores 90 in the factory
	// rulebook (graded, not instant-critical); >=100 is our critical tier.
	f = AnalyzeNvme("nvme0", map[string]int64{"percentage_used": 3, "media_errors": 7, "critical_warning": 0})
	if WorstSeverity(f) != "warning" {
		t.Fatalf("media errors 7 not warning: %+v", f)
	}
	f = AnalyzeNvme("nvme0", map[string]int64{"percentage_used": 3, "media_errors": 150, "critical_warning": 0})
	if WorstSeverity(f) != "critical" {
		t.Fatalf("media errors 150 not critical: %+v", f)
	}
	f = AnalyzeNvme("nvme0", map[string]int64{"percentage_used": 3, "media_errors": 0, "critical_warning": 2})
	if WorstSeverity(f) != "critical" {
		t.Fatalf("critical warning not critical: %+v", f)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

func TestCollectDoesNotCrash(t *testing.T) {
	s := Collect()
	if s == nil {
		t.Fatalf("nil snapshot")
	}
	t.Logf("cpu=%.1f%% mem=%d/%dMB disks=%d smart=%d nvme=%d skipped=%v",
		s.CPUPct, s.MemUsedMB, s.MemTotalMB, len(s.Disks), len(s.Smart), len(s.Nvme), s.Skipped)
}
