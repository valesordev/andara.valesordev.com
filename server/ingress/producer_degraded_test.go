// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kmsg"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// zoneOn is a Zone whose Commands go to the Partition.
func zoneOn(t *testing.T, p int32) string {
	t.Helper()
	for i := range 100_000 {
		if z := "zone" + strconv.Itoa(i); sim.PartitionFor(sim.ZoneID(z)) == p {
			return z
		}
	}
	t.Fatalf("no Zone on Partition %d", p)
	return ""
}

func cmdOn(t *testing.T, p int32) *logv1.LoggedCommand {
	return &logv1.LoggedCommand{ZoneId: zoneOn(t, p)}
}

type producerFixture struct {
	k       *KafkaProducer
	b       *fakeBroker
	metrics *Metrics
	logs    *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// newProducerFixture is a producer over a scripted broker. The probe loop is
// stopped: a test calls probeOnce, or leaves the Partitions to the produce
// path.
func newProducerFixture(t *testing.T, mutate func(*ProducerOptions)) *producerFixture {
	t.Helper()
	b := newFakeBroker(t, CommandsTopic)
	logs := &syncBuffer{}
	metrics := NewMetrics(prometheus.NewRegistry())
	o := ProducerOptions{
		Brokers: []string{b.addr()}, Deadline: time.Second, ProbeInterval: 500 * time.Millisecond,
		Metrics: metrics, Log: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		manualProbe: true,
	}
	if mutate != nil {
		mutate(&o)
	}
	k, err := NewKafkaProducer(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return &producerFixture{k: k, b: b, metrics: metrics, logs: logs}
}

func (f *producerFixture) gauge(p int32) float64 {
	return testutil.ToFloat64(f.metrics.Degraded.WithLabelValues(strconv.Itoa(int(p))))
}

// AC-1 and AC-2: each of the five broker errors, at the point the producer
// observes it — the produce to the Partition fails after the client's
// retries — marks that Partition and no other; a Submit for a Zone on another
// Partition is produced and acknowledged meanwhile; and a Submit for the
// degraded one is refused at once with the producer's UNAVAILABLE, which
// does not name the Partition, and reaches no broker.
func TestProducer_EachBrokerErrorMarksOnlyThatPartition(t *testing.T) {
	const p, q = int32(37), int32(5)
	for _, c := range []struct {
		name string
		code int16
	}{
		{"NOT_ENOUGH_REPLICAS", 19},
		{"NOT_ENOUGH_REPLICAS_AFTER_APPEND", 20},
		{"LEADER_NOT_AVAILABLE", 5},
		{"NOT_LEADER_OR_FOLLOWER", 6},
		{"REQUEST_TIMED_OUT", 7},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newProducerFixture(t, nil)
			f.b.failProduce(p, c.code, -1)

			_, err := f.k.Produce(context.Background(), cmdOn(t, p))
			if !errors.Is(err, ErrDeadline) {
				t.Fatalf("the discovering Submit = %v, want DEADLINE_EXCEEDED (outcome unknown)", err)
			}
			waitFor(t, func() bool { return f.k.Degraded(p) }, c.name+" marks the Partition")
			if got := degradedCount(f.k); got != 1 {
				t.Fatalf("%d Partitions degraded, want only %d", got, p)
			}
			if f.gauge(p) != 1 || gaugeSum(f.metrics) != 1 {
				t.Fatalf("gauge{%d} = %v, sum = %v; want 1 and 1", p, f.gauge(p), gaugeSum(f.metrics))
			}
			if line := f.logs.String(); !strings.Contains(line, `"msg":"partition degraded`) ||
				!strings.Contains(line, `"cause":"produce_error"`) || !strings.Contains(line, `"error_name":"`+c.name+`"`) {
				t.Fatalf("no 'partition degraded' line naming the cause and %s:\n%s", c.name, line)
			}

			// AC-2: a Zone on another Partition is produced and acknowledged.
			acc, err := f.k.Produce(context.Background(), cmdOn(t, q))
			if err != nil || acc.Partition != q {
				t.Fatalf("Submit on Partition %d while %d is degraded = %v, %v", q, p, acc, err)
			}

			// A Submit for the degraded one is refused at once, naming nothing.
			waitFor(t, func() bool { return f.k.clients[p].Load() == nil }, "the Partition's client dropped")
			began := time.Now()
			_, err = f.k.Produce(context.Background(), cmdOn(t, p))
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrDeadline) {
				t.Fatalf("Submit on the degraded Partition = %v, want ErrUnavailable", err)
			}
			if time.Since(began) > 200*time.Millisecond {
				t.Fatalf("a degraded Submit waited %v", time.Since(began))
			}
			if strings.Contains(err.Error(), strconv.Itoa(int(p))) {
				t.Fatalf("the error names the Partition: %v", err)
			}
		})
	}
}

