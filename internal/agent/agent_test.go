package agent

// M11 tests: the agent signs checkin/report after enrollment (and the
// server can verify), and the first-boot readiness gate bounds its wait.

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Oumu33/rackanvil/internal/signreq"
)

const testSignKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAgentSignsAfterInitialize(t *testing.T) {
	var sawInit, sawSignedCheckin atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/client/initialize", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			WantSignKey bool `json:"want_sign_key"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.WantSignKey {
			t.Error("initialize must advertise want_sign_key")
		}
		sawInit.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"host_id": "host-test", "agent_token": "tok",
			"sign_key": testSignKey, "checkin_interval_sec": 30,
		})
	})
	mux.HandleFunc("/api/v1/client/checkin", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get("X-Rackanvil-Timestamp"), 10, 64)
		if !signreq.Verify(testSignKey, "host-test", http.MethodPost,
			"/api/v1/client/checkin", ts, r.Header.Get("X-Rackanvil-Nonce"),
			raw, r.Header.Get("X-Rackanvil-Signature")) {
			t.Errorf("checkin signature did not verify (headers: ts=%q nonce=%q sig=%q)",
				r.Header.Get("X-Rackanvil-Timestamp"), r.Header.Get("X-Rackanvil-Nonce"), r.Header.Get("X-Rackanvil-Signature"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawSignedCheckin.Store(true)
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []any{}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := New(ts.URL, time.Second, log.New(io.Discard, "", 0))
	if err := a.initialize(); err != nil {
		t.Fatal(err)
	}
	if !sawInit.Load() {
		t.Fatal("initialize was not called")
	}
	if a.signKey != testSignKey {
		t.Fatalf("agent did not keep the issued sign key: %q", a.signKey)
	}
	if _, err := a.checkin(); err != nil {
		t.Fatalf("signed checkin failed: %v", err)
	}
	if !sawSignedCheckin.Load() {
		t.Fatal("server never saw a verifiable signed checkin")
	}
}

func TestWaitForServerGate(t *testing.T) {
	// Server up: gate passes fast.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	a := New(up.URL, time.Second, log.New(io.Discard, "", 0))
	a.readyTimeout, a.readyInterval = 2*time.Second, 10*time.Millisecond
	ready, waited := a.waitForServer(context.Background())
	if !ready || waited > time.Second {
		t.Fatalf("up server: ready=%v waited=%v", ready, waited)
	}

	// Server down: gate gives up at the bound and reports not-ready.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	downURL := down.URL
	down.Close()
	b := New(downURL, time.Second, log.New(io.Discard, "", 0))
	b.readyTimeout, b.readyInterval = 150*time.Millisecond, 20*time.Millisecond
	start := time.Now()
	ready, _ = b.waitForServer(context.Background())
	if ready {
		t.Fatal("down server: gate must report not-ready")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("gate overran its bound: %v", elapsed)
	}
}
