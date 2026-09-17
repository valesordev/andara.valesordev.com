// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package auth

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// signingKey is one line of auth.token_key_file.
type signingKey struct {
	ID     string
	Secret []byte
}

// Keyring is the signing keys from auth.token_key_file: the first signs,
// every one verifies. Rotation is: add a key, deploy, make it first, deploy,
// remove the old one after auth.session_ttl — at no point is a token in
// flight unverifiable.
type Keyring struct {
	keys []signingKey
	byID map[string]signingKey
}

// MinKeyBytes is the shortest secret accepted: HMAC-SHA256 gains nothing
// from a key longer than its block, and loses everything from a short one.
const MinKeyBytes = 32

// LoadKeyring reads a file of `key_id: base64` lines. Blank lines and `#`
// comments are ignored. The file must not be readable by other or writable
// by group: the config table says 0400, and a key file the whole host can
// read is a key file that is not one.
func LoadKeyring(path string) (*Keyring, error) {
	if path == "" {
		return nil, errors.New("auth.token_key_file is required (ANDARA_AUTH_TOKEN_KEY_FILE)")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("auth.token_key_file: %w", err)
	}
	// Group read is allowed because that is how a Kubernetes Secret volume
	// with fsGroup presents the file (0440); group write and any other-bit
	// are not.
	if perm := fi.Mode().Perm(); perm&0o027 != 0 {
		return nil, fmt.Errorf("auth.token_key_file %s has mode %04o; require 0400, 0600, or 0440/0640 with a trusted group", path, perm)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("auth.token_key_file: %w", err)
	}
	defer f.Close()
	return ParseKeyring(f)
}

// ParseKeyring parses key lines from any reader. Exposed for tests; the
// server loads through LoadKeyring so the mode check cannot be skipped.
func ParseKeyring(r interface{ Read([]byte) (int, error) }) (*Keyring, error) {
	kr := &Keyring{byID: map[string]signingKey{}}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		id, enc, ok := strings.Cut(text, ":")
		id = strings.TrimSpace(id)
		enc = strings.TrimSpace(enc)
		if !ok || id == "" || enc == "" {
			return nil, fmt.Errorf("auth.token_key_file line %d: want `key_id: base64`", line)
		}
		if strings.ContainsAny(id, " .") {
			return nil, fmt.Errorf("auth.token_key_file line %d: key id %q must not contain spaces or dots", line, id)
		}
		secret, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("auth.token_key_file line %d: key %s is not base64", line, id)
		}
		if len(secret) < MinKeyBytes {
			return nil, fmt.Errorf("auth.token_key_file line %d: key %s is %d bytes; require at least %d", line, id, len(secret), MinKeyBytes)
		}
		if _, dup := kr.byID[id]; dup {
			return nil, fmt.Errorf("auth.token_key_file line %d: duplicate key id %s", line, id)
		}
		k := signingKey{ID: id, Secret: secret}
		kr.keys = append(kr.keys, k)
		kr.byID[id] = k
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("auth.token_key_file: %w", err)
	}
	if len(kr.keys) == 0 {
		return nil, errors.New("auth.token_key_file contains no keys")
	}
	return kr, nil
}

// current is the signing key.
func (k *Keyring) current() signingKey { return k.keys[0] }

// lookup finds a verifying key by ID.
func (k *Keyring) lookup(id string) (signingKey, bool) {
	key, ok := k.byID[id]
	return key, ok
}

// KeyIDs lists the key IDs in file order, current first. For a boot log line.
func (k *Keyring) KeyIDs() []string {
	ids := make([]string, len(k.keys))
	for i, key := range k.keys {
		ids[i] = key.ID
	}
	return ids
}
