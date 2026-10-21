package license

import (
	"strings"
	"testing"
	"time"
)

func TestIssueVerifyRoundTrip(t *testing.T) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	lic, err := Issue(priv, License{
		Customer: "test-lab", MaxHosts: 50,
		IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(24 * time.Hour).Unix(),
		Features: []string{"pxe", "orchestrator"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(pub, strings.TrimSpace(lic))
	if err != nil {
		t.Fatal(err)
	}
	if got.Customer != "test-lab" || got.MaxHosts != 50 {
		t.Fatalf("payload mismatch: %+v", got)
	}
}

func TestVerifyRejectsTampered(t *testing.T) {
	priv, pub, _ := GenerateKeypair()
	lic, _ := Issue(priv, License{Customer: "a", MaxHosts: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	// Flip a byte in the payload part.
	parts := strings.Split(lic, ".")
	b, _ := b64d(parts[0])
	b[5] ^= 0xff
	tampered := b64e(b) + "." + parts[1]
	if _, err := Verify(pub, tampered); err != ErrBadSig {
		t.Fatalf("want ErrBadSig, got %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	priv, pub, _ := GenerateKeypair()
	lic, _ := Issue(priv, License{Customer: "a", MaxHosts: 1,
		IssuedAt:  time.Now().Add(-48 * time.Hour).Unix(),
		ExpiresAt: time.Now().Add(-time.Hour).Unix()})
	if _, err := Verify(pub, lic); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	priv, _, _ := GenerateKeypair()
	_, pub2, _ := GenerateKeypair()
	lic, _ := Issue(priv, License{Customer: "a", MaxHosts: 1, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if _, err := Verify(pub2, lic); err != ErrBadSig {
		t.Fatalf("want ErrBadSig, got %v", err)
	}
}

func TestPubKeyRoundTrip(t *testing.T) {
	_, pub, _ := GenerateKeypair()
	s := PubKeyString(pub)
	p2, err := ParsePubKey(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(p2) != string(pub) {
		t.Fatal("pubkey mismatch")
	}
	if _, err := ParsePubKey("bogus"); err == nil {
		t.Fatal("want error for bogus key")
	}
}
