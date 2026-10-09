// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package tickloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-053 AC-7: an offset the broker does not have is a gap the loop exits
// on (ErrOffsetGap, exit 3), not a silent reset and not starvation the loop
// logs and carries on through.
func TestKafkaSource_AnOffsetTheBrokerNoLongerHasIsAGap(t *testing.T) {
	bk := brokers(t)
	commands, _ := topics(t, bk)
	produce(t, bk, commands, 6)

	cl, err := kgo.NewClient(kgo.SeedBrokers(bk...))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ends, err := adm.ListEndOffsets(ctx, commands)
	if err != nil {
		t.Fatal(err)
	}
	part, end := int32(-1), int64(0)
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset > end {
			part, end = o.Partition, o.Offset
		}
	})
	if part < 0 || end < 2 {
		t.Skipf("the script put fewer than two records on one Partition (%d on %d)", end, part)
	}
	// The source resumes past the log's end, as after an unclean leader
	// election that cut the log below the checkpoint: the broker answers
	// OFFSET_OUT_OF_RANGE to every fetch.
	src, err := NewKafkaSource(ctx, KafkaSourceOptions{Brokers: bk, Group: "gap", Start: map[int32]int64{part: end + 5}, Topic: commands,
		LagEvery: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	deadline := time.Now().Add(15 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		_, last = src.Poll(nil, 10)
		if errors.Is(last, sim.ErrOffsetGap) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("Poll never reported a gap; last = %v", last)
}
