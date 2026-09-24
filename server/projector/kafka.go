// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// StateTopic is the compacted current-state topic this projector alone
// writes (AC-9).
const StateTopic = "andara.state.v1"

// ClientID names the projector's Kafka clients, and is the principal the
// andara.state.v1 write ACL names once the broker authenticates (AC-9).
const ClientID = "andara-projector-state"

// ErrLogGap: history the replica needs is not on the log — a Partition's
// start offset is past where the replica must read from, or the recorded
// boundaries skip a tick. Exit 3.
var ErrLogGap = errors.New("log gap")

// Boundary is a Tick Boundary Record as read, with when it was produced — the
// record timestamp, which is what the lag metric measures from.
type Boundary struct {
	sim.TickCompleted
	ProducedAt time.Time
}

// BoundaryReader streams every TickCompleted on andara.events.v1's boundary
// Partition, in order, from the start. Reading from the start is the cost
// tickloop.Recover pays too (AW-SRV-007's open question): the boundary
// Partition has no index by tick.
type BoundaryReader struct {
	client *kgo.Client
	buf    []Boundary
	// next is the offset after the last record read from the Partition, of
	// any key; hwm the Partition's high watermark as the last fetch reported
	// it. Together they say whether the reader is at the head.
	next, hwm int64
}

// NewBoundaryReader starts reading the boundary Partition from its start.
func NewBoundaryReader(ctx context.Context, brokers []string, eventsTopic string) (*BoundaryReader, error) {
	if eventsTopic == "" {
		eventsTopic = tickloop.EventsTopic
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(ClientID+"-boundaries"),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{eventsTopic: {tickloop.BoundaryPartition: kgo.NewOffset().AtStart()}}),
	)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return &BoundaryReader{client: client}, nil
}

// Next returns up to max boundaries, waiting at most wait for the first. An
// empty result with no error is a quiet log.
func (r *BoundaryReader) Next(ctx context.Context, max int, wait time.Duration) ([]Boundary, error) {
	if len(r.buf) == 0 {
		pctx, cancel := context.WithTimeout(ctx, wait)
		fetches := r.client.PollFetches(pctx)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		for _, fe := range fetches.Errors() {
			if !errors.Is(fe.Err, context.DeadlineExceeded) && !errors.Is(fe.Err, context.Canceled) {
				return nil, fmt.Errorf("read boundaries: %w", fe.Err)
			}
		}
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if p.HighWatermark > r.hwm {
				r.hwm = p.HighWatermark
			}
		})
		fetches.EachRecord(func(rec *kgo.Record) {
			r.next = rec.Offset + 1
			if string(rec.Key) != tickloop.BoundaryKey {
				return
			}
			var tc logv1.TickCompleted
			if proto.Unmarshal(rec.Value, &tc) != nil {
				// Skipped the way tickloop.ReadBoundaries skips it; the tick
				// it carried becomes a boundary gap, which is exit 3.
				return
			}
			r.buf = append(r.buf, Boundary{TickCompleted: sim.TickCompletedFromProto(&tc), ProducedAt: rec.Timestamp})
		})
	}
	n := min(max, len(r.buf))
	out := append([]Boundary(nil), r.buf[:n]...)
	r.buf = r.buf[n:]
	return out, nil
}

// AtHead reports whether every boundary on the Partition, as of the last
// fetch, has been handed out. A World that ticks continuously never leaves the
// Partition quiet, so "caught up" is a position, not an empty read.
func (r *BoundaryReader) AtHead() bool { return len(r.buf) == 0 && r.next >= r.hwm }

// Close closes the reader.
func (r *BoundaryReader) Close() { r.client.Close() }

// CommandSource is a sim.RecordSource over andara.commands.v1 that consumes
// once, from the replica's offsets, and serves Replay's ranges from
// per-Partition buffers. tickloop.KafkaRecords opens a client per Fetch,
// which suits a one-shot recovery and not a replica tailing the log at the
// tick rate.
type CommandSource struct {
	client  *kgo.Client
	topic   string
	timeout time.Duration
	buf     map[int32][]sim.Record
}

// NewCommandSource consumes every Partition in start from its offset. A
// Partition whose log begins past its start offset is history the log no
// longer has: ErrLogGap, naming both.
func NewCommandSource(ctx context.Context, brokers []string, topic string, start map[int32]int64) (*CommandSource, error) {
	if topic == "" {
		topic = tickloop.CommandsTopic
	}
	assign := map[int32]kgo.Offset{}
	for p, off := range start {
		assign[p] = kgo.NewOffset().At(off)
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(ClientID+"-commands"),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign}),
	)
	if err != nil {
		return nil, err
	}
	starts, err := kadm.NewClient(client).ListStartOffsets(ctx, topic)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("commands start offsets: %w", err)
	}
	for p, off := range start {
		if so, ok := starts.Lookup(topic, p); ok && so.Offset > off {
			client.Close()
			return nil, fmt.Errorf("%w: %s partition %d begins at %d, the replica needs %d", ErrLogGap, topic, p, so.Offset, off)
		}
	}
	return &CommandSource{client: client, topic: topic, timeout: 30 * time.Second, buf: map[int32][]sim.Record{}}, nil
}

