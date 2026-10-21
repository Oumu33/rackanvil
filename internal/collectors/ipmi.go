// IPMI sensor parsing.
//
// `ipmitool sensor` (10-column threshold format) and `ipmitool sdr`
// (3-column compact format) / `sdr elist full` (5-column with reading)
// are the BMC's only text interfaces on hosts without Redfish.
// This parser extracts name/value/unit/status for monitoring; threshold
// columns are intentionally ignored (the BMC already evaluates them into
// the status column).
package collectors

import (
	"strings"
)

// SensorReading is one parsed IPMI sensor row.
type SensorReading struct {
	Name   string // e.g. "CPU Temp", "FAN1", "12V"
	Value  string // raw value, e.g. "50.000"; "na" when absent
	Unit   string // e.g. "degrees C", "RPM", "Volts"; "" when absent
	Status string // "ok", "na", "cr", ...
}

// ParseIpmitoolSensor parses `ipmitool sensor` and `ipmitool sdr[. elist]`
// output. Three real-world layouts are handled:
//
//	sensor (10 col): "CPU Temp | 50.000 | degrees C | ok | 0.000 | ..."
//	sdr      (3 col): "CPU Temp | 0Ah | ok"
//	sdr elist full   : "fp.t_amb | 0Ah | ok | 12.0 | 22 degrees C"
//
// Rows whose name is empty are skipped. "na" values are preserved as-is
// so callers can distinguish "sensor absent" from "reading 0".
func ParseIpmitoolSensor(data []byte) []SensorReading {
	var out []SensorReading
	for _, line := range strings.Split(string(data), "\n") {
		parts := splitPipe(line)
		if len(parts) < 3 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		r := SensorReading{Name: name}
		switch {
		case len(parts) >= 10:
			// `ipmitool sensor`: name | value | unit | status | thresholds...
			r.Value = strings.TrimSpace(parts[1])
			r.Unit = strings.TrimSpace(parts[2])
			r.Status = strings.TrimSpace(parts[3])
		case len(parts) >= 5:
			// `sdr elist full`: name | id | status | entity | reading
			r.Status = strings.TrimSpace(parts[2])
			r.Value, r.Unit = splitReading(parts[4])
		default:
			// `sdr` compact: name | id | status (no reading available)
			r.Status = strings.TrimSpace(parts[2])
			r.Value = "na"
		}
		out = append(out, r)
	}
	return out
}

// splitReading splits "22 degrees C" into ("22", "degrees C").
func splitReading(s string) (value, unit string) {
	s = strings.TrimSpace(s)
	if s == "" || s == "na" {
		return "na", ""
	}
	f := strings.Fields(s)
	if len(f) == 0 {
		return "na", ""
	}
	if len(f) == 1 {
		return f[0], ""
	}
	return f[0], strings.Join(f[1:], " ")
}

func splitPipe(line string) []string {
	return strings.Split(line, "|")
}