// A transient broker error that the client's retry clears inside the
// deadline is not a failure and degrades nothing.
func TestProducer_ATransientBrokerErrorDegradesNothing(t *testing.T) {
	const p = int32(12)
	f := newProducerFixture(t, nil)
	f.b.failProduce(p, 19, 1)
	acc, err := f.k.Produce(context.Background(), cmdOn(t, p))
	if err != nil {
		t.Fatalf("a produce retried through one NOT_ENOUGH_REPLICAS = %v", err)
	}
	if acc.Partition != p || f.b.requests(p) < 2 {
		t.Fatalf("accepted %v after %d requests; want the retry to have run", acc, f.b.requests(p))
	}
	if got := degradedCount(f.k); got != 0 {
		t.Fatalf("%d Partitions degraded by an error the retry cleared", got)
	}
}

// A broker that answers REQUEST_TIMED_OUT slowly, after the Submit's wait has
// already ended with the record still in the client, still marks the Partition
// when the record's promise settles: the Submit that discovered the fault is
// not the only chance to see it.
func TestProducer_AnErrorThatArrivesAfterTheSubmitsDeadlineStillMarks(t *testing.T) {
	const p = int32(12)
	f := newProducerFixture(t, nil)
	f.b.failProduce(p, 7, -1)
	f.b.holdProduce(p, 1300*time.Millisecond) // the deadline is 1s
	_, err := f.k.Produce(context.Background(), cmdOn(t, p))
	var u *Unsettled
	if !errors.As(err, &u) || !errors.Is(err, ErrDeadline) {
		t.Fatalf("Produce = %v, want an unsettled DEADLINE_EXCEEDED", err)
	}
	if f.k.Degraded(p) {
		t.Fatal("marked before the broker answered")
	}
	select {
	case <-u.Settled():
	case <-time.After(5 * time.Second):
		t.Fatal("the record's fate was never settled")
	}
	waitFor(t, func() bool { return f.k.Degraded(p) }, "REQUEST_TIMED_OUT that arrived after the wait marks the Partition")
}

// An error a broker answered before a record was enqueued says nothing of
// that record: a produce that later times out with no error from the broker
// does not mark the Partition on the strength of the old one.
func TestProducer_AStaleBrokerErrorDoesNotMarkALaterTimeout(t *testing.T) {
	const p = int32(12)
	f := newProducerFixture(t, nil)
	f.b.failProduce(p, 19, 1)
	if _, err := f.k.Produce(context.Background(), cmdOn(t, p)); err != nil {
		t.Fatal(err) // refused once, then taken: leaves the error behind
	}
	f.b.holdProduce(p, 1500*time.Millisecond) // past the 1s deadline, with no error to show for it
	_, err := f.k.Produce(context.Background(), cmdOn(t, p))
	var u *Unsettled
	if !errors.As(err, &u) || !errors.Is(err, ErrDeadline) {
		t.Fatalf("Produce = %v, want an unsettled DEADLINE_EXCEEDED", err)
	}
	select {
	case <-u.Settled():
	case <-time.After(5 * time.Second):
		t.Fatal("the record's fate was never settled")
	}
	if f.k.Degraded(p) {
		t.Fatal("a timeout with no broker error marked the Partition on a stale one")
	}
}

