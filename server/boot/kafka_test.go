// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/telemetry"
)

// AW-SRV-044 AC-5, AC-6: with brokers and a SASL username, a password file
// that is missing or empty stops New, before any client is built, with an
// error line naming the file and never the password; the caller exits 1. A
// readable file, or no brokers at all, boots.
func TestNew_RefusesAPasswordFileThatHoldsNoPassword(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(good, []byte("s3cret-not-to-be-seen"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	newWith := func(brokers []string, file string) (*Runtime, string, error) {
		cfg := config.Config{ServiceName: "andara-server", Environment: "dev", LogLevel: "info", KafkaTopicPrefix: "it7",
			KafkaBrokers: brokers, KafkaSASLUsername: "andara-server-dev", KafkaSASLPasswordFile: file}
		var logs bytes.Buffer
		tel := telemetry.Setup(cfg, &logs)
		t.Cleanup(func() { tel.Shutdown(context.Background()) })
		rt, err := New(cfg, tel)
		return rt, logs.String(), err
	}
	for name, file := range map[string]string{"missing": filepath.Join(dir, "absent"), "empty": empty} {
		_, logs, err := newWith([]string{"127.0.0.1:1"}, file)
		if err == nil || !strings.Contains(err.Error(), file) {
			t.Errorf("%s: New = %v, want an error naming %s", name, err, file)
		}
		if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, file) {
			t.Errorf("%s: no error line naming the file:\n%s", name, logs)
		}
	}
	rt, logs, err := newWith([]string{"127.0.0.1:1"}, good)
	if err != nil {
		t.Fatalf("a readable password file: %v", err)
	}
	if !rt.Kafka.SASL() || rt.Kafka.SASLPasswordFile != good || rt.Kafka.Metrics == nil {
		t.Fatalf("Runtime.Kafka = %+v", rt.Kafka)
	}
	if strings.Contains(logs, "s3cret") {
		t.Fatalf("the password is in the log:\n%s", logs)
	}
	if _, _, err := newWith(nil, filepath.Join(dir, "absent")); err != nil {
		t.Fatalf("no brokers, no Kafka client to authenticate: %v", err)
	}
}
