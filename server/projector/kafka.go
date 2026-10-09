// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// StateTopic is the compacted current-state topic this projector alone
// writes (AC-9).
const StateTopic = "andara.state.v1"

// ClientID names the projector's Kafka clients, and is the principal the
// andara.state.v1 write ACL names once the broker authenticates (AC-9).
const ClientID = "andara-projector-state"

// ErrLogGap: history the replica needs is not on the log (exit 3). It is the
// tickloop error recovery shares.
var ErrLogGap = tickloop.ErrLogGap

// Boundary is a Tick Boundary Record as read, with when it was produced.
type Boundary = tickloop.Boundary

// BoundaryReader streams the boundary Partition, with the seek recovery shares
// (tickloop.BoundaryReader).
type BoundaryReader = tickloop.BoundaryReader

// CommandSource is a sim.RecordSource over andara.commands.v1 that consumes
// once, from the replica's offsets (tickloop.CommandSource).
type CommandSource = tickloop.CommandSource

// NewBoundaryReader starts reading the boundary Partition from its start, under
// the projector's client ID.
func NewBoundaryReader(ctx context.Context, brokers []string, eventsTopic string) (*BoundaryReader, error) {
	return tickloop.NewBoundaryReader(ctx, brokers, eventsTopic, ClientID)
}

// NewCommandSource consumes every Partition in start from its offset, under the
// projector's client ID.
func NewCommandSource(ctx context.Context, brokers []string, topic string, start map[int32]int64) (*CommandSource, error) {
	return tickloop.NewCommandSource(ctx, brokers, topic, ClientID, start)
}

// Producer writes records to andara.state.v1 on the Partition each names —
// the Zone's, the commands partitioner's — with acks=all and the idempotent
// producer, franz-go's defaults, which keep a key's records in order.
type Producer struct {
	client *kgo.Client
	topic  string
}

// producerOpts are the options the state producer is built with.
func producerOpts(brokers []string) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(ClientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.ManualPartitioner()),
		kgo.ProducerBatchCompression(kgo.ZstdCompression()),
		kgo.RecordDeliveryTimeout(time.Minute),
	}
}

// adminOpts are the options of the projector's metadata and offset clients,
// which neither produce nor consume; purpose is the client.id suffix.
func adminOpts(brokers []string, purpose string) []kgo.Opt {
	return []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.ClientID(ClientID + purpose)}
}

// keysOpts are the options of the state topic key scan, which reads the
// compacted topic from explicit offsets.
func keysOpts(brokers []string) []kgo.Opt {
	return append(adminOpts(brokers, "-keys"), kgo.FetchIsolationLevel(kgo.ReadCommitted()))
}

// MetaOpts are the options of the cmd's topic-size client.
func MetaOpts(brokers []string) []kgo.Opt { return adminOpts(brokers, "-meta") }

// Sites describes every Kafka client this package builds.
func Sites(brokers []string) []kafkaclient.Site {
	return []kafkaclient.Site{
		{Name: "projector state producer", Role: kafkaclient.Producer, Opts: producerOpts(brokers), Partitioning: kafkaclient.Manual},
		{Name: "projector committer", Role: kafkaclient.Admin, Opts: adminOpts(brokers, "-commit")},
		{Name: "projector state key scan", Role: kafkaclient.Consumer, Opts: keysOpts(brokers), AssignedLater: true},
		{Name: "projector topic size", Role: kafkaclient.Admin, Opts: MetaOpts(brokers)},
	}
}

