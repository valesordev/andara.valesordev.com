// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// Topics the loop reads and writes.
const (
	CommandsTopic = "andara.commands.v1"
	EventsTopic   = "andara.events.v1"
	// BoundaryPartition is where every TickCompleted record goes on
	// andara.events.v1, so replay reads one Partition in order. Events go
	// to their Zone's Partition.
	BoundaryPartition int32 = 0
	// BoundaryKey is the record key of a TickCompleted.
	BoundaryKey = "tick-boundary"
	// EventSchemaVersion is andara.log.v1.Event.schema_version today.
	EventSchemaVersion uint32 = 1
)

// KafkaSourceOptions configures a KafkaSource.
type KafkaSourceOptions struct {
	Brokers  []string
	Group    string // andara-sim-<env>: the reserved group the checkpoints are committed under
	ClientID string
	// Partitions and their starting offsets — the engine's next-to-read.
	// The consumer is assigned these directly rather than balanced by a
	// group: the chart assigns Partitions by pod ordinal (AW-INF-003) and a
	// balancer would fight it.
	Start map[int32]int64
	// BufferPerPartition bounds what is fetched ahead of the tick; a full
	// buffer pauses that Partition's fetches. Zero means 4096.
	BufferPerPartition int
	// LagEvery is how often end offsets are read for andara_consumer_lag.
	// Zero means 1s.
	LagEvery time.Duration
}

// KafkaSource consumes andara.commands.v1 for the loop. A goroutine fetches
// into per-Partition buffers so a broker stall is not a tick stall; Poll
// takes from the buffers and never blocks.
type KafkaSource struct {
	opts   KafkaSourceOptions
	client *kgo.Client
	adm    *kadm.Client
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	buffers  map[int32][]sim.Record
	expected map[int32]int64 // next offset the fetcher expects, per partition
	end      map[int32]int64 // broker end offsets, as last listed
	paused   map[int32]bool
	gap      error
	fetchErr error
	closed   bool
}

// NewKafkaSource connects, assigns the Partitions at their starting
// offsets, and starts fetching.
func NewKafkaSource(ctx context.Context, o KafkaSourceOptions) (*KafkaSource, error) {
	if len(o.Brokers) == 0 || o.Group == "" || len(o.Start) == 0 {
		return nil, errors.New("tickloop: brokers, group, and partitions are required")
	}
	if o.ClientID == "" {
		o.ClientID = "andara-server"
	}
	if o.BufferPerPartition <= 0 {
		o.BufferPerPartition = 4096
	}
	if o.LagEvery <= 0 {
		o.LagEvery = time.Second
	}
	assign := map[int32]kgo.Offset{}
	for p, off := range o.Start {
		assign[p] = kgo.NewOffset().At(off)
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(o.Brokers...),
		kgo.ClientID(o.ClientID+"-sim"),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{CommandsTopic: assign}),
	)
	if err != nil {
		return nil, fmt.Errorf("tickloop: consumer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("tickloop: ping brokers: %w", err)
	}
	s := &KafkaSource{
		opts: o, client: client, adm: kadm.NewClient(client), done: make(chan struct{}),
		buffers: map[int32][]sim.Record{}, expected: map[int32]int64{}, end: map[int32]int64{}, paused: map[int32]bool{},
	}
	for p, off := range o.Start {
		s.expected[p] = off
	}
	// A Partition whose start is past the engine's next-to-read is history
	// the log no longer has. Refuse now, with the numbers, rather than at
	// the first fetch.
	starts, err := s.adm.ListStartOffsets(ctx, CommandsTopic)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("tickloop: start offsets: %w", err)
	}
	for p, off := range o.Start {
		if so, ok := starts.Lookup(CommandsTopic, p); ok && so.Offset > off {
			client.Close()
			return nil, fmt.Errorf("%w: partition %d begins at %d, the World needs %d", sim.ErrOffsetGap, p, so.Offset, off)
		}
	}
	fctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.fetch(fctx)
	return s, nil
}

