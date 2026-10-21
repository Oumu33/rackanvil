// Package firmware implements RAID/HBA/NIC firmware upgrade orchestration.
//
// M7 alignment (2026-10-21): the dispatch table, tool names, argument
// templates, file patterns, stage flow and success checks below were aligned
// to the vendor fw_agent's production-verified data, recovered from the
// vendor binary itself:
//   - fw_upgradeDevice.c (Ghidra decompile of main.upgradeDevice,
//     fw_agent/main.go:192): model dispatch — the model string is compared
//     verbatim: "3008IR"/"3008IT"→upgradeHBA3008, "PM8060"→upgradeRAIDPM8060,
//     "9271-8i"/"SAS3108"→upgradeRAIDSimple, "9361-8i"→upgradeRAID9361,
//     "LPE1250"→upgradeHBALPE1250, "MCX455A"→upgradeHBAMellanox,
//     "QLE2670"/"QLE2690"→upgradeHBAQlogic. TargetFW is taken from the
//     firmware file name via `_([^_]+)$`; when !force and
//     CurrentFW == TargetFW (and CurrentFW != "N/A"), the upgrade is
//     skipped as already-done.
//   - Ghidra decompiles of the 7 handlers + detectDevices (M7 session):
//     every tool path is filepath.Join(toolsDir, name) with name in
//     {storcli64, arcconf, sas3ircu, sas3flash, qaucli, elxflash,
//     mlxfwmanager, mst, flint}; arguments, stdin and output checks are
//     reproduced per handler in UpgradeSteps.
//   - detectDevices probes the vendor tools directly (no lspci): the exact
//     list/detail commands and parse regexes are reproduced in
//     DetectWithTools.
//
// This remains a clean-room reimplementation: behavior and data are aligned
// to the vendor's verified values, all code is original, and upgrade
// execution is still gated (allowUpgrade) and was never run against real
// cards in this environment.
package firmware

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Runner executes commands.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// Device is one detected flashable controller.
type Device struct {
	Kind    string `json:"kind"`  // "raid" | "hba" | "nic-fc" | "nic-eth"
	Model   string `json:"model"` // vendor model string, e.g. "9361-8i", "MegaRAID_SAS_9361_8i"
	PCIAddr string `json:"pci_addr"`
	Driver  string `json:"driver"`
	FW      string `json:"fw"` // current firmware version, if known
	// ControllerID is the vendor DeviceInfo.ControllerID: the controller
	// index for storcli64/arcconf/sas3ircu/sas3flash ("/c%s" etc.), and the
	// MST device path (/dev/mst/...) for Mellanox.
	ControllerID string `json:"controller_id"`
	// FWVersionInt is VersionToInt(FW), the vendor's numeric version used
	// for the 9361 interim-flash decision. 0 = unknown.
	FWVersionInt int64 `json:"fw_version_int"`
}

// ---------------------------------------------------------------------------
// Dispatch (vendor upgradeDevice, fw_agent/main.go:192)
// ---------------------------------------------------------------------------

// handlerID identifies one of the vendor's 7 upgrade paths.
type handlerID string

const (
	h9361     handlerID = "9361"
	hSimple   handlerID = "simple"
	hPM8060   handlerID = "pm8060"
	h3008     handlerID = "3008"
	hQLogic   handlerID = "qlogic"
	hLPE1250  handlerID = "lpe1250"
	hMellanox handlerID = "mellanox"
)

// dispatch maps a model string to a vendor handler. The vendor compares the
// exact model strings listed in the package doc; our detect paths may also
// produce descriptive names ("MegaRAID_SAS_9361_8i"), so after the exact
// table we fall back to keyword containment onto the same handlers.
func dispatch(model string) (handlerID, bool) {
	switch model {
	case "9361-8i":
		return h9361, true
	case "9271-8i", "SAS3108":
		return hSimple, true
	case "PM8060":
		return hPM8060, true
	case "3008IR", "3008IT":
		return h3008, true
	case "LPE1250":
		return hLPE1250, true
	case "MCX455A":
		return hMellanox, true
	case "QLE2670", "QLE2690":
		return hQLogic, true
	}
	up := strings.ToUpper(model)
	switch {
	case strings.Contains(up, "9361"):
		return h9361, true
	case strings.Contains(up, "9271"), strings.Contains(up, "3108"):
		return hSimple, true
	case strings.Contains(up, "PM8060"), strings.Contains(up, "SMARTRAID"):
		return hPM8060, true
	case strings.Contains(up, "3008"):
		return h3008, true
	case strings.Contains(up, "LPE1250"), strings.Contains(up, "EMULEX") && strings.Contains(up, "1250"):
		return hLPE1250, true
	case strings.HasPrefix(up, "QLE26"), strings.Contains(up, "QLE26"), strings.Contains(up, "QLOGIC"):
		return hQLogic, true
	case strings.Contains(up, "MCX455"), strings.Contains(up, "MELLANOX"), strings.Contains(up, "CONNECTX"):
		return hMellanox, true
	}
	return "", false
}