// Degrading one Partition does not fail, delay or drop an in-flight produce
// to another, and does not swap the client that carries it.
func TestProducer_DegradingOnePartitionLeavesAnInFlightProduceToAnotherAlone(t *testing.T) {
	const p, q = int32(37), int32(5)
	f := newProducerFixture(t, func(o *ProducerOptions) { o.Deadline = 3 * time.Second })
	if _, err := f.k.Produce(context.Background(), cmdOn(t, q)); err != nil { // builds q's client
		t.Fatal(err)
	}
	before := f.k.clients[q].Load()
	f.b.holdProduce(q, 400*time.Millisecond)

	type result struct {
		err  error
		took time.Duration
	}
	done := make(chan result, 1)
	go func() {
		began := time.Now()
		_, err := f.k.Produce(context.Background(), cmdOn(t, q))
		done <- result{err, time.Since(began)}
	}()
	waitFor(t, func() bool { return f.b.requests(q) >= 2 }, "the produce to the other Partition is in flight")
	f.k.health.MarkProduceError(context.Background(), p, "NOT_ENOUGH_REPLICAS")
	if !f.k.Degraded(p) {
		t.Fatal("not marked")
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("the in-flight produce to Partition %d = %v after %d was degraded", q, r.err, p)
	}
	if r.took < 350*time.Millisecond || r.took > 1500*time.Millisecond {
		t.Fatalf("it took %v; want the broker's 400ms and no more", r.took)
	}
	if f.k.clients[q].Load() != before {
		t.Fatal("the client carrying the other Partition was swapped")
	}
	if f.b.appended(q) != 2 {
		t.Fatalf("%d records on the other Partition, want 2", f.b.appended(q))
	}
}

// AC-5: all 64 series are present at boot, 0.
func TestMetrics_AllDegradedSeriesPresentAtBootAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewMetrics(reg)
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]float64{}
	for _, mf := range fams {
		if mf.GetName() != "andara_ingress_degraded" {
			continue
		}
		for _, m := range mf.GetMetric() {
			if len(m.GetLabel()) != 1 || m.GetLabel()[0].GetName() != "partition" {
				t.Fatalf("labels = %v, want partition only", m.GetLabel())
			}
			seen[m.GetLabel()[0].GetValue()] = m.GetGauge().GetValue()
		}
	}
	if len(seen) != int(sim.PartitionCount) {
		t.Fatalf("%d series at boot, want %d", len(seen), sim.PartitionCount)
	}
	for p := range sim.PartitionCount {
		if v, ok := seen[strconv.Itoa(int(p))]; !ok || v != 0 {
			t.Fatalf(`andara_ingress_degraded{partition="%d"} = %v (present %v), want 0`, p, v, ok)
		}
	}
}

// fakeSource is a scripted view of the log for the probe.
type fakeSource struct {
	mu       sync.Mutex
	view     *topicView
	err      error
	minISR   int
	minErr   error
	minReads int
}

func (s *fakeSource) Topic(context.Context) (*topicView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.view, nil
}

func (s *fakeSource) MinISR(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.minReads++
	return s.minISR, s.minErr
}

func (s *fakeSource) set(fn func(*fakeSource)) { s.mu.Lock(); fn(s); s.mu.Unlock() }

func newSourcedFixture(t *testing.T, src *fakeSource, clock *fakeClock) *producerFixture {
	return newProducerFixture(t, func(o *ProducerOptions) {
		o.source, o.now = src, clock.now
	})
}

