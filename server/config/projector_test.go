// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"io"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) EnvLookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// The projector needs no TLS or auth material: it serves no Protocol. It
// does need a broker, and it reads the server's shared keys the way the
// server does.
func TestParseProjector_DefaultsAndSharedKeys(t *testing.T) {
	p, err := ParseProjector(nil, envMap(map[string]string{
		"ANDARA_KAFKA_BROKERS":  "localhost:9092",
		"ANDARA_SIM_SEED":       "42",
		"ANDARA_CONTENT_SOURCE": "dir",
		"ANDARA_SERVICE_NAME":   "andara-server", // the shared ConfigMap's value
		"ANDARA_ENV":            "dev",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if p.BatchTicks != DefaultProjectorBatchTicks || p.LagBudget != DefaultProjectorLagBudget {
		t.Fatalf("defaults: %+v", p)
	}
	if p.SimSeed != 42 || p.ContentSource != "dir" || p.Environment != "dev" {
		t.Fatalf("shared keys not read: seed %d source %q env %q", p.SimSeed, p.ContentSource, p.Environment)
	}
	if p.ServiceName != ProjectorServiceName {
		t.Fatalf("service name %q, want %q whatever ANDARA_SERVICE_NAME says", p.ServiceName, ProjectorServiceName)
	}
	if got := ProjectorGroup(p.Environment); got != "andara-projector-state-dev" {
		t.Fatalf("group %q", got)
	}
}

func TestParseProjector_OwnKeys(t *testing.T) {
	p, err := ParseProjector([]string{"--rebuild", "--batch-ticks", "25"}, envMap(map[string]string{
		"ANDARA_KAFKA_BROKERS":        "b:9092",
		"ANDARA_PROJECTOR_LAG_BUDGET": "12s",
	}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Rebuild || p.FromZero || p.BatchTicks != 25 || p.LagBudget != 12*time.Second {
		t.Fatalf("%+v", p)
	}
}

func TestParseProjector_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no broker", nil, nil, "kafka.brokers"},
		{"zero batch", []string{"--batch-ticks", "0"}, map[string]string{"ANDARA_KAFKA_BROKERS": "b"}, "batch_ticks"},
		{"bad budget env", nil, map[string]string{"ANDARA_KAFKA_BROKERS": "b", "ANDARA_PROJECTOR_LAG_BUDGET": "soon"}, "lag_budget"},
		{"s3 without bucket", nil, map[string]string{"ANDARA_KAFKA_BROKERS": "b", "ANDARA_SNAPSHOT_STORE": "s3"}, "s3_bucket"},
		{"stray argument", []string{"redis"}, map[string]string{"ANDARA_KAFKA_BROKERS": "b"}, "unexpected argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProjector(tc.args, envMap(tc.env), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
}