// modelSuffixRE is the vendor's TargetFW extraction: `_([^_]+)$` applied to
// the firmware file's base name (main.upgradeDevice).
var modelSuffixRE = regexp.MustCompile(`_([^_]+)$`)

// TargetFWOf extracts the target firmware version from a firmware file name
// exactly as the vendor does.
func TargetFWOf(fwFile string) string {
	m := modelSuffixRE.FindStringSubmatch(filepath.Base(fwFile))
	if m == nil {
		return ""
	}
	return m[1]
}

// VersionToInt converts a dotted firmware version to the vendor's numeric
// form. The vendor calls it versionToInt and compares the 9361 current FW
// against 0x72038 to decide whether the interim image must be flashed
// first. The comparison constant and direction are recovered verbatim
// from the decompile; the packing itself was not fully recoverable, so
// we use major<<16 | minor<<8 | patch as a documented reconstruction and
// the gate is therefore approximate for real version strings.
func VersionToInt(v string) int64 {
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	var n int64
	shift := uint(16)
	for i, p := range parts {
		if i >= 3 {
			break
		}
		x, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			if i == 0 {
				return -1
			}
			break
		}
		n |= x << shift
		shift -= 8
	}
	return n
}

// interimThreshold9361 is the vendor's verbatim comparison constant from
// main.upgradeRAID9361: CurrentFW versionToInt < 0x72038 => flash the
// interim image before the main image.
const interimThreshold9361 = 0x72038

// FindFileByPattern mirrors the vendor's main.findFileByPattern: the first
// file in dir whose name matches the regex.
func FindFileByPattern(dir, pattern string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if re.MatchString(n) {
			return filepath.Join(dir, n), nil
		}
	}
	return "", fmt.Errorf("no file matching %q in %s", pattern, dir)
}

// filePatterns lists, per handler, the vendor findFileByPattern patterns.
// Keys: main, interim, legacy, uefi.
func filePatterns(h handlerID) map[string]string {
	switch h {
	case h9361:
		return map[string]string{"main": `MR_4MB\.rom`, "interim": `.*interim.*\.rom`}
	case hSimple:
		return map[string]string{"main": `\.rom$`}
	case hPM8060:
		return map[string]string{"main": `\.ufi$`}
	case h3008:
		return map[string]string{"main": `\.bin$`, "legacy": `mptsas3\.rom`, "uefi": `mpt3x64\.rom`}
	case hQLogic, hMellanox:
		return map[string]string{"main": `\.bin$`}
	case hLPE1250:
		return nil // elxflash scans the working directory itself (/f /auto)
	}
	return nil
}

// Step is one command in a vendor upgrade sequence.
type Step struct {
	Stage           string // vendor sendStatus stage: get_sas|flash_main|flash_legacy|flash_uefi|write_sas|flash_interim
	Tool            string // binary name (joined under toolsDir by the runner side)
	Args            []string
	Stdin           string // fed to the process (PM8060: "y\n")
	SuccessContains string // lowercased substring required in combined output; "" = exit status only
	CaptureSAS      bool   // parse the SAS address from output (vendor regex) for the write_sas step
	Note            string
}

// sasAddressRE is the vendor's own extraction regex (fw_agent strings):
// `SAS Address\s+:\s+([0-9a-fA-F]{16})`.
var sasAddressRE = regexp.MustCompile(`SAS Address\s+:\s+([0-9a-fA-F]{16})`)