// AC-7: the topic's min.insync.replicas is read at the first probe and
// refreshed every 60 s; a change is honored within 60 s, and a read that
// fails keeps the value it had.
func TestProbe_MinInsyncReplicasIsReadAtBootAndRefreshedEvery60s(t *testing.T) {
	short := healthy().with(7, partitionView{Leader: 1, ISR: 2})
	src := &fakeSource{view: short, minISR: 2}
	clock := &fakeClock{t: time.Unix(5_000_000, 0)}
	f := newSourcedFixture(t, src, clock)
	ctx := context.Background()

	f.k.probeOnce(ctx)
	if src.minReads != 1 || f.k.Degraded(7) {
		t.Fatalf("after the first probe: %d reads, Partition 7 degraded=%v; want 1 read and an ISR of 2 against min 2 healthy", src.minReads, f.k.Degraded(7))
	}
	src.set(func(s *fakeSource) { s.minISR = 3 })

	clock.advance(59 * time.Second)
	f.k.probeOnce(ctx)
	if src.minReads != 1 || f.k.Degraded(7) {
		t.Fatalf("at 59s: %d reads, degraded=%v; the change must not be seen before the refresh", src.minReads, f.k.Degraded(7))
	}
	clock.advance(time.Second)
	f.k.probeOnce(ctx)
	if src.minReads != 2 || !f.k.Degraded(7) {
		t.Fatalf("at 60s: %d reads, degraded=%v; want the new min of 3 honored", src.minReads, f.k.Degraded(7))
	}

	// A refresh that fails keeps the last value rather than forgetting it.
	src.set(func(s *fakeSource) { s.minErr = errors.New("describe configs refused") })
	clock.advance(minISRRefresh)
	f.k.probeOnce(ctx)
	if !f.k.Degraded(7) {
		t.Fatal("a failed refresh dropped the min")
	}
}

// Until the first read succeeds the probe does not judge the ISR, and it
// reads again at every probe.
func TestProbe_UnreadMinInsyncReplicasDoesNotJudgeTheISR(t *testing.T) {
	src := &fakeSource{view: healthy().with(7, partitionView{Leader: 1, ISR: 1}), minErr: errors.New("not yet")}
	f := newSourcedFixture(t, src, &fakeClock{t: time.Unix(5_000_000, 0)})
	f.k.probeOnce(context.Background())
	f.k.probeOnce(context.Background())
	if f.k.Degraded(7) || src.minReads != 2 {
		t.Fatalf("degraded=%v after %d reads; want none judged and a read per probe", f.k.Degraded(7), src.minReads)
	}
	src.set(func(s *fakeSource) { s.minErr, s.minISR = nil, 2 })
	f.k.probeOnce(context.Background())
	if !f.k.Degraded(7) {
		t.Fatal("not judged once the min was read")
	}
}

// AC-3, through the producer: a leader absent on one probe marks nothing,
// on two consecutive probes marks the Partition and its gauge.
func TestProbe_LeaderAbsentOnTwoConsecutiveProbes(t *testing.T) {
	src := &fakeSource{view: healthy().with(9, partitionView{Leader: -1}), minISR: 2}
	f := newSourcedFixture(t, src, &fakeClock{t: time.Unix(5_000_000, 0)})
	f.k.probeOnce(context.Background())
	if f.k.Degraded(9) || f.gauge(9) != 0 {
		t.Fatal("one probe marked")
	}
	f.k.probeOnce(context.Background())
	if !f.k.Degraded(9) || f.gauge(9) != 1 || gaugeSum(f.metrics) != 1 {
		t.Fatalf("two probes: degraded=%v gauge=%v sum=%v", f.k.Degraded(9), f.gauge(9), gaugeSum(f.metrics))
	}
	if line := f.logs.String(); !strings.Contains(line, `"cause":"probe"`) || !strings.Contains(line, `"error_name":"leader_absent"`) {
		t.Fatalf("no probe line naming leader_absent:\n%s", line)
	}
}

