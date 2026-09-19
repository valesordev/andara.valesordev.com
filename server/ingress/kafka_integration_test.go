// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

// Against throwaway topics on a running broker (`make up`, then
// `make test-integration`). Build-tagged so `make test` never needs one.
package ingress

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.34.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("ANDARA_KAFKA_BROKERS")
	if v == "" {
		t.Skip("ANDARA_KAFKA_BROKERS not set; run `make up` and `make test-integration`")
	}
	return strings.Split(v, ",")
}

func admin(t *testing.T) *kadm.Client {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	return kadm.NewClient(cl)
}

// throwawayTopic creates a topic shaped like andara.commands.v1 — 64
// Partitions, delete policy — deleted when the test ends.
func throwawayTopic(t *testing.T, partitions int32) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adm := admin(t)
	name := fmt.Sprintf("andara.test.ingress.%d", time.Now().UnixNano())
	if _, err := adm.CreateTopic(ctx, partitions, 1, nil, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		_, _ = adm.DeleteTopic(dctx, name)
	})
	return name
}

// endOffsets reads the topic's end offsets: what "nothing was written"
// is verified against.
func endOffsets(t *testing.T, topic string) map[int32]int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ends, err := admin(t).ListEndOffsets(ctx, topic)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int32]int64{}
	for _, e := range ends[topic] {
		out[e.Partition] = e.Offset
	}
	return out
}

func sumOffsets(m map[int32]int64) int64 {
	var n int64
	for _, v := range m {
		n += v
	}
	return n
}

// readRecord fetches the one record at partition/offset.
func readRecord(t *testing.T, topic string, partition int32, offset int64) *kgo.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {partition: kgo.NewOffset().At(offset)}}))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	for {
		fetches := cl.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			t.Fatal(err)
		}
		var found *kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			if found == nil && r.Offset == offset {
				found = r
			}
		})
		if found != nil {
			return found
		}
	}
}

// kafkaFixture is the unit fixture over a real producer.
type kafkaFixture struct {
	*fixture
	producer *KafkaProducer
	topic    string
	auditLog *recordlog.Kafka
	audit    string
	spans    *tracetest.SpanRecorder
	tracer   trace.Tracer
}

// tracerFor records spans for the test and, when ANDARA_OTLP_ENDPOINT is
// set, also exports them to the stack's collector through the boot
// sampler and filter — the same path andara-server's spans take — so the
// trace shape can be read back from Tempo.
func tracerFor(t *testing.T) (*tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(sdkresource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName("andara-ingress-test"), semconv.DeploymentEnvironmentName("local"))),
		sdktrace.WithSampler(telemetry.NewSampler(1)),
		sdktrace.WithSpanProcessor(rec),
	}
	if ep := os.Getenv("ANDARA_OTLP_ENDPOINT"); ep != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(ep), otlptracegrpc.WithInsecure())
		if err != nil {
			t.Fatal(err)
		}
		opts = append(opts, sdktrace.WithSpanProcessor(telemetry.NewSpanFilter(sdktrace.NewBatchSpanProcessor(exp))))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = tp.ForceFlush(ctx)
		_ = tp.Shutdown(ctx)
	})
	return rec, tp.Tracer("andara-ingress-test")
}

func newKafkaFixture(t *testing.T, mutate func(*ProducerOptions)) *kafkaFixture {
	t.Helper()
	topic := throwawayTopic(t, sim.PartitionCount)
	auditTopic := throwawayTopic(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auditLog, err := recordlog.NewKafka(ctx, recordlog.KafkaOptions{Brokers: brokers(t), Topic: auditTopic, ClientID: "ingress-test-audit"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = auditLog.Close() })

	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	spans, tracer := tracerFor(t)
	po := ProducerOptions{Brokers: brokers(t), Topic: topic, ClientID: "ingress-test", Deadline: 2 * time.Second, ProbeInterval: 100 * time.Millisecond, Metrics: metrics, Tracer: tracer}
	if os.Getenv("ANDARA_TEST_KGO_DEBUG") != "" {
		po.ClientLogger = kgo.BasicLogger(os.Stderr, kgo.LogLevelDebug, func() string { return t.Name() + " " })
	}
	if mutate != nil {
		mutate(&po)
	}
	producer, err := NewKafkaProducer(po)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })

	f := newFixture(t, func(o *Options) {
		o.Pipeline.Log = producer
		o.Pipeline.Tracer = tracer
		o.Pipeline.Authorizer = &auth.Authorizer{Table: command.Builtin().Roles(), Audit: auth.NewAuditor(auditLog, nil, auth.NewMetrics(nil), time.Now)}
		o.Metrics = metrics
		o.MaxPending = 64
		o.RateLimit = auth.RateLimit{}
	})
	return &kafkaFixture{fixture: f, producer: producer, topic: topic, auditLog: auditLog, audit: auditTopic, spans: spans, tracer: tracer}
}

