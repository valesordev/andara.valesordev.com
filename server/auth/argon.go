// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"

	"golang.org/x/crypto/argon2"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
)

// Argon2Params is the Argon2id cost. The parameters are stored alongside
// every hash so that changing them here is a rehash on the next successful
// login (AC-13), not a migration.
type Argon2Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// DefaultArgon2Params targets ~100 ms on the kind box (story assumption):
// 64 MiB, t=3, p=4.
var DefaultArgon2Params = Argon2Params{MemoryKiB: 65536, Time: 3, Threads: 4}

const (
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// Validate rejects a cost that Argon2 itself would reject or that no one
// could have meant.
func (p Argon2Params) Validate() error {
	if p.MemoryKiB < 8 {
		return errors.New("auth.argon2.memory_kib must be at least 8")
	}
	if p.Time < 1 {
		return errors.New("auth.argon2.time must be at least 1")
	}
	if p.Threads < 1 {
		return errors.New("auth.argon2.threads must be at least 1")
	}
	// Argon2 requires memory >= 8 * threads.
	if p.MemoryKiB < 8*uint32(p.Threads) {
		return errors.New("auth.argon2.memory_kib must be at least 8 * auth.argon2.threads")
	}
	return nil
}

func (p Argon2Params) proto() *accountsv1.Argon2Params {
	return &accountsv1.Argon2Params{MemoryKib: p.MemoryKiB, Time: p.Time, Threads: uint32(p.Threads), KeyLen: argon2KeyLen}
}

func paramsFromProto(p *accountsv1.Argon2Params) Argon2Params {
	if p == nil {
		return Argon2Params{}
	}
	return Argon2Params{MemoryKiB: p.GetMemoryKib(), Time: p.GetTime(), Threads: uint8(p.GetThreads())}
}

// hashCredential derives a new Credential of the given kind for secret.
func hashCredential(kind accountsv1.CredentialKind, secret string, p Argon2Params) *accountsv1.Credential {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	hash := argon2.IDKey([]byte(secret), salt, p.Time, p.MemoryKiB, p.Threads, argon2KeyLen)
	return &accountsv1.Credential{Kind: kind, Hash: hash, Salt: salt, Params: p.proto()}
}

// verifyCredential reports whether secret matches c. It always runs the
// full derivation, so a wrong password costs what a right one does.
func verifyCredential(c *accountsv1.Credential, secret string) bool {
	if c == nil || len(c.GetHash()) == 0 || c.GetParams() == nil {
		return false
	}
	p := c.GetParams()
	keyLen := p.GetKeyLen()
	if keyLen == 0 {
		keyLen = argon2KeyLen
	}
	got := argon2.IDKey([]byte(secret), c.GetSalt(), p.GetTime(), p.GetMemoryKib(), uint8(p.GetThreads()), keyLen)
	return subtle.ConstantTimeCompare(got, c.GetHash()) == 1
}

// needsRehash reports whether c was derived with parameters other than p.
func needsRehash(c *accountsv1.Credential, p Argon2Params) bool {
	return paramsFromProto(c.GetParams()) != p || c.GetParams().GetKeyLen() != argon2KeyLen
}