// UpgradeSteps builds the vendor's full command sequence for a device.
// files maps pattern keys (see filePatterns) to resolved file paths, as the
// vendor resolves them at runtime with findFileByPattern inside the
// firmware package directory.
func UpgradeSteps(d Device, files map[string]string) ([]Step, error) {
	h, ok := dispatch(d.Model)
	if !ok {
		return nil, fmt.Errorf("no upgrade path for model %q", d.Model)
	}
	cid := d.ControllerID
	if cid == "" {
		cid = "0"
	}
	need := func(key string) (string, error) {
		f := files[key]
		if f == "" {
			return "", fmt.Errorf("model %s: firmware file %q not resolved (vendor pattern %q)", d.Model, key, filePatterns(h)[key])
		}
		return f, nil
	}
	switch h {
	case h9361:
		main, err := need("main")
		if err != nil {
			return nil, err
		}
		var steps []Step
		// Vendor: interim first when versionToInt(CurrentFW) < 0x72038,
		// with "resetnow"; the main flash then uses "noverchk".
		if d.FWVersionInt > 0 && d.FWVersionInt < interimThreshold9361 {
			interim, err := need("interim")
			if err != nil {
				return nil, err
			}
			steps = append(steps, Step{
				Stage: "flash_interim", Tool: "storcli64",
				Args:            []string{"/c" + cid, "download", "file=" + interim, "resetnow"},
				SuccessContains: "status = success",
			})
		}
		steps = append(steps, Step{
			Stage: "flash_main", Tool: "storcli64",
			Args:            []string{"/c" + cid, "download", "file=" + main, "noverchk"},
			SuccessContains: "status = success",
		})
		return steps, nil
	case hSimple:
		main, err := need("main")
		if err != nil {
			return nil, err
		}
		return []Step{{
			Stage: "flash_main", Tool: "storcli64",
			Args:            []string{"/c" + cid, "download", "file=" + main, "noverchk"},
			SuccessContains: "status = success",
		}}, nil
	case hPM8060:
		main, err := need("main")
		if err != nil {
			return nil, err
		}
		// Vendor runs exec.Command directly (not runCommand) and feeds the
		// confirmation "y\n" on stdin; success = output contains "success".
		return []Step{{
			Stage: "flash_main", Tool: "arcconf",
			Args:            []string{"romupdate", cid, main},
			Stdin:           "y\n",
			SuccessContains: "success",
		}}, nil
	case h3008:
		main, err := need("main")
		if err != nil {
			return nil, err
		}
		legacy, err := need("legacy")
		if err != nil {
			return nil, err
		}
		uefi, err := need("uefi")
		if err != nil {
			return nil, err
		}
		return []Step{
			{Stage: "get_sas", Tool: "sas3ircu", Args: []string{cid, "display"}, CaptureSAS: true,
				Note: "vendor reads the current SAS address first and writes it back after flashing"},
			{Stage: "flash_main", Tool: "sas3flash", Args: []string{"-o", "-f", main, "-c", cid}},
			{Stage: "flash_legacy", Tool: "sas3flash", Args: []string{"-o", "-b", legacy, "-c", cid}},
			{Stage: "flash_uefi", Tool: "sas3flash", Args: []string{"-o", "-b", uefi, "-c", cid}},
			{Stage: "write_sas", Tool: "sas3flash", Args: []string{"-o", "-sasadd", "{sas_address}", "-c", cid},
				Note: "{sas_address} is substituted from the get_sas step output"},
		}, nil
	case hQLogic:
		main, err := need("main")
		if err != nil {
			return nil, err
		}
		return []Step{{
			Stage: "flash_main", Tool: "qaucli",
			Args:            []string{"-b", "all", main},
			SuccessContains: "update successful",
		}}, nil
	case hLPE1250:
		// Vendor passes no file argument: elxflash /f /auto scans the
		// firmware package directory (the agent's CWD) itself.
		// M8 evidence for the delivery mechanism (decompiled
		// main.upgradeHBALPE1250 @0x515380 + main.runCommand @0x510640 in
		// fw_agent; fwupgrade.StartUpgradeProcess in the server): the
		// handler ignores the firmware-file argument entirely and never
		// chdirs; runCommand does not set Cmd.Dir, so elxflash inherits
		// fw_agent's working directory. The server stages the run on the
		// target host: `mkdir -p <dir> && chmod 700 <dir>`,
		// `unzip -o <pkg> -d <dir>`, writes DEVICE_JSON to a
		// `.device_env_<n>` file, then runs
		// `DEVICE_JSON=$(cat <env>) <dir>/fw_agent --tools-dir=<tools>
		// --action=upgrade --d=<dir> <fwfile>` from that directory — the
		// unzipped firmware file in the CWD is what /auto picks up.
		return []Step{{
			Stage: "flash_main", Tool: "elxflash",
			Args: []string{"/f", "/auto", "/q"},
			Note: "no file argument in the vendor invocation; the firmware file must be present in the process working directory (vendor stages it by unzipping the package into the run directory)",
		}}, nil
	case hMellanox:
		main, err := need("main")
		if err != nil {
			return nil, err
		}
		return []Step{{
			Stage: "flash_main", Tool: "mlxfwmanager",
			Args: []string{"-u", "-f", "-d", cid, "-i", main},
			Note: "ControllerID carries the MST device path (/dev/mst/...) for Mellanox",
		}}, nil
	}
	return nil, fmt.Errorf("unreachable handler %q", h)
}