// AC-6, through the producer: a metadata failure on two consecutive probes
// degrades all 64, one degrades none, and the first answer clears them.
func TestProbe_MetadataFailureOnTwoConsecutiveProbes(t *testing.T) {
	src := &fakeSource{view: healthy(), minISR: 2, err: errors.New("no broker answers")}
	f := newSourcedFixture(t, src, &fakeClock{t: time.Unix(5_000_000, 0)})
	f.k.probeOnce(context.Background())
	if got := degradedCount(f.k); got != 0 || gaugeSum(f.metrics) != 0 {
		t.Fatalf("one failed probe degraded %d", got)
	}
	f.k.probeOnce(context.Background())
	if got := degradedCount(f.k); got != int(sim.PartitionCount) || gaugeSum(f.metrics) != float64(sim.PartitionCount) {
		t.Fatalf("two failed probes degraded %d, gauge sum %v; want 64", got, gaugeSum(f.metrics))
	}
	src.set(func(s *fakeSource) { s.err = nil })
	f.k.probeOnce(context.Background())
	if got := degradedCount(f.k); got != 0 || gaugeSum(f.metrics) != 0 {
		t.Fatalf("%d still degraded after the log answered", got)
	}
}

// AC-8 at the producer: a produce-error mark holds on the injected clock
// across healthy probes, then leaves on the first healthy probe past the
// hold, with the recovery line.
func TestProbe_ProduceErrorMarkLeavesAfterTheHoldAndAHealthyProbe(t *testing.T) {
	src := &fakeSource{view: healthy(), minISR: 2}
	clock := &fakeClock{t: time.Unix(5_000_000, 0)}
	f := newSourcedFixture(t, src, clock)
	ctx := context.Background()
	f.k.health.MarkProduceError(ctx, 3, "REQUEST_TIMED_OUT")

	clock.advance(DefaultDegradedHold - time.Second)
	f.k.probeOnce(ctx)
	if !f.k.Degraded(3) || f.gauge(3) != 1 {
		t.Fatal("left inside the hold")
	}
	clock.advance(time.Second)
	f.k.probeOnce(ctx)
	if f.k.Degraded(3) || f.gauge(3) != 0 {
		t.Fatal("did not leave after the hold and a healthy probe")
	}
	if line := f.logs.String(); !strings.Contains(line, `"msg":"partition recovered`) || !strings.Contains(line, `"error_name":"REQUEST_TIMED_OUT"`) {
		t.Fatalf("no recovery line:\n%s", line)
	}
}

// AC-4 against the wire: the broker answers every request, the topic's ISR
// for one Partition is below min.insync.replicas, and the probe marks it at
// the first probe, then clears it when the ISR is full again, without a
// restart.
func TestProbe_ISRBelowMinWhileEveryBrokerAnswers(t *testing.T) {
	f := newProducerFixture(t, nil)
	f.b.setPartition(11, partitionView{Leader: 0, ISR: 1}) // min.insync.replicas is 2
	ctx := context.Background()

	f.k.probeOnce(ctx)
	if !f.k.Degraded(11) || degradedCount(f.k) != 1 {
		t.Fatalf("degraded: 11=%v, %d in all", f.k.Degraded(11), degradedCount(f.k))
	}
	if !f.k.minISRKnown.Load() || f.k.minISR.Load() != 2 {
		t.Fatalf("min.insync.replicas = %d (known %v), want 2 read from the broker", f.k.minISR.Load(), f.k.minISRKnown.Load())
	}

	f.b.setPartition(11, partitionView{Leader: 0, ISR: 3})
	f.k.probeOnce(ctx)
	if f.k.Degraded(11) {
		t.Fatal("not cleared on recovery")
	}
}

