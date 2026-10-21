// Fixture regression tests: real storcli outputs from public sources
// (see testdata/ headers for provenance).
package raid

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

func TestParseShowAllReal(t *testing.T) {
	// REAL sample: prometheus node-exporter fixture (`storcli /cALL show all J`).
	sa, err := ParseShowAll(loadFixture(t, "storcli-show-all.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sa.Controllers) == 0 {
		t.Fatal("no controllers parsed")
	}
	c := sa.Controllers[0]
	if c.Model == "" {
		t.Error("controller model empty")
	}
	t.Logf("controller: id=%s model=%q", c.ID, c.Model)
	// Fixture: PRAID EP420i with VDs and PDs.
	if len(sa.VDs) == 0 {
		t.Error("no VDs parsed from fixture")
	}
	for _, vd := range sa.VDs {
		if vd.RAID == "" || vd.State == "" {
			t.Errorf("VD missing fields: %+v", vd)
		}
	}
	if len(sa.PDs) == 0 {
		t.Error("no PDs parsed from fixture")
	}
	for _, pd := range sa.PDs {
		if pd.Model == "" || pd.Enclosure == "" {
			t.Errorf("PD missing fields: %+v", pd)
		}
	}
	t.Logf("VDs=%d PDs=%d", len(sa.VDs), len(sa.PDs))
}

func TestParseEallSallReal(t *testing.T) {
	// REAL sample: `storcli /cALL/eALL/sALL show all J` — per-drive detail.
	sa, err := ParseShowAll(loadFixture(t, "storcli-eall-sall-show-all.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sa.Controllers) == 0 {
		t.Fatal("no controllers parsed")
	}
}

func TestParseShowTextReal(t *testing.T) {
	// REAL sample: H3C doc `storcli64 /c0 show` (AVAGO MegaRAID 9361-8i).
	model, serial := ParseStorcliShowText(loadFixture(t, "storcli-c0-show.txt"))
	if model != "AVAGO MegaRAID SAS 9361-8i" {
		t.Errorf("model = %q", model)
	}
	if serial != "SK64909030" {
		t.Errorf("serial = %q", serial)
	}
}

func TestParseVDListTextReal(t *testing.T) {
	// REAL sample: Aerospike doc VD LIST table (mix of sized VDs and a
	// named one "r0").
	vds := ParseStorcliVDListText("0", loadFixture(t, "storcli-vall-show.txt"))
	if len(vds) != 4 {
		t.Fatalf("parsed %d VDs, want 4", len(vds))
	}
	if vds[0].ID != "0" || vds[0].RAID != "RAID0" || vds[0].State != "Optl" {
		t.Errorf("vd0 = %+v", vds[0])
	}
	if !strings.Contains(vds[0].Size, "465.25") {
		t.Errorf("vd0 size = %q", vds[0].Size)
	}
	if !strings.Contains(vds[2].Size, "372.0") {
		t.Errorf("vd2 (named r0) size = %q", vds[2].Size)
	}
}
