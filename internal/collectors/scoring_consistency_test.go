package collectors

// M11 consistency guard: rule TEXT must never disagree with the rule's
// enforced threshold. The vendor's own rulebook carries this bug — its
// NVMe percentage_used copy reads "95%/90%" while its code enforces
// 96/91 (M8 recovery) — and M7/M8 initially copied the vendor text into
// our trend keys verbatim. These tests pin every number that appears
// in a rule message/key/advice to the constant that enforces it.

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var thresholdNumRe = regexp.MustCompile(`(\d+)\s*(?:%|C\b)`)

// TestStaticRuleTextMatchesThresholds walks the static-track rule table:
// any number presented as a percentage or temperature in a condition's
// message must equal that condition's threshold value.
func TestStaticRuleTextMatchesThresholds(t *testing.T) {
	for _, r := range vendorRules {
		for _, c := range r.conds {
			for _, m := range thresholdNumRe.FindAllStringSubmatch(c.msg, -1) {
				n, _ := strconv.ParseInt(m[1], 10, 64)
				if n != c.value {
					t.Errorf("rule %s cond %s %d: message %q cites %d, want the enforced threshold",
						r.id, c.op, c.value, c.msg, n)
				}
			}
		}
	}
}

func pctFindings(t *testing.T, used int64) []TrendFinding {
	t.Helper()
	res := ScoreDiskTrend(TrendInput{Track: "NVME", Attrs: map[string]int64{"percentage_used": used}})
	var out []TrendFinding
	for _, f := range res.Findings {
		if strings.Contains(f.Key, "Percentage_Used") {
			out = append(out, f)
		}
	}
	return out
}

// TestTrendPctUsedTextMatchesThresholds checks the boundary behavior of
// the NVMe percentage_used trend tiers and that the key text names the
// threshold that actually fired.
func TestTrendPctUsedTextMatchesThresholds(t *testing.T) {
	if got := pctFindings(t, nvmePctUsedHigh); len(got) != 1 || !strings.Contains(got[0].Key, strconv.Itoa(nvmePctUsedHigh)) || got[0].Deduct != 80 {
		t.Fatalf("used=%d: got %+v, want one high-tier finding naming %d", nvmePctUsedHigh, got, nvmePctUsedHigh)
	}
	if got := pctFindings(t, nvmePctUsedHigh-1); len(got) != 1 || !strings.Contains(got[0].Key, strconv.Itoa(nvmePctUsedWarn)) || got[0].Deduct != 40 {
		t.Fatalf("used=%d: got %+v, want one warn-tier finding naming %d", nvmePctUsedHigh-1, got, nvmePctUsedWarn)
	}
	if got := pctFindings(t, nvmePctUsedWarn); len(got) != 1 || !strings.Contains(got[0].Key, strconv.Itoa(nvmePctUsedWarn)) {
		t.Fatalf("used=%d: got %+v, want one warn-tier finding", nvmePctUsedWarn, got)
	}
	if got := pctFindings(t, nvmePctUsedWarn-1); len(got) != 0 {
		t.Fatalf("used=%d: got %+v, want no percentage_used finding", nvmePctUsedWarn-1, got)
	}
}

// TestTrendAdviceThresholds checks advice strings that cite a count
// threshold ("超过10个") only appear when the value is actually past it.
func TestTrendAdviceThresholds(t *testing.T) {
	for _, track := range []string{"HDD", "SSD"} {
		over := ScoreDiskTrend(TrendInput{Track: track, Attrs: map[string]int64{"197": 11}})
		found := false
		for _, f := range over.Findings {
			if strings.Contains(f.Key, "Current_Pending_Sector") && strings.Contains(f.Advice, "超过10") {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 197=11: want the >10 advice variant, findings %+v", track, over.Findings)
		}
		at := ScoreDiskTrend(TrendInput{Track: track, Attrs: map[string]int64{"197": 10}})
		for _, f := range at.Findings {
			if strings.Contains(f.Advice, "超过10") {
				t.Fatalf("%s 197=10: advice %q cites >10 but value is exactly 10", track, f.Advice)
			}
		}
	}
	// NVMe temperature key cites 50C and fires strictly above 50.
	hot := ScoreDiskTrend(TrendInput{Track: "NVME", Attrs: map[string]int64{"temperature": 51}})
	found := false
	for _, f := range hot.Findings {
		if strings.Contains(f.Key, "50C") {
			found = true
		}
	}
	if !found {
		t.Fatalf("temperature=51: want the 50C finding, got %+v", hot.Findings)
	}
	edge := ScoreDiskTrend(TrendInput{Track: "NVME", Attrs: map[string]int64{"temperature": 50}})
	for _, f := range edge.Findings {
		if strings.Contains(f.Key, "temperature") {
			t.Fatalf("temperature=50: finding %q must not fire at exactly 50", f.Key)
		}
	}
}
