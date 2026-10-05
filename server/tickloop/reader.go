// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// The boundary reader and the command source are what recovery and the state
// projector both read the log with (AW-SRV-007): they began as the
// projector's (AW-SRV-019) and moved here when recovery needed the same seek.
// The client ID names the caller's Kafka clients.

// ErrLogGap: history the replica needs is not on the log — a Partition's
// start offset is past where the replica must read from, or the recorded
// boundaries skip a tick. Exit 3.
var ErrLogGap = errors.New("log gap")

// LogGapError is ErrLogGap with what disagreed: the Partition's earliest
// offset on the log (Have) is past where the caller must read from (Need)
// (AW-SRV-007 AC-2). errors.Is(err, ErrLogGap) holds.
type LogGapError struct {
	Topic     string
	Partition int32
	Need      int64
	Have      int64
}

func (e *LogGapError) Error() string {
	return fmt.Sprintf("%v: %s partition %d begins at %d, the replica needs %d", ErrLogGap, e.Topic, e.Partition, e.Have, e.Need)
}

func (e *LogGapError) Unwrap() error { return ErrLogGap }

// Boundary is a Tick Boundary Record as read, with when it was produced — the
// record timestamp, which is what the lag metric measures from.
type Boundary struct {
	sim.TickCompleted
	ProducedAt time.Time
}

// BoundaryReader streams every TickCompleted on andara.events.v1's boundary
// Partition, in order, from the start, or from a tick SeekAfter found. The
// Partition has no index by tick (AW-SRV-007's open question), so a replica
// that bootstraps from a round finds its place by binary search rather than
// by reading the history before it (AW-SRV-019 AC-6).
type BoundaryReader struct {
	// client talks to the broker and consumes nothing: it lists offsets and
	// pings. consumer reads the Partition, and is built by consume at the
	// offset the reader was positioned at. It is never moved with SetOffsets
	// from a position it had already started fetching from: a client built at
	// the start or the end and then moved can race its own first offset
	// resolution, and a reader that lost sat where it began and read nothing
	// (the intermittent projector timeouts after AW-SRV-007, #422).
	client   *kgo.Client
	consumer *kgo.Client
	brokers  []string
	topic    string
	clientID string
	buf      []Boundary
	// next is the offset after the last record read from the Partition, of
	// any key; hwm the Partition's high watermark as the last fetch reported
	// it. Together they say whether the reader is at the head.
	next, hwm int64
	// expect, once SeekAfter has moved the reader, is the tick the first
	// boundary must carry. A later one means the search landed past it —
	// boundaries out of tick order — and the reader starts over from the
	// Partition's start, which is what it did before it could seek.
	expect sim.Tick
	start  int64
	// boundaryAfter is whether SeekAfter found a boundary for a tick after
	// the one it searched for, anywhere from where its search landed to the
	// Partition's end: records that aren't boundaries (a tick's Events
	// sent before its boundary) can follow the last one (review of #365).
	boundaryAfter bool
	// positioned is whether the reader has a consumer at the position it was
	// told to read from: by SeekAfter, or at the Partition's start by the
	// first Next. The consumer is built then, and not before, so it prefetches
	// nothing while SeekAfter is still searching for the place past the history
	// (AW-SRV-007 AC-12).
	positioned bool
}

// NewBoundaryReader starts reading the boundary Partition from its start.
func NewBoundaryReader(ctx context.Context, brokers []string, eventsTopic, clientID string) (*BoundaryReader, error) {
	if eventsTopic == "" {
		eventsTopic = EventsTopic
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID+"-boundaries"),
	)
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return &BoundaryReader{client: client, brokers: brokers, topic: eventsTopic, clientID: clientID}, nil
}

// SeekAfter moves the reader to where the boundary for tick+1 is, or just
// before it, so the boundaries at or before tick are never read. Boundary
// ticks rise with the offset, so the first boundary at or after an offset is
// a monotonic function of it and a binary search over the Partition finds
// the place in log2(records) probes. Called before the first Next.
//
// It is a position, not a promise: the first boundary Next delivers is
// checked against tick+1, and a reader that finds a later one falls back to
// the start (see expect).
func (r *BoundaryReader) SeekAfter(ctx context.Context, tick sim.Tick) (int64, error) {
	adm := kadm.NewClient(r.client)
	starts, err := adm.ListStartOffsets(ctx, r.topic)
	if err != nil {
		return 0, fmt.Errorf("boundary start offset: %w", err)
	}
	ends, err := adm.ListEndOffsets(ctx, r.topic)
	if err != nil {
		return 0, fmt.Errorf("boundary end offset: %w", err)
	}
	so, _ := starts.Lookup(r.topic, BoundaryPartition)
	eo, _ := ends.Lookup(r.topic, BoundaryPartition)
	if so.Err != nil || eo.Err != nil {
		return 0, fmt.Errorf("boundary offsets: %w", errors.Join(so.Err, eo.Err))
	}
	lo, hi := so.Offset, eo.Offset
	for lo < hi {
		mid := lo + (hi-lo)/2
		t, at, ok, err := r.probe(ctx, mid, hi)
		if err != nil {
			return 0, err
		}
		if ok && t <= tick {
			lo = at + 1
		} else {
			hi = mid
		}
	}
	r.boundaryAfter = false
	if lo < eo.Offset {
		t, _, ok, err := r.probe(ctx, lo, eo.Offset)
		if err != nil {
			return 0, err
		}
		r.boundaryAfter = ok && t > tick
	}
	r.start, r.expect = so.Offset, tick+1
	if err := r.consume(lo); err != nil {
		return 0, err
	}
	r.positioned = true
	return lo, nil
}

