// ARM bootscript verification: the iPXE bootscript endpoint must serve
// architecture-aware assets (aarch64 -> debian-bookworm-arm64/linux).
package api

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Oumu33/rackanvil/internal/store"
)

func TestBootScriptArchAware(t *testing.T) {
	srv := New(store.New(), nil, "http://10.0.0.1:9100", log.Default())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(query string) string {
		resp, err := http.Get(ts.URL + "/api/v1/ipxe/bootscript" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	arm := get("?mac=52:54:00:aa:bb:cc&arch=aarch64")
	if !strings.Contains(arm, "/ipxe/arm64/linux") || !strings.Contains(arm, "initrd.gz") {
		t.Errorf("aarch64 bootscript missing arm64 assets:\n%s", arm)
	}
	if !strings.Contains(arm, "ttyAMA0") {
		t.Errorf("aarch64 bootscript missing ttyAMA0 console:\n%s", arm)
	}
	if strings.Contains(arm, "ttyS0") {
		t.Errorf("aarch64 bootscript must not use x86 console:\n%s", arm)
	}

	x86 := get("?mac=52:54:00:aa:bb:cc")
	if !strings.Contains(x86, "/ipxe/vmlinuz") || !strings.Contains(x86, "ttyS0") {
		t.Errorf("default bootscript must be x86_64:\n%s", x86)
	}
}
