package collectors

import (
	"os"
	"testing"
)

// Fixtures in testdata/ are SYNTHETIC samples modeled on the output
// formats the vendor's pxe-agent consumes (M10 recovery); they are
// not captured from vendor hardware.

func TestParseLsblkJSON(t *testing.T) {
	data, err := os.ReadFile("testdata/lsblk.json")
	if err != nil {
		t.Fatal(err)
	}
	devs, err := ParseLsblkJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("want 2 top devices, got %d", len(devs))
	}
	sda := devs[0]
	if sda.Name != "sda" || sda.Model != "ST8000NM0055" || sda.Serial != "ZA1B2C3D" {
		t.Fatalf("sda mismatch: %+v", sda)
	}
	if sda.SizeBytes < 7_000_000_000_000 {
		t.Fatalf("human size parse off: %d", sda.SizeBytes)
	}
	if len(sda.Children) != 2 || len(sda.Children[1].Children) != 1 {
		t.Fatalf("partition tree mismatch: %+v", sda.Children)
	}
	if sda.Children[0].Mountpoint != "/boot" {
		t.Fatalf("child mountpoint: %+v", sda.Children[0])
	}
	if devs[1].SizeBytes != 8001563222016 {
		t.Fatalf("numeric size: %d", devs[1].SizeBytes)
	}
}

func TestParseStorcliDrives(t *testing.T) {
	data, err := os.ReadFile("testdata/storcli-drives.txt")
	if err != nil {
		t.Fatal(err)
	}
	drives := ParseStorcliDrives(string(data))
	if len(drives) != 3 {
		t.Fatalf("want 3 drives, got %d: %+v", len(drives), drives)
	}
	if drives[0].EnclosureSlot != "252:0" || drives[0].DeviceID != 9 || drives[0].State != "Onln" {
		t.Fatalf("row0: %+v", drives[0])
	}
	if drives[0].Size != "7.276 TB" || drives[0].Interface != "SATA" || drives[0].Medium != "HDD" || drives[0].Model != "ST8000NM0055" {
		t.Fatalf("row0 fields: %+v", drives[0])
	}
	if drives[2].State != "UBad" || drives[2].Interface != "SAS" || drives[2].Medium != "SSD" {
		t.Fatalf("row2 (DG=-) must parse: %+v", drives[2])
	}
}

func TestParseSas3ircuDisplay(t *testing.T) {
	data, err := os.ReadFile("testdata/sas3ircu-display.txt")
	if err != nil {
		t.Fatal(err)
	}
	drives := ParseSas3ircuDisplay(string(data))
	if len(drives) != 2 {
		t.Fatalf("want 2 hard disks (enclosure skipped), got %d: %+v", len(drives), drives)
	}
	d0 := drives[0]
	if d0.Enclosure != "1" || d0.Slot != "0" || d0.State != "Optimal (OPT)" {
		t.Fatalf("d0: %+v", d0)
	}
	if d0.SizeMB != 7630885 || d0.Manufacturer != "HGST" || d0.Model != "HUH721010ALE600" || d0.Protocol != "SAS" {
		t.Fatalf("d0 fields: %+v", d0)
	}
	if drives[1].Protocol != "SATA" || drives[1].DriveType != "SATA_SSD" {
		t.Fatalf("d1: %+v", drives[1])
	}
}

func TestWipeSteps(t *testing.T) {
	steps := WipeSteps("/dev/sdb", 15628053167)
	if len(steps) != 7 {
		t.Fatalf("want 7 steps with tail, got %d", len(steps))
	}
	if steps[0].Name != "dd" || steps[0].Args[3] != "count=204800" {
		t.Fatalf("head dd: %+v", steps[0])
	}
	wantSeek := "seek=15627848367" // total - 204800
	if steps[1].Args[3] != wantSeek {
		t.Fatalf("tail dd seek: %v want %s", steps[1].Args, wantSeek)
	}
	order := []string{"dd", "dd", "wipefs", "swapoff", "vgchange", "mdadm", "partprobe"}
	for i, s := range steps {
		if s.Name != order[i] {
			t.Fatalf("step %d = %s, want %s (vendor order)", i, s.Name, order[i])
		}
	}
	small := WipeSteps("/dev/sdc", 1000)
	if len(small) != 6 {
		t.Fatalf("unknown-size disk must omit tail step, got %d", len(small))
	}
}
