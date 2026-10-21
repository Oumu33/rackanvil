// Package license implements our own license issuance and verification.
//
// Design (deliberately NOT compatible with the reference product):
//   - Ed25519 signatures over a canonical JSON payload (stdlib crypto/ed25519).
//   - License file format: base64url(payload) + "." + base64url(signature).
//   - The reference product's RSA public key was extracted during reversing
//     but is never used here; we mint our own keypair.
package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrBadFormat   = errors.New("license: bad format")
	ErrBadSig      = errors.New("license: bad signature")
	ErrExpired     = errors.New("license: expired")
	ErrNotYetValid = errors.New("license: not yet valid")
)

// License is the signed payload.
type License struct {
	Customer  string   `json:"customer"`
	MaxHosts  int      `json:"max_hosts"`
	IssuedAt  int64    `json:"issued_at"`  // unix
	ExpiresAt int64    `json:"expires_at"` // unix, 0 = never
	Features  []string `json:"features"`
}

// GenerateKeypair mints a fresh Ed25519 keypair.
func GenerateKeypair() (priv, pub []byte, err error) {
	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	return priv, pub, err
}

func b64e(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func b64d(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// Issue signs l with priv and returns the license file content.
func Issue(priv ed25519.PrivateKey, l License) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("license: bad private key size %d", len(priv))
	}
	payload, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return b64e(payload) + "." + b64e(sig), nil
}

// Verify checks the signature against pub and enforces the validity window.
func Verify(pub ed25519.PublicKey, s string) (*License, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return nil, ErrBadFormat
	}
	payload, err := b64d(parts[0])
	if err != nil {
		return nil, ErrBadFormat
	}
	sig, err := b64d(parts[1])
	if err != nil {
		return nil, ErrBadFormat
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, ErrBadSig
	}
	var l License
	if err := json.Unmarshal(payload, &l); err != nil {
		return nil, ErrBadFormat
	}
	now := time.Now().Unix()
	if l.ExpiresAt != 0 && now > l.ExpiresAt {
		return nil, ErrExpired
	}
	if l.IssuedAt != 0 && now < l.IssuedAt-300 {
		return nil, ErrNotYetValid
	}
	return &l, nil
}

// PubKeyString encodes a public key for storage/flags.
func PubKeyString(pub ed25519.PublicKey) string { return b64e([]byte(pub)) }

// ParsePubKey decodes a stored public key.
func ParsePubKey(s string) (ed25519.PublicKey, error) {
	b, err := b64d(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: bad public key")
	}
	return ed25519.PublicKey(b), nil
}
