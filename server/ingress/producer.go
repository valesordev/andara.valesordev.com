// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/protobuf/proto"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
)

// CommandsTopic is where Commands go; the same name tickloop consumes.
const CommandsTopic = "andara.commands.v1"

// canonical is the one marshal every log record goes through (AW-SRV-004
// AC-3): deterministic, so a record is byte-identical however often it is
// serialized.
var canonical = proto.MarshalOptions{Deterministic: true}

// ProducerOptions configures a KafkaProducer.
type ProducerOptions struct {
	Brokers []string
	// Topic overrides CommandsTopic; tests use throwaway topics.
	Topic string
	// ClientID appears in broker logs. Defaults to andara-server.
	ClientID string
	// Deadline is ingress.produce_deadline: how long one produce may take
	// before its outcome is reported unknown. It is the client's record
	// delivery timeout; a produce request times out at half of it so one
	// idempotent retry fits inside (AC-7).
	Deadline time.Duration
	// MaxBuffered bounds the records buffered across the producer; a full
	// buffer refuses a Submit with pending_full rather than growing. Zero
	// means 4096.
	MaxBuffered int
	// ProbeInterval is how often the probe reads the topic's metadata.
	// Zero means one second.
	ProbeInterval time.Duration
	// DegradedHold is ingress.degraded_hold: how long a Partition a
	// produce error marked stays read-only. Zero means 120 s; below 90 s
	// is refused (the floor config load enforces).
	DegradedHold time.Duration

	// Dialer replaces the client's dialer; a test injects a fault with it.
	// It must return a conn that is not encrypted by the dialer's caller
	// (see tap.go); a tls.Dialer's DialContext is fine.
	Dialer func(ctx context.Context, network, host string) (net.Conn, error)
	// ClientLogger receives the Kafka client's own log; nil for none.
	ClientLogger kgo.Logger

	Metrics *Metrics
	Log     *slog.Logger
	Tracer  trace.Tracer

	// source replaces the probe's view of the log, and manualProbe stops
	// the probe loop so a test drives probeOnce itself; now replaces the
	// clock the hold and the min.insync.replicas refresh read.
	source      logSource
	manualProbe bool
	now         func() time.Time
}

// minISRRefresh is how often the topic's min.insync.replicas is read again
// (client-contract.md, "Entering the state"). A change is honored within it.
const minISRRefresh = 60 * time.Second

