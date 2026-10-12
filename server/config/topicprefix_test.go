// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AW-SRV-058 AC-10: kafka.topic_prefix resolves flag > env > file > default,
// in the server and the projector alike.
func TestKafkaTopicPrefix_Precedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "andara.yaml")
	if err := os.WriteFile(path, []byte("kafka:\n  topic_prefix: file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(prefix string) EnvLookup {
		return withTLS(func(k string) (string, bool) {
			switch {
			case k == "ANDARA_KAFKA_TOPIC_PREFIX" && prefix != "":
				return prefix, true
			case k == "ANDARA_CONTENT_SOURCE":
				return "dir", true
			}
			return "", false
		})
	}
	for _, c := range []struct {
		name string
		args []string
		env  EnvLookup
		want string
	}{
		{"default", nil, env(""), ""},
		{"file", []string{"--config", path}, env(""), "file"},
		{"env over file", []string{"--config", path}, env("env"), "env"},
		{"flag over env and file", []string{"--config", path, "--kafka-topic-prefix", "flag"}, env("env"), "flag"},
	} {
		got, err := Parse(c.args, c.env, io.Discard)
		if err != nil {
			t.Fatalf("server %s: %v", c.name, err)
		}
		if got.KafkaTopicPrefix != c.want {
			t.Errorf("server %s: kafka.topic_prefix = %q, want %q", c.name, got.KafkaTopicPrefix, c.want)
		}
		p, err := ParseProjector(c.args, c.env, io.Discard)
		if err != nil {
			t.Fatalf("projector %s: %v", c.name, err)
		}
		if p.KafkaTopicPrefix != c.want {
			t.Errorf("projector %s: kafka.topic_prefix = %q, want %q", c.name, p.KafkaTopicPrefix, c.want)
		}
	}
}

// AC-8: a prefix that is not empty and not [a-z0-9]+ fails validation, naming
// the key, in the server and the projector.
func TestKafkaTopicPrefix_MalformedIsRefused(t *testing.T) {
	for _, bad := range []string{"dev.", "Dev", "dev_1", "dev-1", "dév", "dev local"} {
		env := withTLS(func(k string) (string, bool) {
			if k == "ANDARA_KAFKA_TOPIC_PREFIX" {
				return bad, true
			}
			return "", false
		})
		if _, err := Parse(nil, env, io.Discard); err == nil || !strings.Contains(err.Error(), "kafka.topic_prefix") {
			t.Errorf("server %q: err = %v, want one naming kafka.topic_prefix", bad, err)
		}
		if _, err := ParseProjector(nil, env, io.Discard); err == nil || !strings.Contains(err.Error(), "kafka.topic_prefix") {
			t.Errorf("projector %q: err = %v, want one naming kafka.topic_prefix", bad, err)
		}
	}
	for _, good := range []string{"", "dev", "local", "it4821", "prod"} {
		if err := ValidateTopicPrefix(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}
}