// AC-10: a produced Submit's trace is command.execute spanning
// command.parse, command.authorize, and log.produce, the last carrying
// the Partition and offset — under the Game/Submit root the gateway
// starts, stood in for here.
func TestKafka_TraceShape(t *testing.T) {
	f := newKafkaFixture(t, nil)
	ctx, root := f.tracer.Start(context.Background(), telemetry.SubmitSpan)
	resp, err := f.submit(ctx, "s-alice", "look")
	root.End()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range f.spans.Ended() {
		byName[s.Name()] = s
	}
	exec, ok := byName["command.execute"]
	if !ok || exec.Parent().SpanID() != root.SpanContext().SpanID() {
		t.Fatalf("command.execute missing or not under the Submit root: %v", keys(byName))
	}
	for _, child := range []string{"command.parse", "command.authorize", "log.produce"} {
		s, ok := byName[child]
		if !ok || s.Parent().SpanID() != exec.SpanContext().SpanID() {
			t.Fatalf("%s missing or not under command.execute: %v", child, keys(byName))
		}
	}
	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range byName["log.produce"].Attributes() {
		attrs[kv.Key] = kv.Value
	}
	if attrs["partition"].AsInt64() != int64(resp.GetPartition()) || attrs["offset"].AsInt64() != resp.GetAcceptedOffset() {
		t.Fatalf("log.produce attributes %v; response %v", attrs, resp)
	}
	for _, want := range []attribute.Key{"retries", "acks_wait_ms"} {
		if _, ok := attrs[want]; !ok {
			t.Fatalf("log.produce lacks %s: %v", want, attrs)
		}
	}
	t.Logf("trace %s", exec.SpanContext().TraceID())
}

func keys(m map[string]sdktrace.ReadOnlySpan) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// AC-1, AC-9: the response names the Partition and offset the record is
// actually at, and the Partition is the Zone's — for a Zone this process
// would not own as much as for one it would.
func TestKafka_ProduceLandsWhereItSays(t *testing.T) {
	f := newKafkaFixture(t, nil)
	f.bindings.Bind("s-far", command.Binding{Actor: "zed", Zone: "a-zone-owned-elsewhere"})
	for _, tc := range []struct{ session, zone string }{{"s-alice", "town"}, {"s-far", "a-zone-owned-elsewhere"}} {
		resp, err := f.submit(context.Background(), tc.session, "look")
		if err != nil {
			t.Fatal(err)
		}
		if resp.GetPartition() != sim.PartitionFor(sim.ZoneID(tc.zone)) {
			t.Fatalf("%s: partition %d, want %d", tc.zone, resp.GetPartition(), sim.PartitionFor(sim.ZoneID(tc.zone)))
		}
		rec := readRecord(t, f.topic, resp.GetPartition(), resp.GetAcceptedOffset())
		var cmd logv1.LoggedCommand
		if err := proto.Unmarshal(rec.Value, &cmd); err != nil {
			t.Fatal(err)
		}
		if cmd.GetSessionId() != tc.session || cmd.GetZoneId() != tc.zone || string(rec.Key) != tc.zone || cmd.GetLook() == nil {
			t.Fatalf("record at %d/%d = %v key=%q", resp.GetPartition(), resp.GetAcceptedOffset(), &cmd, rec.Key)
		}
	}
	if got := testutil.ToFloat64(f.in.Metrics().PartitionSkew.WithLabelValues(fmt.Sprint(sim.PartitionFor("town")))); got != 1 {
		t.Fatalf("partition_skew[town] = %v", got)
	}
}

