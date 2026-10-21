// Fixture regression tests: real command outputs from public sources
// (see testdata/ headers for provenance) are fed through the parsers.
package collectors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// loadJSONFixture strips the `#` provenance header lines that every fixture
// file carries, so the JSON parsers see exactly what the real tool emitted.
func loadJSONFixture(t *testing.T, name string) []byte {
	t.Helper()
	var sb strings.Builder
	for _, line := range strings.Split(string(loadFixture(t, name)), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return []byte(sb.String())
}

func TestSmartctlTextFailingDrive(t *testing.T) {
	// REAL sample: Seagate ST500LM021 (toadjaune.eu). Overall-health says
	// PASSED while 187/197/198 scream failure — the analyzer must still
	// flag critical from attributes alone.
	attrs, passed, model, serial, err := ParseSmartctlText(loadFixture(t, "smartctl-sata-hdd-failing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if model != "ST500LM021-1KJ152" {
		t.Errorf("model = %q", model)
	}
	if serial != "W62DD2AZ" {
		t.Errorf("serial = %q", serial)
	}
	if !passed {
		t.Error("health line says PASSED; parser must report passed=true")
	}
	want := map[string]int64{"187": 59352, "197": 16, "198": 16, "5": 0, "194": 42, "188": 210460672117}
	for id, v := range want {
		if attrs[id] != v {
			t.Errorf("attr %s = %d, want %d", id, attrs[id], v)
		}
	}
	// 194's "42 (0 17 0 0 0)" must parse as 42, not fail on the suffix.
	if attrs["194"] != 42 {
		t.Errorf("194 = %d, want 42 (suffix must be stripped)", attrs["194"])
	}

	findings := AnalyzeSmart("sda", attrs, nil, passed)
	crit := 0
	for _, f := range findings {
		if f.Severity == "critical" {
			crit++
		}
	}
	if crit == 0 {
		t.Error("failing drive produced no critical findings")
	}
	t.Logf("findings: %+v", findings)
}

func TestSmartctlHealthFailed(t *testing.T) {
	// SYNTHETIC smartctl -H FAILED: must surface as critical.
	_, passed, _, _, err := ParseSmartctlText(loadFixture(t, "smartctl-health-fail.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if passed {
		t.Error("FAILED health line must parse as passed=false")
	}
	f := AnalyzeSmart("sda", map[string]int64{}, nil, passed)
	found := false
	for _, x := range f {
		if x.Severity == "critical" {
			found = true
		}
	}
	if !found {
		t.Error("FAILED overall-health must yield a critical finding")
	}
}

func TestSmartctlJSONFormats(t *testing.T) {
	// SYNTHETIC smartctl --json (schema-documented). Covers raw.value as
	// number AND as string ("21892 (3 79 0)" style suffix in .string is
	// ignored; .value is authoritative).
	attrs, passed, err := ParseSmartctlJSON(loadJSONFixture(t, "smartctl-sata-hdd.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !passed {
		t.Error("smart_status.passed=true must be honored")
	}
	if attrs["187"] != 59352 || attrs["197"] != 16 || attrs["194"] != 42 {
		t.Errorf("attrs = %v", attrs)
	}
}

func TestSmartctlNvmeJSON(t *testing.T) {
	// SYNTHETIC smartctl --json for NVMe: temperature is Kelvin (302K = 29°C).
	attrs, _, err := ParseSmartctlJSON(loadJSONFixture(t, "smartctl-nvme.json"))
	if err != nil {
		t.Fatal(err)
	}
	// NVMe has no ata_smart_attributes table; attrs empty is fine.
	if len(attrs) != 0 {
		t.Errorf("NVMe json has no ATA table; attrs = %v", attrs)
	}
}

func TestNvmeTextReal(t *testing.T) {
	// REAL sample: Hetzner docs `nvme smart-log` text.
	m := ParseNvmeText(loadFixture(t, "nvme-smart-log.txt"))
	want := map[string]int64{
		"critical_warning": 0, "temperature": 302, // 29C -> 302K
		"avail_spare": 100, "percentage_used": 0,
		"media_errors": 0, "power_on_hours": 395,
		"data_units_read": 113446, // thousand separator stripped
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %d, want %d", k, m[k], v)
		}
	}
}

func TestNvmeTextKelvinVariant(t *testing.T) {
	// nvme-cli default style: "temperature : 43°C (316 Kelvin)".
	m := ParseNvmeText([]byte("Smart Log for NVME device:nvme0\ntemperature                         : 43°C (316 Kelvin)\npercentage_used                     : 35%\n"))
	if m["temperature"] != 316 {
		t.Errorf("temperature = %d, want 316", m["temperature"])
	}
	if m["percentage_used"] != 35 {
		t.Errorf("percentage_used = %d, want 35", m["percentage_used"])
	}
}

func TestNvmeJSONCli(t *testing.T) {
	// SYNTHETIC nvme-cli -o json: key renames + Kelvin temperature.
	m, err := ParseNvmeJSON(loadJSONFixture(t, "nvme-cli-smart-log.json"))
	if err != nil {
		t.Fatal(err)
	}
	if m["percentage_used"] != 2 {
		t.Errorf("percentage_used = %d, want 2 (renamed from percent_used)", m["percentage_used"])
	}
	if m["temperature"] != 302 {
		t.Errorf("temperature = %d, want 302", m["temperature"])
	}
	if c, ok := NvmeTempCelsius(m); !ok || int(c) != 28 {
		t.Errorf("NvmeTempCelsius = %v,%v; want ~28.85,true", c, ok)
	}
}

// TestNvmeTempUnknown is the vendor-bug regression guard: a missing,
// zero, or absurd temperature reading must yield "unknown" — never a
// fabricated four-digit Celsius number (the 1739°C class of bug).
func TestNvmeTempUnknown(t *testing.T) {
	cases := []struct {
		name string
		log  map[string]int64
	}{
		{"missing field", map[string]int64{"percentage_used": 3}},
		{"zero kelvin", map[string]int64{"temperature": 0}},
		{"absurd high (2012K -> 1739C class)", map[string]int64{"temperature": 2012}},
		{"absurd low", map[string]int64{"temperature": 50}},
	}
	for _, c := range cases {
		if v, ok := NvmeTempCelsius(c.log); ok {
			t.Errorf("%s: got %.1f°C, want unknown", c.name, v)
		}
	}
	// Sanity: normal values convert correctly.
	if c, ok := NvmeTempCelsius(map[string]int64{"temperature": 316}); !ok || int(c) != 42 {
		t.Errorf("316K -> want ~42.85°C,true; got %v,%v", c, ok)
	}
}

func TestIpmitoolSensorReal(t *testing.T) {
	// REAL sample: servethehome forum `ipmitool sensor`.
	rows := ParseIpmitoolSensor(loadFixture(t, "ipmitool-sensor.txt"))
	if len(rows) < 10 {
		t.Fatalf("parsed %d rows, want >= 10", len(rows))
	}
	byName := map[string]SensorReading{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	cpu := byName["CPU Temp"]
	if cpu.Value != "50.000" || cpu.Unit != "degrees C" || cpu.Status != "ok" {
		t.Errorf("CPU Temp = %+v", cpu)
	}
	// "na" rows must be preserved, not dropped or zeroed.
	dimm := byName["DIMMA2 Temp"]
	if dimm.Value != "na" || dimm.Status != "na" {
		t.Errorf("DIMMA2 Temp = %+v, want na/na", dimm)
	}
	fan := byName["FAN1"]
	if fan.Value != "1500.000" || fan.Unit != "RPM" {
		t.Errorf("FAN1 = %+v", fan)
	}
}

func TestIpmitoolSdrReal(t *testing.T) {
	// REAL sample: Oracle docs `ipmitool sdr elist full`.
	rows := ParseIpmitoolSensor(loadFixture(t, "ipmitool-sdr.txt"))
	if len(rows) != 15 {
		t.Fatalf("parsed %d rows, want 15", len(rows))
	}
	r0 := rows[0]
	if r0.Name != "fp.t_amb" || r0.Value != "22" || r0.Unit != "degrees C" || r0.Status != "ok" {
		t.Errorf("row0 = %+v", r0)
	}
	// Compact 3-col sdr format: no reading available.
	rows3 := ParseIpmitoolSensor([]byte("CPU Temp | 0Ah | ok\n"))
	if len(rows3) != 1 || rows3[0].Status != "ok" || rows3[0].Value != "na" {
		t.Errorf("compact sdr = %+v", rows3)
	}
}