// HeadTick is the tick of the last boundary on the Partition now, 0 for an
// empty log: where a replay that must end at "the head" ends, fixed when it is
// asked. A log still being written (a verify against a running server) has a
// moving end, and a replay that chased it would not finish. It reads backward
// from the end in growing windows, so the cost is the tail, not the history.
func (r *BoundaryReader) HeadTick(ctx context.Context) (sim.Tick, error) {
	adm := kadm.NewClient(r.client)
	starts, err := adm.ListStartOffsets(ctx, r.topic)
	if err != nil {
		return 0, fmt.Errorf("boundary start offset: %w", err)
	}
	ends, err := adm.ListEndOffsets(ctx, r.topic)
	if err != nil {
		return 0, fmt.Errorf("boundary end offset: %w", err)
	}
	so, _ := starts.Lookup(r.topic, BoundaryPartition)
	eo, _ := ends.Lookup(r.topic, BoundaryPartition)
	if so.Err != nil || eo.Err != nil {
		return 0, fmt.Errorf("boundary offsets: %w", errors.Join(so.Err, eo.Err))
	}
	for window := int64(64); ; window *= 4 {
		from := max(so.Offset, eo.Offset-window)
		if t, ok, err := r.lastBoundary(ctx, from, eo.Offset); err != nil || ok {
			return t, err
		}
		if from <= so.Offset {
			return 0, nil
		}
	}
}

// lastBoundary reads [from, end) and returns the last boundary's tick.
func (r *BoundaryReader) lastBoundary(ctx context.Context, from, end int64) (sim.Tick, bool, error) {
	if from >= end {
		return 0, false, nil
	}
	cl, err := r.probeClient(from)
	if err != nil {
		return 0, false, err
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var (
		tick  sim.Tick
		found bool
	)
	for next := from; next < end; {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return 0, false, ctx.Err()
		}
		if err := fetches.Err0(); err != nil {
			return 0, false, fmt.Errorf("read the boundary head at %d: %w", from, err)
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			next = rec.Offset + 1
			if rec.Offset >= end || string(rec.Key) != BoundaryKey {
				return
			}
			var tc logv1.TickCompleted
			if proto.Unmarshal(rec.Value, &tc) != nil {
				return
			}
			tick, found = sim.Tick(tc.GetTick()), true
		})
	}
	return tick, found, nil
}

// probeClient is a client for one probe, built at the offset it reads from and
// never moved. Its fetches are small: a probe wants the first boundary at an
// offset, not a megabyte, and a 1 MiB fetch per probe was tens of MB of peak
// RSS in a recovery over a long history (AW-SRV-007 AC-12).
func (r *BoundaryReader) probeClient(offset int64) (*kgo.Client, error) {
	return kgo.NewClient(
		kgo.SeedBrokers(r.brokers...),
		kgo.ClientID(r.clientID+"-boundaries"),
		kgo.FetchMaxBytes(256<<10),
		kgo.FetchMaxPartitionBytes(128<<10),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{r.topic: {BoundaryPartition: kgo.NewOffset().At(offset)}}),
	)
}

// probe reads from offset until the first boundary, returning its tick and
// offset, or ok=false if there is none before end.
func (r *BoundaryReader) probe(ctx context.Context, offset, end int64) (sim.Tick, int64, bool, error) {
	cl, err := r.probeClient(offset)
	if err != nil {
		return 0, 0, false, err
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return 0, 0, false, ctx.Err()
		}
		if err := fetches.Err0(); err != nil {
			return 0, 0, false, fmt.Errorf("probe boundaries at %d: %w", offset, err)
		}
		var (
			found bool
			tick  sim.Tick
			at    int64
			last  = offset - 1
		)
		fetches.EachRecord(func(rec *kgo.Record) {
			last = rec.Offset
			if found || string(rec.Key) != BoundaryKey {
				return
			}
			var tc logv1.TickCompleted
			if proto.Unmarshal(rec.Value, &tc) != nil {
				return
			}
			found, tick, at = true, sim.Tick(tc.GetTick()), rec.Offset
		})
		if found {
			return tick, at, true, nil
		}
		if last+1 >= end {
			return 0, 0, false, nil
		}
	}
}

