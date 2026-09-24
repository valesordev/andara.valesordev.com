// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
)

// The content store's three topics (ADR-0004). Declared in
// deploy/kafka/topics.yaml and created by `make topics-apply`; this package
// reads them and never creates them.
const (
	TopicBlobs    = "andara.content.blobs.v1"
	TopicVersions = "andara.content.versions.v1"
	TopicActive   = "andara.content.active.v1"
)

// PointerMove is one Active Pointer write observed on andara.content.active.v1.
type PointerMove struct {
	Pack    string
	Version uint64
}

// KafkaResolver reads the content store from a broker.
//
// All three topics are compacted and keyed, and a compacted topic cannot be
// fetched by key: there is no random access, only a scan. So resolution
// materializes — read a topic from the start to the end offset captured when
// the scan began, keep the newest value per key, stop. That is affordable for
// the pointer and manifest topics, which hold one small record per pack and
// per version. It would not be for blobs, which is exactly what the on-disk
// cache is for: a warm cache resolves without touching the blob topic at all.
type KafkaResolver struct {
	brokers  []string
	clientID string
	cache    BlobCache
	maxBlob  int64
	metrics  *Metrics
	topics   Topics

	mu     sync.Mutex
	closed bool
}

// KafkaOptions configures a KafkaResolver.
type KafkaOptions struct {
	Brokers  []string
	ClientID string
	Cache    BlobCache
	// MaxBlobBytes refuses a blob larger than this (content.max_blob_bytes).
	// Zero means no limit.
	MaxBlobBytes int64
	// Metrics records cache outcomes. Nil gets an unregistered set.
	Metrics *Metrics
	// Topics overrides the three topic names. The zero value is the real
	// ones; a test against a live broker sets throwaway topics so it never
	// writes into the store the dev stack is serving from.
	Topics Topics
}

// Topics names the three content topics.
type Topics struct {
	Blobs, Versions, Active string
}

func (t Topics) orDefault() Topics {
	if t.Blobs == "" {
		t.Blobs = TopicBlobs
	}
	if t.Versions == "" {
		t.Versions = TopicVersions
	}
	if t.Active == "" {
		t.Active = TopicActive
	}
	return t
}

// NewKafkaResolver builds a resolver. It does not connect: the first scan
// does, and a content store that is unreachable should be a load failure with
// the previous version retained, not a constructor that panics a boot.
func NewKafkaResolver(o KafkaOptions) (*KafkaResolver, error) {
	if len(o.Brokers) == 0 {
		return nil, errors.New("content: no brokers configured")
	}
	if o.ClientID == "" {
		o.ClientID = "andara-server-content"
	}
	m := o.Metrics
	if m == nil {
		m = NewMetrics(nil)
	}
	return &KafkaResolver{
		brokers:  o.Brokers,
		clientID: o.ClientID,
		cache:    o.Cache,
		maxBlob:  o.MaxBlobBytes,
		metrics:  m,
		topics:   o.Topics.orDefault(),
	}, nil
}