// AC-6 against the wire: a broker that does not answer metadata on two
// consecutive probes degrades all 64.
func TestProbe_AnUnansweredMetadataRequestOnTwoProbes(t *testing.T) {
	f := newProducerFixture(t, nil)
	ctx := context.Background()
	f.b.mu.Lock()
	f.b.noMetadata = true
	f.b.mu.Unlock()
	f.k.probeOnce(ctx)
	if degradedCount(f.k) != 0 {
		t.Fatal("one unanswered probe marked")
	}
	f.k.probeOnce(ctx)
	if degradedCount(f.k) != int(sim.PartitionCount) {
		t.Fatalf("%d degraded after two unanswered probes, want 64", degradedCount(f.k))
	}
	f.b.mu.Lock()
	f.b.noMetadata = false
	f.b.mu.Unlock()
	f.k.probeOnce(ctx)
	if degradedCount(f.k) != 0 {
		t.Fatalf("%d still degraded after the broker answered", degradedCount(f.k))
	}
}

// A broker that does not report min.insync.replicas (Redpanda) is held to the
// Kafka default of 1, so the probe judges the ISR against it instead of never
// reading, and warns nothing a second.
func TestProbe_AnUnreportedMinInsyncReplicasIsTheDefault(t *testing.T) {
	f := newProducerFixture(t, nil)
	f.b.setMinISR("") // answers an empty config list
	f.b.mu.Lock()
	f.b.noMinISR = true
	f.b.mu.Unlock()
	f.b.setPartition(6, partitionView{Leader: 0, ISR: 0})
	f.k.probeOnce(context.Background())
	if !f.k.minISRKnown.Load() || f.k.minISR.Load() != defaultMinISR {
		t.Fatalf("min = %d (known %v), want the default %d", f.k.minISR.Load(), f.k.minISRKnown.Load(), defaultMinISR)
	}
	if !f.k.Degraded(6) {
		t.Fatal("an ISR of 0 against the default min of 1 was not marked")
	}
	if strings.Contains(f.logs.String(), "unread") {
		t.Fatalf("warned about an unreported property:\n%s", f.logs.String())
	}
}

// #129: a broker whose pod is gone but whose address is still in metadata
// never answers. The probe asks every discovered broker at once and the first
// answer wins, so the one that is dark costs nothing: probes in a row succeed
// and no Partition is marked while another broker serves.
func TestProbe_ADarkBrokerInMetadataIsNotTheLogBeingUnreachable(t *testing.T) {
	const dark = "10.255.255.1:9092"
	var dials sync.Map
	f := newProducerFixture(t, func(o *ProducerOptions) {
		o.ProbeInterval = 400 * time.Millisecond
		o.Dialer = func(ctx context.Context, network, host string) (net.Conn, error) {
			if host == dark {
				dials.Store(host, true)
				<-ctx.Done() // a SYN that is never answered
				return nil, ctx.Err()
			}
			return (&net.Dialer{}).DialContext(ctx, network, host)
		}
	})
	f.b.mu.Lock()
	f.b.extra = []string{dark}
	f.b.mu.Unlock()
	ctx := context.Background()
	f.k.probeOnce(ctx) // the first answer names the dark broker to the client
	for i := range 12 {
		began := time.Now()
		pctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		_, err := f.k.source.Topic(pctx)
		cancel()
		if err != nil || time.Since(began) > 200*time.Millisecond {
			t.Fatalf("metadata request %d took %v and returned %v; the dark broker was waited on", i, time.Since(began), err)
		}
		f.k.probeOnce(ctx)
		if got := degradedCount(f.k); got != 0 {
			t.Fatalf("probe %d: %d Partitions degraded while a broker answered", i, got)
		}
	}
	if _, asked := dials.Load(dark); !asked {
		t.Fatal("the dark broker was never dialed: the test did not put it in the way")
	}
}

