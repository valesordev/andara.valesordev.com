// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const dark = "127.0.0.1:1"

func producerOpts(extra ...kgo.Opt) []kgo.Opt {
	return append([]kgo.Opt{
		kgo.SeedBrokers(dark), kgo.ClientID("andara-server-ingress"),
		kgo.RequiredAcks(kgo.AllISRAcks()), kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.ProducerBatchCompression(kgo.ZstdCompression()), kgo.RecordDeliveryTimeout(5 * time.Second),
	}, extra...)
}

func consumerOpts(extra ...kgo.Opt) []kgo.Opt {
	return append([]kgo.Opt{
		kgo.SeedBrokers(dark), kgo.ClientID("andara-server-recovery"), kgo.FetchIsolationLevel(kgo.ReadCommitted()),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{"t": {0: kgo.NewOffset().At(7)}}),
	}, extra...)
}

func producer(opts []kgo.Opt) Site {
	return Site{Name: "p", Role: Producer, Opts: opts, Partitioning: Manual, DeliveryTimeout: 5 * time.Second}
}

// failing is the findings that fail: a clean client has none.
func failing(t *testing.T, s Site) map[string]Finding {
	t.Helper()
	out := map[string]Finding{}
	for _, f := range Check(s) {
		if !f.OK {
			out[f.Setting] = f
		}
	}
	return out
}

func only(t *testing.T, got map[string]Finding, setting string, sev Severity) Finding {
	t.Helper()
	f, ok := got[setting]
	if !ok || len(got) != 1 || f.Severity != sev {
		t.Fatalf("deviations = %+v, want only %s (%v)", got, setting, sev)
	}
	return f
}

func TestCheck_ACleanProducerAndConsumerDeviateNowhere(t *testing.T) {
	if got := failing(t, producer(producerOpts())); len(got) != 0 {
		t.Fatalf("producer: %+v", got)
	}
	if got := failing(t, Site{Name: "c", Role: Consumer, Opts: consumerOpts()}); len(got) != 0 {
		t.Fatalf("consumer: %+v", got)
	}
}

// AC-1: a deviation names the client, the setting, the value in effect and
// the contract's value.
func TestCheck_AFindingNamesTheClientTheSettingTheValueAndTheContract(t *testing.T) {
	// A leader-only ack needs idempotence off in franz-go; the acks row is
	// the one this test reads.
	got := failing(t, producer(producerOpts(kgo.DisableIdempotentWrite(), kgo.RequiredAcks(kgo.LeaderAck()))))
	f, ok := got["acks"]
	if !ok {
		t.Fatalf("deviations = %+v", got)
	}
	line := f.String()
	for _, want := range []string{`client="p"`, "setting=acks", "value=", "contract=all"} {
		if !strings.Contains(line, want) {
			t.Fatalf("%q lacks %q", line, want)
		}
	}
	if f.Value == "all" || f.Value == f.Contract {
		t.Fatalf("value in effect = %q", f.Value)
	}
}

// AC-2.
func TestCheck_AnIdempotenceOffProducerFailsAndNamesTheClient(t *testing.T) {
	s := producer(producerOpts(kgo.DisableIdempotentWrite()))
	s.Name = "ingress producer"
	f := only(t, failing(t, s), "enable.idempotence", Fail)
	if f.Client != "ingress producer" || f.Value != "false" {
		t.Fatalf("finding = %+v", f)
	}
}

// AC-3.
func TestCheck_AProducerWithoutTheExplicitPartitionerFails(t *testing.T) {
	opts := producerOpts(kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)))
	only(t, failing(t, producer(opts)), "partitioner", Fail)
	// The library's default is no more explicit than the key hash.
	var def []kgo.Opt
	for _, o := range producerOpts() {
		def = append(def, o)
	}
	def = append(def, kgo.RecordPartitioner(kgo.RoundRobinPartitioner()))
	only(t, failing(t, producer(def)), "partitioner", Fail)
}

// AC-4: compression is a warning and no more.
func TestCheck_CompressionIsAWarning(t *testing.T) {
	f := only(t, failing(t, producer(producerOpts(kgo.ProducerBatchCompression(kgo.SnappyCompression())))), "compression.type", Warn)
	if f.Value != "snappy" {
		t.Fatalf("value = %q", f.Value)
	}
	only(t, failing(t, producer(producerOpts(kgo.ProducerBatchCompression(kgo.NoCompression())))), "compression.type", Warn)
}

// AC-5.
func TestCheck_AClientIDOutsideThePrincipalRuleFails(t *testing.T) {
	for _, id := range []string{"", "andara-server-ingress-pod-7f9", "andara-server-Ingress", "someone-else", "andara-server_x"} {
		t.Run(id, func(t *testing.T) {
			opts := producerOpts()
			if id == "" {
				opts = opts[:1:1]
				opts = append(opts, producerOpts()[2:]...) // no ClientID at all
			} else {
				opts = append(opts, kgo.ClientID(id))
			}
			only(t, failing(t, producer(opts)), "client.id", Fail)
		})
	}
	for _, id := range Principals {
		opts := append(producerOpts(), kgo.ClientID(id+"-state-commit"))
		if got := failing(t, producer(opts)); len(got) != 0 {
			t.Fatalf("%s: %+v", id, got)
		}
	}
}

func TestCheck_ADeliveryTimeoutOffTheDeadlineFails(t *testing.T) {
	only(t, failing(t, producer(producerOpts(kgo.RecordDeliveryTimeout(time.Minute)))), "delivery.timeout.ms", Fail)
}

