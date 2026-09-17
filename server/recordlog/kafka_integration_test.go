// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// Against a throwaway topic on a running broker (`make up`, then
// `make test-integration`). Build-tagged so `make test` never needs a broker.
package recordlog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	return strings.Split(v, ",")
}

// throwawayTopic creates a compacted topic that is deleted when the test ends.
func throwawayTopic(t *testing.T, partitions int32) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	name := fmt.Sprintf("andara.test.recordlog.%d", time.Now().UnixNano())
	configs := map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}
	if _, err := adm.CreateTopic(ctx, partitions, 1, configs, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopic(dctx, name)
		cl.Close()
	})
	return name
}

func TestKafka_AppendAndReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	topic := throwawayTopic(t, 3)

	log, err := NewKafka(ctx, KafkaOptions{Brokers: brokers(t), Topic: topic, ClientID: "recordlog-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	// An empty topic replays nothing and returns.
	n := 0
	if err := log.Replay(ctx, func(Record) error { n++; return nil }); err != nil || n != 0 {
		t.Fatalf("empty replay: n=%d err=%v", n, err)
	}

	// Keys repeat, so the replay must carry every version and a caller's
	// last-write-wins lands on the final one.
	const keys, versions = 20, 5
	for v := 1; v <= versions; v++ {
		for k := 0; k < keys; k++ {
			if err := log.Append(ctx, fmt.Sprintf("acct-%02d", k), []byte(fmt.Sprintf("v%d", v))); err != nil {
				t.Fatal(err)
			}
		}
	}
	latest := map[string]string{}
	seen := 0
	if err := log.Replay(ctx, func(r Record) error {
		seen++
		latest[r.Key] = string(r.Value)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != keys*versions {
		t.Fatalf("replayed %d records, want %d (compaction has not run on a fresh topic)", seen, keys*versions)
	}
	for k := 0; k < keys; k++ {
		if got := latest[fmt.Sprintf("acct-%02d", k)]; got != fmt.Sprintf("v%d", versions) {
			t.Errorf("acct-%02d: last write %q", k, got)
		}
	}

	// A second Kafka over the same topic — a restarted server — sees the same.
	again, err := NewKafka(ctx, KafkaOptions{Brokers: brokers(t), Topic: topic})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	seen = 0
	if err := again.Replay(ctx, func(Record) error { seen++; return nil }); err != nil || seen != keys*versions {
		t.Fatalf("second replay: n=%d err=%v", seen, err)
	}

	// Replay stops when fn says so.
	calls := 0
	stop := fmt.Errorf("enough")
	if err := again.Replay(ctx, func(Record) error { calls++; return stop }); err != stop || calls != 1 {
		t.Fatalf("stop: calls=%d err=%v", calls, err)
	}

	// After Close, both operations refuse.
	_ = again.Close()
	if err := again.Append(ctx, "k", []byte("v")); err == nil {
		t.Fatal("append after close")
	}
}

func TestKafka_MissingTopic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := NewKafka(ctx, KafkaOptions{Brokers: brokers(t), Topic: "andara.test.does-not-exist"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing topic: %v", err)
	}
}