// Definition of done: the partitioner is covered by a test that fails if
// it fell back to the library default. The default hashes the key with
// murmur2; sim.PartitionFor uses FNV-1a; for some Zone they disagree, and
// that Zone lands where PartitionFor says.
func TestKafka_ExplicitPartitioner(t *testing.T) {
	f := newKafkaFixture(t, nil)
	probe, err := kgo.NewClient(kgo.SeedBrokers(brokers(t)...), kgo.DefaultProduceTopic(f.topic))
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var zone sim.ZoneID
	var libraryPartition int32
	for i := 0; i < 64 && zone == ""; i++ {
		candidate := sim.ZoneID(fmt.Sprintf("zone-%d", i))
		res := probe.ProduceSync(ctx, &kgo.Record{Key: []byte(candidate), Value: []byte("probe")})
		if err := res.FirstErr(); err != nil {
			t.Fatal(err)
		}
		if p := res[0].Record.Partition; p != sim.PartitionFor(candidate) {
			zone, libraryPartition = candidate, p
		}
	}
	if zone == "" {
		t.Fatal("no Zone in 64 candidates where the library default disagrees with sim.PartitionFor; the test cannot discriminate")
	}
	f.bindings.Bind("s-z", command.Binding{Actor: "z", Zone: zone})
	resp, err := f.in.submit(context.Background(), "s-z", player, nil, &gamev1.SubmitRequest{Raw: "look"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetPartition() != sim.PartitionFor(zone) || resp.GetPartition() == libraryPartition {
		t.Fatalf("%s landed on %d; PartitionFor says %d, the library default %d", zone, resp.GetPartition(), sim.PartitionFor(zone), libraryPartition)
	}
}

// AC-2 under concurrency: one Session's Submits from many goroutines
// land at distinct, contiguous offsets, each record the Intent its
// response was for.
func TestKafka_ConcurrentSubmitsOrdered(t *testing.T) {
	f := newKafkaFixture(t, nil)
	const n = 32
	var wg sync.WaitGroup
	offsets := make([]int64, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := f.in.submit(context.Background(), "s-alice", player, nil, &gamev1.SubmitRequest{Raw: "look", ClientRef: fmt.Sprint(i)})
			if err != nil {
				errs[i] = err
				return
			}
			offsets[i] = resp.GetAcceptedOffset()
		}()
	}
	wg.Wait()
	seen := map[int64]int{}
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("submit %d: %v", i, errs[i])
		}
		seen[offsets[i]] = i
	}
	if len(seen) != n {
		t.Fatalf("offsets not distinct: %v", offsets)
	}
	p := sim.PartitionFor("town")
	for off := int64(0); off < n; off++ {
		i, ok := seen[off]
		if !ok {
			t.Fatalf("offset %d missing: %v", off, offsets)
		}
		rec := readRecord(t, f.topic, p, off)
		var cmd logv1.LoggedCommand
		_ = proto.Unmarshal(rec.Value, &cmd)
		if cmd.GetClientRef() != fmt.Sprint(i) {
			t.Fatalf("offset %d holds client_ref %q, want %d", off, cmd.GetClientRef(), i)
		}
	}
}

// AC-3, AC-4: nothing is written on a rejection, verified against the
// broker; an authorize rejection writes one audit record naming the
// actor, the verb, and the Session.
func TestKafka_NothingWrittenOnRejection(t *testing.T) {
	f := newKafkaFixture(t, nil)
	before := endOffsets(t, f.topic)
	auditBefore := endOffsets(t, f.audit)

	_, err := f.submit(context.Background(), "s-alice", "frobnicate")
	if codeOf(t, err) != connect.CodeInvalidArgument {
		t.Fatalf("parse: %v", err)
	}
	if after := endOffsets(t, f.topic); sumOffsets(after) != sumOffsets(before) {
		t.Fatalf("a parse rejection wrote to the log: %v → %v", before, after)
	}

	_, err = f.submit(context.Background(), "s-nobody", "look")
	if codeOf(t, err) != connect.CodePermissionDenied {
		t.Fatalf("authorize: %v", err)
	}
	if after := endOffsets(t, f.topic); sumOffsets(after) != sumOffsets(before) {
		t.Fatalf("an authorize rejection wrote to the log: %v → %v", before, after)
	}
	auditAfter := endOffsets(t, f.audit)
	if sumOffsets(auditAfter) != sumOffsets(auditBefore)+1 {
		t.Fatalf("audit records: %v → %v", auditBefore, auditAfter)
	}
	rec := readRecord(t, f.audit, 0, auditAfter[0]-1)
	var ar auditv1.AuditRecord
	if err := proto.Unmarshal(rec.Value, &ar); err != nil {
		t.Fatal(err)
	}
	if ar.GetActorAccountId() != player.AccountID || ar.GetTarget() != "look" || ar.GetSessionId() != "s-nobody" || ar.GetOutcome() != auth.AuditDenied {
		t.Fatalf("audit record = %v", &ar)
	}
}