func TestCheck_MaxInFlightAboveFiveFails(t *testing.T) {
	// franz-go refuses more than one in flight with idempotence on, so the
	// only way to exceed five is without it.
	got := failing(t, producer(producerOpts(kgo.DisableIdempotentWrite(), kgo.MaxProduceRequestsInflightPerBroker(6))))
	if _, ok := got["max.in.flight.requests.per.connection"]; !ok {
		t.Fatalf("deviations = %+v", got)
	}
}

// AC-6.
func TestCheck_AReaderAtAStartOrEndFailsUnlessItIsANamedOneShot(t *testing.T) {
	scan := func(o OneShot, extra ...kgo.Opt) Site {
		opts := []kgo.Opt{kgo.SeedBrokers(dark), kgo.ClientID("andara-server-scan"), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.ConsumeTopics("t")}
		return Site{Name: "scan", Role: Consumer, Opts: append(opts, extra...), OneShot: o}
	}
	f := only(t, failing(t, scan(NotOneShot)), "start offset", Fail)
	if !strings.Contains(f.Value, "AtStart") {
		t.Fatalf("value = %q", f.Value)
	}
	only(t, failing(t, scan(NotOneShot, kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()))), "start offset", Fail)
	if got := failing(t, scan(AccountsScan)); len(got) != 0 {
		t.Fatalf("a named one-shot: %+v", got)
	}
	only(t, failing(t, scan("somebody's scan")), "start offset", Fail)

	// A named one-shot that resumes at At(offset) is not what the table lists.
	only(t, failing(t, Site{Name: "r", Role: Consumer, Opts: consumerOpts(), OneShot: AccountsScan}), "start offset", Fail)
	// A reader assigned AtStart on a Partition.
	atStart := consumerOpts(kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{"t": {0: kgo.NewOffset().AtStart()}}))
	only(t, failing(t, Site{Name: "r", Role: Consumer, Opts: atStart}), "start offset", Fail)
}

// AC-7.
func TestCheck_AConsumerGroupOrCommittedOffsetsFail(t *testing.T) {
	opts := []kgo.Opt{kgo.SeedBrokers(dark), kgo.ClientID("andara-server-sim"), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.ConsumeTopics("t"), kgo.ConsumerGroup("andara-sim")}
	got := failing(t, Site{Name: "g", Role: Consumer, Opts: opts})
	if _, ok := got["group membership"]; !ok {
		t.Fatalf("deviations = %+v", got)
	}
	if _, ok := got["committed offsets in Kafka"]; !ok {
		t.Fatalf("deviations = %+v", got)
	}
}

func TestCheck_AnUncommittedReaderFails(t *testing.T) {
	opts := []kgo.Opt{kgo.SeedBrokers(dark), kgo.ClientID("andara-server-recovery"),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{"t": {0: kgo.NewOffset().At(7)}})}
	f := only(t, failing(t, Site{Name: "r", Role: Consumer, Opts: opts}), "isolation.level", Fail)
	if f.Value != "read_uncommitted" {
		t.Fatalf("value = %q", f.Value)
	}
}

// An admin client has a client.id and nothing else to hold.
func TestCheck_AnAdminClientIsHeldToItsClientIDOnly(t *testing.T) {
	ok := Site{Name: "a", Role: Admin, Opts: []kgo.Opt{kgo.SeedBrokers(dark), kgo.ClientID("andara-projector-state-meta")}}
	if got := failing(t, ok); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	bad := Site{Name: "a", Role: Admin, Opts: []kgo.Opt{kgo.SeedBrokers(dark)}}
	only(t, failing(t, bad), "client.id", Fail)
}

func TestCheck_AnAssignedLaterReaderMeetsTheStartRow(t *testing.T) {
	opts := []kgo.Opt{kgo.SeedBrokers(dark), kgo.ClientID("andara-projector-state-keys"), kgo.FetchIsolationLevel(kgo.ReadCommitted())}
	if got := failing(t, Site{Name: "k", Role: Consumer, Opts: opts, AssignedLater: true}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	only(t, failing(t, Site{Name: "k", Role: Consumer, Opts: opts}), "start offset", Fail)
}

// Data / state impact: pinning the partitioner keeps the key-to-Partition
// mapping of the topics' history. The fixture is the mapping the library's
// default partitioner gave when it was written down.
func TestKeyHash_ThePinnedPartitionerKeepsTheDefaultMapping(t *testing.T) {
	def, err := kgo.NewClient(kgo.SeedBrokers(dark))
	if err != nil {
		t.Fatal(err)
	}
	defer def.Close()
	pinned, err := kgo.NewClient(kgo.SeedBrokers(dark), kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	for k, want := range keyHashSample {
		for name, cl := range map[string]*kgo.Client{"default": def, "pinned": pinned} {
			tp := cl.OptValue(kgo.RecordPartitioner).(kgo.Partitioner).ForTopic("t")
			if got := partitionOf(tp, &kgo.Record{Key: []byte(k)}, 6); got != want {
				t.Errorf("%s partitioner: key %q -> %d, history has %d", name, k, got, want)
			}
		}
	}
	if len(keyHashSample) < 10 {
		t.Fatalf("fixture has %d keys", len(keyHashSample))
	}
}

func TestKeyHash_AManualPartitionerOnAKeyedTopicFails(t *testing.T) {
	s := Site{Name: "log", Role: Producer, Partitioning: KeyHash, Opts: producerOpts()}
	f := only(t, failing(t, s), "partitioner", Fail)
	if !strings.Contains(f.Value, "maps to Partition") {
		t.Fatalf("value = %q", f.Value)
	}
	s.Opts = producerOpts(kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)))
	if got := failing(t, s); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}
