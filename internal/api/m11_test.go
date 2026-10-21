package api

// M11 tests: agent request signing (issue -> enforce -> replay reject),
// auto-generated checksums, and the preseed template cache.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Oumu33/rackanvil/internal/signreq"
	"github.com/Oumu33/rackanvil/internal/store"
)

// signedPost performs a POST exactly the way the M11 agent does.
func signedPost(t *testing.T, srv *Server, path string, body any, key string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	if key != "" {
		var probe struct {
			HostID string `json:"host_id"`
		}
		_ = json.Unmarshal(buf.Bytes(), &probe)
		nonce, err := signreq.NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Unix()
		sig, err := signreq.Sign(key, probe.HostID, http.MethodPost, path, ts, nonce, buf.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Rackanvil-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Rackanvil-Nonce", nonce)
		req.Header.Set("X-Rackanvil-Signature", sig)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	resp := rec.Result()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func enrollSigned(t *testing.T, srv *Server) InitializeResponse {
	t.Helper()
	code, data := postJSON(t, srv, "/api/v1/client/initialize", InitializeRequest{
		Hostname:    "signed-node",
		MACs:        []string{"52:54:00:11:22:33"},
		WantSignKey: true,
	})
	if code != 200 {
		t.Fatalf("initialize: status=%d body=%s", code, data)
	}
	var resp InitializeResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.SignKey == "" {
		t.Fatal("initialize with want_sign_key must return a sign_key")
	}
	return resp
}

func TestSigningEnforcedAfterEnrollment(t *testing.T) {
	srv := newTestServer()
	init := enrollSigned(t, srv)
	ck := CheckinRequest{HostID: init.HostID, AgentToken: init.AgentToken}

	// Unsigned checkin from a signed host: rejected.
	if code, data := postJSON(t, srv, "/api/v1/client/checkin", ck); code != 401 {
		t.Fatalf("unsigned checkin: status=%d, want 401 (body %s)", code, data)
	}
	// Properly signed checkin: accepted.
	if code, data := signedPost(t, srv, "/api/v1/client/checkin", ck, init.SignKey); code != 200 {
		t.Fatalf("signed checkin: status=%d, want 200 (body %s)", code, data)
	}
	// Signed metrics pass the same front door (503 here only because the
	// test server has no metrics store wired — what matters is not 401).
	code, data := signedPost(t, srv, "/api/v1/metrics", MetricsRequest{
		HostID: init.HostID, AgentToken: init.AgentToken,
	}, init.SignKey)
	if code == 401 {
		t.Fatalf("signed metrics rejected as unauthorized (body %s)", data)
	}
	if code != 200 && code != 503 {
		t.Fatalf("signed metrics: unexpected status=%d (body %s)", code, data)
	}

	// Wrong key: rejected.
	if code, _ := signedPost(t, srv, "/api/v1/client/checkin", ck, strings.Repeat("ab", 32)); code != 401 {
		t.Fatalf("wrong-key checkin: status=%d, want 401", code)
	}

	// Stale timestamp: rejected.
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(ck)
	nonce, _ := signreq.NewNonce()
	oldTS := time.Now().Unix() - signreq.MaxSkewSeconds - 60
	sig, _ := signreq.Sign(init.SignKey, init.HostID, http.MethodPost, "/api/v1/client/checkin", oldTS, nonce, buf.Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/client/checkin", &buf)
	req.Header.Set("X-Rackanvil-Timestamp", strconv.FormatInt(oldTS, 10))
	req.Header.Set("X-Rackanvil-Nonce", nonce)
	req.Header.Set("X-Rackanvil-Signature", sig)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Result().StatusCode != 401 {
		t.Fatalf("stale-ts checkin: status=%d, want 401", rec.Result().StatusCode)
	}
}

func TestSigningReplayRejected(t *testing.T) {
	srv := newTestServer()
	init := enrollSigned(t, srv)
	ck := CheckinRequest{HostID: init.HostID, AgentToken: init.AgentToken}

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(ck)
	nonce, _ := signreq.NewNonce()
	ts := time.Now().Unix()
	sig, _ := signreq.Sign(init.SignKey, init.HostID, http.MethodPost, "/api/v1/client/checkin", ts, nonce, buf.Bytes())
	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/client/checkin", bytes.NewReader(buf.Bytes()))
		req.Header.Set("X-Rackanvil-Timestamp", strconv.FormatInt(ts, 10))
		req.Header.Set("X-Rackanvil-Nonce", nonce)
		req.Header.Set("X-Rackanvil-Signature", sig)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec.Result().StatusCode
	}
	if code := send(); code != 200 {
		t.Fatalf("first send: status=%d, want 200", code)
	}
	if code := send(); code != 401 {
		t.Fatalf("replayed send: status=%d, want 401", code)
	}
}

func TestLegacyHostStaysTokenOnly(t *testing.T) {
	srv := newTestServer()
	code, data := postJSON(t, srv, "/api/v1/client/initialize", InitializeRequest{
		Hostname: "legacy-node",
		MACs:     []string{"52:54:00:44:55:66"},
	})
	if code != 200 {
		t.Fatalf("initialize: status=%d", code)
	}
	var resp InitializeResponse
	_ = json.Unmarshal(data, &resp)
	if resp.SignKey != "" {
		t.Fatal("host that did not ask for signing must not get a key")
	}
	if code, _ := postJSON(t, srv, "/api/v1/client/checkin", CheckinRequest{
		HostID: resp.HostID, AgentToken: resp.AgentToken,
	}); code != 200 {
		t.Fatalf("legacy unsigned checkin: status=%d, want 200", code)
	}
}

func TestAgentChecksumEndpoints(t *testing.T) {
	srv := newTestServer()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "rackanvil-agent")
	content := []byte("fake agent binary v1")
	if err := os.WriteFile(binPath, content, 0o755); err != nil {
		t.Fatal(err)
	}
	srv.AttachM3(M3Deps{AgentBinPath: binPath, PreseedDir: dir})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:]) + "  rackanvil-agent\n"

	resp, err := http.Get(ts.URL + "/agent/download.sha256")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != want {
		t.Fatalf("download.sha256 = %q, want %q", body, want)
	}

	// Manifest covers the agent binary.
	resp, err = http.Get(ts.URL + "/checksums.sha256")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), want) {
		t.Fatalf("manifest missing agent line; got %q", body)
	}

	// Replacing the file must change the served checksum (no stale cache).
	content2 := []byte("fake agent binary v2 - longer")
	if err := os.WriteFile(binPath, content2, 0o755); err != nil {
		t.Fatal(err)
	}
	sum2 := sha256.Sum256(content2)
	resp, err = http.Get(ts.URL + "/agent/download.sha256")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != hex.EncodeToString(sum2[:])+"  rackanvil-agent\n" {
		t.Fatalf("checksum did not refresh after file replacement: %q", body)
	}
}