// UpgradePlan returns the primary flash command for a device (the vendor's
// flash_main step). Kept for callers that only need the headline command;
// the full sequence is UpgradeSteps.
func UpgradePlan(d Device, fwFile string) ([]string, error) {
	if fwFile == "" {
		return nil, fmt.Errorf("firmware file required")
	}
	steps, err := UpgradeSteps(d, map[string]string{"main": fwFile})
	if err != nil {
		return nil, err
	}
	for _, s := range steps {
		if s.Stage == "flash_main" {
			return append([]string{s.Tool}, s.Args...), nil
		}
	}
	s := steps[0]
	return append([]string{s.Tool}, s.Args...), nil
}

// ResolveFiles resolves a handler's vendor file patterns inside a firmware
// package directory.
func ResolveFiles(d Device, dir string) (map[string]string, error) {
	h, ok := dispatch(d.Model)
	if !ok {
		return nil, fmt.Errorf("no upgrade path for model %q", d.Model)
	}
	out := map[string]string{}
	for key, pat := range filePatterns(h) {
		if key == "interim" {
			// Interim is conditional; absence is only an error when the
			// version gate actually requires it (checked in UpgradeSteps).
			if f, err := FindFileByPattern(dir, pat); err == nil {
				out[key] = f
			}
			continue
		}
		f, err := FindFileByPattern(dir, pat)
		if err != nil {
			return nil, err
		}
		out[key] = f
	}
	return out, nil
}

// Upgrade runs the vendor step sequence. allowUpgrade=false always refuses:
// flashing needs real hardware and the right image, and a wrong flash
// bricks the card. fwPath may be a single firmware file or a package
// directory (patterns resolved per handler, like the vendor).
func Upgrade(ctx context.Context, r Runner, d Device, fwPath string, allowUpgrade bool) (string, error) {
	if !allowUpgrade {
		return "", fmt.Errorf("refusing: firmware upgrade requires allowUpgrade=true and real hardware (bricking risk)")
	}
	var files map[string]string
	if st, err := os.Stat(fwPath); err == nil && st.IsDir() {
		var err error
		files, err = ResolveFiles(d, fwPath)
		if err != nil {
			return "", err
		}
	} else {
		files = map[string]string{"main": fwPath}
	}
	steps, err := UpgradeSteps(d, files)
	if err != nil {
		return "", err
	}
	var log strings.Builder
	sas := ""
	for _, s := range steps {
		args := make([]string, len(s.Args))
		for i, a := range s.Args {
			args[i] = strings.ReplaceAll(a, "{sas_address}", sas)
		}
		out, se, err := runStep(ctx, r, s, args)
		fmt.Fprintf(&log, "[%s] %s %s\n%s%s", s.Stage, s.Tool, strings.Join(args, " "), out, se)
		if err != nil {
			return log.String(), fmt.Errorf("stage %s: %s: %w", s.Stage, s.Tool, err)
		}
		if s.SuccessContains != "" && !strings.Contains(strings.ToLower(out+se), s.SuccessContains) {
			return log.String(), fmt.Errorf("stage %s: %s output missing %q", s.Stage, s.Tool, s.SuccessContains)
		}
		if s.CaptureSAS {
			if m := sasAddressRE.FindStringSubmatch(out); m != nil {
				sas = m[1]
			}
		}
	}
	return log.String(), nil
}

// runStep executes one step. The Runner interface has no stdin; steps that
// need stdin (PM8060's "y\n") require the runner to implement stdinRunner.
// When it cannot, we refuse rather than downgrade to a shell.
type stdinRunner interface {
	RunStdin(ctx context.Context, stdin, name string, args ...string) (string, string, error)
}

