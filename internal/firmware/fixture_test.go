// Fixture regression test: `lspci -nn` parsing against a documented
// sample (PCI IDs are real; see testdata/lspci-nn.txt header).
package firmware

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyLineFixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "lspci-nn.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var devs []Device
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if d, ok := classifyLine(line); ok {
			devs = append(devs, d)
		}
	}
	if len(devs) != 4 {
		t.Fatalf("classified %d devices, want 4: %+v", len(devs), devs)
	}
	// 1000:005d must resolve via PCI ID even if the text were localised.
	if devs[0].Kind != "raid" || devs[0].Model != "MegaRAID_SAS_9361_8i" {
		t.Errorf("dev0 = %+v", devs[0])
	}
	if devs[0].PCIAddr != "0000:3b:00.0" {
		t.Errorf("dev0 PCIAddr = %q", devs[0].PCIAddr)
	}
	if devs[1].Model != "Dell_PERC_H730" {
		t.Errorf("dev1 = %+v", devs[1])
	}
	if devs[2].Kind != "nvme" {
		t.Errorf("dev2 = %+v", devs[2])
	}
	if devs[3].Kind != "nic-eth" {
		t.Errorf("dev3 = %+v", devs[3])
	}
	// Keyword fallback still works for IDs not in the table.
	d, ok := classifyLine("0000:01:00.0 RAID bus controller [0104]: Broadcom MegaRAID SAS 9361-8i [ffff:ffff] (rev 02)")
	if !ok || d.Model != "MegaRAID_SAS_9361_8i" {
		t.Errorf("keyword fallback = %+v, %v", d, ok)
	}
}