// fakeInstallStore is the minimal InstallStore for the preseed tests.
type fakeInstallStore struct{ ctx map[string]string }

func (f *fakeInstallStore) SetInstallPending(string, bool) error { return nil }
func (f *fakeInstallStore) InstallPending(string) bool           { return true }
func (f *fakeInstallStore) SetInstallContext(hostID, pw string) error {
	f.ctx[hostID] = pw
	return nil
}
func (f *fakeInstallStore) GetInstallContext(hostID string) (string, bool) {
	v, ok := f.ctx[hostID]
	return v, ok
}
func (f *fakeInstallStore) ClearInstallContext(hostID string) error {
	delete(f.ctx, hostID)
	return nil
}

func TestPreseedTemplateCacheInvalidation(t *testing.T) {
	st := store.New()
	h := st.RegisterHost("cache-node", []string{"52:54:00:77:88:99"}, nil, "", 0)
	inst := &fakeInstallStore{ctx: map[string]string{}}
	if err := inst.SetInstallContext(h.ID, "rootpw"); err != nil {
		t.Fatal(err)
	}
	srv2 := New(st, nil, "http://127.0.0.1:9100", log.New(io.Discard, "", 0))
	dir := t.TempDir()
	tmplV1 := "hostname {{.Hostname}} v1"
	if err := os.WriteFile(filepath.Join(dir, "debian.cfg.tmpl"), []byte(tmplV1), 0o644); err != nil {
		t.Fatal(err)
	}
	srv2.AttachM3(M3Deps{PreseedDir: dir, Install: inst})
	ts := httptest.NewServer(srv2.Handler())
	defer ts.Close()
	get := func() string {
		resp, err := http.Get(ts.URL + "/preseed/" + h.Token)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return string(b)
	}
	if got := get(); !strings.Contains(got, "v1") {
		t.Fatalf("first render: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "debian.cfg.tmpl"), []byte("hostname {{.Hostname}} v2-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := get(); !strings.Contains(got, "v2-changed") {
		t.Fatalf("render after template edit must serve the new template, got %q", got)
	}
}
