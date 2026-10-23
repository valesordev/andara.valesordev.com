// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package tickloop

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// prefixed resolves Topics for an it<nonce> prefix (ADR-0013 Amendment 17:
// never an environment's name) and creates its commands topic, deleted with
// the it-<nonce> group when the test ends.
func prefixed(t *testing.T, bk kafkaclient.Config) (kafkaclient.Topics, string, string) {
	t.Helper()
	nonce := fmt.Sprint(time.Now().UnixNano() % 1_000_000_000)
	tp, err := kafkaclient.TopicsFor(config.Config{KafkaTopicPrefix: "it" + nonce})
	if err != nil {
		t.Fatal(err)
	}
	group := "it-" + nonce
	cl, err := kafkaclient.New(bk.For("andara-it"))
	if err != nil {
		t.Fatal(err)
	}
	adm := kadm.NewClient(cl)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := adm.CreateTopic(ctx, sim.PartitionCount, 1, nil, tp.Commands()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteGroups(dctx, group)
		_, _ = adm.DeleteTopics(dctx, tp.Commands())
		cl.Close()
	})
	return tp, nonce, group
}

// AW-SRV-058 AC-2: the ingress producer and the tick loop's Kafka source, built
// from Topics with prefix it<nonce>, write and read it<nonce>.andara.commands.v1
// and commit only under it-<nonce>.
func TestPrefix_IngressAndTheTickLoopSourceUseThePrefixedCommandsTopic(t *testing.T) {
	bk := brokers(t)
	tp, nonce, group := prefixed(t, bk)
	want := "it" + nonce + ".andara.commands.v1"
	if tp.Commands() != want {
		t.Fatalf("commands topic %q, want %q", tp.Commands(), want)
	}

	prod, err := ingress.NewKafkaProducer(ingress.ProducerOptions{Kafka: bk, Topic: tp.Commands(), ClientID: "andara-server", Deadline: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = prod.Close() }()
	cmd := simtest.Move("zone-"+nonce, "actor-"+nonce, "east")
	acc, err := prod.Produce(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}

	// The record the produce acknowledged is on the prefixed topic, keyed
	// with the nonce.
	cl, err := kafkaclient.New(bk.For("andara-it"), kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{tp.Commands(): {acc.Partition: kgo.NewOffset().At(acc.Offset)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	var got *kgo.Record
	eventually.Observed(t, 30*time.Second, "the acknowledged record on "+want, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cl.PollFetches(ctx).EachRecord(func(r *kgo.Record) {
			if r.Offset == acc.Offset && string(r.Key) == "zone-"+nonce {
				got = r
			}
		})
		return got != nil, fmt.Sprintf("partition %d offset %d not read yet", acc.Partition, acc.Offset)
	})
	if got.Topic != want {
		t.Errorf("record topic %q, want %q", got.Topic, want)
	}

	// The tick loop's source reads it from there, and commits under the
	// it- group alone.
	src, err := NewKafkaSource(context.Background(), KafkaSourceOptions{
		Kafka: bk, Group: group, Topic: tp.Commands(), Start: map[int32]int64{acc.Partition: acc.Offset}, LagEvery: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	var recs []sim.Record
	eventually.Observed(t, 30*time.Second, "the source reads the nonce's Command", func() (bool, string) {
		rs, err := src.Poll(nil, 10)
		if err != nil {
			return false, err.Error()
		}
		recs = append(recs, rs...)
		for _, r := range recs {
			if r.Offset == acc.Offset && r.Command.GetActorId() == "actor-"+nonce {
				return true, ""
			}
		}
		return false, fmt.Sprintf("%d records", len(recs))
	})
	if err := src.Commit(context.Background(), map[int32]int64{acc.Partition: acc.Offset + 1}); err != nil {
		t.Fatal(err)
	}

	adm := kadm.NewClient(cl)
	eventually.Observed(t, 30*time.Second, "offsets committed under "+group+" on "+want+" only", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := adm.FetchOffsets(ctx, group)
		if err != nil {
			return false, err.Error()
		}
		offs := resp.Offsets()
		o, ok := offs.Lookup(want, acc.Partition)
		return ok && len(offs) == 1 && o.At == acc.Offset+1, fmt.Sprintf("%v", offs)
	})
}
