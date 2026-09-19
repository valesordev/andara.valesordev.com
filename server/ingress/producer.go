// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
	// MaxBuffered bounds the client's buffer; a full one refuses a Submit
	// with pending_full rather than growing. Zero means 4096.
	MaxBuffered int
	// ProbeInterval is how often a degraded producer pings the brokers to
	// notice they are back. Zero means one second.
	ProbeInterval time.Duration

	// Dialer replaces the client's dialer; a test injects a fault with it.
	Dialer func(ctx context.Context, network, host string) (net.Conn, error)
	// ClientLogger receives the Kafka client's own log; nil for none.
	ClientLogger kgo.Logger

	Metrics *Metrics
	Log     *slog.Logger
	Tracer  trace.Tracer
}

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
// unrecoverable. The client is built with ManualPartitioner so a record
// without an explicit Partition is a bug, not a fallback.
//
// The read-only state. A probe pings the brokers once a second, always;
// when none answers — or a produce fails and a ping then fails — the
// producer is degraded: Submits fail at once with ErrUnavailable, no wait
// and no buffer, until the probe reaches a broker again. The World is
// read-only, not down; the tick and the Event stream do not pass through
// here.
//
// Entering the state swaps the client for a new one and closes the old,
// which fails everything the old one still held. A record the client had
// already tried to send cannot otherwise be taken back: the idempotent
// producer keeps it, and would deliver it when a broker returned —
// minutes later, to a player who was told the World was read-only.
// Dropping the client is the bounded buffer the story asks for. The
// Submits whose records went with it were answered ErrDeadline, outcome
// unknown, because a record whose response was lost in the outage's
// first moment may be in the log; every Submit after detection is
// ErrUnavailable, nothing written.
type KafkaProducer struct {
	opts     []kgo.Opt
	topic    string
	deadline time.Duration
	probe    time.Duration
	metrics  *Metrics
	log      *slog.Logger
	tracer   trace.Tracer

	client   atomic.Pointer[kgo.Client]
	degraded atomic.Bool
	retries  atomic.Uint64
	warnAt   atomic.Int64 // unix nanos of the last sampled warning

	mu     sync.Mutex
	closed bool
	stop   chan struct{}
	done   sync.WaitGroup
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
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	k := &KafkaProducer{topic: o.Topic, deadline: o.Deadline, probe: o.ProbeInterval, metrics: o.Metrics, log: o.Log, tracer: o.Tracer, stop: make(chan struct{})}
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
		kgo.MaxBufferedRecords(o.MaxBuffered),
		kgo.WithHooks(retryHook{k}),
	}
	if o.Dialer != nil {
		opts = append(opts, kgo.Dialer(o.Dialer))
	}
	if o.ClientLogger != nil {
		opts = append(opts, kgo.WithLogger(o.ClientLogger))
	}
	k.opts = opts
	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("ingress: producer: %w", err)
	}
	k.client.Store(client)
	k.done.Add(1)
	go k.probeLoop()
	return k, nil
}

// Degraded reports whether the World is read-only because the log is
// unreachable.
func (k *KafkaProducer) Degraded() bool { return k.degraded.Load() }

// Produce implements command.Producer: one record, on its Zone's
// Partition, acknowledged by the ISR before it returns.
func (k *KafkaProducer) Produce(ctx context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	zone := sim.ZoneID(cmd.GetZoneId())
	partition := sim.PartitionFor(zone)
	ctx, span := k.tracer.Start(ctx, "log.produce", trace.WithAttributes(attribute.Int64("partition", int64(partition))))
	defer span.End()

	if k.degraded.Load() {
		span.SetStatus(codes.Error, ErrUnavailable.Error())
		return command.Accepted{}, ErrUnavailable
	}
	body, err := canonical.Marshal(cmd)
	if err != nil {
		return command.Accepted{}, fmt.Errorf("ingress: marshal command: %w", err)
	}
	rec := &kgo.Record{Topic: k.topic, Partition: partition, Key: []byte(zone), Value: body}

	// The record's own context is never canceled: franz-go fails every
	// unsent record on the Partition when the first one's context ends,
	// and those belong to other Sessions. The deadline is ours to wait
	// on; the client's delivery timeout bounds the record.
	done := make(chan error, 1)
	retriesBefore := k.retries.Load()
	enqueued := time.Now()
	k.client.Load().TryProduce(context.WithoutCancel(ctx), rec, func(_ *kgo.Record, err error) { done <- err })

	dctx, cancel := context.WithTimeout(ctx, k.deadline)
	defer cancel()
	select {
	case err = <-done:
	case <-dctx.Done():
		err = dctx.Err()
	}
	wait := time.Since(enqueued)
	retries := k.retries.Load() - retriesBefore
	span.SetAttributes(attribute.Int64("retries", int64(retries)), attribute.Float64("acks_wait_ms", float64(wait.Microseconds())/1000))
	if err != nil {
		err = k.classify(ctx, err)
		span.SetStatus(codes.Error, err.Error())
		return command.Accepted{}, err
	}
	if k.metrics != nil {
		k.metrics.ProduceDuration.Observe(wait.Seconds())
		k.metrics.PartitionSkew.WithLabelValues(strconv.Itoa(int(partition))).Inc()
	}
	span.SetAttributes(attribute.Int64("offset", rec.Offset))
	return command.Accepted{Partition: rec.Partition, Offset: rec.Offset}, nil
}

