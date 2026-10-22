// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package projector_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/kafkaclient"
)

// AW-SRV-058 AC-6: the projector's readers, producer and committer, built from
// Topics with prefix it<nonce> and group it-<nonce> (never an environment's
// group, ADR-0013 Amendment 17), read it<nonce>.andara.commands.v1 and
// it<nonce>.andara.events.v1, write it<nonce>.andara.state.v1, and commit only
// under it-<nonce>.
func TestRun_UnderAPrefixReadsAndWritesThePrefixedTopics(t *testing.T) {
	nonce := fmt.Sprint(time.Now().UnixNano() % 1_000_000_000)
	tp, err := kafkaclient.TopicsFor(config.Config{KafkaTopicPrefix: "it" + nonce})
	if err != nil {
		t.Fatal(err)
	}
	pre := "it" + nonce + "."
	if tp.Commands() != pre+"andara.commands.v1" || tp.Events() != pre+"andara.events.v1" || tp.State() != pre+"andara.state.v1" {
		t.Fatalf("topics %q %q %q", tp.Commands(), tp.Events(), tp.State())
	}
	b := newBrokerNamed(t, tp.Commands(), tp.Events(), tp.State(), "it-"+nonce)
	w := script(t)
	b.mirror(w, 0)
	r := start(b.options(w))
	defer r.stop(t)

	// The committed tick is the round read from the prefixed commands and
	// events topics; the state it produced is on the prefixed state topic.
	b.waitCommitted(t, w.live.Tick())
	sameContent(t, b.topic(t), dumpOf(t, w))

	eventually.Observed(t, 30*time.Second, "offsets committed under it-"+nonce+" on "+tp.Commands()+" only", func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := b.adm.FetchOffsets(ctx, b.group)
		if err != nil {
			return false, err.Error()
		}
		offs := resp.Offsets()
		_, ok := offs[tp.Commands()]
		return ok && len(offs) == 1, fmt.Sprint(offs)
	})
}
