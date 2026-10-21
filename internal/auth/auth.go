// Package auth provides password hashing (bcrypt) and session tokens.
//
// Tokens are plain HS256 JWTs implemented with the standard library
// (crypto/hmac + encoding/json). No third-party JWT dependency, no custom
// crypto primitives: HMAC-SHA256 is used exactly as specified by RFC 7519.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var ErrBadToken = errors.New("auth: invalid or expired token")

// HashPassword hashes with bcrypt (cost 12).
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// CheckPassword verifies a password against its bcrypt hash.
func CheckPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// RandomPassword generates a 16-char alphanumeric password (admin bootstrap).
func RandomPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	var b [16]byte
	rand.Read(b[:])
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:])
}

// Claims carried in the JWT payload.
type Claims struct {
	Subject  string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

// Signer signs and verifies HS256 tokens.
type Signer struct {
	secret []byte
}

// NewSigner creates a signer. The secret must be 32+ random bytes, persisted
// server-side (we keep it in system_settings.jwt_secret).
func NewSigner(secret []byte) *Signer { return &Signer{secret: secret} }

func b64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }
func unb64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// Sign issues a token valid for ttl.
func (s *Signer) Sign(subject, username, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(Claims{
		Subject: subject, Username: username, Role: role,
		IssuedAt: now.Unix(), Expires: now.Add(ttl).Unix(),
	})
	if err != nil {
		return "", err
	}
	payload := b64(payloadBytes)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(header + "." + payload))
	return header + "." + payload + "." + b64(mac.Sum(nil)), nil
}

// Verify checks signature and expiry, returning the claims.
func (s *Signer) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrBadToken
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := unb64(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, ErrBadToken
	}
	payload, err := unb64(parts[1])
	if err != nil {
		return nil, ErrBadToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrBadToken
	}
	if time.Now().Unix() > c.Expires {
		return nil, fmt.Errorf("%w: expired", ErrBadToken)
	}
	return &c, nil
}