// KafkaProducer is command.Producer over andara.commands.v1.
//
// The produce properties AW-SRV-010 fixes: acks=all, the idempotent
// producer (both franz-go defaults, stated here so a default change is
// visible), at most five produce requests in flight per broker — the
// number the idempotent producer preserves order at, and what franz-go
// pins it to — a delivery timeout of ingress.produce_deadline, the record
// keyed by ZoneID, and the Partition chosen by sim.PartitionFor rather
// than any library default. The last is the line most likely to be dropped
// as redundant: a library's default hash can change between versions and
// silently move a Zone's history to another Partition, which is
// unrecoverable. The clients are built with ManualPartitioner so a record
// without an explicit Partition is a bug, not a fallback.
//
// The read-only state is per Partition (AW-SRV-052, client-contract.md,
// "Degraded state is per Partition"). A Partition whose produce fails with a
// broker's NOT_ENOUGH_REPLICAS, NOT_ENOUGH_REPLICAS_AFTER_APPEND,
// LEADER_NOT_AVAILABLE, NOT_LEADER_OR_FOLLOWER or REQUEST_TIMED_OUT, or whose
// topic metadata shows no leader on two consecutive probes or an ISR below
// min.insync.replicas, is read-only: a Submit for a Zone on it fails at once
// with ErrUnavailable, no wait and no buffer, while a Submit for a Zone on
// any other Partition is produced as usual. The World is read-only, not
// down; the tick and the Event stream do not pass through here.
//
// One client per Partition. A record a client had already tried to send
// cannot otherwise be taken back: the idempotent producer keeps it, and
// would deliver it when a broker returned — minutes later, to a player who
// was told the World was read-only. Closing the client is the only way to
// drop it, and one client for all 64 Partitions would take the other 63's
// in-flight records with it. So each Partition's records go through a client
// of their own, built on first use; entering the state closes that
// Partition's client and the next produce after it leaves builds another.
// The Submits whose records went with it were answered ErrDeadline, outcome
// unknown, because a record whose response was lost in the outage's first
// moment may be in the log; every Submit after detection is ErrUnavailable,
// nothing written.
type KafkaProducer struct {
	opts     []kgo.Opt
	topic    string
	deadline time.Duration
	probe    time.Duration
	metrics  *Metrics
	log      *slog.Logger
	tracer   trace.Tracer
	now      func() time.Time
	health   *partitionHealth
	source   logSource

	clients   [sim.PartitionCount]atomic.Pointer[kgo.Client]
	lastErr   [sim.PartitionCount]atomic.Pointer[brokerError]
	buffered  atomic.Int64
	maxBuf    int64
	retries   atomic.Uint64
	tapLoss   atomic.Uint64 // connections the produce tap stopped following
	written   atomic.Uint64 // produce requests that left this process
	warnAt    atomic.Int64  // unix nanos of the last sampled warning
	tapWarnAt atomic.Int64  // unix nanos of the last tap-lost warning

	minISR       atomic.Int64
	minISRKnown  atomic.Bool
	minISRReadAt time.Time // probe goroutine only

	mu      sync.Mutex
	closed  bool
	probeCl *kgo.Client
	stop    chan struct{}
	done    sync.WaitGroup
}

// brokerError is the most recent error a broker answered a produce to a
// Partition with, by protocol name, and when it arrived.
type brokerError struct {
	name string
	at   int64 // unix nanos
}

