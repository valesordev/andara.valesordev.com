// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/sim"
)

const saslTopic = "it7.andara.commands.v1"

type saslFixture struct {
	k       *KafkaProducer
	c       *kfake.Cluster
	kc      kafkaclient.Config
	metrics *Metrics
	logs    *syncBuffer
	spans   *tracetest.SpanRecorder
}

// newSASLFixture is the ingress producer, tap attached, against an
// in-process broker that requires SCRAM-SHA-512 of andara-server-dev.
func newSASLFixture(t *testing.T) *saslFixture {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.EnableSASL(),
		kfake.Superuser(kafkaclient.Mechanism, "andara-server-dev", "pw"), kfake.SeedTopics(sim.PartitionCount, saslTopic))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	pw := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pw, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	kc := kafkaclient.Config{Brokers: c.ListenAddrs(), SASLUsername: "andara-server-dev", SASLPasswordFile: pw,
		Metrics: kafkaclient.NewMetrics(prometheus.NewRegistry()), Log: log}
	spans := tracetest.NewSpanRecorder()
	metrics := NewMetrics(prometheus.NewRegistry())
	k, err := NewKafkaProducer(ProducerOptions{
		Kafka: kc, Topic: saslTopic, Deadline: 2 * time.Second, ProbeInterval: 500 * time.Millisecond,
		Metrics: metrics, Log: log, Tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test"),
		manualProbe: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return &saslFixture{k: k, c: c, kc: kc, metrics: metrics, logs: logs, spans: spans}
}

// refuseProduces answers every produce to Partition p with code, and leaves
// the others to the broker.
func (f *saslFixture) refuseProduces(p int32, code int16) {
	f.c.ControlKey(int16(kmsg.Produce), func(req kmsg.Request) (kmsg.Response, error, bool) {
		pr := req.(*kmsg.ProduceRequest)
		hit := false
		resp := pr.ResponseKind().(*kmsg.ProduceResponse)
		for _, rt := range pr.Topics {
			st := kmsg.NewProduceResponseTopic()
			// Produce v13 names the topic by ID; the response must too.
			st.Topic, st.TopicID = rt.Topic, rt.TopicID
			for _, rp := range rt.Partitions {
				sp := kmsg.NewProduceResponseTopicPartition()
				sp.Partition = rp.Partition
				if rp.Partition == p {
					hit = true
					sp.ErrorCode = code
				}
				st.Partitions = append(st.Partitions, sp)
			}
			resp.Topics = append(resp.Topics, st)
		}
		if !hit {
			return nil, nil, false
		}
		f.c.KeepControl()
		return resp, nil, true
	})
}

// AC-8 through the ingress producer: a broker that refuses the principal the
// commands topic answers the Submit at once with a *kafkaclient.AuthError
// naming the topic and the principal, counted once, without retrying and
// without degrading the Partition; the produce span carries error_name and
// the error line the trace.
func TestProducer_ATopicRefusalIsAnAuthErrorNotADegradedPartition(t *testing.T) {
	f := newSASLFixture(t)
	const p = int32(11)
	f.refuseProduces(p, kerr.TopicAuthorizationFailed.Code)

	ctx, root := sdktrace.NewTracerProvider().Tracer("t").Start(context.Background(), "submit")
	start := time.Now()
	_, err := f.k.Produce(ctx, cmdOn(t, p))
	root.End()
	took := time.Since(start)

	var ae *kafkaclient.AuthError
	if !errors.As(err, &ae) || ae.Reason != kafkaclient.ReasonTopicUnauthorized || ae.Topic != saslTopic || ae.Principal != "andara-server-dev" {
		t.Fatalf("Produce = %v, want a topic_unauthorized AuthError on %s as andara-server-dev", err, saslTopic)
	}
	if took >= 2*time.Second {
		t.Fatalf("the refusal took %v, the whole produce deadline: it was retried", took)
	}
	if got := testutil.ToFloat64(f.kc.Metrics.Failures.WithLabelValues("andara-server-ingress", kafkaclient.ReasonTopicUnauthorized)); got != 1 {
		t.Fatalf("topic_unauthorized = %v, want 1", got)
	}
	if f.k.Degraded(p) {
		t.Fatal("an authorization refusal marked the Partition degraded")
	}
	var errName string
	for _, s := range f.spans.Ended() {
		if s.Name() == "log.produce" {
			for _, a := range s.Attributes() {
				if a.Key == "error_name" {
					errName = a.Value.AsString()
				}
			}
		}
	}
	if errName != kafkaclient.ReasonTopicUnauthorized {
		t.Fatalf("log.produce error_name = %q", errName)
	}
	logs := f.logs.String()
	if !strings.Contains(logs, `"reason":"topic_unauthorized"`) || !strings.Contains(logs, `"trace_id":"`+root.SpanContext().TraceID().String()+`"`) {
		t.Fatalf("no error line with the reason and the Submit's trace:\n%s", logs)
	}
	if strings.Contains(logs, `"pw"`) {
		t.Fatalf("the password is in the log:\n%s", logs)
	}
}

// Codex on PR #620: a refusal on a retry proves only that the retry wrote
// nothing. When an earlier attempt of the same record left the process (here
// the broker drops the connection under the first produce request, so its
// fate is unknown), the Submit keeps the outcome-unknown settlement instead of
// a bare AuthError, so its client_ref isn't dropped and a resubmission can't
// append the Command twice. The refusal is still in the chain and counted.
func TestProducer_ARefusalAfterAnEarlierAttemptKeepsTheOutcomeUnknown(t *testing.T) {
	f := newSASLFixture(t)
	const p = int32(19)
	attempts := 0
	f.c.ControlKey(int16(kmsg.Produce), func(req kmsg.Request) (kmsg.Response, error, bool) {
		pr := req.(*kmsg.ProduceRequest)
		if len(pr.Topics) == 0 || len(pr.Topics[0].Partitions) == 0 || pr.Topics[0].Partitions[0].Partition != p {
			return nil, nil, false
		}
		f.c.KeepControl()
		attempts++
		if attempts == 1 {
			return nil, errors.New("kfake: drop the connection mid-produce"), true
		}
		resp := pr.ResponseKind().(*kmsg.ProduceResponse)
		st := kmsg.NewProduceResponseTopic()
		st.Topic, st.TopicID = pr.Topics[0].Topic, pr.Topics[0].TopicID
		sp := kmsg.NewProduceResponseTopicPartition()
		sp.Partition, sp.ErrorCode = p, kerr.TopicAuthorizationFailed.Code
		st.Partitions = append(st.Partitions, sp)
		resp.Topics = append(resp.Topics, st)
		return resp, nil, true
	})

	_, err := f.k.Produce(context.Background(), cmdOn(t, p))
	var u *Unsettled
	if !errors.As(err, &u) {
		t.Fatalf("Produce = %v, want an Unsettled outcome (an earlier attempt may have landed)", err)
	}
	if !kafkaclient.Refused(err) {
		t.Fatalf("Produce = %v, want the refusal in the chain", err)
	}
	<-u.Settled()
	if _, ferr := u.Outcome(); !errors.Is(ferr, ErrOutcomeUnknown) {
		t.Fatalf("settled as %v, want ErrOutcomeUnknown", ferr)
	}
	if got := testutil.ToFloat64(f.kc.Metrics.Failures.WithLabelValues("andara-server-ingress", kafkaclient.ReasonTopicUnauthorized)); got != 1 {
		t.Fatalf("topic_unauthorized = %v, want 1", got)
	}
}

// Codex on PR #620: the Submit's trace-bearing refusal line goes through the
// same per-minute limit as the package's own line, so a revoked ACL under
// steady Submits writes one a minute, not one per Submit.
func TestProducer_TheRefusalLineIsRateLimited(t *testing.T) {
	f := newSASLFixture(t)
	const p = int32(11)
	f.refuseProduces(p, kerr.TopicAuthorizationFailed.Code)
	for range 5 {
		if _, err := f.k.Produce(context.Background(), cmdOn(t, p)); !kafkaclient.Refused(err) {
			t.Fatalf("Produce = %v", err)
		}
	}
	if n := strings.Count(f.logs.String(), "command produce refused"); n != 1 {
		t.Fatalf("%d refusal lines for 5 refused Submits within a minute, want 1:\n%s", n, f.logs.String())
	}
	if got := testutil.ToFloat64(f.kc.Metrics.Failures.WithLabelValues("andara-server-ingress", kafkaclient.ReasonTopicUnauthorized)); got != 5 {
		t.Fatalf("topic_unauthorized = %v, want every refusal (5)", got)
	}
}

// AC-12: with SASL on and the tap attached, the tap follows every connection
// (the SCRAM exchange included, tapLoss stays 0), a produce to a healthy
// Partition is acknowledged, and a NOT_ENOUGH_REPLICAS the broker answers
// still marks its Partition degraded (AW-SRV-052).
func TestProducer_UnderSASLTheTapStillMarksAPartition(t *testing.T) {
	f := newSASLFixture(t)
	const p, q = int32(37), int32(5)
	if _, err := f.k.Produce(context.Background(), cmdOn(t, q)); err != nil {
		t.Fatalf("produce to a healthy Partition under SASL: %v", err)
	}
	f.refuseProduces(p, kerr.NotEnoughReplicas.Code)
	if _, err := f.k.Produce(context.Background(), cmdOn(t, p)); err == nil {
		t.Fatal("a produce the broker refused with NOT_ENOUGH_REPLICAS succeeded")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !f.k.Degraded(p) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !f.k.Degraded(p) {
		t.Fatalf("NOT_ENOUGH_REPLICAS under SASL did not mark Partition %d degraded", p)
	}
	if f.k.Degraded(q) {
		t.Fatalf("Partition %d degraded too", q)
	}
	if n := f.k.tapLoss.Load(); n != 0 {
		t.Fatalf("tapLoss = %d under SASL, want 0", n)
	}
	if got := testutil.ToFloat64(f.kc.Metrics.Failures.WithLabelValues("andara-server-ingress", kafkaclient.ReasonSASLFailed)); got != 0 {
		t.Fatalf("sasl_failed = %v against the right password", got)
	}
}
