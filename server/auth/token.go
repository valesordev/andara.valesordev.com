// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// tokenPayload is what a session token signs. Deliberately no roles and no
// username: roles come from the Account index at use (AC-12), and the token
// is shown to whoever holds it.
type tokenPayload struct {
	AccountID string `json:"aid"`
	ActingAs  string `json:"act,omitempty"`
	Exp       int64  `json:"exp"`
	KeyID     string `json:"kid"`
	// Nonce makes two tokens issued in the same second distinct, so a
	// leaked token identifies one issuance rather than every login that
	// second.
	Nonce string `json:"n"`
}

var b64 = base64.RawURLEncoding

// sign produces base64url(payload).base64url(HMAC-SHA256(payload)) with the
// current key.
func (k *Keyring) sign(p tokenPayload) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	p.Nonce = b64.EncodeToString(nonce[:])
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	enc := b64.EncodeToString(body)
	key := k.current()
	mac := hmac.New(sha256.New, key.Secret)
	mac.Write([]byte(enc))
	return enc + "." + b64.EncodeToString(mac.Sum(nil)), nil
}

// errBadToken is internal; every caller maps it to ErrUnauthenticated with
// the fixed message. The token itself is never in the error.
var errBadToken = errors.New("bad token")

// verify checks the signature and the expiry and returns the payload.
func (k *Keyring) verify(token string, now time.Time) (tokenPayload, error) {
	enc, sig, ok := strings.Cut(token, ".")
	if !ok || enc == "" || sig == "" {
		return tokenPayload{}, errBadToken
	}
	body, err := b64.DecodeString(enc)
	if err != nil {
		return tokenPayload{}, errBadToken
	}
	var p tokenPayload
	if err := json.Unmarshal(body, &p); err != nil || p.AccountID == "" || p.KeyID == "" {
		return tokenPayload{}, errBadToken
	}
	key, ok := k.lookup(p.KeyID)
	if !ok {
		return tokenPayload{}, errBadToken
	}
	want := hmac.New(sha256.New, key.Secret)
	want.Write([]byte(enc))
	got, err := b64.DecodeString(sig)
	if err != nil || subtle.ConstantTimeCompare(got, want.Sum(nil)) != 1 {
		return tokenPayload{}, errBadToken
	}
	if !now.Before(time.Unix(p.Exp, 0)) {
		return tokenPayload{}, errBadToken
	}
	return p, nil
}

// newRefreshToken returns a fresh opaque refresh token and the hash the
// record stores. 32 random bytes, base64url without padding.
func newRefreshToken() (token string, hash []byte) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	token = b64.EncodeToString(raw[:])
	return token, hashSecret(token)
}

// newInviteCode returns a fresh Invite Code and its hash. 20 random bytes,
// base32 without padding, lower-cased: something a human can read out.
func newInviteCode() (code string, hash []byte) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	code = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
	return code, hashSecret(code)
}

// newAPIKey returns `ak_` + 32 random bytes for an agent Account. It is
// Argon2id-hashed like a password; only the prefix distinguishes it in a
// config file.
func newAPIKey() string {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return "ak_" + b64.EncodeToString(raw[:])
}

// hashSecret is SHA-256 for the tokens that are random to begin with: a
// refresh token or an invite code has 160+ bits of entropy, so a slow hash
// buys nothing over a fast one, and the index lookups need to be cheap.
func hashSecret(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// newAccountID is 16 random bytes, hex. Opaque and unguessable, so an ID in
// a log line names an Account without hinting at its username.
func newAccountID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0x0f]
	}
	return string(out)
}
