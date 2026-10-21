package firmware

// DetectWithTools reproduces the vendor fw_agent's main.detectDevices:
// it probes the vendor tools directly (never lspci). Every command and
// parse regex below was recovered from the Ghidra decompile of
// detectDevices plus the fw_agent string table (M7):
//
//	storcli64  show                  list:  (?m)^(\d+)\s+([^\s]+)\s+
//	storcli64  /c{id} show all       FW Version\s+:\s+([^\s]+) / Product Name\s+:\s+(.+)
//	arcconf    list                  list:  (?m)^Controller\s+(\d+)
//	arcconf    getconfig {id} ad     Firmware\s+:\s+([^\s]+)
//	sas3ircu   list                  list:  (?m)^\s*(\d+)\s+SAS.*(1000h)\s+([0-9a-fA-F]{4})
//	sas3ircu   {id} display          Firmware version\s+:\s+([^\s]+)
//	elxflash   /q                    Firmware=([\d\.]+)
//	qaucli     -i                    split on "HBA Model"; :\s*(QLE26\d{2}) / BIOS Version\s*:\s*([\d\.]+)
//	mst        start | status        devices: (?m)^(\/dev\/mst\/mt\d+.*_pciconf0)
//	flint      -d {dev} q            FW Version:\s*([\d\.]+)
//
// Model mapping (vendor): storcli Product Name containing "9361-8i" ->
// "9361-8i", "3108" -> "SAS3108", "9271-8i" -> "9271-8i"; arcconf ->
// "PM8060"; sas3ircu -> SAS3008 family: the IR/IT discriminator is the
// device-ID capture group itself (M8, Ghidra decompile of
// main.detectDevices @0x5109e0 in fw_agent: the captured 4-hex device ID
// is compared byte-wise against "000b", "0012" and "000B" -> "3008IR",
// anything else -> "3008IT"; every sas3ircu match yields one of the two
// model strings); elxflash -> "LPE1250"; qaucli -> the QLE26xx capture;
// Mellanox -> "MCX455A".
//
// Probes are best-effort: a missing/failing tool skips its family (the
// vendor's error policy per probe was not recoverable).

import (
	"context"
	"regexp"
	"strings"
)

var (
	storcliListRE = regexp.MustCompile(`(?m)^(\d+)\s+([^\s]+)\s+`)
	storcliFWRE   = regexp.MustCompile(`FW Version\s+:\s+([^\s]+)`)
	storcliProdRE = regexp.MustCompile(`Product Name\s+:\s+(.+)`)
	arcconfListRE = regexp.MustCompile(`(?m)^Controller\s+(\d+)`)
	arcconfFWRE   = regexp.MustCompile(`Firmware\s+:\s+([^\s]+)`)
	sas3ListRE    = regexp.MustCompile(`(?m)^\s*(\d+)\s+SAS.*(1000h)\s+([0-9a-fA-F]{4})`)
	sas3FWRE      = regexp.MustCompile(`Firmware version\s+:\s+([^\s]+)`)
	elxFWRE       = regexp.MustCompile(`Firmware=([\d\.]+)`)
	qlogicModelRE = regexp.MustCompile(`:\s*(QLE26\d{2})`)
	qlogicBIOSRE  = regexp.MustCompile(`BIOS Version\s*:\s*([\d\.]+)`)
	mstDeviceRE   = regexp.MustCompile(`(?m)^(\/dev\/mst\/mt\d+.*_pciconf0)`)
	flintFWRE     = regexp.MustCompile(`FW Version:\s*([\d\.]+)`)
)

// SAS3008Model maps the sas3ircu list device-ID capture to the vendor
// fw_agent's model string (M8 evidence: decompiled main.detectDevices —
// the capture is compared as raw bytes against "000b"/"0012"/"000B";
// the comparison is case-sensitive at the byte level, both spellings of
// the 0x000B ID appear in the vendor's compare set).
func SAS3008Model(devID string) string {
	switch devID {
	case "000b", "0012", "000B":
		return "3008IR"
	}
	return "3008IT"
}

