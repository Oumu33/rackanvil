package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundtrip(t *testing.T) {
	h, err := HashPassword("s3cr3t!")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !CheckPassword("s3cr3t!", h) {
		t.Fatalf("correct password rejected")
	}
	if CheckPassword("wrong", h) {
		t.Fatalf("wrong password accepted")
	}
	if !strings.HasPrefix(h, "$2a$12$") {
		t.Fatalf("unexpected bcrypt format: %s", h[:7])
	}
}

func TestJWT(t *testing.T) {
	s := NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	tok, err := s.Sign("u-1", "admin", "admin", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Username != "admin" || c.Role != "admin" {
		t.Fatalf("claims: %+v", c)
	}
	// Tampered payload must fail.
	parts := strings.Split(tok, ".")
	if _, err := s.Verify(parts[0] + "." + parts[1] + "AAAA." + parts[2]); err == nil {
		t.Fatalf("tampered token accepted")
	}
	// Wrong secret must fail.
	s2 := NewSigner([]byte("ffffffffffffffffffffffffffffffff"))
	if _, err := s2.Verify(tok); err == nil {
		t.Fatalf("foreign secret accepted")
	}
	// Expired token must fail.
	old, err := s.Sign("u-1", "admin", "admin", -time.Second)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := s.Verify(old); err == nil {
		t.Fatalf("expired token accepted")
	}
}

func TestRandomPassword(t *testing.T) {
	p := RandomPassword()
	if len(p) != 16 {
		t.Fatalf("len %d", len(p))
	}
	p2 := RandomPassword()
	if p == p2 {
		t.Fatalf("not random")
	}
}