// A Partition with a leader but no ISR members is below any min.
func TestProbe_PartitionErrorWithALeaderIdIsNoLeader(t *testing.T) {
	f := newProducerFixture(t, nil)
	f.b.setPartition(2, partitionView{Leader: -1, ISR: 0})
	f.k.probeOnce(context.Background())
	f.k.probeOnce(context.Background())
	if !f.k.Degraded(2) {
		t.Fatal("a Partition LEADER_NOT_AVAILABLE twice was not marked")
	}
}

// AC-12's other half: the Submit span carries degraded_partition when the
// producer refuses it, on the Submit's own span as well as log.produce's.
func TestProduce_TheSubmitSpanNamesTheDegradedPartition(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	f := newProducerFixture(t, func(o *ProducerOptions) { o.Tracer = tp.Tracer("test") })
	const p = int32(21)
	f.k.health.MarkProduceError(context.Background(), p, "NOT_ENOUGH_REPLICAS")

	ctx, submit := tp.Tracer("test").Start(context.Background(), "command.execute")
	_, err := f.k.Produce(ctx, cmdOn(t, p))
	submit.End()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Produce = %v", err)
	}
	got := map[string]int64{}
	for _, s := range rec.Ended() {
		for _, kv := range s.Attributes() {
			if kv.Key == attribute.Key("degraded_partition") {
				got[s.Name()] = kv.Value.AsInt64()
			}
		}
	}
	if got["command.execute"] != int64(p) || got["log.produce"] != int64(p) {
		t.Fatalf("degraded_partition by span = %v, want %d on command.execute and log.produce", got, p)
	}
}

// The floor is the producer's to refuse as well as config load's.
func TestNewKafkaProducer_RefusesAHoldBelowTheFloor(t *testing.T) {
	_, err := NewKafkaProducer(ProducerOptions{Brokers: []string{"127.0.0.1:1"}, Deadline: time.Second, DegradedHold: 89 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "floor") {
		t.Fatalf("err = %v", err)
	}
}

// The tap follows requests and responses through whatever chunks the
// connection delivers them in, and decodes a produce response's partition
// error codes at a non-flexible and a flexible version alike.
func TestTap_DecodesProduceErrorsAcrossChunksAndVersions(t *testing.T) {
	for _, version := range []int16{7, 9} {
		t.Run("v"+strconv.Itoa(int(version)), func(t *testing.T) {
			var mu sync.Mutex
			var got []partitionCode
			c := &tapConn{onProduce: func(p int32, code int16) {
				mu.Lock()
				got = append(got, partitionCode{p, code})
				mu.Unlock()
			}}

			frame := func(key, ver int16, corr int32, tagged bool, body []byte) []byte {
				h := []byte{0, 0, 0, 0, byte(key >> 8), byte(key), byte(ver >> 8), byte(ver), byte(corr >> 24), byte(corr >> 16), byte(corr >> 8), byte(corr), 0, 0}
				if tagged {
					h = append(h, 0)
				}
				out := append(h, body...)
				putU32(out, uint32(len(out)-4))
				return out
			}
			respFrame := func(corr int32, tagged bool, body []byte) []byte {
				out := []byte{0, 0, 0, 0, byte(corr >> 24), byte(corr >> 16), byte(corr >> 8), byte(corr)}
				if tagged {
					out = append(out, 0)
				}
				out = append(out, body...)
				putU32(out, uint32(len(out)-4))
				return out
			}

			resp := kmsg.NewPtrProduceResponse()
			resp.SetVersion(version)
			rt := kmsg.NewProduceResponseTopic()
			rt.Topic = "t"
			for _, pc := range []partitionCode{{3, 0}, {4, 19}, {5, 7}} {
				rp := kmsg.NewProduceResponseTopicPartition()
				rp.Partition, rp.ErrorCode = pc.partition, pc.code
				rt.Partitions = append(rt.Partitions, rp)
			}
			resp.Topics = []kmsg.ProduceResponseTopic{rt}

			// A metadata request in front of the produce, and its answer: the
			// tap keeps step without decoding it.
			wire := append(frame(3, 12, 1, true, []byte("metadata request body")), frame(0, version, 2, version >= 9, []byte("produce request body"))...)
			read := append(respFrame(1, true, []byte("metadata response body")), respFrame(2, version >= 9, resp.AppendTo(nil))...)

			for i := range wire { // a byte at a time
				c.noteWrite(wire[i : i+1])
			}
			for i := range read {
				c.noteRead(read[i : i+1])
			}
			mu.Lock()
			defer mu.Unlock()
			want := []partitionCode{{3, 0}, {4, 19}, {5, 7}}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("decoded %v, want %v", got, want)
			}
		})
	}
}

