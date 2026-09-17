// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type jwksServer struct {
	rsaKey  *rsa.PrivateKey
	ecKey   *ecdsa.PrivateKey
	fetches atomic.Int32
	srv     *httptest.Server
}

func newJWKSServer(t *testing.T) *jwksServer {
	t.Helper()
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	j := &jwksServer{rsaKey: rk, ecKey: ek}
	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		j.fetches.Add(1)
		set := map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": "rsa1", "use": "sig", "n": b64.EncodeToString(rk.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(rk.E)).Bytes())},
			{"kty": "EC", "kid": "ec1", "crv": "P-256", "x": b64.EncodeToString(ek.X.FillBytes(make([]byte, 32))), "y": b64.EncodeToString(ek.Y.FillBytes(make([]byte, 32)))},
			{"kty": "OKP", "kid": "weird"},
		}}
		_ = json.NewEncoder(w).Encode(set)
	}))
	t.Cleanup(j.srv.Close)
	return j
}

func (j *jwksServer) token(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	cb, _ := json.Marshal(claims)
	signed := b64.EncodeToString(hb) + "." + b64.EncodeToString(cb)
	sum := sha256.Sum256([]byte(signed))
	var sig []byte
	switch alg {
	case "RS256":
		s, err := rsa.SignPKCS1v15(rand.Reader, j.rsaKey, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = s
	case "ES256":
		r, s, err := ecdsa.Sign(rand.Reader, j.ecKey, sum[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	}
	return signed + "." + b64.EncodeToString(sig)
}

func TestJWKSVerifier(t *testing.T) {
	j := newJWKSServer(t)
	const issuer = "https://kubernetes.default.svc"
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	v := NewJWKSVerifier(issuer, j.srv.URL)
	v.now = func() time.Time { return now }
	good := map[string]any{"iss": issuer, "sub": "system:serviceaccount:andara:town", "exp": now.Add(time.Hour).Unix()}

	for _, alg := range []struct{ alg, kid string }{{"RS256", "rsa1"}, {"ES256", "ec1"}} {
		sub, err := v.VerifySubject(context.Background(), j.token(t, alg.alg, alg.kid, good))
		if err != nil || sub != "system:serviceaccount:andara:town" {
			t.Fatalf("%s: sub=%q err=%v", alg.alg, sub, err)
		}
	}
	if j.fetches.Load() != 1 {
		t.Fatalf("fetched %d times, want 1 (cached)", j.fetches.Load())
	}

	bad := map[string]map[string]any{
		"wrong issuer": {"iss": "https://evil", "sub": "s", "exp": now.Add(time.Hour).Unix()},
		"expired":      {"iss": issuer, "sub": "s", "exp": now.Add(-time.Second).Unix()},
		"not yet":      {"iss": issuer, "sub": "s", "exp": now.Add(time.Hour).Unix(), "nbf": now.Add(time.Minute).Unix()},
		"no sub":       {"iss": issuer, "exp": now.Add(time.Hour).Unix()},
		"no exp":       {"iss": issuer, "sub": "s"},
	}
	for name, claims := range bad {
		if _, err := v.VerifySubject(context.Background(), j.token(t, "RS256", "rsa1", claims)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Wrong key for the alg, alg confusion, tampered payload, garbage.
	if _, err := v.VerifySubject(context.Background(), j.token(t, "RS256", "ec1", good)); err == nil {
		t.Error("RS256 with an EC key accepted")
	}
	tok := j.token(t, "RS256", "rsa1", good)
	if _, err := v.VerifySubject(context.Background(), tok[:len(tok)-4]+"AAAA"); err == nil {
		t.Error("tampered signature accepted")
	}
	if _, err := v.VerifySubject(context.Background(), "not.a.jwt.at.all"); err == nil {
		t.Error("garbage accepted")
	}
	// An unknown kid triggers at most one refetch per minute.
	for i := range 3 {
		if _, err := v.VerifySubject(context.Background(), j.token(t, "RS256", fmt.Sprintf("unknown%d", i), good)); err == nil {
			t.Error("unknown kid accepted")
		}
	}
	if j.fetches.Load() != 1 {
		t.Fatalf("fetched %d times, want 1 (throttled: the set was fetched under a minute ago)", j.fetches.Load())
	}
	now = now.Add(2 * time.Minute)
	_, _ = v.VerifySubject(context.Background(), j.token(t, "RS256", "unknown9", good))
	if j.fetches.Load() != 2 {
		t.Fatalf("fetched %d times, want 2 (throttle expired)", j.fetches.Load())
	}
}
