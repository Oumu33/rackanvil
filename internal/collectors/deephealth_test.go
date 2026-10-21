package collectors

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectDeepEDAC(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "devices/system/edac/mc/mc0/ce_count"), "42\n")
	writeFile(t, filepath.Join(root, "devices/system/edac/mc/mc0/ue_count"), "1\n")
	writeFile(t, filepath.Join(root, "devices/system/edac/mc/mc1/ce_count"), "8\n")
	writeFile(t, filepath.Join(root, "devices/system/edac/mc/mc1/ue_count"), "0\n")
	d := CollectDeep(root)
	if d.MemCE != 50 || d.MemUE != 1 {
		t.Fatalf("got CE=%d UE=%d, want 50/1", d.MemCE, d.MemUE)
	}
	f := AnalyzeDeep(d)
	if WorstSeverity(f) != "critical" {
		t.Fatalf("UE>0 must be critical, got %v", f)
	}
}

func TestCollectDeepAER(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bus/pci/devices/0000:00:01.0/aer_dev_correctable"), "5\n")
	writeFile(t, filepath.Join(root, "bus/pci/devices/0000:00:01.0/aer_dev_fatal"), "0\n")
	writeFile(t, filepath.Join(root, "bus/pci/devices/0000:00:01.0/aer_dev_nonfatal"), "2\n")
	d := CollectDeep(root)
	if d.AERCorrectable != 5 || d.AERNonFatal != 2 {
		t.Fatalf("got %+v", d)
	}
	f := AnalyzeDeep(d)
	if WorstSeverity(f) != "warning" {
		t.Fatalf("nonfatal AER must warn, got %v", f)
	}
}

func TestAnalyzeNvmeSpare(t *testing.T) {
	f := AnalyzeNvmeSpare("nvme0", map[string]int64{"avail_spare": 3})
	if WorstSeverity(f) != "critical" {
		t.Fatalf("spare=3%% must be critical, got %v", f)
	}
	f = AnalyzeNvmeSpare("nvme0", map[string]int64{"avail_spare": 80})
	if len(f) != 0 {
		t.Fatalf("spare=80%% must be silent, got %v", f)
	}
	f = AnalyzeNvmeSpare("nvme0", map[string]int64{})
	if len(f) != 0 {
		t.Fatalf("absent signal must be silent, got %v", f)
	}
}
