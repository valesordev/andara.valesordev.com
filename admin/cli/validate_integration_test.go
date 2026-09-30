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

// TestContentValidate_PublishedVersionOverRedpanda is AC-6 against a
// throwaway Redpanda: the content topics are real, and Admin reads each blob
// back from the broker on a cold cache before streaming it to the CLI.
func TestContentValidate_PublishedVersionOverRedpanda(t *testing.T) {
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	brokers := strings.Split(v, ",")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	stamp := time.Now().UnixNano()
	topics := map[string]string{}
	for _, name := range []string{"blobs", "versions", "active", "audit"} {
		topics[name] = fmt.Sprintf("andara.test.validate.%s.%d", name, stamp)
		policy := "compact"
		if name == "audit" {
			policy = "delete"
		}
		if _, err := adm.CreateTopic(ctx, 1, 1, map[string]*string{"cleanup.policy": kadm.StringPtr(policy)}, topics[name]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopics(dctx, topics["blobs"], topics["versions"], topics["active"], topics["audit"])
		cl.Close()
	})

	logs := map[string]recordlog.Log{}
	open := func(name string) recordlog.Log {
		if l, ok := logs[name]; ok {
			return l
		}
		l, err := recordlog.NewKafka(context.Background(), recordlog.KafkaOptions{Brokers: brokers, Topic: topics[name]})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		logs[name] = l
		return l
	}
	resolver, err := content.NewKafkaResolver(content.KafkaOptions{
		Brokers: brokers, Cache: content.BlobCache{Dir: t.TempDir()},
		Topics: content.Topics{Blobs: topics["blobs"], Versions: topics["versions"], Active: topics["active"]},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	testContentValidatePack(t, startContentServer(t, open, resolver))
}
