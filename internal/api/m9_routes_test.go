package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestM9Routes(t *testing.T) {
	srv := newTestServerDB(t)

	get := func(path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := get("/api/v1/ipxe/server-time"); code != 200 || !strings.Contains(body, "unix") {
		t.Fatalf("server-time: %d %s", code, body)
	}
	if code, body := get("/api/v1/ipxe/supported-os"); code != 200 || !strings.Contains(body, `"count":54`) {
		t.Fatalf("supported-os: %d %s", code, body)
	}
	if code, body := get("/api/v1/dashboard/summary"); code != 200 || !strings.Contains(body, "hosts_total") {
		t.Fatalf("summary: %d %s", code, body)
	}
	if code, body := get("/api/v1/raid/topology"); code != 200 || !strings.Contains(body, "hosts") {
		t.Fatalf("raid topology: %d %s", code, body)
	}
	if code, _ := get("/api/v1/ipxe/network-config"); code != 400 {
		t.Fatalf("network-config without mac: %d", code)
	}
	if code, _ := get("/api/v1/ipxe/network-config?mac=00:11:22:33:44:55"); code != 404 {
		t.Fatalf("network-config unknown host: %d", code)
	}
	// license import without M3 deps -> honest 501.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/license/import",
		strings.NewReader(`{"license":"x"}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 501 {
		t.Fatalf("license import unconfigured: %d %s", rec.Code, rec.Body.String())
	}
}