// fetch fills the buffers until Close.
func (s *KafkaSource) fetch(ctx context.Context) {
	defer close(s.done)
	lagTick := time.NewTicker(s.opts.LagEvery)
	defer lagTick.Stop()
	for ctx.Err() == nil {
		select {
		case <-lagTick.C:
			s.listEnds(ctx)
		default:
		}
		pctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		fetches := s.client.PollFetches(pctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if err := fetches.Err0(); err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			s.fetchErr = err
		} else if len(fetches) > 0 && fetches.Err0() == nil {
			s.fetchErr = nil
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if s.gap != nil {
				return
			}
			want := s.expected[r.Partition]
			if r.Offset != want {
				s.gap = fmt.Errorf("%w: partition %d expected offset %d, broker delivered %d", sim.ErrOffsetGap, r.Partition, want, r.Offset)
				return
			}
			var cmd logv1.LoggedCommand
			if err := proto.Unmarshal(r.Value, &cmd); err != nil {
				// A record the binary cannot decode is a schema break, and
				// skipping it would skip history. Treated like a gap.
				s.gap = fmt.Errorf("%w: partition %d offset %d does not decode: %v", sim.ErrOffsetGap, r.Partition, r.Offset, err)
				return
			}
			s.expected[r.Partition] = r.Offset + 1
			s.buffers[r.Partition] = append(s.buffers[r.Partition], sim.Record{Partition: r.Partition, Offset: r.Offset, Command: &cmd})
		})
		s.applyBackpressureLocked()
		s.mu.Unlock()
	}
}

// applyBackpressureLocked pauses full Partitions and resumes drained ones.
func (s *KafkaSource) applyBackpressureLocked() {
	var pause, resume []int32
	for p := range s.expected {
		full := len(s.buffers[p]) >= s.opts.BufferPerPartition
		switch {
		case full && !s.paused[p]:
			pause = append(pause, p)
			s.paused[p] = true
		case !full && s.paused[p] && len(s.buffers[p]) < s.opts.BufferPerPartition/2:
			resume = append(resume, p)
			s.paused[p] = false
		}
	}
	if len(pause) > 0 {
		s.client.PauseFetchPartitions(map[string][]int32{CommandsTopic: pause})
	}
	if len(resume) > 0 {
		s.client.ResumeFetchPartitions(map[string][]int32{CommandsTopic: resume})
	}
}

func (s *KafkaSource) listEnds(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ends, err := s.adm.ListEndOffsets(lctx, CommandsTopic)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.expected {
		if e, ok := ends.Lookup(CommandsTopic, p); ok {
			s.end[p] = e.Offset
		}
	}
}

// Poll implements Source.
func (s *KafkaSource) Poll(frozen []int32, max int) ([]sim.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gap != nil {
		return nil, s.gap
	}
	out := selectRoundRobin(s.buffers, frozen, max)
	if len(out) == 0 && s.fetchErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrStarved, s.fetchErr)
	}
	return out, nil
}

// Requeue implements Source.
func (s *KafkaSource) Requeue(recs []sim.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requeue(s.buffers, recs)
}

// Commit implements Source: the offsets are committed under the group so a
// dashboard, a runbook, and AW-SRV-006's recovery can read them.
func (s *KafkaSource) Commit(ctx context.Context, offsets map[int32]int64) error {
	var o kadm.Offsets
	for p, off := range offsets {
		o.Add(kadm.Offset{Topic: CommandsTopic, Partition: p, At: off, LeaderEpoch: -1})
	}
	resp, err := s.adm.CommitOffsets(ctx, s.opts.Group, o)
	if err != nil {
		return fmt.Errorf("tickloop: commit offsets: %w", err)
	}
	if err := resp.Error(); err != nil {
		return fmt.Errorf("tickloop: commit offsets: %w", err)
	}
	return nil
}

// Lag implements Source: end offset minus the next offset the loop will
// read (buffered records count as lag until applied).
func (s *KafkaSource) Lag() map[int32]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int32]int64, len(s.expected))
	for p, next := range s.expected {
		nextToApply := next - int64(len(s.buffers[p]))
		if end, ok := s.end[p]; ok && end > nextToApply {
			out[p] = end - nextToApply
		} else {
			out[p] = 0
		}
	}
	return out
}

// Pending implements Source.
func (s *KafkaSource) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.buffers {
		n += len(b)
	}
	return n
}

// Close implements Source.
func (s *KafkaSource) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	<-s.done
	s.client.Close()
	return nil
}

// KafkaPublisher writes Events and Tick Boundary Records to
// andara.events.v1 and cross-Zone Commands to andara.commands.v1, each on
// the Partition sim.PartitionFor names — never a client default partitioner
// (AW-SRV-001), which is why the client is built with ManualPartitioner.
type KafkaPublisher struct {
	client *kgo.Client
}

