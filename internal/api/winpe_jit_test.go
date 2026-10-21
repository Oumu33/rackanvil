package api

import (
	"strings"
	"testing"
)

func TestRenderInstallBatVerbatimFragments(t *testing.T) {
	body := renderInstallBat(installBatConfig{
		ServerHost: "10.0.0.1:9100",
		Scheme:     "http",
		SMBServer:  "10.0.0.1",
		SMBShare:   "winpe",
		SMBDir:     "win2022",
		SMBUser:    "rackanvil",
		SMBPass:    "pw",
		Disk:       "0",
		ConfigURL:  "http://10.0.0.1:9100/api/v1/ipxe/config?mac=aa&asset_id=7&boot_mode=uefi",
		DecryptKey: "per-install-key",
	})
	verbatim := []string{
		"echo Waiting for network connection to 10.0.0.1:9100",
		"ping -n 1 10.0.0.1:9100 >nul",
		`net use Z: "\\10.0.0.1\winpe" "pw" /user:rackanvil >nul`,
		`if exist Z:\win2022\setup.exe set SETUP_PATH=Z:\win2022\setup.exe`,
		`echo select disk 0 > X:\wipe_disk.txt`,
		"echo Cleaning target disk 0...",
		"echo [INFO] Deep cleaning Target Disk 0 defined by Orchestrator...",
		`%DECRYPTOR_PATH% -url "http://10.0.0.1:9100/api/v1/ipxe/config?mac=aa&asset_id=7&boot_mode=uefi" -key "per-install-key" -out "X:\Windows\System32\autounattend.xml"`,
	}
	for _, v := range verbatim {
		if !strings.Contains(body, v) {
			t.Errorf("missing verbatim fragment: %q\n--- body ---\n%s", v, body)
		}
	}
	if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
		t.Errorf("install.bat must use CRLF line endings throughout")
	}
	// Order: network wait -> share map -> wipe -> decrypt -> setup.
	idx := func(s string) int { return strings.Index(body, s) }
	if !(idx("ping -n 1") < idx("net use") && idx("net use") < idx("diskpart") &&
		idx("diskpart") < idx("DECRYPTOR_PATH") && idx("DECRYPTOR_PATH") < idx("SETUP_PATH%")) {
		t.Errorf("flow order wrong:\n%s", body)
	}
}
