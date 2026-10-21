package osimage

import "testing"

func TestDefinitionsLoad(t *testing.T) {
	defs, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 54 {
		t.Fatalf("want 54 vendor definitions, got %d", len(defs))
	}
}
// M11: the proxmox definition shipped disabled (.bak) in the vendor
// build; it must load (catalogue fidelity) but never match.
func TestProxmoxDefinitionDisabled(t *testing.T) {
	defs, err := Definitions()
	if err != nil {
		t.Fatal(err)
	}
	var prox *Definition
	for i := range defs {
		if defs[i].ID == "proxmox-ve" {
			prox = &defs[i]
		}
		if defs[i].Disabled && defs[i].ID != "proxmox-ve" {
			t.Fatalf("unexpected disabled definition: %s", defs[i].ID)
		}
	}
	if prox == nil {
		t.Fatal("proxmox-ve definition missing from catalogue")
	}
	if !prox.Disabled {
		t.Fatal("proxmox-ve must be flagged Disabled (shipped as .bak)")
	}
	files := map[string]string{
		"EULA":           "Proxmox VE EULA",
		"boot/linux26":   "",
		"boot/initrd.img": "",
	}
	if d, ok := Identify(files); ok {
		t.Fatalf("disabled definition must not identify, got %+v", d)
	}
}

func TestIdentifyDebian12(t *testing.T) {	files := map[string]string{
		".disk/info":            "Debian GNU/Linux 12.7 amd64 DVD",
		"install.amd/vmlinuz":   "",
		"install.amd/initrd.gz": "",
	}
	d, ok := Identify(files)
	if !ok || d.ID != "Debian12" || d.Family != "Debian" || d.MajorVersion != 12 {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
}

func TestIdentifyRHEL9(t *testing.T) {
	files := map[string]string{
		".discinfo":                 "Red Hat Enterprise Linux 9.4\nx86_64",
		"images/pxeboot/vmlinuz":    "",
		"images/pxeboot/initrd.img": "",
		"LICENSE":                   "",
	}
	d, ok := Identify(files)
	if !ok || d.Family != "rhel" && d.Family != "RedHat" && d.ID == "" {
		t.Fatalf("got %+v ok=%v", d, ok)
	}
	if !ok {
		t.Fatalf("RHEL9 tree not identified")
	}
}

func TestIdentifyRejectsIncomplete(t *testing.T) {
	files := map[string]string{".disk/info": "Debian GNU/Linux 12.7 amd64 DVD"}
	if _, ok := Identify(files); ok {
		t.Fatal("missing validation files must not identify")
	}
}