// Fetch implements sim.RecordSource. Ranges on a Partition are requested in
// order, so a served range is dropped from the buffer.
func (c *CommandSource) Fetch(p int32, from, to int64) ([]sim.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	for {
		b := c.buf[p]
		for len(b) > 0 && b[0].Offset < from {
			b = b[1:]
		}
		c.buf[p] = b
		if len(b) > 0 && b[0].Offset != from {
			return nil, fmt.Errorf("%w: partition %d expected offset %d, buffer starts at %d", sim.ErrOffsetGap, p, from, b[0].Offset)
		}
		if int64(len(b)) >= to-from {
			out := b[:to-from]
			c.buf[p] = b[to-from:]
			return out, nil
		}
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil, fmt.Errorf("fetch %s partition %d [%d,%d): %w", c.topic, p, from, to, ctx.Err())
		}
		if err := fetches.Err0(); err != nil {
			return nil, err
		}
		var derr error
		fetches.EachRecord(func(r *kgo.Record) {
			if derr != nil {
				return
			}
			var cmd logv1.LoggedCommand
			if err := proto.Unmarshal(r.Value, &cmd); err != nil {
				derr = fmt.Errorf("%w: partition %d offset %d does not decode: %v", sim.ErrOffsetGap, r.Partition, r.Offset, err)
				return
			}
			c.buf[r.Partition] = append(c.buf[r.Partition], sim.Record{Partition: r.Partition, Offset: r.Offset, Command: &cmd})
		})
		if derr != nil {
			return nil, derr
		}
	}
}

// Close closes the source.
func (c *CommandSource) Close() { c.client.Close() }

// Producer writes records to andara.state.v1 on the Partition each names —
// the Zone's, the commands partitioner's — with acks=all and the idempotent
// producer, franz-go's defaults, which keep a key's records in order.
type Producer struct {
	client *kgo.Client
	topic  string
}

// NewProducer connects the producer.
func NewProducer(ctx context.Context, brokers []string, topic string) (*Producer, error) {
	if topic == "" {
		topic = StateTopic
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(ClientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.RecordDeliveryTimeout(time.Minute),
	)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return &Producer{client: client, topic: topic}, nil
}

// Produce writes recs and waits until every one is acknowledged; the first
// failure is returned. Offsets are committed only after this returns nil.
func (p *Producer) Produce(ctx context.Context, recs []Out) error {
	if len(recs) == 0 {
		return nil
	}
	krecs := make([]*kgo.Record, len(recs))
	for i, r := range recs {
		krecs[i] = &kgo.Record{Topic: p.topic, Partition: r.Partition, Key: []byte(r.Key), Value: r.Value}
	}
	return p.client.ProduceSync(ctx, krecs...).FirstErr()
}

// Close closes the producer.
func (p *Producer) Close() { p.client.Close() }

// Checkpoint is where the projector has produced through: a tick and the
// next-to-read offset per commands Partition after it.
type Checkpoint struct {
	Tick    sim.Tick
	Offsets map[int32]int64
}

// Committer commits Checkpoints under the projector's consumer group on
// andara.commands.v1, with the tick in each offset's metadata. The group is
// never joined — the projector reads by assignment, as the tick loop does —
// so the commit is the group's only content.
type Committer struct {
	adm   *kadm.Client
	cl    *kgo.Client
	group string
	topic string
}

// NewCommitter connects an admin client for group.
func NewCommitter(brokers []string, group, commandsTopic string) (*Committer, error) {
	if commandsTopic == "" {
		commandsTopic = tickloop.CommandsTopic
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID(ClientID+"-commit"))
	if err != nil {
		return nil, err
	}
	return &Committer{adm: kadm.NewClient(cl), cl: cl, group: group, topic: commandsTopic}, nil
}

const tickMeta = "tick="

// Commit records cp.
func (c *Committer) Commit(ctx context.Context, cp Checkpoint) error {
	var o kadm.Offsets
	meta := tickMeta + strconv.FormatUint(uint64(cp.Tick), 10)
	for p, off := range cp.Offsets {
		o.Add(kadm.Offset{Topic: c.topic, Partition: p, At: off, LeaderEpoch: -1, Metadata: meta})
	}
	resp, err := c.adm.CommitOffsets(ctx, c.group, o)
	if err != nil {
		return fmt.Errorf("commit %s: %w", c.group, err)
	}
	return resp.Error()
}

// Last reads the committed Checkpoint; ok is false when the group has none.
func (c *Committer) Last(ctx context.Context) (Checkpoint, bool, error) {
	resp, err := c.adm.FetchOffsets(ctx, c.group)
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("fetch %s offsets: %w", c.group, err)
	}
	if err := resp.Error(); err != nil {
		return Checkpoint{}, false, fmt.Errorf("fetch %s offsets: %w", c.group, err)
	}
	cp := Checkpoint{Offsets: map[int32]int64{}}
	found := false
	var tick uint64
	for _, r := range resp.Offsets()[c.topic] {
		if r.At < 0 {
			continue
		}
		t, ok := strings.CutPrefix(r.Metadata, tickMeta)
		if !ok {
			return Checkpoint{}, false, fmt.Errorf("group %s partition %d carries metadata %q, not a tick; was it committed by something else?", c.group, r.Partition, r.Metadata)
		}
		n, err := strconv.ParseUint(t, 10, 64)
		if err != nil {
			return Checkpoint{}, false, fmt.Errorf("group %s partition %d: bad tick metadata %q", c.group, r.Partition, r.Metadata)
		}
		if found && n != tick {
			return Checkpoint{}, false, fmt.Errorf("group %s: partitions disagree on the committed tick (%d and %d)", c.group, tick, n)
		}
		tick, found = n, true
		cp.Offsets[r.Partition] = r.At
	}
	cp.Tick = sim.Tick(tick)
	return cp, found, nil
}

