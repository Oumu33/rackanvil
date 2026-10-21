package api

// WinPE install.bat JIT generator (M9) — the vendor server generates
// install.bat per install task (handleWinInstallBat @0x190cda0, log
// prefix "[WinPE-JIT]"); winpeshl.ini launches it inside WinPE. M8
// archived the mechanism and the verbatim fragments; M9 implements
// the generator. Lines marked VERBATIM are the vendor's recovered
// format strings (evidence: m8-evidence/strings/
// server__deplOS_pkg_ipxeinstall.InstallationManager.handleWinInstallBat.strings.txt);
// connective lines (labels, the ping retry loop, the diskpart
// invocation, the setup launch) are our reconstruction of the
// documented flow, NOT recovered text.
//
// Flow (vendor): wait for network -> net use Z: (SMB, UNC \\srv\share)
// -> locate setup.exe on Z: -> write diskpart wipe script
// X:\wipe_disk.txt and clean the target disk -> decryptor.exe pulls
// the AES-encrypted unattend answer file to
// X:\Windows\System32\autounattend.xml -> run setup.
//
// Clean-room note: the decryptor transport FORMAT is reproduced
// (-url/-key/-out), but keys are per-install values supplied by the
// operator/task config on OUR side; no vendor key material exists in
// the recovered fragments (the key is task data, not a constant) and
// none is reproduced.

import (
	"fmt"
	"net/http"
	"strings"
)

// installBatConfig carries the per-asset values the vendor handler
// renders into install.bat.
type installBatConfig struct {
	ServerHost string // host[:port] of this server (ping target + URL host)
	Scheme     string // http | https (vendor: X-Forwarded-Proto aware)
	SMBServer  string
	SMBShare   string
	SMBDir     string // directory on the share holding setup.exe (may be "")
	SMBUser    string
	SMBPass    string
	Disk       string // target disk index for the diskpart wipe
	ConfigURL  string // unattend config URL for decryptor -url
	DecryptKey string // per-install key for decryptor -key
}

// renderInstallBat assembles install.bat (CRLF line endings).
func renderInstallBat(c installBatConfig) string {
	unc := fmt.Sprintf(`\\%s\%s`, c.SMBServer, c.SMBShare) // VERBATIM format: \\%s\%s
	setupDir := c.SMBDir
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\r\n") }
	line("@echo off")
	// VERBATIM fragments: "echo Waiting for network connection to ",
	// "ping -n 1 ", " >nul" (retry loop glue is ours).
	line("echo Waiting for network connection to " + c.ServerHost)
	line(":waitnet")
	line("ping -n 1 " + c.ServerHost + " >nul")
	line("if errorlevel 1 goto waitnet")
	// VERBATIM: net use Z: "%s" "%s" /user:%s >nul
	line(fmt.Sprintf(`net use Z: "%s" "%s" /user:%s >nul`, unc, c.SMBPass, c.SMBUser))
	// VERBATIM: if exist Z:\%s\setup.exe set SETUP_PATH=Z:\%s\setup.exe
	line(fmt.Sprintf(`if exist Z:\%s\setup.exe set SETUP_PATH=Z:\%s\setup.exe`, setupDir, setupDir))
	// VERBATIM fragments: "echo select disk ", " > X:\\wipe_disk.txt",
	// "echo Cleaning target disk ", "...".
	line(fmt.Sprintf(`echo select disk %s > X:\wipe_disk.txt`, c.Disk))
	line(`echo clean >> X:\wipe_disk.txt`)
	line(fmt.Sprintf("echo Cleaning target disk %s...", c.Disk))
	line(`diskpart /s X:\wipe_disk.txt`)
	// VERBATIM fragment: "echo [INFO] Deep cleaning Target Disk ",
	// " defined by Orchestrator...".
	line(fmt.Sprintf("echo [INFO] Deep cleaning Target Disk %s defined by Orchestrator...", c.Disk))
	// VERBATIM: %%DECRYPTOR_PATH%% -url "%s" -key "%s" -out "X:\Windows\System32\autounattend.xml"
	line(fmt.Sprintf(`%%DECRYPTOR_PATH%% -url "%s" -key "%s" -out "X:\Windows\System32\autounattend.xml"`, c.ConfigURL, c.DecryptKey))
	// Glue (ours): launch the located setup.exe.
	line("if defined SETUP_PATH %SETUP_PATH%")
	return b.String()
}

// handleWinInstallBat serves the JIT-generated install.bat at the
// vendor route /api/v1/ipxe/win/install.bat (loaded as a wimboot
// initrd; winpeshl.ini points at %SYSTEMDRIVE%\Windows\System32\
// install.bat). Vendor errors: "MAC required" (400), "Task not
// found" / "OS Asset not found" (404) — mirrored here.
func (s *Server) handleWinInstallBat(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mac := q.Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "MAC required")
		return
	}
	if _, err := s.store.GetHostByMAC(mac); err != nil {
		writeError(w, http.StatusNotFound, "Task not found")
		return
	}
	getSetting := func(key string) string {
		if gs, ok := any(s.store).(interface {
			GetSetting(string) (string, bool)
		}); ok {
			if v, ok := gs.GetSetting(key); ok {
				return v
			}
		}
		return ""
	}
	scheme := q.Get("scheme")
	if scheme == "" {
		scheme = "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto // vendor: X-Forwarded-Proto aware
		}
	}
	host := r.Host
	serverHost := strings.TrimPrefix(strings.TrimPrefix(s.serverURL, "https://"), "http://")
	smbServer := q.Get("smb_server")
	if smbServer == "" {
		smbServer = serverHost
		if i := strings.IndexAny(smbServer, ":/"); i >= 0 {
			smbServer = smbServer[:i]
		}
	}
	get := func(key, def string) string {
		if v := q.Get(key); v != "" {
			return v
		}
		if v := getSetting(key); v != "" {
			return v
		}
		return def
	}
	cfg := installBatConfig{
		ServerHost: serverHost,
		Scheme:     scheme,
		SMBServer:  smbServer,
		SMBShare:   get("smb_share", "winpe"),
		SMBDir:     q.Get("smb_dir"),
		SMBUser:    get("winpe_smb_user", "rackanvil"),
		SMBPass:    get("winpe_smb_pass", ""),
		Disk:       get("disk", "0"),
		DecryptKey: get("winpe_decrypt_key", ""),
	}
	// Vendor config URL shape: %s://%s/api/v1/ipxe/config?mac=%s&asset_id=%s&boot_mode=%s
	cfg.ConfigURL = fmt.Sprintf("%s://%s/api/v1/ipxe/config?mac=%s&asset_id=%s&boot_mode=%s",
		scheme, host, mac, q.Get("asset_id"), q.Get("boot_mode"))
	s.logger.Printf("[WinPE-JIT] install.bat mac=%s asset_id=%s boot_mode=%s from=%s",
		mac, q.Get("asset_id"), q.Get("boot_mode"), r.RemoteAddr)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(renderInstallBat(cfg)))
}