// NewKafkaProducer builds the producer. It does not reach the brokers:
// the tick loop already fails the boot when they are unreachable, and an
// outage after boot is the degraded state, not an error here.
func NewKafkaProducer(o ProducerOptions) (*KafkaProducer, error) {
	if len(o.Brokers) == 0 {
		return nil, errors.New("ingress: no brokers configured")
	}
	if o.Topic == "" {
		o.Topic = CommandsTopic
	}
	if o.ClientID == "" {
		o.ClientID = "andara-server"
	}
	if o.Deadline <= 0 {
		return nil, errors.New("ingress: produce deadline must be positive")
	}
	if o.MaxBuffered <= 0 {
		o.MaxBuffered = 4096
	}
	if o.ProbeInterval <= 0 {
		o.ProbeInterval = time.Second
	}
	switch {
	case o.DegradedHold == 0:
		o.DegradedHold = DefaultDegradedHold
	case o.DegradedHold < MinDegradedHold:
		return nil, fmt.Errorf("ingress: degraded hold %v is below the %v floor", o.DegradedHold, MinDegradedHold)
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.now == nil {
		o.now = time.Now
	}
	k := &KafkaProducer{topic: o.Topic, deadline: o.Deadline, probe: o.ProbeInterval, metrics: o.Metrics, log: o.Log,
		tracer: o.Tracer, now: o.now, maxBuf: int64(o.MaxBuffered), stop: make(chan struct{})}
	k.minISR.Store(1)
	k.health = newPartitionHealth(o.now, o.DegradedHold, k.onHealthChange)

	opts := []kgo.Opt{
		kgo.SeedBrokers(o.Brokers...),
		kgo.ClientID(o.ClientID + "-ingress"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.RecordDeliveryTimeout(o.Deadline),
		kgo.ProduceRequestTimeout(o.Deadline / 2),
		// A failed produce triggers a metadata refresh and the retry waits
		// for it; the client's default holds refreshes five seconds apart,
		// which would put every retry past the deadline. A quarter of the
		// deadline keeps a retry inside it and still bounds the refresh
		// rate under a broker outage.
		kgo.MetadataMinAge(o.Deadline / 4),
		// Each client carries one Partition, so its own bound is the whole
		// producer's; the producer-wide one is k.buffered.
		kgo.MaxBufferedRecords(o.MaxBuffered),
		kgo.WithHooks(retryHook{k}),
		kgo.Dialer(tapDialer(o.Dialer, k.noteProduceResponse, k.tapLost)),
	}
	if o.ClientLogger != nil {
		opts = append(opts, kgo.WithLogger(o.ClientLogger))
	}
	k.opts = opts

	if o.source != nil {
		k.source = o.source
	} else {
		probeOpts := []kgo.Opt{
			kgo.SeedBrokers(o.Brokers...),
			kgo.ClientID(o.ClientID + "-ingress-probe"),
			kgo.MetadataMinAge(o.ProbeInterval / 2),
		}
		if o.Dialer != nil {
			probeOpts = append(probeOpts, kgo.Dialer(o.Dialer))
		}
		if o.ClientLogger != nil {
			probeOpts = append(probeOpts, kgo.WithLogger(o.ClientLogger))
		}
		cl, err := kgo.NewClient(probeOpts...)
		if err != nil {
			return nil, fmt.Errorf("ingress: producer: %w", err)
		}
		k.probeCl = cl
		k.source = &kafkaSource{client: cl, topic: o.Topic}
	}
	if !o.manualProbe {
		k.done.Add(1)
		go k.probeLoop()
	}
	return k, nil
}

// Degraded reports whether the Partition is read-only.
func (k *KafkaProducer) Degraded(partition int32) bool { return k.health.Degraded(partition) }

// onHealthChange is the one place a Partition's state is published: the
// gauge, the line, and — on entering — the drop of its client.
func (k *KafkaProducer) onHealthChange(ctx context.Context, partition int32, degraded bool, cause, name string) {
	if k.metrics != nil {
		// The tracker's current truth, not the transition's: two goroutines'
		// notifications can arrive out of order.
		v := 0.0
		if k.health.Degraded(partition) {
			v = 1
		}
		k.metrics.Degraded.WithLabelValues(strconv.Itoa(int(partition))).Set(v)
	}
	if k.health.Degraded(partition) != degraded {
		// Another goroutine moved the Partition on since this transition;
		// its own notification carries the side effects.
		return
	}
	attrs := []slog.Attr{slog.Int("partition", int(partition)), slog.String("cause", cause), slog.String("error_name", name)}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		attrs = append(attrs, slog.String("trace_id", sc.TraceID().String()))
	}
	if degraded {
		k.log.LogAttrs(ctx, slog.LevelWarn, "partition degraded", append(attrs, slog.String("effect", "Commands for its Zones are refused until it recovers"))...)
		k.dropClient(partition)
		return
	}
	k.log.LogAttrs(ctx, slog.LevelInfo, "partition recovered", append(attrs, slog.String("effect", "it accepts Commands again"))...)
}

// clientFor is the client that carries the Partition, built on first use.
func (k *KafkaProducer) clientFor(partition int32) (*kgo.Client, error) {
	if c := k.clients[partition].Load(); c != nil {
		return c, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil, errors.New("ingress: producer closed")
	}
	if c := k.clients[partition].Load(); c != nil {
		return c, nil
	}
	c, err := kgo.NewClient(k.opts...)
	if err != nil {
		return nil, fmt.Errorf("ingress: producer client: %w", err)
	}
	k.clients[partition].Store(c)
	return c, nil
}

// dropClient closes the Partition's client, which fails everything it still
// held, and leaves none: the next produce builds a new one.
func (k *KafkaProducer) dropClient(partition int32) {
	if old := k.clients[partition].Swap(nil); old != nil {
		go old.Close()
	}
}

// noteProduceResponse is the tap's report of one partition of a produce
// response. A client carries one Partition, so partition is the client's.
func (k *KafkaProducer) noteProduceResponse(partition int32, code int16) {
	if partition < 0 || int(partition) >= len(k.lastErr) {
		return
	}
	// A success is the latest word too: an error a success followed is not
	// what a record that fails later heard.
	name := ""
	if code != 0 {
		name = brokerErrorName(code)
	}
	k.lastErr[partition].Store(&brokerError{name: name, at: time.Now().UnixNano()})
}

// tapLost counts a connection the tap stopped following. The produce-error
// trigger is blind on it, and the probe is the only one left, so the loss is
// said aloud, sampled to one line a minute.
func (k *KafkaProducer) tapLost() {
	k.tapLoss.Add(1)
	now := time.Now().UnixNano()
	if last := k.tapWarnAt.Load(); now-last < int64(time.Minute) || !k.tapWarnAt.CompareAndSwap(last, now) {
		return
	}
	k.log.LogAttrs(context.Background(), slog.LevelWarn, "produce tap lost step with a broker connection; produce errors on it go unseen and only the probe marks Partitions there")
}

// noteProduceFailure marks the Partition when the produce that just failed
// heard one of the five produce-error names from the broker while the record
// was alive: the client retried the error to the end of the record's life.
// A transient error the retry cleared never reaches here, because the record
// succeeded.
func (k *KafkaProducer) noteProduceFailure(ctx context.Context, partition int32, enqueued int64) {
	e := k.lastErr[partition].Load()
	if e == nil || e.at < enqueued || !slices.Contains(produceErrorNames, e.name) {
		return
	}
	k.health.MarkProduceError(ctx, partition, e.name)
}

// Produce implements command.Producer: one record, on its Zone's
// Partition, acknowledged by the ISR before it returns.
func (k *KafkaProducer) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	zone := sim.ZoneID(cmd.GetZoneId())
	partition := sim.CommandPartition(cmd)
	submit := trace.SpanFromContext(ctx)
	ctx, span := k.tracer.Start(ctx, "log.produce", trace.WithAttributes(attribute.Int64("partition", int64(partition))))
	defer span.End()

	if k.health.Degraded(partition) {
		// The Submit's own span says which Partition refused it; the
		// error the player reads does not.
		degraded := attribute.Int64("degraded_partition", int64(partition))
		span.SetAttributes(degraded)
		submit.SetAttributes(degraded)
		span.SetStatus(codes.Error, ErrUnavailable.Error())
		return command.Accepted{}, ErrUnavailable
	}
	body, err := canonical.Marshal(cmd)
	if err != nil {
		return command.Accepted{}, fmt.Errorf("ingress: marshal command: %w", err)
	}
	if k.buffered.Add(1) > k.maxBuf {
		k.buffered.Add(-1)
		span.SetStatus(codes.Error, ErrPendingFull.Error())
		return command.Accepted{}, ErrPendingFull
	}
	client, err := k.clientFor(partition)
	if err != nil {
		k.buffered.Add(-1)
		return command.Accepted{}, err
	}
	rec := &kgo.Record{Topic: k.topic, Partition: partition, Key: []byte(zone), Value: body}

	// The record's own context is never canceled: franz-go fails every
	// unsent record on the Partition when the first one's context ends,
	// and those belong to other Sessions. The deadline is ours to wait
	// on; the client's delivery timeout bounds the record.
	done := make(chan error, 1)
	retriesBefore, writtenBefore := k.retries.Load(), k.written.Load()
	enqueued := time.Now()
	client.TryProduce(context.WithoutCancel(ctx), rec, func(_ *kgo.Record, err error) { k.buffered.Add(-1); done <- err })

	dctx, cancel := context.WithTimeout(ctx, k.deadline)
	defer cancel()
	fired := true // the promise, or the wait ran out first
	select {
	case err = <-done:
	case <-dctx.Done():
		err, fired = dctx.Err(), false
	}
	wait := time.Since(enqueued)
	retries := k.retries.Load() - retriesBefore
	span.SetAttributes(attribute.Int64("retries", int64(retries)), attribute.Float64("acks_wait_ms", float64(wait.Microseconds())/1000))
	if err != nil {
		promised := err
		err = k.classify(ctx, err)
		span.SetStatus(codes.Error, err.Error())
		if errors.Is(err, ErrPendingFull) {
			return command.Accepted{}, err
		}
		// A record the client retried to the end of its life against a
		// broker error marks its Partition; a transient error the retry
		// cleared left a success after it.
		// Only a promise that has failed says the retries are over; a wait
		// that ran out first (the caller's own deadline) may still succeed.
		marked := false
		if fired && !errors.Is(err, context.Canceled) {
			k.noteProduceFailure(ctx, partition, enqueued.UnixNano())
			marked = k.health.Degraded(partition)
		}
		// The record was live in the client: its fate goes with the error
		// (AW-SRV-031), now if the promise already fired, else when it does.
		u := newUnsettled(err)
		if fired {
			k.settle(u, rec, promised, writtenBefore)
		} else {
			go func() {
				late := <-done
				if !marked && late != nil {
					k.noteProduceFailure(context.WithoutCancel(ctx), partition, enqueued.UnixNano())
				}
				k.settle(u, rec, late, writtenBefore)
			}()
		}
		return command.Accepted{}, u
	}
	if k.metrics != nil {
		k.metrics.ProduceDuration.Observe(wait.Seconds())
		k.metrics.Produced.WithLabelValues(strconv.Itoa(int(partition))).Inc()
	}
	span.SetAttributes(attribute.Int64("offset", rec.Offset))
	return command.Accepted{Partition: rec.Partition, Offset: rec.Offset}, nil
}

