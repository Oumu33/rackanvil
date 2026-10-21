// Deep hardware health signals ("硬件故障感知").
//
// Doc basis (vendor article "我之前说它是个装系统的工具，是我说小了"):
// "HrnrOS 看的是更底层的东西：CPU 的 MCE 报错、内存的 CE/UCE 纠错码、
// PCIe 链路的 AER 错误、NVMe 盘的可备用块余量。"
//
// Our clean-room implementation reads the stable kernel sysfs ABIs:
//   - EDAC: /sys/devices/system/edac/mc/mc<N>/{ce_count,ue_count}
//   - PCIe AER: /sys/bus/pci/devices/<bdf>/aer_dev_{correctable,fatal,nonfatal}
//   - NVMe spare: avail_spare from the NVMe health log (see AnalyzeNvme)
//   - MCE: presence of /dev/mcelog or rasdaemon state is noted; decoding
//     MCE banks is out of scope (honestly labeled).
package collectors

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DeepHealth is the beyond-SMART hardware fault picture.
type DeepHealth struct {
	// EDAC memory error counters (sum over all memory controllers).
	MemCE int64 `json:"mem_ce"`
	MemUE int64 `json:"mem_ue"`
	// PCIe AER counters (sum over all PCI devices).
	AERCorrectable int64 `json:"aer_correctable"`
	AERFatal       int64 `json:"aer_fatal"`
	AERNonFatal    int64 `json:"aer_nonfatal"`
	// MCEAvailable notes whether machine-check logging exists.
	MCEAvailable bool `json:"mce_available"`
	// Skipped notes unavailable sources.
	Skipped []string `json:"skipped"`
}

// CollectDeep gathers deep health from sysfsRoot ("/sys" in production).
func CollectDeep(sysfsRoot string) *DeepHealth {
	d := &DeepHealth{}
	d.MemCE, d.MemUE = edacCounts(sysfsRoot)
	d.AERCorrectable, d.AERFatal, d.AERNonFatal = aerCounts(sysfsRoot)
	if _, err := os.Stat("/dev/mcelog"); err == nil {
		d.MCEAvailable = true
	} else {
		d.Skipped = append(d.Skipped, "mce: /dev/mcelog absent (no mcelog/rasdaemon)")
	}
	return d
}

func readInt64(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

// edacCounts sums ce_count/ue_count over /sys/devices/system/edac/mc/*.
func edacCounts(sysfsRoot string) (ce, ue int64) {
	mcs, _ := filepath.Glob(filepath.Join(sysfsRoot, "devices/system/edac/mc/mc*"))
	if len(mcs) == 0 {
		return 0, 0
	}
	for _, mc := range mcs {
		if v, ok := readInt64(filepath.Join(mc, "ce_count")); ok {
			ce += v
		}
		if v, ok := readInt64(filepath.Join(mc, "ue_count")); ok {
			ue += v
		}
	}
	return ce, ue
}

// aerCounts sums PCIe AER counters over all PCI devices.
func aerCounts(sysfsRoot string) (corr, fatal, nonfatal int64) {
	devs, _ := filepath.Glob(filepath.Join(sysfsRoot, "bus/pci/devices/*"))
	for _, dev := range devs {
		if v, ok := readInt64(filepath.Join(dev, "aer_dev_correctable")); ok {
			corr += v
		}
		if v, ok := readInt64(filepath.Join(dev, "aer_dev_fatal")); ok {
			fatal += v
		}
		if v, ok := readInt64(filepath.Join(dev, "aer_dev_nonfatal")); ok {
			nonfatal += v
		}
	}
	return corr, fatal, nonfatal
}

// AnalyzeDeep turns deep signals into findings.
func AnalyzeDeep(d *DeepHealth) []Finding {
	var out []Finding
	add := func(sev, msg string) {
		out = append(out, Finding{Device: "platform", Severity: sev, Message: msg})
	}
	if d.MemUE > 0 {
		add("critical", fmt.Sprintf("EDAC uncorrectable memory errors (UE)=%d: DIMM likely failing", d.MemUE))
	} else if d.MemCE > 100 {
		add("critical", fmt.Sprintf("EDAC correctable errors (CE)=%d: excessive, DIMM degrading", d.MemCE))
	} else if d.MemCE > 0 {
		add("warning", fmt.Sprintf("EDAC correctable errors (CE)=%d observed", d.MemCE))
	}
	if d.AERFatal > 0 {
		add("critical", fmt.Sprintf("PCIe AER fatal errors=%d", d.AERFatal))
	} else if d.AERNonFatal > 0 {
		add("warning", fmt.Sprintf("PCIe AER non-fatal errors=%d", d.AERNonFatal))
	} else if d.AERCorrectable > 1000 {
		add("warning", fmt.Sprintf("PCIe AER correctable errors=%d: link marginal", d.AERCorrectable))
	}
	if len(out) == 0 {
		out = append(out, Finding{Device: "platform", Severity: "ok", Message: "no EDAC/AER anomalies"})
	}
	return out
}

// AnalyzeNvmeSpare extends AnalyzeNvme with the spare-blocks signal
// ("NVMe 盘的可备用块余量" from the vendor doc).
func AnalyzeNvmeSpare(device string, log map[string]int64) []Finding {
	var out []Finding
	spare, ok := log["avail_spare"]
	if !ok {
		return out // signal absent; not an error
	}
	switch {
	case spare <= 5:
		out = append(out, Finding{Device: device, Severity: "critical",
			Message: fmt.Sprintf("NVMe avail_spare=%d%%: replace soon", spare)})
	case spare <= 15:
		out = append(out, Finding{Device: device, Severity: "warning",
			Message: fmt.Sprintf("NVMe avail_spare=%d%%: wearing out", spare)})
	}
	return out
}
