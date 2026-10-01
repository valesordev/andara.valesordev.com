// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/recordlog"
)

// redpanda is a throwaway set of content topics on the local broker: the
// three content topics and an audit topic, deleted when the test ends.
type redpanda struct {
	brokers []string
	topics  map[string]string
	logs    map[string]recordlog.Log
}

func throwawayRedpanda(t *testing.T) *redpanda {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	r := &redpanda{brokers: strings.Split(v, ","), topics: map[string]string{}, logs: map[string]recordlog.Log{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(r.brokers...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	stamp := time.Now().UnixNano()
	for _, name := range []string{"blobs", "versions", "active", "audit"} {
		r.topics[name] = fmt.Sprintf("andara.test.cli.%s.%d", name, stamp)
		policy := "compact"
		if name == "audit" {
			policy = "delete"
		}
		if _, err := adm.CreateTopic(ctx, 1, 1, map[string]*string{"cleanup.policy": kadm.StringPtr(policy)}, r.topics[name]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, r.topics["blobs"], r.topics["versions"], r.topics["active"], r.topics["audit"])
		cl.Close()
	})
	return r
}

func (r *redpanda) open(t *testing.T) func(name string) recordlog.Log {
	return func(name string) recordlog.Log {
		if l, ok := r.logs[name]; ok {
			return l
		}
		l, err := recordlog.NewKafka(context.Background(), recordlog.KafkaOptions{Brokers: r.brokers, Topic: r.topics[name]})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		r.logs[name] = l
		return l
	}
}

// resolver reads the topics back on a cold cache, as a server's
// KafkaResolver does: the store blobs are read from, and the watcher of the
// active topic.
func (r *redpanda) resolver(t *testing.T) *content.KafkaResolver {
	t.Helper()
	res, err := content.NewKafkaResolver(content.KafkaOptions{
		Brokers: r.brokers, Cache: content.BlobCache{Dir: t.TempDir()},
		Topics: content.Topics{Blobs: r.topics["blobs"], Versions: r.topics["versions"], Active: r.topics["active"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Close() })
	return res
}

// TestContentValidate_PublishedVersionOverRedpanda is AW-CLI-002 AC-6
// against a throwaway Redpanda: Admin reads each blob back from the broker on
// a cold cache before streaming it to the CLI.
func TestContentValidate_PublishedVersionOverRedpanda(t *testing.T) {
	r := throwawayRedpanda(t)
	testContentValidatePack(t, startContentServer(t, r.open(t), r.resolver(t)))
}

// TestContentPublishPath_TwoIdentitiesOverRedpanda is AW-CLI-003's
// rehearsal against a throwaway Redpanda: every write lands on the broker,
// the Loader follows the active topic through a KafkaResolver, and `server
// info` shows what the Engine applied (AC-1–8).
func TestContentPublishPath_TwoIdentitiesOverRedpanda(t *testing.T) {
	r := throwawayRedpanda(t)
	res := r.resolver(t)
	testTwoIdentities(t, startContentStack(t, r.open(t), res, res))
}

// TestContentPublish_StaleParentOverRedpanda is AW-CLI-003's stale-parent
// case on the broker: another publish lands on the same parent between this
// one's uploads and its PublishVersion, and the second exits 1 stale_parent
// with its hint.
func TestContentPublish_StaleParentOverRedpanda(t *testing.T) {
	r := throwawayRedpanda(t)
	res := r.resolver(t)
	testStaleParent(t, startContentStack(t, r.open(t), res, res))
}