// Active returns every Active Pointer, newest write per pack.
func (r *KafkaResolver) Active(ctx context.Context) (map[string]uint64, error) {
	out := map[string]uint64{}
	err := r.scan(ctx, r.topics.Active, func(key, value []byte) error {
		if len(value) == 0 {
			// A tombstone retires a pack. Compaction will remove it; until
			// then it means "no active version", not "version zero".
			delete(out, string(key))
			return nil
		}
		var av contentv1.ActiveVersion
		if err := proto.Unmarshal(value, &av); err != nil {
			return fmt.Errorf("content: decode active pointer %q: %w", key, err)
		}
		pack := av.GetPackId()
		if pack == "" {
			pack = string(key)
		}
		out[pack] = av.GetVersion()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Manifest returns the version manifest for pack@version.
func (r *KafkaResolver) Manifest(ctx context.Context, pack string, version uint64) (*contentv1.ContentVersion, error) {
	want := ManifestKey(pack, version)
	var found *contentv1.ContentVersion
	err := r.scan(ctx, r.topics.Versions, func(key, value []byte) error {
		if string(key) != want || len(value) == 0 {
			return nil
		}
		var cv contentv1.ContentVersion
		if err := proto.Unmarshal(value, &cv); err != nil {
			return fmt.Errorf("content: decode manifest %s: %w", want, err)
		}
		found = &cv
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("content: no manifest for %s on %s", want, r.topics.Versions)
	}
	return found, nil
}

// Blobs returns the body of every BlobRef, by path. Cached bodies are used
// without touching the broker; the blob topic is scanned only for what is
// missing, and everything fetched is cached on the way out.
func (r *KafkaResolver) Blobs(ctx context.Context, refs []*contentv1.BlobRef) (map[string][]byte, error) {
	bodies := make(map[string][]byte, len(refs))
	missing := map[string][]*contentv1.BlobRef{} // hex hash -> refs wanting it
	for _, ref := range refs {
		if r.maxBlob > 0 && int64(ref.GetSizeBytes()) > r.maxBlob {
			return nil, fmt.Errorf("content: blob %s is %d bytes, over content.max_blob_bytes %d",
				ref.GetPath(), ref.GetSizeBytes(), r.maxBlob)
		}
		if body, err := r.cache.Get(ref.GetHash()); err == nil {
			r.metrics.CacheHits.WithLabelValues(OutcomeHit).Inc()
			bodies[ref.GetPath()] = body
			continue
		}
		r.metrics.CacheHits.WithLabelValues(OutcomeMiss).Inc()
		h := hex.EncodeToString(ref.GetHash())
		missing[h] = append(missing[h], ref)
	}
	if len(missing) == 0 {
		return bodies, nil
	}

	err := r.scan(ctx, r.topics.Blobs, func(key, value []byte) error {
		h := hex.EncodeToString(key)
		refs, want := missing[h]
		if !want || len(value) == 0 {
			return nil
		}
		var b contentv1.Blob
		if err := proto.Unmarshal(value, &b); err != nil {
			return fmt.Errorf("content: decode blob %s: %w", h, err)
		}
		body := b.GetBody()
		for _, ref := range refs {
			bodies[ref.GetPath()] = body
		}
		if err := r.cache.Put(key, body); err != nil {
			// A cache that cannot be written is slow, not wrong.
			_ = err
		}
		delete(missing, h)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, refs := range missing {
		// Deterministic: report the first path that wanted the absent hash.
		return nil, &ErrBlobMissing{Hash: refs[0].GetHash(), Path: refs[0].GetPath()}
	}
	return bodies, nil
}

// Watch delivers every Active Pointer write from now on. It does not replay
// history: the caller has already resolved the current pointers, and replaying
// them would reload content that is already serving.
func (r *KafkaResolver) Watch(ctx context.Context) (<-chan PointerMove, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(r.brokers...),
		kgo.ClientID(r.clientID+"-watch"),
		kgo.ConsumeTopics(r.topics.Active),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtEnd()),
	)
	if err != nil {
		return nil, fmt.Errorf("content: watch %s: %w", r.topics.Active, err)
	}
	out := make(chan PointerMove, 16)
	go func() {
		defer close(out)
		defer client.Close()
		for {
			fetches := client.PollFetches(ctx)
			if ctx.Err() != nil {
				return
			}
			if fetches.IsClientClosed() {
				return
			}
			// A fetch error on a watch is transient by nature — a broker
			// restart, a leader move. Poll again; the retained version keeps
			// serving in the meantime.
			fetches.EachRecord(func(rec *kgo.Record) {
				if len(rec.Value) == 0 {
					return
				}
				var av contentv1.ActiveVersion
				if err := proto.Unmarshal(rec.Value, &av); err != nil {
					return
				}
				pack := av.GetPackId()
				if pack == "" {
					pack = string(rec.Key)
				}
				select {
				case out <- PointerMove{Pack: pack, Version: av.GetVersion()}:
				case <-ctx.Done():
				}
			})
		}
	}()
	return out, nil
}

// Close releases nothing yet — every scan opens and closes its own client, and
// Watch's client dies with its context. It exists so the caller's shutdown
// path does not have to know that.
func (r *KafkaResolver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// ManifestKey is the record key on andara.content.versions.v1.
func ManifestKey(pack string, version uint64) string {
	return fmt.Sprintf("%s@%d", pack, version)
}

// scan reads topic from the start of every partition to the end offset
// captured now, calling fn for each record in offset order per partition.
//
// The termination rule is the one server/recordlog uses, and for the same
// reason: compaction leaves gaps, so "we have read enough" is the last fetched
// offset reaching the captured end, never a count of records.
func (r *KafkaResolver) scan(ctx context.Context, topic string, fn func(key, value []byte) error) error {
	admClient, err := kgo.NewClient(kgo.SeedBrokers(r.brokers...), kgo.ClientID(r.clientID+"-admin"))
	if err != nil {
		return fmt.Errorf("content: connect for %s: %w", topic, err)
	}
	adm := kadm.NewClient(admClient)
	starts, serr := adm.ListStartOffsets(ctx, topic)
	ends, eerr := adm.ListEndOffsets(ctx, topic)
	admClient.Close()
	if serr != nil {
		return fmt.Errorf("content: start offsets of %s: %w", topic, serr)
	}
	if eerr != nil {
		return fmt.Errorf("content: end offsets of %s: %w", topic, eerr)
	}
	if err := starts.Error(); err != nil {
		return fmt.Errorf("content: start offsets of %s: %w", topic, err)
	}
	if err := ends.Error(); err != nil {
		return fmt.Errorf("content: end offsets of %s: %w", topic, err)
	}

	end := map[int32]int64{}
	for _, e := range ends[topic] {
		s, ok := starts.Lookup(topic, e.Partition)
		if ok && e.Offset > s.Offset {
			end[e.Partition] = e.Offset
		}
	}
	if len(end) == 0 {
		return nil // every partition is empty
	}

	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(r.brokers...),
		kgo.ClientID(r.clientID+"-scan"),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return fmt.Errorf("content: scan consumer for %s: %w", topic, err)
	}
	defer consumer.Close()

	for len(end) > 0 {
		fetches := consumer.PollFetches(ctx)
		if err := fetches.Err0(); err != nil {
			return fmt.Errorf("content: scan %s: %w", topic, err)
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
			for _, rec := range p.Records {
				if rec.Offset >= stop {
					break // past the captured end: a concurrent publish, not ours
				}
				if err := fn(rec.Key, rec.Value); err != nil {
					ferr = err
					return
				}
			}
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