// classify names a failed produce. A full buffer is pending_full. For
// the rest the record was enqueued, so its outcome is unknown: it is
// ErrDeadline, and if the brokers do not answer a ping now the World is
// read-only from here.
func (k *KafkaProducer) classify(ctx context.Context, err error) error {
	if errors.Is(err, kgo.ErrMaxBuffered) {
		return ErrPendingFull
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return err
	}
	if !k.degraded.Load() {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), k.deadline/4)
		perr := k.client.Load().Ping(pctx)
		cancel()
		if perr != nil {
			k.degrade(perr)
		}
	}
	return fmt.Errorf("%w: %w", ErrDeadline, err)
}

// degrade enters the read-only state once: the gauge, the log line, and
// the client swap that drops what the old one held.
func (k *KafkaProducer) degrade(cause error) {
	if !k.degraded.CompareAndSwap(false, true) {
		return
	}
	if k.metrics != nil {
		k.metrics.Degraded.Set(1)
	}
	k.log.LogAttrs(context.Background(), slog.LevelInfo, "command log unreachable: the World is read-only until a broker answers",
		slog.String("detail", cause.Error()))
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return
	}
	fresh, err := kgo.NewClient(k.opts...)
	if err != nil {
		// The options built a client once; they will again. Keep the old.
		k.log.LogAttrs(context.Background(), slog.LevelError, "replacement producer client", slog.String("detail", err.Error()))
		return
	}
	old := k.client.Swap(fresh)
	go old.Close()
}

// probeLoop pings a broker every probe interval for the life of the
// producer: a failure degrades, a success recovers. Detection without
// traffic is what lets a Submit during an outage fail at once rather
// than discover it at the deadline, and what moves
// andara_ingress_degraded while nobody is playing.
func (k *KafkaProducer) probeLoop() {
	defer k.done.Done()
	t := time.NewTicker(k.probe)
	defer t.Stop()
	for {
		select {
		case <-k.stop:
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(context.Background(), k.probe)
			err := k.client.Load().Ping(pctx)
			cancel()
			if err != nil {
				k.degrade(err)
			} else {
				k.recover()
			}
		}
	}
}

// recover leaves the read-only state.
func (k *KafkaProducer) recover() {
	if !k.degraded.CompareAndSwap(true, false) {
		return
	}
	if k.metrics != nil {
		k.metrics.Degraded.Set(0)
	}
	k.log.LogAttrs(context.Background(), slog.LevelInfo, "command log reachable: the World accepts Commands again")
}

// Close stops the probe, flushes for up to the deadline, and closes the
// client.
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
	client := k.client.Load()
	err := client.Flush(ctx)
	client.Close()
	return err
}

// retryHook counts produce requests that failed on the wire — each is
// sent again by the client, under the idempotent producer's sequence, so a
// count here is a retry that did not duplicate — and warns, sampled to one
// line a second.
type retryHook struct{ k *KafkaProducer }

const produceKey = 0

func (h retryHook) OnBrokerWrite(meta kgo.BrokerMetadata, key int16, _ int, _, _ time.Duration, err error) {
	if key == produceKey && err != nil {
		h.k.retry(meta, "write", err)
	}
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