// A connection the tap cannot keep step with is let go: nothing decoded,
// nothing blocked, and the bytes still reach the client.
func TestTap_StopsFollowingAConnectionOutOfStep(t *testing.T) {
	calls := 0
	lost := 0
	c := &tapConn{onProduce: func(int32, int16) { calls++ }, onLost: func() { lost++ }}
	// A response with no request before it: a TLS conn would look like this.
	c.noteRead([]byte{0, 0, 0, 8, 0, 0, 0, 9, 1, 2, 3, 4, 5, 6, 7, 8})
	if !c.off || calls != 0 || lost != 1 {
		t.Fatalf("off=%v calls=%d lost=%d; want it let go once", c.off, calls, lost)
	}
	c.noteWrite([]byte{0, 0, 0, 12, 0, 0, 0, 7, 0, 0, 0, 1, 0, 0, 0, 0})
	c.noteRead([]byte{0, 0, 0, 4, 0, 0, 0, 1})
	if calls != 0 {
		t.Fatal("decoded after stopping")
	}
}

func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// degradedCount is how many Partitions the producer holds read-only.
func degradedCount(k *KafkaProducer) int {
	n := 0
	for p := range sim.PartitionCount {
		if k.Degraded(p) {
			n++
		}
	}
	return n
}

// gaugeSum is sum(andara_ingress_degraded).
func gaugeSum(m *Metrics) float64 {
	var sum float64
	for p := range sim.PartitionCount {
		sum += testutil.ToFloat64(m.Degraded.WithLabelValues(strconv.Itoa(int(p))))
	}
	return sum
}

// The source reads the broker's metadata errors as the tracker needs them.
func TestSource_TopicReadsMetadataErrors(t *testing.T) {
	f := newProducerFixture(t, nil)
	ctx := context.Background()

	f.b.mu.Lock()
	f.b.topicErr = 3 // UNKNOWN_TOPIC_OR_PARTITION
	f.b.mu.Unlock()
	v, err := f.k.source.Topic(ctx)
	if err != nil || !v.Missing {
		t.Fatalf("unknown topic = %+v, %v; want Missing", v, err)
	}

	f.b.mu.Lock()
	f.b.topicErr = 0
	f.b.partErr = map[int32]int16{4: 9, 5: 6} // REPLICA_NOT_AVAILABLE, NOT_LEADER_OR_FOLLOWER
	f.b.mu.Unlock()
	v, err = f.k.source.Topic(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.Partitions[4].Leader < 0 {
		t.Fatal("REPLICA_NOT_AVAILABLE read as a Partition without a leader")
	}
	if v.Partitions[5].Leader != -1 {
		t.Fatalf("a Partition error left leader %d", v.Partitions[5].Leader)
	}
}

// The published gauge follows the tracker's current state, so a notification
// that arrives after a later one cannot leave it wrong.
func TestProducer_AStaleNotificationDoesNotSetTheGauge(t *testing.T) {
	f := newProducerFixture(t, nil)
	const p = int32(12)
	f.k.onHealthChange(context.Background(), p, true, CauseProbe, ErrNameLeaderAbsent) // the tracker says healthy
	if got := f.gauge(p); got != 0 {
		t.Fatalf("gauge = %v with a healthy Partition", got)
	}
}