// Wipe deletes the group, for --rebuild.
func (c *Committer) Wipe(ctx context.Context) error {
	resp, err := c.adm.DeleteGroups(ctx, c.group)
	if err != nil {
		return err
	}
	for _, r := range resp {
		if r.Err != nil && !strings.Contains(r.Err.Error(), "GROUP_ID_NOT_FOUND") {
			return fmt.Errorf("delete group %s: %w", c.group, r.Err)
		}
	}
	return nil
}

// Close closes the committer.
func (c *Committer) Close() { c.cl.Close() }

// TopicKeys reads the compacted state topic end to end and returns every live
// key with its Partition — the input Reconcile needs. A key whose latest
// record is a tombstone is not live. Reading a compacted topic costs time
// proportional to live state, which is the point of the topic.
func TopicKeys(ctx context.Context, brokers []string, topic string) (map[string]int32, error) {
	if topic == "" {
		topic = StateTopic
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID(ClientID+"-keys"))
	if err != nil {
		return nil, err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	starts, err := adm.ListStartOffsets(ctx, topic)
	if err != nil {
		return nil, err
	}
	ends, err := adm.ListEndOffsets(ctx, topic)
	if err != nil {
		return nil, err
	}
	assign := map[int32]kgo.Offset{}
	remaining := map[int32]int64{}
	ends.Each(func(o kadm.ListedOffset) {
		so, _ := starts.Lookup(topic, o.Partition)
		if o.Offset > so.Offset {
			assign[o.Partition] = kgo.NewOffset().At(so.Offset)
			remaining[o.Partition] = o.Offset
		}
	})
	live := map[string]int32{}
	if len(assign) == 0 {
		return live, nil
	}
	cl.AddConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign})
	for len(remaining) > 0 {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := fetches.Err0(); err != nil {
			return nil, err
		}
		fetches.EachRecord(func(r *kgo.Record) {
			end, ok := remaining[r.Partition]
			if !ok || r.Offset >= end {
				return
			}
			if r.Value == nil {
				delete(live, string(r.Key))
			} else {
				live[string(r.Key)] = r.Partition
			}
			if r.Offset+1 >= end {
				delete(remaining, r.Partition)
			}
		})
	}
	return live, nil
}

// TopicBytes sums the topic's log size over partitions, one replica each —
// the largest, since replicas of a partition hold the same log.
func TopicBytes(ctx context.Context, adm *kadm.Client, topic string) (int64, error) {
	dirs, err := adm.DescribeAllLogDirs(ctx, kadm.TopicsSet{topic: nil})
	if err != nil {
		return 0, err
	}
	sizes := map[int32]int64{}
	dirs.Each(func(d kadm.DescribedLogDir) {
		d.Topics.Each(func(p kadm.DescribedLogDirPartition) {
			if p.Topic == topic && p.Size > sizes[p.Partition] {
				sizes[p.Partition] = p.Size
			}
		})
	})
	parts := make([]int, 0, len(sizes))
	for p := range sizes {
		parts = append(parts, int(p))
	}
	sort.Ints(parts)
	var total int64
	for _, p := range parts {
		total += sizes[int32(p)]
	}
	return total, nil
}
