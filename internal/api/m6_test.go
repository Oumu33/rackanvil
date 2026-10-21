// M6 tests: full request logging with raw paths, slash normalization for
// boot clients that do not follow redirects (shim), and the dynamically
// rendered ARM64 grub.cfg (must point at THIS server's public URL, never a
// hardcoded throwaway port).
package api

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"text/template"

	"github.com/Oumu33/rackanvil/internal/store"
)

func TestRequestLogRawPathAndDoubleSlash(t *testing.T) {
	var buf bytes.Buffer
	srv := New(store.New(), nil, "http://10.0.2.2:19110", log.New(&buf, "", 0))
	srv.SetRequestLog(true)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// A double-slash path (what shim actually requests) must be served by
	// the matching route, NOT answered with a 307 the client won't follow.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(ts.URL + "/ipxe/arm64//grub.cfg")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("double-slash grub.cfg: status=%d, want 200 (body: %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "10.0.2.2:19110") {
		t.Fatalf("rendered grub.cfg must point at this server's public URL:\n%s", body)
	}
	logs := buf.String()
	if !strings.Contains(logs, "reqlog: GET /ipxe/arm64//grub.cfg -> 200") {
		t.Fatalf("reqlog must record the RAW path; logs:\n%s", logs)
	}
}

func TestDoubleSlashBootAssetNoRedirect(t *testing.T) {
	srv := New(store.New(), nil, "http://10.0.2.2:19110", log.Default())
	srv.SetAssetDir("../../assets")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Head(ts.URL + "/ipxe/arm64//grubaa64.efi")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD //grubaa64.efi: status=%d, want 200", resp.StatusCode)
	}
}

// TestPreseedLateCommandSyncsBeforeInstallDone guards the M6-D root cause:
// late_command must flush /target (sync) BEFORE posting install-done, because
// install-done is the orchestrator's cue to power-cycle the host and any
// not-yet-flushed agent files would be lost (0-byte agent/unit in the field).
func TestPreseedLateCommandSyncsBeforeInstallDone(t *testing.T) {
	tmpl, err := template.ParseFiles("../../assets/preseed/debian.cfg.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	data := preseedData{
		Hostname: "h1", ServerURL: "http://10.0.2.2:19105", HostID: "host-x",
		Token: "tok", RootPW: "pw", MirrorHost: "10.0.2.2:18998", MirrorDir: "/",
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	iSync := strings.Index(out, "sync ; sleep 2")
	iDone := strings.Index(out, "/api/v1/client/install-done")
	iDL := strings.Index(out, "/agent/download")
	if iSync < 0 || iDone < 0 || iDL < 0 {
		t.Fatalf("late_command missing pieces: sync=%d install-done=%d download=%d", iSync, iDone, iDL)
	}
	if !(iDL < iSync && iSync < iDone) {
		t.Fatalf("late_command order wrong: download=%d sync=%d install-done=%d (want download < sync < install-done)", iDL, iSync, iDone)
	}
	// M11: checksum verification sits between download and sync, the
	// download is retried, and install-done is gated on the verified flag.
	iSum := strings.Index(out, "/agent/download.sha256")
	iVerify := strings.Index(out, "sha256sum -c")
	iGate := strings.Index(out, "test -f /target/tmp/rackanvil-agent.verified")
	if iSum < 0 || iVerify < 0 || iGate < 0 {
		t.Fatalf("late_command missing M11 integrity pieces: sha256=%d verify=%d gate=%d", iSum, iVerify, iGate)
	}
	if !(iDL < iSum && iSum < iVerify && iVerify < iSync) {
		t.Fatalf("late_command order wrong: download=%d sha256=%d verify=%d sync=%d (want download < sha256 < verify < sync)", iDL, iSum, iVerify, iSync)
	}
	if !(iGate < iDone) {
		t.Fatalf("install-done must be gated on the verified flag: gate=%d done=%d", iGate, iDone)
	}
	if !strings.Contains(out, "wget --tries=3 --waitretry=5") {
		t.Fatal("late_command agent download must retry 3 times (M11 first-boot alignment)")
	}
}
