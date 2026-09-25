// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
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
	// Topic overrides CommandsTopic; tests use throwaway topics.
	Topic string
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
	if o.Topic == "" {
		o.Topic = CommandsTopic
	}
	assign := map[int32]kgo.Offset{}
	for p, off := range o.Start {
		assign[p] = kgo.NewOffset().At(off)
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(o.Brokers...),
		kgo.ClientID(o.ClientID+"-sim"),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{o.Topic: assign}),
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
	starts, err := s.adm.ListStartOffsets(ctx, o.Topic)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("tickloop: start offsets: %w", err)
	}
	for p, off := range o.Start {
		if so, ok := starts.Lookup(o.Topic, p); ok && so.Offset > off {
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
	var lastPing time.Time
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
		// franz-go retries a lost broker internally and a poll just comes
		// back empty, so an empty poll is checked with a ping: a broker that
		// answers is a quiet log, one that does not is starvation (AC-9).
		var pingErr error
		pinged := false
		var fetchErr error
		for _, fe := range fetches.Errors() {
			if !errors.Is(fe.Err, context.DeadlineExceeded) && !errors.Is(fe.Err, context.Canceled) {
				fetchErr = fe.Err
				break
			}
		}
		if fetches.NumRecords() == 0 && time.Since(lastPing) >= s.opts.LagEvery {
			lastPing = time.Now()
			pinged = true
			pctx, pcancel := context.WithTimeout(ctx, 500*time.Millisecond)
			pingErr = s.client.Ping(pctx)
			pcancel()
		}
		s.mu.Lock()
		switch {
		case fetchErr != nil:
			s.fetchErr = fetchErr
		case pinged && pingErr != nil && ctx.Err() == nil:
			s.fetchErr = fmt.Errorf("broker unreachable: %w", pingErr)
		case fetches.NumRecords() > 0 || (pinged && pingErr == nil):
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
		s.client.PauseFetchPartitions(map[string][]int32{s.opts.Topic: pause})
	}
	if len(resume) > 0 {
		s.client.ResumeFetchPartitions(map[string][]int32{s.opts.Topic: resume})
	}
}

func (s *KafkaSource) listEnds(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ends, err := s.adm.ListEndOffsets(lctx, s.opts.Topic)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for p := range s.expected {
		if e, ok := ends.Lookup(s.opts.Topic, p); ok {
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
	// Bounded: a checkpoint during an outage must fail fast, not hold the
	// tick until the broker returns.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var o kadm.Offsets
	for p, off := range offsets {
		o.Add(kadm.Offset{Topic: s.opts.Topic, Partition: p, At: off, LeaderEpoch: -1})
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
//
// Produce is asynchronous: records go into the client's bounded buffer and
// the tick continues, because Events are derived and a broker stall must
// not be a tick stall (ADR-0002 §3). A delivery that fails after the
// client's retries is counted through OnFailure; a full buffer refuses the
// tick's records outright rather than blocking. The drain flushes.
type KafkaPublisher struct {
	client *kgo.Client
	// Commands and Events override the topic names; tests use throwaway
	// topics.
	Commands, Events string
	// OnFailure, if set, is called once per record the broker never
	// acknowledged, with the topic it was for.
	OnFailure func(topic string, err error)
	// OnBoundaryLost, if set, is called once, the first time a Tick
	// Boundary Record is not delivered.
	OnBoundaryLost func(tick sim.Tick, err error)

	// OnBoundaryAcked, if set, is called when a Tick Boundary Record is
	// acknowledged, with how long after Publish that was — the Event
	// producer's lag behind the tick (andara_event_publish_lag_seconds).
	OnBoundaryAcked func(tick sim.Tick, lag time.Duration)

	boundaryLost atomic.Bool
	lostAtTick   atomic.Uint64
}

// canonical is the one marshal every log record goes through: deterministic,
// so two serializations of one Event are byte-identical (AW-SRV-004 AC-3).
// Protobuf is not canonical by default; with no map fields in log.v1 (a test
// walks the descriptors) and this option set, it is here.
var canonical = proto.MarshalOptions{Deterministic: true}

// EventRecord renders one Event as its andara.events.v1 record.
func EventRecord(ev sim.Event) (*logv1.Event, error) {
	payload, err := canonical.Marshal(ev.Envelope)
	if err != nil {
		return nil, fmt.Errorf("tickloop: marshal event %d: %w", ev.ID, err)
	}
	return &logv1.Event{EventId: ev.ID, Tick: uint64(ev.Tick), ZoneId: string(ev.Zone), SchemaVersion: EventSchemaVersion, Payload: payload, Scope: ev.Scope.Proto()}, nil
}

// ErrBoundaryLost: a Tick Boundary Record was not delivered, so no later
// one will be published by this process — see Publish.
var ErrBoundaryLost = errors.New("tick boundary lost; boundaries are no longer published by this process")

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
		// A record is retried for a minute before it is given up on, so an
		// outage shorter than that loses nothing; a longer one is counted.
		kgo.RecordDeliveryTimeout(time.Minute),
		kgo.MaxBufferedRecords(1<<16),
	)
	if err != nil {
		return nil, fmt.Errorf("tickloop: producer: %w", err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("tickloop: ping brokers: %w", err)
	}
	return &KafkaPublisher{client: client, Commands: CommandsTopic, Events: EventsTopic}, nil
}

// send buffers records without blocking. ErrMaxBuffered — the buffer is
// full — is returned; delivery failures arrive later through OnFailure.
func (k *KafkaPublisher) send(ctx context.Context, recs []*kgo.Record) error {
	var full error
	ctx = context.WithoutCancel(ctx)
	for _, r := range recs {
		k.client.TryProduce(ctx, r, func(r *kgo.Record, err error) {
			if err == nil {
				return
			}
			if errors.Is(err, kgo.ErrMaxBuffered) {
				// Refused now, synchronously: the promise runs inline.
				full = fmt.Errorf("tickloop: produce to %s: %w", r.Topic, err)
				return
			}
			if k.OnFailure != nil {
				k.OnFailure(r.Topic, err)
			}
		})
		if full != nil {
			return full
		}
	}
	return nil
}

// Publish implements Publisher. A zero-tick TickCompleted (the drain's) is
// not written.
//
// Boundaries have an invariant Events do not: the sequence on the topic
// must be gapless, because recovery refuses to replay past a gap (a lost
// batching decision, sim.ErrBoundaryGap). franz-go fails everything
// buffered behind a failed record on the same Partition, so an outage
// longer than the delivery timeout would leave `…, N, [gap], M, …` and a
// World that cannot boot. So: once one boundary is lost, this process
// publishes no more. It keeps ticking and Events keep flowing (AC-9); the
// next restart recovers exactly to the last delivered boundary and
// re-batches after it, which is the policy AW-SRV-007 inherits. A full
// buffer counts as a loss for the same reason — the boundary was not
// enqueued, and the next one must not be either.
func (k *KafkaPublisher) Publish(ctx context.Context, events []sim.Event, tc sim.TickCompleted) error {
	recs := make([]*kgo.Record, 0, len(events)+1)
	for _, ev := range events {
		rec, err := EventRecord(ev)
		if err != nil {
			return err
		}
		body, err := canonical.Marshal(rec)
		if err != nil {
			return err
		}
		p := BoundaryPartition
		if ev.Zone != "" {
			p = sim.PartitionFor(ev.Zone)
		}
		recs = append(recs, &kgo.Record{Topic: k.Events, Partition: p, Key: []byte(ev.Zone), Value: body})
	}
	if err := k.send(ctx, recs); err != nil {
		return err
	}
	if tc.Tick == 0 {
		return nil
	}
	if k.boundaryLost.Load() {
		return fmt.Errorf("%w (since tick %d)", ErrBoundaryLost, k.lostAtTick.Load())
	}
	body, err := canonical.Marshal(tc.Proto())
	if err != nil {
		return err
	}
	published := time.Now()
	// The record's own context is never canceled: franz-go fails a record
	// whose context ends before delivery, and the tick's context ends with
	// the tick.
	tick := tc.Tick
	lost := func(err error) {
		if k.boundaryLost.CompareAndSwap(false, true) {
			k.lostAtTick.Store(uint64(tick))
			if k.OnBoundaryLost != nil {
				k.OnBoundaryLost(tick, err)
			}
		}
	}
	k.client.TryProduce(context.WithoutCancel(ctx), &kgo.Record{Topic: k.Events, Partition: BoundaryPartition, Key: []byte(BoundaryKey), Value: body}, func(_ *kgo.Record, err error) {
		if err != nil {
			lost(err)
			return
		}
		if k.OnBoundaryAcked != nil {
			k.OnBoundaryAcked(tick, time.Since(published))
		}
	})
	if k.boundaryLost.Load() {
		return fmt.Errorf("%w (since tick %d)", ErrBoundaryLost, k.lostAtTick.Load())
	}
	return nil
}

// Produce implements Publisher.
func (k *KafkaPublisher) Produce(ctx context.Context, cmds []*logv1.LoggedCommand) error {
	recs := make([]*kgo.Record, 0, len(cmds))
	for _, cmd := range cmds {
		body, err := canonical.Marshal(cmd)
		if err != nil {
			return err
		}
		zone := sim.ZoneID(cmd.GetZoneId())
		recs = append(recs, &kgo.Record{Topic: k.Commands, Partition: sim.CommandPartition(cmd), Key: []byte(zone), Value: body})
	}
	return k.send(ctx, recs)
}

// ProduceSnapshots implements ManifestPublisher: the SnapshotWritten records
// for a completed round, each to its Zone's Partition on the events topic
// under the SnapshotKey record key (AW-SRV-006 AC-7).
//
// Its failure does not fail the round, and it deliberately does not take the
// boundary-loss path TickCompleted does. A gapless sequence matters for
// boundaries because recovery refuses to replay past a gap; a manifest is
// audit and tooling, nothing in the recovery path reads it, and a missing one
// costs an operator a `snapshot list` rather than a World that will not boot.
func (k *KafkaPublisher) ProduceSnapshots(ctx context.Context, records []*logv1.SnapshotWritten) error {
	recs := make([]*kgo.Record, 0, len(records))
	for _, r := range records {
		body, err := canonical.Marshal(r)
		if err != nil {
			return err
		}
		zone := sim.ZoneID(r.GetZoneId())
		recs = append(recs, &kgo.Record{Topic: k.Events, Partition: sim.PartitionFor(zone), Key: []byte(SnapshotKey), Value: body})
	}
	return k.send(ctx, recs)
}

// Flush waits for buffered records to be acknowledged, or for ctx.
func (k *KafkaPublisher) Flush(ctx context.Context) error { return k.client.Flush(ctx) }

// Close flushes for up to ten seconds, then closes.
func (k *KafkaPublisher) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := k.client.Flush(ctx)
	k.client.Close()
	return err
}

// ReadBoundaries reads every TickCompleted on andara.events.v1 from the
// start, in order, for replay (ADR-0002 §4). The RecordSource for replay
// is KafkaRecords.
func ReadBoundaries(ctx context.Context, brokers []string, eventsTopic string) ([]sim.TickCompleted, error) {
	if eventsTopic == "" {
		eventsTopic = EventsTopic
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{eventsTopic: {BoundaryPartition: kgo.NewOffset().AtStart()}}),
	)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	adm := kadm.NewClient(client)
	ends, err := adm.ListEndOffsets(ctx, eventsTopic)
	if err != nil {
		return nil, err
	}
	end, _ := ends.Lookup(eventsTopic, BoundaryPartition)
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
	Topic   string // empty means CommandsTopic
}

// Fetch implements sim.RecordSource.
func (k KafkaRecords) Fetch(partition int32, from, to int64) ([]sim.Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topic := k.Topic
	if topic == "" {
		topic = CommandsTopic
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(k.Brokers...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {partition: kgo.NewOffset().At(from)}}),
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

// Recover replays every Tick Boundary Record on the events topic onto e,
// fetching the records each boundary names from the commands topic, and
// returns how many ticks were replayed. This is the restart path until
// AW-SRV-006 gives it a snapshot to start from: correct and slow, and
// exact — a hash that does not match a recorded boundary is
// sim.ErrHashMismatch, and the process must not serve that World.
//
// after, if set, is called with each replayed tick's result once its hash is
// verified, as sim.Engine.ReplayEach calls it: how the content source learns
// the swaps recovery applied (AW-SRV-012), and where a log that predates the
// content-in-effect rule is refused.
func Recover(ctx context.Context, brokers []string, commandsTopic, eventsTopic string, e *sim.Engine, after func(sim.StepResult) error) (int, error) {
	boundaries, err := ReadBoundaries(ctx, brokers, eventsTopic)
	if err != nil {
		return 0, fmt.Errorf("tickloop: read boundaries: %w", err)
	}
	// Boundaries from before the engine's tick — a restart that already
	// replayed part of the log from a snapshot — are skipped; the rest must
	// be contiguous from it.
	i := 0
	for i < len(boundaries) && boundaries[i].Tick <= e.Tick() {
		i++
	}
	boundaries = boundaries[i:]
	if len(boundaries) == 0 {
		return 0, nil
	}
	if err := e.ReplayEach(boundaries, KafkaRecords{Brokers: brokers, Topic: commandsTopic}, after); err != nil {
		return 0, err
	}
	return len(boundaries), nil
}