// Next returns up to max boundaries, waiting at most wait for the first. An
// empty result with no error is a quiet log.
func (r *BoundaryReader) Next(ctx context.Context, max int, wait time.Duration) ([]Boundary, error) {
	if !r.positioned {
		if err := r.positionAtStart(ctx); err != nil {
			return nil, err
		}
	}
	if len(r.buf) == 0 {
		pctx, cancel := context.WithTimeout(ctx, wait)
		fetches := r.consumer.PollFetches(pctx)
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
			if string(rec.Key) != BoundaryKey {
				return
			}
			var tc logv1.TickCompleted
			if proto.Unmarshal(rec.Value, &tc) != nil {
				// Skipped the way ReadBoundaries skips it; the tick
				// it carried becomes a boundary gap, which is exit 3.
				return
			}
			r.buf = append(r.buf, Boundary{TickCompleted: sim.TickCompletedFromProto(&tc), ProducedAt: rec.Timestamp})
		})
	}
	if r.expect != 0 && len(r.buf) > 0 {
		if got := r.buf[0].Tick; got > r.expect {
			// The search landed past the boundary it was looking for. Read
			// from the start instead, as the reader did before it could seek.
			if err := r.consume(r.start); err != nil {
				return nil, err
			}
			r.buf, r.next, r.expect = nil, r.start, 0
			return nil, nil
		}
		r.expect = 0
	}
	n := min(max, len(r.buf))
	out := append([]Boundary(nil), r.buf[:n]...)
	r.buf = r.buf[n:]
	return out, nil
}

// positionAtStart points the reader at the Partition's first offset, for a
// caller that read before it sought.
func (r *BoundaryReader) positionAtStart(ctx context.Context) error {
	starts, err := kadm.NewClient(r.client).ListStartOffsets(ctx, r.topic)
	if err != nil {
		return fmt.Errorf("boundary start offset: %w", err)
	}
	so, ok := starts.Lookup(r.topic, BoundaryPartition)
	if !ok || so.Err != nil {
		return fmt.Errorf("boundary start offset: %w", so.Err)
	}
	r.start, r.next = so.Offset, so.Offset
	if err := r.consume(so.Offset); err != nil {
		return err
	}
	r.positioned = true
	return nil
}

// consume (re)builds the consumer at offset.
func (r *BoundaryReader) consume(offset int64) error {
	if r.consumer != nil {
		r.consumer.Close()
		r.consumer = nil
	}
	c, err := kgo.NewClient(
		kgo.SeedBrokers(r.brokers...),
		kgo.ClientID(r.clientID+"-boundaries"),
		kgo.FetchMaxBytes(16<<20),
		kgo.FetchMaxPartitionBytes(4<<20),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{r.topic: {BoundaryPartition: kgo.NewOffset().At(offset)}}),
	)
	if err != nil {
		return fmt.Errorf("boundary consumer at %d: %w", offset, err)
	}
	r.consumer = c
	return nil
}

// BoundaryAfter reports whether SeekAfter found a boundary for a tick after
// the one it searched for: the log still holds the round's own boundary, or a
// later one. False means the history the caller needs is gone (ErrLogGap).
func (r *BoundaryReader) BoundaryAfter() bool { return r.boundaryAfter }

// Unread puts boundaries back at the front of the reader's buffer, for a
// caller that read ahead to learn the Partition set and then wants to replay
// from the first one.
func (r *BoundaryReader) Unread(bs []Boundary) {
	r.buf = append(append([]Boundary(nil), bs...), r.buf...)
}

// AtHead reports whether every boundary on the Partition, as of the last
// fetch, has been handed out. A World that ticks continuously never leaves the
// Partition quiet, so "caught up" is a position, not an empty read.
func (r *BoundaryReader) AtHead() bool { return len(r.buf) == 0 && r.next >= r.hwm }

// Close closes the reader.
func (r *BoundaryReader) Close() {
	r.client.Close()
	if r.consumer != nil {
		r.consumer.Close()
	}
}

// CommandSource is a sim.RecordSource over andara.commands.v1 that consumes
// once, from the replica's offsets, and serves Replay's ranges from
// per-Partition buffers. KafkaRecords opens a client per Fetch,
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
func NewCommandSource(ctx context.Context, brokers []string, topic, clientID string, start map[int32]int64) (*CommandSource, error) {
	if topic == "" {
		topic = CommandsTopic
	}
	assign := map[int32]kgo.Offset{}
	for p, off := range start {
		assign[p] = kgo.NewOffset().At(off)
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID+"-commands"),
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
			return nil, &LogGapError{Topic: topic, Partition: p, Need: off, Have: so.Offset}
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