// classify names a failed produce. A full buffer is pending_full. For
// the rest the record was enqueued, so its outcome is unknown: it is
// ErrDeadline.
func (k *KafkaProducer) classify(ctx context.Context, err error) error {
	if errors.Is(err, kgo.ErrMaxBuffered) {
		return ErrPendingFull
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return err
	}
	return fmt.Errorf("%w: %w", ErrDeadline, err)
}

// settle names the fate of a record whose Submit was answered with the
// outcome unknown, from its promise: landed, at the offset the promise
// reports; not written, when no produce request from this process
// reached a socket since the record was enqueued — nothing carrying it
// can have reached a broker; or ErrOutcomeUnknown, when one did and the
// promise failed anyway. franz-go's promise does not say whether a
// failed record was ever sent (a written-then-failed batch and a
// never-written one arrive identical), so the counter of produce
// requests written is the discriminator, and it is process-wide: under
// concurrent produce traffic a never-sent record is classified unknown.
// That is the safe direction. The dangerous one — calling a sent record
// not written, and letting a retry produce it again — cannot happen:
// the counter is loaded before TryProduce, so any write of this record's
// batch bumps it; and an errored conn.Write is not counted because a
// produce request is one frame per Write, so a Write that returned an
// error did not hand the broker a frame it could process. Do not "fix"
// the nil-error filter (AW-SRV-031).
func (k *KafkaProducer) settle(u *Unsettled, rec *kgo.Record, err error, writtenBefore uint64) {
	switch {
	case err == nil:
		if k.metrics != nil {
			k.metrics.Produced.WithLabelValues(strconv.Itoa(int(rec.Partition))).Inc()
		}
		u.settle(command.Accepted{Partition: rec.Partition, Offset: rec.Offset}, nil)
	case k.written.Load() == writtenBefore:
		u.settle(command.Accepted{}, fmt.Errorf("%w: %w", ErrNotWritten, err))
	default:
		u.settle(command.Accepted{}, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err))
	}
}

