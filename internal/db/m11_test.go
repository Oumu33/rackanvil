package db

// M11: signing-key persistence across the hosts.sign_key migration.

import "testing"

func TestSignKeyRoundtrip(t *testing.T) {
	d, err := Open("sqlite::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := d.RegisterHost("sign-node", []string{"52:54:00:99:88:77"}, nil, "", 0)
	if _, ok := d.HostSignKey(h.ID); ok {
		t.Fatal("fresh host must have no sign key")
	}
	key, err := d.IssueSignKey(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 64 {
		t.Fatalf("sign key length %d, want 64 hex chars", len(key))
	}
	got, ok := d.HostSignKey(h.ID)
	if !ok || got != key {
		t.Fatalf("HostSignKey = %q,%v want %q,true", got, ok, key)
	}
	// Rotation replaces the key.
	key2, err := d.IssueSignKey(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if key2 == key {
		t.Fatal("rotation must produce a new key")
	}
	// Hosts() carries the key too.
	found := false
	for _, hh := range d.Hosts() {
		if hh.ID == h.ID && hh.SignKey == key2 {
			found = true
		}
	}
	if !found {
		t.Fatal("Hosts() does not carry the rotated sign key")
	}
	if _, err := d.IssueSignKey("no-such-host"); err == nil {
		t.Fatal("IssueSignKey for unknown host must error")
	}
}
