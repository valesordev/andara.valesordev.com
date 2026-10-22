// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// AW-SRV-044: kafka.sasl_username and kafka.sasl_password_file resolve
// flag > env > file > default, in the server and the projector alike; no
// default carries a secret.
func TestKafkaSASL_Precedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "andara.yaml")
	if err := os.WriteFile(path, []byte("kafka:\n  sasl_username: file-user\n  sasl_password_file: /file/pw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(set bool) EnvLookup {
		return withTLS(func(k string) (string, bool) {
			switch {
			case set && k == "ANDARA_KAFKA_SASL_USERNAME":
				return "env-user", true
			case set && k == "ANDARA_KAFKA_SASL_PASSWORD_FILE":
				return "/env/pw", true
			case k == "ANDARA_CONTENT_SOURCE":
				return "dir", true
			}
			return "", false
		})
	}
	for _, c := range []struct {
		name       string
		args       []string
		env        EnvLookup
		user, file string
	}{
		{"default", nil, env(false), "", DefaultKafkaSASLPasswordFile},
		{"file", []string{"--config", path}, env(false), "file-user", "/file/pw"},
		{"env over file", []string{"--config", path}, env(true), "env-user", "/env/pw"},
		{"flag over env and file", []string{"--config", path, "--kafka-sasl-username", "flag-user", "--kafka-sasl-password-file", "/flag/pw"}, env(true), "flag-user", "/flag/pw"},
	} {
		got, err := Parse(c.args, c.env, io.Discard)
		if err != nil {
			t.Fatalf("server %s: %v", c.name, err)
		}
		if got.KafkaSASLUsername != c.user || got.KafkaSASLPasswordFile != c.file {
			t.Errorf("server %s: %q %q, want %q %q", c.name, got.KafkaSASLUsername, got.KafkaSASLPasswordFile, c.user, c.file)
		}
		p, err := ParseProjector(c.args, c.env, io.Discard)
		if err != nil {
			t.Fatalf("projector %s: %v", c.name, err)
		}
		if p.KafkaSASLUsername != c.user || p.KafkaSASLPasswordFile != c.file {
			t.Errorf("projector %s: %q %q, want %q %q", c.name, p.KafkaSASLUsername, p.KafkaSASLPasswordFile, c.user, c.file)
		}
	}
}
