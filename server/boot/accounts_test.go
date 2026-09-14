// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

func keyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token-keys")
	if err := os.WriteFile(path, []byte("k1: "+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func authConfig(t *testing.T, mutate func(*config.Config)) config.Config {
	t.Helper()
	cfg, err := config.Parse([]string{"--validate-only"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ValidateOnly = false
	cfg.AuthStore = "memory"
	cfg.AuthTokenKeyFile = keyFile(t)
	cfg.AuthArgon2MemoryKiB, cfg.AuthArgon2Time, cfg.AuthArgon2Threads = 64, 1, 1
	cfg.OTLPEndpoint = ""
	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

// OpenAccounts wires the store from configuration and applies the
// bootstrap operator exactly once.
func TestOpenAccounts_Bootstrap(t *testing.T) {
	cfg := authConfig(t, func(c *config.Config) { c.AuthBootstrapOperator = "brian:first-operator-pw" })
	tel := telemetry.Setup(cfg, &bytes.Buffer{})
	defer tel.Shutdown(context.Background())
	rt := New(cfg, tel)
	store, err := rt.OpenAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair, err := store.Authenticate(context.Background(), "brian", "first-operator-pw", "test")
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Verify(context.Background(), pair.SessionToken)
	if err != nil || !p.Has(auth.RoleOperator) {
		t.Fatalf("principal %+v err=%v", p, err)
	}

	// Malformed bootstrap and a loose key file are boot failures.
	for name, mutate := range map[string]func(*config.Config){
		"no colon":     func(c *config.Config) { c.AuthBootstrapOperator = "brian" },
		"short pw":     func(c *config.Config) { c.AuthBootstrapOperator = "brian:short" },
		"missing keys": func(c *config.Config) { c.AuthTokenKeyFile = filepath.Join(t.TempDir(), "none") },
		"bad limit":    func(c *config.Config) { c.AuthRateLimit = "lots" },
		"bad store":    func(c *config.Config) { c.AuthStore = "redis" },
	} {
		bad := authConfig(t, mutate)
		badTel := telemetry.Setup(bad, &bytes.Buffer{})
		if _, err := New(bad, badTel).OpenAccounts(context.Background()); err == nil {
			t.Errorf("%s: boot succeeded", name)
		}
		badTel.Shutdown(context.Background())
	}
}

// AC-7: a World's serialized form carries no Account, credential, or token
// bytes. Account state is a different type on a different topic, and this
// pins it: a planted username, password hash, and token are searched for in
// the bytes a Snapshot would be built from.
func TestSnapshotBytesCarryNoAccountState(t *testing.T) {
	cfg := authConfig(t, func(c *config.Config) { c.AuthBootstrapOperator = "planted-username-9c1e:planted-password-4f7a" })
	tel := telemetry.Setup(cfg, &bytes.Buffer{})
	defer tel.Shutdown(context.Background())
	store, err := New(cfg, tel).OpenAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pair, err := store.Authenticate(context.Background(), "planted-username-9c1e", "planted-password-4f7a", "test")
	if err != nil {
		t.Fatal(err)
	}

	inputs, errs := content.LoadDir(filepath.Join("..", "..", "testdata", "content", "valid"))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	world, verrs := sim.BuildWorld(inputs, sim.Options{})
	for _, e := range verrs {
		if !sim.IsWarning(e, false) {
			t.Fatal(e)
		}
	}
	snapshot := string(sim.CanonicalBytes(world))
	if len(snapshot) < 100 {
		t.Fatalf("suspiciously small snapshot: %d bytes", len(snapshot))
	}
	for name, needle := range map[string]string{
		"username":       "planted-username-9c1e",
		"password":       "planted-password-4f7a",
		"session token":  pair.SessionToken,
		"refresh token":  pair.RefreshToken,
		"session prefix": strings.SplitN(pair.SessionToken, ".", 2)[0],
		"hex of token":   hex.EncodeToString([]byte(pair.RefreshToken)),
	} {
		if strings.Contains(snapshot, needle) {
			t.Errorf("World bytes contain the %s", name)
		}
	}
}
