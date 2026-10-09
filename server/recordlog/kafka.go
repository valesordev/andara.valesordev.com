// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recordlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/kafkaclient"
)

// Kafka is a Log over one topic on a Kafka or Redpanda broker.
//
// Append is a synchronous produce with acks=all and the idempotent producer
// on, which are franz-go's defaults and ADR-0002's requirements. Replay opens
// a throwaway consumer with no group — the server is the single writer and
// the only reader of these topics, and a committed offset would only ever be
// stale — reads from the start of every partition to the end offset captured
// at the moment Replay began, and stops.
type Kafka struct {
	topic   string
	brokers []string
	opts    []kgo.Opt

	mu     sync.Mutex
	client *kgo.Client
	closed bool
}

// KafkaOptions configures a Kafka log.
type KafkaOptions struct {
	Brokers []string
	Topic   string
	// ClientID appears in broker logs and metrics. Defaults to andara-server.
	ClientID string
	// ProduceTimeout bounds a single Append. Zero means 10s.
	ProduceTimeout time.Duration
	// MaxRecordBytes raises the producer's batch limit (franz-go's default
	// is about 1 MB) for a topic whose records are larger: the content
	// store's blobs, up to content.max_blob_bytes (AW-SRV-013). The topic's
	// max.message.bytes has to allow it too. Zero keeps the default.
	MaxRecordBytes int32
}

// producerOpts are the options a log's producer is built with, o's defaults
// applied. The partitioner is pinned to the murmur2 key hash the topics'
// history was written under, so a library upgrade cannot move a key and
// split its compaction history (AW-SRV-053).
func producerOpts(o KafkaOptions) []kgo.Opt {
	if o.ClientID == "" {
		o.ClientID = "andara-server"
	}
	if o.ProduceTimeout <= 0 {
		o.ProduceTimeout = 10 * time.Second
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(o.Brokers...),
		kgo.ClientID(o.ClientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerBatchCompression(kgo.ZstdCompression()),
		kgo.ProduceRequestTimeout(o.ProduceTimeout),
		kgo.RecordDeliveryTimeout(o.ProduceTimeout),
		kgo.DefaultProduceTopic(o.Topic),
	}
	if o.MaxRecordBytes > 0 {
		opts = append(opts, kgo.ProducerBatchMaxBytes(o.MaxRecordBytes))
	}
	return opts
}

// ProducerSite describes the producer NewKafka builds for o.
func ProducerSite(name string, o KafkaOptions) kafkaclient.Site {
	return kafkaclient.Site{Name: name, Role: kafkaclient.Producer, Opts: producerOpts(o), Partitioning: kafkaclient.KeyHash}
}

// replayOpts are the options a replay consumer is built with: a bounded
// replay of a delete-policy topic from the start, no position resumed.
func replayOpts(brokers []string, topic string) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID("andara-server-replay"),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	}
}

// ReplaySite describes the consumer Replay builds. oneShot names its row in
// the contract's table of one-shot readers.
func ReplaySite(name string, brokers []string, topic string, oneShot kafkaclient.OneShot) kafkaclient.Site {
	return kafkaclient.Site{Name: name, Role: kafkaclient.Consumer, Opts: replayOpts(brokers, topic), OneShot: oneShot}
}

// NewKafka connects to the brokers and verifies the topic exists. It does
// not create the topic: topics are declared in deploy/kafka/topics.yaml and
// applied by `make topics-apply` (AW-INF-004), never by a producer that
// happened to start first.
func NewKafka(ctx context.Context, o KafkaOptions) (*Kafka, error) {
	if len(o.Brokers) == 0 {
		return nil, errors.New("recordlog: no brokers configured")
	}
	if o.Topic == "" {
		return nil, errors.New("recordlog: topic is required")
	}
	if o.ClientID == "" {
		o.ClientID = "andara-server"
	}
	if o.ProduceTimeout <= 0 {
		o.ProduceTimeout = 10 * time.Second
	}
	base := producerOpts(o)
	client, err := kgo.NewClient(base...)
	if err != nil {
		return nil, fmt.Errorf("recordlog: connect %s: %w", o.Topic, err)
	}
	if err := client.Ping(ctx); err != nil {
		client.Close()
		return nil, fmt.Errorf("recordlog: ping brokers for %s: %w", o.Topic, err)
	}
	adm := kadm.NewClient(client)
	md, err := adm.ListTopics(ctx, o.Topic)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("recordlog: describe %s: %w", o.Topic, err)
	}
	if !md.Has(o.Topic) {
		client.Close()
		return nil, fmt.Errorf("recordlog: topic %s does not exist; run `make topics-apply`", o.Topic)
	}
	return &Kafka{topic: o.Topic, brokers: o.Brokers, opts: base, client: client}, nil
}

