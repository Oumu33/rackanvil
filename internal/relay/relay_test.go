package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBootScriptCache(t *testing.T) {
	var upstreamHits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/ipxe/bootscript"):
			atomic.AddInt64(&upstreamHits, 1)
			w.Write([]byte("#!ipxe\nchain test\n"))
		case strings.HasPrefix(r.URL.Path, "/api/v1/relay/nodes"):
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	rl, err := New("relay-a", upstream.URL, dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rl.Handler())
	defer srv.Close()

	get := func(mac string) (string, string) {
		resp, err := http.Get(srv.URL + "/api/v1/ipxe/bootscript?mac=" + mac)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), resp.Header.Get("X-Relay-Cache")
	}
	body, cache := get("aa:bb:cc:dd:ee:ff")
	if !strings.Contains(body, "#!ipxe") || cache != "MISS" {
		t.Fatalf("first fetch: body=%q cache=%s", body, cache)
	}
	_, cache = get("aa:bb:cc:dd:ee:ff")
	if cache != "HIT" {
		t.Fatalf("second fetch should HIT, got %s", cache)
	}
	if n := atomic.LoadInt64(&upstreamHits); n != 1 {
		t.Fatalf("upstream hit %d times, want 1", n)
	}
}

func TestAssetCache(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("KERNEL-BYTES"))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	rl, _ := New("relay-a", upstream.URL, dir)
	srv := httptest.NewServer(rl.Handler())
	defer srv.Close()

	for i, want := range []string{"MISS", "HIT"} {
		resp, err := http.Get(srv.URL + "/ipxe/vmlinuz")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if got := resp.Header.Get("X-Relay-Cache"); got != want {
			t.Fatalf("fetch %d: cache=%s want %s", i, got, want)
		}
		if string(b) != "KERNEL-BYTES" {
			t.Fatalf("bad body %q", b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "vmlinuz")); err != nil {
		t.Fatal("asset not cached to disk")
	}
}

func TestHeartbeat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/relay/nodes" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(201)
	}))
	defer upstream.Close()

	rl, _ := New("relay-a", upstream.URL, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rl.Heartbeat(ctx, []string{"10.20.1.0/24"}); err != nil {
		t.Fatal(err)
	}
}