// NewProducer connects the producer.
func NewProducer(ctx context.Context, brokers []string, topic string) (*Producer, error) {
	if topic == "" {
		topic = StateTopic
	}
	client, err := kgo.NewClient(producerOpts(brokers)...)
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
//
// Diverged, when set, is an unresolved divergence: the projector halted at
// Diverged.Tick, and Tick is the one before it. It survives a restart
// (AW-SRV-019 feedback §2, architecture's ruling), because a restart that
// bootstrapped from a newer round would replay past the divergent tick and
// erase the only evidence the projector exists to produce. Only --rebuild,
// which deletes the group, clears it.
type Checkpoint struct {
	Tick     sim.Tick
	Offsets  map[int32]int64
	Diverged *Divergence
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
	cl, err := kgo.NewClient(adminOpts(brokers, "-commit")...)
	if err != nil {
		return nil, err
	}
	return &Committer{adm: kadm.NewClient(cl), cl: cl, group: group, topic: commandsTopic}, nil
}

// The offset metadata: "tick=<T>", and for a halted projector
// "tick=<T>;diverged=<tick>:<recorded hex>:<replayed hex>". Well inside the
// broker's default offset.metadata.max.bytes (4096).
const (
	tickMeta     = "tick="
	divergedMeta = ";diverged="
)

func checkpointMeta(cp Checkpoint) string {
	meta := tickMeta + strconv.FormatUint(uint64(cp.Tick), 10)
	if d := cp.Diverged; d != nil {
		meta += fmt.Sprintf("%s%d:%x:%x", divergedMeta, d.Tick, d.Recorded, d.Replayed)
	}
	return meta
}

// parseCheckpointMeta is checkpointMeta's inverse. Diverged comes back
// without LastGood; the caller fills it from the committed offsets.
func parseCheckpointMeta(meta string) (sim.Tick, *Divergence, error) {
	t, ok := strings.CutPrefix(meta, tickMeta)
	if !ok {
		return 0, nil, fmt.Errorf("metadata %q is not a tick", meta)
	}
	t, div, hasDiv := strings.Cut(t, divergedMeta)
	n, err := strconv.ParseUint(t, 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("bad tick in metadata %q", meta)
	}
	if !hasDiv {
		return sim.Tick(n), nil, nil
	}
	parts := strings.Split(div, ":")
	if len(parts) != 3 {
		return 0, nil, fmt.Errorf("bad divergence in metadata %q", meta)
	}
	dt, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("bad divergent tick in metadata %q", meta)
	}
	d := &Divergence{Tick: sim.Tick(dt)}
	for i, dst := range []*[32]byte{&d.Recorded, &d.Replayed} {
		b, err := hex.DecodeString(parts[i+1])
		if err != nil || len(b) != len(dst) {
			return 0, nil, fmt.Errorf("bad hash in metadata %q", meta)
		}
		copy(dst[:], b)
	}
	return sim.Tick(n), d, nil
}

// Commit records cp.
func (c *Committer) Commit(ctx context.Context, cp Checkpoint) error {
	var o kadm.Offsets
	meta := checkpointMeta(cp)
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
	var meta string
	for _, r := range resp.Offsets()[c.topic] {
		if r.At < 0 {
			continue
		}
		n, d, err := parseCheckpointMeta(r.Metadata)
		if err != nil {
			return Checkpoint{}, false, fmt.Errorf("group %s partition %d: %w; was it committed by something else?", c.group, r.Partition, err)
		}
		if found && r.Metadata != meta {
			return Checkpoint{}, false, fmt.Errorf("group %s: partitions disagree on the checkpoint (%q and %q)", c.group, meta, r.Metadata)
		}
		meta, found = r.Metadata, true
		cp.Tick, cp.Diverged = n, d
		cp.Offsets[r.Partition] = r.At
	}
	if cp.Diverged != nil {
		cp.Diverged.LastGood = cp.Offsets
	}
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
	cl, err := kgo.NewClient(keysOpts(brokers)...)
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
//
// The partitions are named in the request. A topic with an empty partition
// set is a request for none of them, and Redpanda answers it with nothing and
// no error, so the gauge read 0 over a topic holding records (feedback §1 of
// AW-SRV-019). A describe that covers fewer partitions than the topic has is an
// error, never a smaller number.
func TopicBytes(ctx context.Context, adm *kadm.Client, topic string) (int64, error) {
	topics, err := adm.ListTopics(ctx, topic)
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", topic, err)
	}
	td, ok := topics[topic]
	if !ok || td.Err != nil {
		if ok {
			err = td.Err
		} else {
			err = errors.New("not in the metadata")
		}
		return 0, fmt.Errorf("list %s: %w", topic, err)
	}
	want := td.Partitions.Numbers()
	if len(want) == 0 {
		return 0, fmt.Errorf("%s has no partitions", topic)
	}
	var set kadm.TopicsSet
	set.Add(topic, want...)
	dirs, err := adm.DescribeAllLogDirs(ctx, set)
	if err != nil {
		return 0, fmt.Errorf("describe log dirs of %s: %w", topic, err)
	}
	sizes := map[int32]int64{}
	var dirErr error
	dirs.Each(func(d kadm.DescribedLogDir) {
		if d.Err != nil {
			dirErr = errors.Join(dirErr, fmt.Errorf("broker %d dir %s: %w", d.Broker, d.Dir, d.Err))
			return
		}
		d.Topics.Each(func(p kadm.DescribedLogDirPartition) {
			if p.Topic != topic {
				return
			}
			if cur, seen := sizes[p.Partition]; !seen || p.Size > cur {
				sizes[p.Partition] = p.Size
			}
		})
	})
	if len(sizes) < len(want) {
		return 0, fmt.Errorf("describe log dirs of %s: %d of %d partitions reported: %w", topic, len(sizes), len(want), dirErr)
	}
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