// Append produces one record and waits for acks=all.
func (k *Kafka) Append(ctx context.Context, key string, value []byte) error {
	k.mu.Lock()
	c, closed := k.client, k.closed
	k.mu.Unlock()
	if closed {
		return errors.New("recordlog: log is closed")
	}
	res := c.ProduceSync(ctx, &kgo.Record{Topic: k.topic, Key: []byte(key), Value: value})
	if err := res.FirstErr(); err != nil {
		return fmt.Errorf("recordlog: append to %s: %w", k.topic, err)
	}
	return nil
}

// Replay reads every partition from its start to the end offset captured
// now, then returns.
func (k *Kafka) Replay(ctx context.Context, fn func(Record) error) error {
	k.mu.Lock()
	closed := k.closed
	k.mu.Unlock()
	if closed {
		return errors.New("recordlog: log is closed")
	}
	adm := kadm.NewClient(k.client)
	starts, err := adm.ListStartOffsets(ctx, k.topic)
	if err != nil {
		return fmt.Errorf("recordlog: start offsets of %s: %w", k.topic, err)
	}
	ends, err := adm.ListEndOffsets(ctx, k.topic)
	if err != nil {
		return fmt.Errorf("recordlog: end offsets of %s: %w", k.topic, err)
	}
	if err := starts.Error(); err != nil {
		return fmt.Errorf("recordlog: start offsets of %s: %w", k.topic, err)
	}
	if err := ends.Error(); err != nil {
		return fmt.Errorf("recordlog: end offsets of %s: %w", k.topic, err)
	}
	// end is the first offset each non-empty partition does NOT owe us. A
	// partition whose start equals its end is empty and is not waited on.
	end := map[int32]int64{}
	for _, e := range ends[k.topic] {
		s, ok := starts.Lookup(k.topic, e.Partition)
		if ok && e.Offset > s.Offset {
			end[e.Partition] = e.Offset
		}
	}
	if len(end) == 0 {
		return nil
	}

	consumer, err := kgo.NewClient(replayOpts(k.brokers, k.topic)...)
	if err != nil {
		return fmt.Errorf("recordlog: replay consumer for %s: %w", k.topic, err)
	}
	defer consumer.Close()

	for len(end) > 0 {
		fetches := consumer.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			return fmt.Errorf("recordlog: replay %s: %w", k.topic, err)
		}
		var ferr error
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if ferr != nil {
				return
			}
			stop, want := end[p.Partition]
			if !want {
				return
			}
			for _, r := range p.Records {
				if r.Offset >= stop {
					break // past the captured end: a concurrent write, not ours to replay
				}
				if err := fn(Record{Key: string(r.Key), Value: r.Value}); err != nil {
					ferr = err
					return
				}
			}
			// Compaction leaves gaps, so completion is the last fetched offset
			// reaching the captured end — not a count of records. The record
			// at end-1 always exists on a compact-only topic (it is in the
			// active segment, which compaction never touches), which is what
			// makes this terminate. The empty-fetch clause covers a partition
			// whose tail was removed by a delete policy; franz-go rarely
			// delivers an empty partition, so a compact+delete topic would
			// want an explicit high-watermark check here (AW-SRV-019).
			if p.HighWatermark >= stop && (len(p.Records) == 0 || p.Records[len(p.Records)-1].Offset+1 >= stop) {
				delete(end, p.Partition)
			}
		})
		if ferr != nil {
			return ferr
		}
	}
	return nil
}

// Close closes the producer client.
func (k *Kafka) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.closed {
		return nil
	}
	k.closed = true
	k.client.Close()
	return nil
}
