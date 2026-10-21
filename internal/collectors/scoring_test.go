package collectors

// Tests for the vendor-aligned scoring model (M7). Threshold expectations
// are the factory values from the extracted rulebook
// (deplos-reversing/m7-vendor-disk-rules.json, disk_dual_track 3.1).

import "testing"

func i64p(v int64) *int64 { return &v }
func intp(v int) *int     { return &v }

func TestScoreDiskHealthVendorTable(t *testing.T) {
	cases := []struct {
		name string
		in   HealthInput
		id   string
		want int
	}{
		{"cw set", HealthInput{NVMeCriticalWarning: i64p(4)}, "critical_warning", 0},
		{"cw clear", HealthInput{NVMeCriticalWarning: i64p(0)}, "critical_warning", 100},
		{"pu 79", HealthInput{NVMePercentageUsed: i64p(79)}, "percentage_used", 100},
		{"pu 80", HealthInput{NVMePercentageUsed: i64p(80)}, "percentage_used", 80},
		{"pu 90", HealthInput{NVMePercentageUsed: i64p(90)}, "percentage_used", 60},
		{"pu 98", HealthInput{NVMePercentageUsed: i64p(98)}, "percentage_used", 20},
		{"pu 100", HealthInput{NVMePercentageUsed: i64p(100)}, "percentage_used", 0},
		{"me 0", HealthInput{NVMeMediaErrors: i64p(0)}, "media_errors", 100},
		{"me 1", HealthInput{NVMeMediaErrors: i64p(1)}, "media_errors", 90},
		{"me 10", HealthInput{NVMeMediaErrors: i64p(10)}, "media_errors", 60},
		{"me 100", HealthInput{NVMeMediaErrors: i64p(100)}, "media_errors", 30},
		{"me 1000", HealthInput{NVMeMediaErrors: i64p(1000)}, "media_errors", 0},
		{"gd 0", HealthInput{SASGrownDefects: i64p(0)}, "grown_defects", 100},
		{"gd 1", HealthInput{SASGrownDefects: i64p(1)}, "grown_defects", 95},
		{"gd 100", HealthInput{SASGrownDefects: i64p(100)}, "grown_defects", 70},
		{"gd 500", HealthInput{SASGrownDefects: i64p(500)}, "grown_defects", 40},
		{"gd 1000", HealthInput{SASGrownDefects: i64p(1000)}, "grown_defects", 0},
		{"ue 1", HealthInput{SASUncorrected: i64p(1)}, "uncorrected_errors", 65},
		{"ue 10", HealthInput{SASUncorrected: i64p(10)}, "uncorrected_errors", 40},
		{"ue 50", HealthInput{SASUncorrected: i64p(50)}, "uncorrected_errors", 0},
		{"temp 55", HealthInput{TemperatureC: intp(55)}, "temperature", 100},
		{"temp 56", HealthInput{TemperatureC: intp(56)}, "temperature", 75},
		{"temp 66", HealthInput{TemperatureC: intp(66)}, "temperature", 40},
		{"temp 76", HealthInput{TemperatureC: intp(76)}, "temperature", 0},
	}
	for _, c := range cases {
		overall, dims := ScoreDiskHealth(c.in)
		if len(dims) != 1 || dims[0].ID != c.id || dims[0].Score != c.want || overall != c.want {
			t.Errorf("%s: got overall=%d dims=%+v, want %d", c.name, overall, dims, c.want)
		}
	}
	// No inputs -> nothing scored.
	if overall, dims := ScoreDiskHealth(HealthInput{}); overall != -1 || dims != nil {
		t.Errorf("empty input: overall=%d dims=%v", overall, dims)
	}
	// Overall = min over dimensions (our documented aggregation).
	overall, _ := ScoreDiskHealth(HealthInput{NVMePercentageUsed: i64p(85), NVMeMediaErrors: i64p(5), TemperatureC: intp(40)})
	if overall != 80 {
		t.Errorf("overall min: got %d want 80", overall)
	}
}

func TestScanDmesgCritical(t *testing.T) {
	// Note: the vendor ext4 pattern requires the bare device name in
	// parens ("(sda):"); a partition suffix ("(sda1):") does NOT match
	// the factory regex — reproduced faithfully, quirk included.
	log := "ata1.00: Device offlined, resetting\nEXT4-fs (sda): I/O error count 3\nnvme0: controller is down\n"
	got := ScanDmesgCritical("sda", log)
	// sata_fatal (Device offlined), ext4_phys_error, hdd_io_error_generic
	// (bare "I/O error"), dmesg_nvme_controller = exactly 4 rule hits.
	if len(got) != 4 {
		t.Fatalf("want 4 instant findings, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Severity != "critical" {
			t.Errorf("instant finding not critical: %+v", f)
		}
	}
	if got := ScanDmesgCritical("sda", "all quiet\n"); len(got) != 0 {
		t.Errorf("clean log must yield nothing, got %+v", got)
	}
}

func TestParseSmartctlTextSAS(t *testing.T) {
	// SYNTHETIC SAS smartctl text layout (canonical smartctl SAS page).
	sample := []byte(`Current Drive Temperature:     34 C
Elements in grown defect list: 12
Error counter log:
       Errors Corrected by           Total   Correction     Gigabytes    Total
           ECC          rereads/    errors   algorithm      processed    uncorrected
       fast | delayed   rewrites  corrected  invocations   [10^9 bytes]  errors
read:          0        0         0         0       1234        45.678           3
write:         0        0         0         0       1234         1.234           2
verify:        0        0         0         0       1234         0.000           0
`)
	attrs, _, _, _, err := ParseSmartctlText(sample)
	if err != nil {
		t.Fatal(err)
	}
	if attrs["grown_defect_list"] != 12 {
		t.Errorf("grown_defect_list = %d", attrs["grown_defect_list"])
	}
	if attrs["sas_uncorrected_errors"] != 5 {
		t.Errorf("sas_uncorrected_errors = %d, want 5", attrs["sas_uncorrected_errors"])
	}
	// grown_defects=12 -> 95 (vendor >0 tier); uncorrected=5 -> 65 (>0 tier);
	// overall = min = 65.
	overall, _ := ScoreFromSmart(attrs, nil, nil)
	if overall != 65 {
		t.Errorf("score from SAS attrs = %d, want 65", overall)
	}
}

func TestMegaRaidSmartArgs(t *testing.T) {
	got := MegaRaidSmartArgs(3, "/dev/sda")
	want := []string{"--json", "-a", "-d", "megaraid,3", "/dev/sda"}
	if len(got) != len(want) {
		t.Fatalf("args = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %v, want %v", got, want)
		}
	}
	if !hasMegaRAIDController("01:00.0 RAID [0104]: Broadcom MegaRAID [1000:005d]") {
		t.Error("1000:005d must be detected as MegaRAID")
	}
	if hasMegaRAIDController("01:00.0 Ethernet [0200]: Intel [8086:1521]") {
		t.Error("non-RAID lspci must not match")
	}
}
