// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// JWKSVerifier verifies WORKLOAD_JWT credentials — projected Kubernetes
// service-account tokens — against the issuer's JWKS endpoint (ADR-0005's
// cluster path). It checks the signature (RS256 or ES256), `iss`, `exp`,
// and `nbf`, and returns `sub` for the Store to compare with the Account's
// workload_subject. Keys are cached and refetched on an unknown `kid`, at
// most once per minute, so a rotated signing key is picked up without a
// restart and a flood of bad tokens cannot turn into a flood of fetches.
type JWKSVerifier struct {
	issuer string
	url    string
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey
	lastFetch time.Time
}

// NewJWKSVerifier returns a verifier for tokens issued by issuer whose keys
// are published at jwksURL.
func NewJWKSVerifier(issuer, jwksURL string) *JWKSVerifier {
	return &JWKSVerifier{
		issuer: issuer,
		url:    jwksURL,
		client: &http.Client{Timeout: 5 * time.Second},
		now:    time.Now,
		keys:   map[string]crypto.PublicKey{},
	}
}

var errBadJWT = errors.New("bad workload token")

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
	Nbf int64  `json:"nbf"`
}

// VerifySubject implements WorkloadVerifier.
func (v *JWKSVerifier) VerifySubject(ctx context.Context, token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errBadJWT
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return "", errBadJWT
	}
	var h jwtHeader
	if err := json.Unmarshal(hb, &h); err != nil || h.Kid == "" {
		return "", errBadJWT
	}
	cb, err := b64.DecodeString(parts[1])
	if err != nil {
		return "", errBadJWT
	}
	var c jwtClaims
	if err := json.Unmarshal(cb, &c); err != nil {
		return "", errBadJWT
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return "", errBadJWT
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return "", err
	}
	signed := []byte(parts[0] + "." + parts[1])
	if err := verifySignature(h.Alg, key, signed, sig); err != nil {
		return "", errBadJWT
	}
	now := v.now().Unix()
	if c.Iss != v.issuer || c.Sub == "" || c.Exp == 0 || now >= c.Exp || (c.Nbf != 0 && now < c.Nbf) {
		return "", errBadJWT
	}
	return c.Sub, nil
}

// key returns the public key for kid, fetching the JWKS if it is unknown
// and the last fetch is more than a minute old.
func (v *JWKSVerifier) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if v.now().Sub(v.lastFetch) < time.Minute {
		return nil, errBadJWT
	}
	v.lastFetch = v.now()
	if err := v.fetchLocked(ctx); err != nil {
		return nil, fmt.Errorf("workload jwks: %w", err)
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, errBadJWT
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (v *JWKSVerifier) fetchLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.url, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", v.url, resp.StatusCode)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&set); err != nil {
		return err
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			continue // one malformed key does not poison the set
		}
		keys[k.Kid] = pub
	}
	v.keys = keys
	return nil
}

func (k jwk) publicKey() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		n, err := b64.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		e, err := b64.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("unsupported curve %s", k.Crv)
		}
		x, err := b64.DecodeString(k.X)
		if err != nil {
			return nil, err
		}
		y, err := b64.DecodeString(k.Y)
		if err != nil {
			return nil, err
		}
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, err
		}
		return pub, nil
	}
	return nil, fmt.Errorf("unsupported kty %s", k.Kty)
}

func verifySignature(alg string, key crypto.PublicKey, signed, sig []byte) error {
	switch alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return errBadJWT
		}
		sum := sha256.Sum256(signed)
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig)
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok || len(sig) != 64 {
			return errBadJWT
		}
		sum := sha256.Sum256(signed)
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, sum[:], r, s) {
			return errBadJWT
		}
		return nil
	}
	// RS512 and ES512 are not what kube-apiserver issues; add them here if
	// another issuer ever does.
	return errBadJWT
}