// faultDialer is the client's dialer with two faults: refuse, which
// fails every dial and cuts every open connection (the broker gone), and
// dropOneProduceResponse, which loses the response to the next produce
// request after the broker has it (an ambiguous timeout).
type faultDialer struct {
	refuse                 atomic.Bool
	dropOneProduceResponse atomic.Bool
	mu                     sync.Mutex
	conns                  []net.Conn
}

func (d *faultDialer) dial(ctx context.Context, network, host string) (net.Conn, error) {
	if d.refuse.Load() {
		return nil, errors.New("fault: refused")
	}
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, host)
	if err != nil {
		return nil, err
	}
	fc := &faultConn{Conn: c, d: d}
	d.mu.Lock()
	d.conns = append(d.conns, fc)
	d.mu.Unlock()
	return fc, nil
}

func (d *faultDialer) cut() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		_ = c.Close()
	}
	d.conns = nil
}

type faultConn struct {
	net.Conn
	d        *faultDialer
	dropNext atomic.Bool
}

const produceAPIKey = 0

func (c *faultConn) Write(p []byte) (int, error) {
	// A request frame: int32 size, int16 api key. One Write per request.
	if len(p) >= 6 && binary.BigEndian.Uint16(p[4:6]) == produceAPIKey && c.d.dropOneProduceResponse.CompareAndSwap(true, false) {
		c.dropNext.Store(true)
	}
	return c.Conn.Write(p)
}

func (c *faultConn) Read(p []byte) (int, error) {
	if c.dropNext.Load() {
		// The broker has the request and will answer; we never read it.
		// Drain and discard what arrives, then fail the read: the client
		// sees a dead connection with the produce's outcome unknown.
		_ = c.Conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, _ = io.Copy(io.Discard, c.Conn)
		_ = c.Conn.Close()
		return 0, io.ErrUnexpectedEOF
	}
	return c.Conn.Read(p)
}

// AC-5, AC-6: with the broker unreachable, Submit does not hang, buffers
// nothing that lands later, and marks the World read-only: the Submit in
// flight when the log went away is answered at the deadline with its
// outcome unknown, or UNAVAILABLE if the probe noticed first; every one
// after that is UNAVAILABLE at once. When a broker answers again the
// probe clears it without a restart, and the next Submit is produced.
func TestKafka_UnreachableIsReadOnlyThenRecovers(t *testing.T) {
	d := &faultDialer{}
	f := newKafkaFixture(t, func(o *ProducerOptions) { o.Dialer = d.dial })
	if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
		t.Fatal(err)
	}
	d.refuse.Store(true)
	d.cut()
	began := time.Now()
	_, err := f.submit(context.Background(), "s-alice", "look")
	took := time.Since(began)
	switch codeOf(t, err) {
	case connect.CodeDeadlineExceeded:
		if infoOf(t, err).GetReason() != ReasonDeadline {
			t.Fatalf("discovering submit: %v", err)
		}
	case connect.CodeUnavailable:
		if infoOf(t, err).GetReason() != ReasonReadOnly {
			t.Fatalf("submit after detection: %v", err)
		}
	default:
		t.Fatalf("broker gone: %v", err)
	}
	if took > 2*time.Second+time.Second {
		t.Fatalf("took %s, past the produce deadline", took)
	}
	waitFor(t, func() bool { return f.producer.Degraded() }, "degraded")
	if testutil.ToFloat64(f.in.Metrics().Degraded) != 1 {
		t.Fatal("gauge not 1")
	}
	// Degraded: the next one fails at once, not after a deadline.
	began = time.Now()
	_, err = f.submit(context.Background(), "s-alice", "look")
	if codeOf(t, err) != connect.CodeUnavailable || infoOf(t, err).GetReason() != ReasonReadOnly {
		t.Fatalf("while degraded: %v", err)
	}
	if time.Since(began) > 500*time.Millisecond {
		t.Fatal("a degraded Submit waited")
	}
	if got := f.outcome(OutcomeUnavailable) + f.outcome(OutcomeDeadline); got != 2 {
		t.Fatalf("unavailable+deadline = %v", got)
	}

	d.refuse.Store(false)
	waitFor(t, func() bool { return !f.producer.Degraded() }, "probe recovery")
	if testutil.ToFloat64(f.in.Metrics().Degraded) != 0 {
		t.Fatal("gauge still 1 after recovery")
	}
	resp, err := f.submit(context.Background(), "s-alice", "look")
	if err != nil || resp.GetAcceptedOffset() != 1 {
		t.Fatalf("after recovery: %v %v", resp, err)
	}
	// Nothing buffered during the outage landed: exactly two records exist.
	if ends := endOffsets(t, f.topic); sumOffsets(ends) != 2 {
		t.Fatalf("end offsets %v; a Submit answered during the outage was delivered after it", ends)
	}
}

