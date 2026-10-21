package firmware

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type mockRunner struct{ lspci string }

func (m *mockRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "lspci" {
		return m.lspci, "", nil
	}
	return "", "", errors.New("not found")
}

const fakeLSPCI = `0000:01:00.0 RAID bus controller [0104]: Broadcom / LSI MegaRAID SAS 9361-8i [1000:005d]
0000:02:00.0 Serial Attached SCSI controller [0107]: Broadcom / LSI SAS3008 [1000:0097]
0000:03:00.0 Ethernet controller [0200]: Mellanox Technologies MT27800 [15b3:1013]
0000:04:00.0 VGA compatible controller [0300]: NVIDIA [10de:1eb8]
`

func TestDetect(t *testing.T) {
	m := &mockRunner{lspci: fakeLSPCI}
	devs, err := Detect(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 3 {
		t.Fatalf("want 3 flashable devices, got %d: %+v", len(devs), devs)
	}
	kinds := map[string]bool{}
	for _, d := range devs {
		kinds[d.Kind] = true
	}
	for _, want := range []string{"raid", "hba", "nic-eth"} {
		if !kinds[want] {
			t.Errorf("missing kind %s", want)
		}
	}
}

// TestDispatchTable locks the vendor's full model->handler dispatch table
// recovered from main.upgradeDevice (fw_upgradeDevice.c hex immediates).
func TestDispatchTable(t *testing.T) {
	cases := map[string]handlerID{
		"9361-8i": h9361,
		"9271-8i": hSimple,
		"SAS3108": hSimple,
		"PM8060":  hPM8060,
		"3008IR":  h3008,
		"3008IT":  h3008,
		"LPE1250": hLPE1250,
		"MCX455A": hMellanox,
		"QLE2670": hQLogic,
		"QLE2690": hQLogic,
		// descriptive names from our own detect paths map onto the same handlers
		"MegaRAID_SAS_9361_8i": h9361,
		"Microchip_PM8060":     hPM8060,
		"LSI_SAS3008":          h3008,
		"Emulex_LPe1250":       hLPE1250,
		"Mellanox_ConnectX":    hMellanox,
		"QLogic_FC_HBA":        hQLogic,
	}
	for model, want := range cases {
		got, ok := dispatch(model)
		if !ok || got != want {
			t.Errorf("dispatch(%q) = %q,%v; want %q", model, got, ok, want)
		}
	}
	if _, ok := dispatch("Unknown_XYZ"); ok {
		t.Error("unknown model must not dispatch")
	}
}

// TestUpgradeStepsVendorAlignment locks every handler's vendor command
// sequence (tool + args + stdin + success check) as recovered in M7.
func TestUpgradeStepsVendorAlignment(t *testing.T) {
	files := map[string]string{
		"main": "/pkg/fw.bin", "interim": "/pkg/interim_fw.rom",
		"legacy": "/pkg/mptsas3.rom", "uefi": "/pkg/mpt3x64.rom",
	}
	type want struct {
		stage, tool string
		args        []string
		stdin       string
		success     string
	}
	cases := []struct {
		model string
		cid   string
		steps []want
	}{
		{"9361-8i", "0", []want{
			{"flash_main", "storcli64", []string{"/c0", "download", "file=/pkg/fw.bin", "noverchk"}, "", "status = success"},
		}},
		{"9271-8i", "1", []want{
			{"flash_main", "storcli64", []string{"/c1", "download", "file=/pkg/fw.bin", "noverchk"}, "", "status = success"},
		}},
		{"SAS3108", "0", []want{
			{"flash_main", "storcli64", []string{"/c0", "download", "file=/pkg/fw.bin", "noverchk"}, "", "status = success"},
		}},
		{"PM8060", "1", []want{
			{"flash_main", "arcconf", []string{"romupdate", "1", "/pkg/fw.bin"}, "y\n", "success"},
		}},
		{"3008IT", "0", []want{
			{"get_sas", "sas3ircu", []string{"0", "display"}, "", ""},
			{"flash_main", "sas3flash", []string{"-o", "-f", "/pkg/fw.bin", "-c", "0"}, "", ""},
			{"flash_legacy", "sas3flash", []string{"-o", "-b", "/pkg/mptsas3.rom", "-c", "0"}, "", ""},
			{"flash_uefi", "sas3flash", []string{"-o", "-b", "/pkg/mpt3x64.rom", "-c", "0"}, "", ""},
			{"write_sas", "sas3flash", []string{"-o", "-sasadd", "{sas_address}", "-c", "0"}, "", ""},
		}},
		{"QLE2670", "", []want{
			{"flash_main", "qaucli", []string{"-b", "all", "/pkg/fw.bin"}, "", "update successful"},
		}},
		{"LPE1250", "", []want{
			{"flash_main", "elxflash", []string{"/f", "/auto", "/q"}, "", ""},
		}},
		{"MCX455A", "/dev/mst/mt4119_pciconf0", []want{
			{"flash_main", "mlxfwmanager", []string{"-u", "-f", "-d", "/dev/mst/mt4119_pciconf0", "-i", "/pkg/fw.bin"}, "", ""},
		}},
	}
	for _, c := range cases {
		d := Device{Model: c.model, ControllerID: c.cid, FWVersionInt: interimThreshold9361 + 1}
		steps, err := UpgradeSteps(d, files)
		if err != nil {
			t.Errorf("%s: %v", c.model, err)
			continue
		}
		if len(steps) != len(c.steps) {
			t.Errorf("%s: want %d steps, got %d (%+v)", c.model, len(c.steps), len(steps), steps)
			continue
		}
		for i, w := range c.steps {
			s := steps[i]
			if s.Stage != w.stage || s.Tool != w.tool || !reflect.DeepEqual(s.Args, w.args) || s.Stdin != w.stdin || s.SuccessContains != w.success {
				t.Errorf("%s step %d:\n got  %+v\n want stage=%s tool=%s args=%v stdin=%q success=%q",
					c.model, i, s, w.stage, w.tool, w.args, w.stdin, w.success)
			}
		}
	}
}

// TestUpgradeSteps9361Interim: below the vendor threshold 0x72038 the
// interim image is flashed first with "resetnow".
func TestUpgradeSteps9361Interim(t *testing.T) {
	if interimThreshold9361 != 0x72038 {
		t.Fatalf("vendor constant drifted: %#x", interimThreshold9361)
	}
	files := map[string]string{"main": "/pkg/MR_4MB.rom", "interim": "/pkg/interim.rom"}
	d := Device{Model: "9361-8i", ControllerID: "0", FWVersionInt: 0x70000}
	steps, err := UpgradeSteps(d, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Stage != "flash_interim" {
		t.Fatalf("want interim+main, got %+v", steps)
	}
	if got := steps[0].Args; !reflect.DeepEqual(got, []string{"/c0", "download", "file=/pkg/interim.rom", "resetnow"}) {
		t.Errorf("interim args = %v", got)
	}
	// Above threshold: no interim step, and no interim file required.
	d.FWVersionInt = 0x73000
	steps, err = UpgradeSteps(d, map[string]string{"main": "/pkg/MR_4MB.rom"})
	if err != nil || len(steps) != 1 {
		t.Fatalf("above threshold: steps=%+v err=%v", steps, err)
	}
}

func TestTargetFWOf(t *testing.T) {
	if got := TargetFWOf("/pkg/MR_4MB_4.680.00-8532.rom"); got != "4.680.00-8532.rom" {
		// vendor regex takes the last _-segment verbatim, extension included
		t.Errorf("TargetFWOf = %q", got)
	}
	if got := TargetFWOf("fw_5.10"); got != "5.10" {
		t.Errorf("TargetFWOf = %q", got)
	}
	if got := TargetFWOf("plainname"); got != "" {
		t.Errorf("TargetFWOf = %q, want empty", got)
	}
}

func TestUpgradePlanHeadline(t *testing.T) {
	plan, err := UpgradePlan(Device{Model: "MegaRAID_SAS_9361_8i", PCIAddr: "0000:01:00.0"}, "/tmp/fw.bin")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"storcli64", "/c0", "download", "file=/tmp/fw.bin", "noverchk"}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("plan = %v, want %v", plan, want)
	}
	if _, err := UpgradePlan(Device{Model: "Unknown_XYZ"}, "/tmp/fw.bin"); err == nil {
		t.Error("unknown model must not get a plan")
	}
}

func TestUpgradeGated(t *testing.T) {
	m := &mockRunner{}
	_, err := Upgrade(context.Background(), m, Device{Model: "MegaRAID_SAS_9361_8i"}, "/tmp/fw.bin", false)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("upgrade must refuse without explicit allow, got %v", err)
	}
}
