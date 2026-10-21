package signreq

import (
	"testing"
	"time"
)

func TestSignVerifyRoundtrip(t *testing.T) {
	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"host_id":"host-1","agent_token":"abc"}`)
	ts := time.Now().Unix()
	nonce, _ := NewNonce()
	sig, err := Sign(key, "host-1", "POST", "/api/v1/client/checkin", ts, nonce, body)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(key, "host-1", "POST", "/api/v1/client/checkin", ts, nonce, body, sig) {
		t.Fatal("valid signature rejected")
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	key, _ := NewKey()
	otherKey, _ := NewKey()
	body := []byte(`{"status":"done"}`)
	ts := time.Now().Unix()
	nonce, _ := NewNonce()
	sig, _ := Sign(key, "host-1", "POST", "/api/v1/client/report", ts, nonce, body)

	cases := []struct {
		name  string
		key   string
		host  string
		path  string
		ts    int64
		nonce string
		body  []byte
	}{
		{"tampered body", key, "host-1", "/api/v1/client/report", ts, nonce, []byte(`{"status":"failed"}`)},
		{"wrong key", otherKey, "host-1", "/api/v1/client/report", ts, nonce, body},
		{"wrong host", key, "host-2", "/api/v1/client/report", ts, nonce, body},
		{"wrong path", key, "host-1", "/api/v1/client/checkin", ts, nonce, body},
		{"wrong ts", key, "host-1", "/api/v1/client/report", ts + 1, nonce, body},
		{"wrong nonce", key, "host-1", "/api/v1/client/report", ts, nonce + "00", body},
	}
	for _, c := range cases {
		if Verify(c.key, c.host, "POST", c.path, c.ts, c.nonce, c.body, sig) {
			t.Fatalf("%s: signature accepted, want rejection", c.name)
		}
	}
	if Verify("not-hex", "host-1", "POST", "/x", ts, nonce, body, sig) {
		t.Fatal("non-hex key accepted")
	}
	if Verify(key, "host-1", "POST", "/api/v1/client/report", ts, nonce, body, "deadbeef") {
		t.Fatal("garbage signature accepted")
	}
}
