package firmware

// SYNTHETIC samples: the output layouts below were constructed to match the
// vendor detectDevices parse regexes recovered in M7 (the regexes dictate
// the parseable shape). They are NOT captures from real vendor tools.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type toolRunner struct{ outputs map[string]string }

func (m *toolRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	key := name + " " + strings.Join(args, " ")
	if out, ok := m.outputs[key]; ok {
		return out, "", nil
	}
	return "", "", errors.New("not found")
}

func TestDetectWithTools(t *testing.T) {
	r := &toolRunner{outputs: map[string]string{
		"storcli64 show": `Controllers:
0  SAS3108  ...`, // list regex: ^(\d+)\s+([^\s]+)
		"storcli64 /c0 show all":              "Product Name : MegaRAID SAS3108 (9361-8i)\nFW Version : 4.680.00-8532\n",
		"arcconf list":                        "Controllers found: 1\nController 1\n",
		"arcconf getconfig 1 ad":              "Firmware : 3.02\n",
		"sas3ircu list":                       "Index  Type  ...\n 0  SAS3008  PCI  1000h  0097h\n",
		"sas3ircu 0 display":                  "Firmware version : 16.00.00.00\n",
		"elxflash /q":                         "LPE1250 adapter Firmware=3.2.1\n",
		"qaucli -i":                           "HBA Model: QLE2660\nBIOS Version: 3.45\nHBA Model: QLE2692\nBIOS Version: 3.50\n",
		"mst status":                          "/dev/mst/mt4119_pciconf0 - PCI configuration cycles access.\n",
		"flint -d /dev/mst/mt4119_pciconf0 q": "FW Version: 16.32.1010\n",
	}}
	devs, err := DetectWithTools(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]Device{}
	for _, d := range devs {
		byModel[d.Model] = d
	}
	// storcli list sample's product token is SAS3108 but detail Product Name
	// contains 9361-8i first in the vendor's check order -> 9361-8i.
	if d, ok := byModel["9361-8i"]; !ok || d.FW != "4.680.00-8532" || d.ControllerID != "0" {
		t.Errorf("storcli device = %+v (all: %+v)", d, devs)
	}
	if d, ok := byModel["PM8060"]; !ok || d.FW != "3.02" || d.ControllerID != "1" {
		t.Errorf("arcconf device = %+v", d)
	}
	if d, ok := byModel["3008IT"]; !ok || d.FW != "16.00.00.00" {
		t.Errorf("sas3ircu device = %+v", d)
	}
	if d, ok := byModel["LPE1250"]; !ok || d.FW != "3.2.1" {
		t.Errorf("elxflash device = %+v", d)
	}
	if _, ok := byModel["QLE2660"]; !ok {
		t.Errorf("qlogic QLE2660 missing: %+v", devs)
	}
	if _, ok := byModel["QLE2692"]; !ok {
		t.Errorf("qlogic QLE2692 missing: %+v", devs)
	}
	if d, ok := byModel["MCX455A"]; !ok || d.FW != "16.32.1010" || d.ControllerID != "/dev/mst/mt4119_pciconf0" {
		t.Errorf("mellanox device = %+v", d)
	}
}

// M8: SAS3008 IR/IT discrimination — the vendor fw_agent compares the
// sas3ircu device-ID capture byte-wise against "000b"/"0012"/"000B"
// (IR) and defaults to IT (decompiled main.detectDevices).
func TestSAS3008Model(t *testing.T) {
	for devID, want := range map[string]string{
		"000b": "3008IR",
		"000B": "3008IR",
		"0012": "3008IR",
		"0097": "3008IT", // the common SAS3008 HBA device ID
		"0096": "3008IT",
		"00c0": "3008IT",
	} {
		if got := SAS3008Model(devID); got != want {
			t.Errorf("SAS3008Model(%q) = %q, want %q", devID, got, want)
		}
	}

	// End-to-end through DetectWithTools: an IR device ID in the
	// sas3ircu list output must surface as model 3008IR.
	r := &toolRunner{outputs: map[string]string{
		"sas3ircu list":      " 0  SAS3008  PCI  1000h  000Bh\n",
		"sas3ircu 0 display": "Firmware version : 16.00.00.00\n",
	}}
	devs, err := DetectWithTools(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Model != "3008IR" {
		t.Fatalf("IR detect = %+v", devs)
	}
}