// DetectWithTools runs the vendor tool probes. toolsDir is where the
// vendor tools live (the vendor joins toolsDir + tool name; "" = PATH).
func DetectWithTools(ctx context.Context, r Runner, toolsDir string) ([]Device, error) {
	tool := func(name string) string {
		if toolsDir == "" {
			return name
		}
		return toolsDir + "/" + name
	}
	var devs []Device

	// --- Broadcom MegaRAID via storcli64 ---
	if out, _, err := r.Run(ctx, tool("storcli64"), "show"); err == nil {
		for _, m := range storcliListRE.FindAllStringSubmatch(out, -1) {
			id := m[1]
			d := Device{Kind: "raid", ControllerID: id}
			if detail, _, err := r.Run(ctx, tool("storcli64"), "/c"+id, "show", "all"); err == nil {
				if fm := storcliFWRE.FindStringSubmatch(detail); fm != nil {
					d.FW = fm[1]
					d.FWVersionInt = VersionToInt(fm[1])
				}
				if pm := storcliProdRE.FindStringSubmatch(detail); pm != nil {
					prod := strings.TrimSpace(pm[1])
					switch {
					case strings.Contains(prod, "9361-8i"):
						d.Model = "9361-8i"
					case strings.Contains(prod, "3108"):
						d.Model = "SAS3108"
					case strings.Contains(prod, "9271-8i"):
						d.Model = "9271-8i"
					default:
						d.Model = prod
					}
				}
			}
			if d.Model == "" {
				d.Model = m[2]
			}
			devs = append(devs, d)
		}
	}

	// --- Microchip SmartRAID via arcconf ---
	if out, _, err := r.Run(ctx, tool("arcconf"), "list"); err == nil {
		for _, m := range arcconfListRE.FindAllStringSubmatch(out, -1) {
			id := m[1]
			d := Device{Kind: "raid", Model: "PM8060", ControllerID: id}
			if detail, _, err := r.Run(ctx, tool("arcconf"), "getconfig", id, "ad"); err == nil {
				if fm := arcconfFWRE.FindStringSubmatch(detail); fm != nil {
					d.FW = fm[1]
					d.FWVersionInt = VersionToInt(fm[1])
				}
			}
			devs = append(devs, d)
		}
	}

	// --- LSI SAS3 HBA via sas3ircu ---
	if out, _, err := r.Run(ctx, tool("sas3ircu"), "list"); err == nil {
		for _, m := range sas3ListRE.FindAllStringSubmatch(out, -1) {
			id, devID := m[1], m[3]
			d := Device{Kind: "hba", ControllerID: id, Model: SAS3008Model(devID)}
			if detail, _, err := r.Run(ctx, tool("sas3ircu"), id, "display"); err == nil {
				if fm := sas3FWRE.FindStringSubmatch(detail); fm != nil {
					d.FW = fm[1]
					d.FWVersionInt = VersionToInt(fm[1])
				}
			}
			devs = append(devs, d)
		}
	}

	// --- Emulex via elxflash /q ---
	if out, _, err := r.Run(ctx, tool("elxflash"), "/q"); err == nil {
		if strings.Contains(out, "LPE") {
			d := Device{Kind: "nic-fc", Model: "LPE1250"}
			if fm := elxFWRE.FindStringSubmatch(out); fm != nil {
				d.FW = fm[1]
				d.FWVersionInt = VersionToInt(fm[1])
			}
			devs = append(devs, d)
		}
	}

	// --- QLogic via qaucli -i ---
	if out, _, err := r.Run(ctx, tool("qaucli"), "-i"); err == nil {
		for _, section := range strings.Split(out, "HBA Model") {
			mm := qlogicModelRE.FindStringSubmatch(section)
			if mm == nil {
				continue
			}
			d := Device{Kind: "nic-fc", Model: mm[1]}
			if bm := qlogicBIOSRE.FindStringSubmatch(section); bm != nil {
				d.FW = bm[1]
				d.FWVersionInt = VersionToInt(bm[1])
			}
			devs = append(devs, d)
		}
	}

	// --- Mellanox via mst + flint ---
	_, _, _ = r.Run(ctx, tool("mst"), "start")
	if out, _, err := r.Run(ctx, tool("mst"), "status"); err == nil {
		for _, m := range mstDeviceRE.FindAllStringSubmatch(out, -1) {
			dev := strings.TrimSpace(m[1])
			d := Device{Kind: "nic-eth", Model: "MCX455A", ControllerID: dev}
			if q, _, err := r.Run(ctx, tool("flint"), "-d", dev, "q"); err == nil {
				if fm := flintFWRE.FindStringSubmatch(q); fm != nil {
					d.FW = fm[1]
					d.FWVersionInt = VersionToInt(fm[1])
				}
			}
			devs = append(devs, d)
		}
	}

	return devs, nil
}