func runStep(ctx context.Context, r Runner, s Step, args []string) (string, string, error) {
	if s.Stdin != "" {
		if sr, ok := r.(stdinRunner); ok {
			return sr.RunStdin(ctx, s.Stdin, s.Tool, args...)
		}
		return "", "", fmt.Errorf("step needs stdin %q but runner cannot provide it", s.Stdin)
	}
	return r.Run(ctx, s.Tool, args...)
}

// ---------------------------------------------------------------------------
// Detect — lspci fallback (our original path, kept)
// ---------------------------------------------------------------------------

// Detect parses `lspci -nn -D` output for known flashable families.
func Detect(ctx context.Context, r Runner) ([]Device, error) {
	out, _, err := r.Run(ctx, "lspci", "-nn", "-D")
	if err != nil {
		return nil, fmt.Errorf("lspci: %w", err)
	}
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		if d, ok := classifyLine(line); ok {
			devs = append(devs, d)
		}
	}
	return devs, nil
}

// pciIDKind maps well-known PCI vendor:device IDs to (kind, model).
// IDs are publicly documented (e.g. Broadcom/LSI 1000:005d = MegaRAID
// 9361-8i; the same IDs appear in the H3C storcli sample's "Vendor Id /
// Device Id" fields in internal/raid/testdata/storcli-c0-show.txt).
var pciIDKind = map[string][2]string{
	"1000:005d": {"raid", "MegaRAID_SAS_9361_8i"},
	"1000:0079": {"raid", "Dell_PERC_H730"},
	"1000:0014": {"raid", "MegaRAID_SAS_9271_4i"},
	"1000:005b": {"raid", "MegaRAID_SAS_9271_8i"},
	"1000:0094": {"raid", "MegaRAID_SAS3108"},
	"1000:0097": {"hba", "LSI_SAS3008"},
	"1000:0087": {"hba", "LSI_SAS3008"},
	"1000:00c9": {"hba", "LSI_SAS3416"},
	"9005:028b": {"raid", "Adaptec_ASM2812"},
	"144d:a804": {"nvme", "Samsung_NVMe_SSD"},
	"1b4b:2242": {"nvme", "Marvell_NVMe_SSD"},
	"15b3:1013": {"nic-eth", "Mellanox_ConnectX_4"},
	"15b3:101b": {"nic-eth", "Mellanox_ConnectX_5"},
	"8086:1521": {"nic-eth", "Intel_I350"},
	"1077:8071": {"nic-fc", "QLogic_FC_HBA"},
	"10df:0724": {"nic-fc", "Emulex_LPe16000"},
}

var pciIDRe = regexp.MustCompile(`\[([0-9a-fA-F]{4}):([0-9a-fA-F]{4})\]`)

// classifyLine maps an lspci line to a Device. PCI IDs are matched first
// (stable across locales and kernel driver names); keyword matching is the
// fallback for IDs not in the table above.
func classifyLine(line string) (Device, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return Device{}, false
	}
	pci := fields[0]
	if m := pciIDRe.FindStringSubmatch(line); m != nil {
		if km, ok := pciIDKind[strings.ToLower(m[1]+":"+m[2])]; ok {
			return Device{Kind: km[0], Model: km[1], PCIAddr: pci}, true
		}
	}
	up := strings.ToUpper(line)
	mk := func(kind, model string) (Device, bool) {
		return Device{Kind: kind, Model: model, PCIAddr: pci}, true
	}
	switch {
	case strings.Contains(up, "9361"):
		return mk("raid", "MegaRAID_SAS_9361_8i")
	case strings.Contains(up, "PM8060") || strings.Contains(up, "SMARTRAID"):
		return mk("raid", "Microchip_PM8060")
	case strings.Contains(up, "SAS3008"):
		return mk("hba", "LSI_SAS3008")
	case strings.Contains(up, "QLOGIC"):
		return mk("nic-fc", "QLogic_FC_HBA")
	case strings.Contains(up, "LPE1250") || (strings.Contains(up, "EMULEX") && strings.Contains(up, "1250")):
		return mk("nic-fc", "Emulex_LPe1250")
	case strings.Contains(up, "EMULEX"):
		return mk("nic-fc", "Emulex_FC_HBA")
	case strings.Contains(up, "MELLANOX"):
		return mk("nic-eth", "Mellanox_ConnectX")
	}
	return Device{}, false
}