// The probe notices an outage without traffic: the gauge moves while
// nobody is playing, and the first Submit after detection fails at once.
func TestKafka_ProbeDetectsOutageWithoutTraffic(t *testing.T) {
	d := &faultDialer{}
	f := newKafkaFixture(t, func(o *ProducerOptions) { o.Dialer = d.dial })
	if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
		t.Fatal(err)
	}
	d.refuse.Store(true)
	d.cut()
	waitFor(t, func() bool { return f.producer.Degraded() }, "probe detection")
	began := time.Now()
	if _, err := f.submit(context.Background(), "s-alice", "look"); codeOf(t, err) != connect.CodeUnavailable {
		t.Fatalf("after detection: %v", err)
	}
	if time.Since(began) > 500*time.Millisecond {
		t.Fatal("waited after detection")
	}
	d.refuse.Store(false)
	waitFor(t, func() bool { return !f.producer.Degraded() }, "probe recovery")
	if resp, err := f.submit(context.Background(), "s-alice", "look"); err != nil || resp.GetAcceptedOffset() != 1 {
		t.Fatalf("after recovery: %v %v", resp, err)
	}
}

// AC-7: a produce whose response is lost after the broker has it is
// retried by the idempotent producer and lands once.
func TestKafka_AmbiguousTimeoutNoDuplicate(t *testing.T) {
	d := &faultDialer{}
	f := newKafkaFixture(t, func(o *ProducerOptions) { o.Dialer = d.dial })
	if _, err := f.submit(context.Background(), "s-alice", "look"); err != nil {
		t.Fatal(err)
	}
	retriesBefore := testutil.ToFloat64(f.in.Metrics().ProduceRetries)
	d.dropOneProduceResponse.Store(true)
	const n = 8
	for i := range n {
		resp, err := f.submit(context.Background(), "s-alice", "look")
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if resp.GetAcceptedOffset() != int64(i+1) {
			t.Fatalf("submit %d landed at %d", i, resp.GetAcceptedOffset())
		}
	}
	if d.dropOneProduceResponse.Load() {
		t.Fatal("the fault never fired")
	}
	if got := testutil.ToFloat64(f.in.Metrics().ProduceRetries); got <= retriesBefore {
		t.Fatalf("no retry counted (%v → %v)", retriesBefore, got)
	}
	if ends := endOffsets(t, f.topic); sumOffsets(ends) != n+1 {
		t.Fatalf("end offsets %v: a retry duplicated a record", ends)
	}
	// And every record is one Intent, once.
	p := sim.PartitionFor("town")
	var refs []string
	for off := int64(0); off <= n; off++ {
		rec := readRecord(t, f.topic, p, off)
		refs = append(refs, string(bytes.TrimSpace(rec.Key)))
	}
	if len(refs) != n+1 {
		t.Fatalf("records %v", refs)
	}
}