// probeLoop reads the topic's metadata every probe interval for the life of
// the producer. Detection without traffic is what lets a Submit during an
// outage fail at once rather than discover it at the deadline, and what
// moves andara_ingress_degraded while nobody is playing.
func (k *KafkaProducer) probeLoop() {
	defer k.done.Done()
	t := time.NewTicker(k.probe)
	defer t.Stop()
	for {
		k.probeOnce(context.Background())
		select {
		case <-k.stop:
			return
		case <-t.C:
		}
	}
}

// probeOnce is one probe: the topic's metadata, bounded by the probe
// interval, judged against min.insync.replicas, which is read at the first
// probe that reaches the brokers and every minISRRefresh after. A probe does
// not ping a broker: a reachable broker says nothing about a Partition.
func (k *KafkaProducer) probeOnce(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, k.probe)
	view, err := k.source.Topic(pctx)
	cancel()
	if err != nil {
		k.health.ObserveProbe(ctx, nil, int(k.minISR.Load()))
		return
	}
	if !k.minISRKnown.Load() || k.now().Sub(k.minISRReadAt) >= minISRRefresh {
		k.refreshMinISR(ctx)
	}
	k.health.ObserveProbe(ctx, view, int(k.minISR.Load()))
}

// refreshMinISR reads the topic's min.insync.replicas. A read that fails
// keeps the last value, or 1 — no ISR check — until the first succeeds.
func (k *KafkaProducer) refreshMinISR(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, k.probe)
	defer cancel()
	n, err := k.source.MinISR(pctx)
	if err != nil {
		if !k.minISRKnown.Load() {
			k.log.LogAttrs(ctx, slog.LevelWarn, "topic min.insync.replicas unread; the probe does not check the ISR until it is",
				slog.String("topic", k.topic), slog.String("detail", err.Error()))
		}
		return
	}
	k.minISRReadAt = k.now()
	if old := k.minISR.Swap(int64(n)); !k.minISRKnown.Swap(true) || old != int64(n) {
		k.log.LogAttrs(ctx, slog.LevelInfo, "topic min.insync.replicas read",
			slog.String("topic", k.topic), slog.Int("min_insync_replicas", n))
	}
}