// NewKafkaPublisher connects a producer with acks=all and the idempotent
// producer on.
func NewKafkaPublisher(ctx context.Context, brokers []string, clientID string) (*KafkaPublisher, error) {
	if clientID == "" {
		clientID = "andara-server"
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID+"-tick"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.ProduceRequestTimeout(5*time.Second),
		kgo.RecordDeliveryTimeout(5*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("tickloop: producer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("tickloop: ping brokers: %w", err)
	}
	return &KafkaPublisher{client: client}, nil
}

// Publish implements Publisher. A zero-tick TickCompleted (the drain's) is
// not written.
func (k *KafkaPublisher) Publish(ctx context.Context, events []sim.Event, tc sim.TickCompleted) error {
	recs := make([]*kgo.Record, 0, len(events)+1)
	for _, ev := range events {
		payload, err := proto.Marshal(ev.Envelope)
		if err != nil {
			return fmt.Errorf("tickloop: marshal event %d: %w", ev.ID, err)
		}
		body, err := proto.Marshal(&logv1.Event{EventId: ev.ID, Tick: uint64(ev.Tick), ZoneId: string(ev.Zone), SchemaVersion: EventSchemaVersion, Payload: payload})
		if err != nil {
			return err
		}
		p := BoundaryPartition
		if ev.Zone != "" {
			p = sim.PartitionFor(ev.Zone)
		}
		recs = append(recs, &kgo.Record{Topic: EventsTopic, Partition: p, Key: []byte(ev.Zone), Value: body})
	}
	if tc.Tick > 0 {
		body, err := proto.Marshal(tc.Proto())
		if err != nil {
			return err
		}
		recs = append(recs, &kgo.Record{Topic: EventsTopic, Partition: BoundaryPartition, Key: []byte(BoundaryKey), Value: body})
	}
	if len(recs) == 0 {
		return nil
	}
	if err := k.client.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return fmt.Errorf("tickloop: publish to %s: %w", EventsTopic, err)
	}
	return nil
}

// Produce implements Publisher.
func (k *KafkaPublisher) Produce(ctx context.Context, cmds []*logv1.LoggedCommand) error {
	recs := make([]*kgo.Record, 0, len(cmds))
	for _, cmd := range cmds {
		body, err := proto.Marshal(cmd)
		if err != nil {
			return err
		}
		zone := sim.ZoneID(cmd.GetZoneId())
		recs = append(recs, &kgo.Record{Topic: CommandsTopic, Partition: sim.PartitionFor(zone), Key: []byte(zone), Value: body})
	}
	if len(recs) == 0 {
		return nil
	}
	if err := k.client.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return fmt.Errorf("tickloop: produce to %s: %w", CommandsTopic, err)
	}
	return nil
}

// Close implements Publisher.
func (k *KafkaPublisher) Close() error {
	k.client.Close()
	return nil
}

// ReadBoundaries reads every TickCompleted on andara.events.v1 from the
// start, in order, for replay (ADR-0002 §4). The RecordSource for replay
// is KafkaRecords.
func ReadBoundaries(ctx context.Context, brokers []string) ([]sim.TickCompleted, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{EventsTopic: {BoundaryPartition: kgo.NewOffset().AtStart()}}),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	adm := kadm.NewClient(client)
	ends, err := adm.ListEndOffsets(ctx, EventsTopic)
	if err != nil {
		return nil, err
	}
	end, _ := ends.Lookup(EventsTopic, BoundaryPartition)
	var out []sim.TickCompleted
	var next int64
	for next < end.Offset {
		fetches := client.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			return nil, err
		}
		fetches.EachRecord(func(r *kgo.Record) {
			next = r.Offset + 1
			if string(r.Key) != BoundaryKey {
				return
			}
			var tc logv1.TickCompleted
			if proto.Unmarshal(r.Value, &tc) == nil {
				out = append(out, sim.TickCompletedFromProto(&tc))
			}
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tick < out[j].Tick })
	return out, nil
}

// KafkaRecords is a sim.RecordSource over andara.commands.v1.
type KafkaRecords struct {
	Brokers []string
}

// Fetch implements sim.RecordSource.
func (k KafkaRecords) Fetch(partition int32, from, to int64) ([]sim.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := kgo.NewClient(
		kgo.SeedBrokers(k.Brokers...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{CommandsTopic: {partition: kgo.NewOffset().At(from)}}),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var out []sim.Record
	next := from
	for next < to {
		fetches := client.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			return nil, err
		}
		var ferr error
		fetches.EachRecord(func(r *kgo.Record) {
			if ferr != nil || r.Offset >= to {
				return
			}
			if r.Offset != next {
				ferr = fmt.Errorf("%w: partition %d expected %d, got %d", sim.ErrOffsetGap, partition, next, r.Offset)
				return
			}
			var cmd logv1.LoggedCommand
			if err := proto.Unmarshal(r.Value, &cmd); err != nil {
				ferr = err
				return
			}
			out = append(out, sim.Record{Partition: partition, Offset: r.Offset, Command: &cmd})
			next = r.Offset + 1
		})
		if ferr != nil {
			return nil, ferr
		}
	}
	return out, nil
}