// Close stops the probe, flushes for up to the deadline, and closes the
// clients.
func (k *KafkaProducer) Close() error {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return nil
	}
	k.closed = true
	close(k.stop)
	k.mu.Unlock()
	k.done.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), k.deadline)
	defer cancel()
	var (
		wg   sync.WaitGroup
		emu  sync.Mutex
		errs []error
	)
	for p := range k.clients {
		client := k.clients[p].Swap(nil)
		if client == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Flush(ctx); err != nil {
				emu.Lock()
				errs = append(errs, err)
				emu.Unlock()
			}
			client.Close()
		}()
	}
	wg.Wait()
	if k.probeCl != nil {
		k.probeCl.Close()
	}
	return errors.Join(errs...)
}

// retryHook counts produce requests that failed on the wire — each is
// sent again by the client, under the idempotent producer's sequence, so a
// count here is a retry that did not duplicate — and warns, sampled to one
// line a second.
type retryHook struct{ k *KafkaProducer }

const produceKey = 0

func (h retryHook) OnBrokerWrite(meta kgo.BrokerMetadata, key int16, _ int, _, _ time.Duration, err error) {
	if key != produceKey {
		return
	}
	if err != nil {
		h.k.retry(meta, "write", err)
		return
	}
	h.k.written.Add(1)
}

func (h retryHook) OnBrokerRead(meta kgo.BrokerMetadata, key int16, _ int, _, _ time.Duration, err error) {
	if key == produceKey && err != nil {
		h.k.retry(meta, "read", err)
	}
}

func (k *KafkaProducer) retry(meta kgo.BrokerMetadata, phase string, err error) {
	k.retries.Add(1)
	if k.metrics != nil {
		k.metrics.ProduceRetries.Inc()
	}
	now := time.Now().UnixNano()
	if last := k.warnAt.Load(); now-last < int64(time.Second) || !k.warnAt.CompareAndSwap(last, now) {
		return
	}
	k.log.LogAttrs(context.Background(), slog.LevelWarn, "produce request failed; the client retries under the idempotent producer",
		slog.String("broker", net.JoinHostPort(meta.Host, strconv.Itoa(int(meta.Port)))), slog.String("phase", phase), slog.String("detail", err.Error()))
}
