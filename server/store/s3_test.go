// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/valesordev/andara/server/sim"
)

// The S3 store against a real S3-compatible endpoint. Skipped unless one is
// named, the way the broker-backed tests skip without ANDARA_KAFKA_BROKERS:
//
//	docker run -d --rm -p 19000:9000 \
//	  -e MINIO_ROOT_USER=andaratest -e MINIO_ROOT_PASSWORD=andaratest123 \
//	  minio/minio server /data
//	ANDARA_S3_TEST_ENDPOINT=localhost:19000 \
//	ANDARA_S3_TEST_ACCESS_KEY=andaratest ANDARA_S3_TEST_SECRET_KEY=andaratest123 \
//	  go test ./server/store/
//
// Not build-tagged, because unlike the broker tests these need no `make up`:
// the skip is the whole gate, and the day AW-INF-002 puts MinIO in the local
// stack this starts running with one environment variable.
func s3Store(t *testing.T) (*S3, string) {
	t.Helper()
	endpoint := os.Getenv("ANDARA_S3_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("ANDARA_S3_TEST_ENDPOINT not set; see the comment on s3Store for a one-line MinIO")
	}
	access := os.Getenv("ANDARA_S3_TEST_ACCESS_KEY")
	secret := os.Getenv("ANDARA_S3_TEST_SECRET_KEY")
	// A bucket per test, removed when it ends, so tests do not see each
	// other's objects and a failure leaves nothing behind.
	bucket := fmt.Sprintf("andara-test-%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("minio client: %v", err)
	}
	if err := admin.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
		t.Fatalf("make bucket: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		for obj := range admin.ListObjects(cctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
			if obj.Err == nil {
				_ = admin.RemoveObject(cctx, bucket, obj.Key, minio.RemoveObjectOptions{})
			}
		}
		_ = admin.RemoveBucket(cctx, bucket)
	})

	insecure := false
	s, err := NewS3(S3Options{
		Bucket:    bucket,
		Endpoint:  endpoint,
		AccessKey: access,
		SecretKey: secret,
		UseSSL:    &insecure,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	return s, bucket
}

func TestS3PutGetRoundTrips(t *testing.T) {
	s, _ := s3Store(t)
	ctx := context.Background()
	key := sim.SnapshotKey("village", 1, 42, 42)
	want := []byte("envelope bytes")
	if err := s.Put(ctx, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Get = %q, want %q", got, want)
	}
}

// The same classification the filesystem store gives, so a caller can tell a
// Zone that has never been snapshotted from a store that is refusing.
// GetObject is lazy in minio-go — a missing key surfaces on the first read,
// not on the call — which is the bug this pins.
func TestS3GetMissingIsNotFound(t *testing.T) {
	s, _ := s3Store(t)
	_, err := s.Get(context.Background(), sim.SnapshotKey("village", 1, 7, 7))
	if !errors.Is(err, sim.ErrSnapshotNotFound) {
		t.Fatalf("Get missing = %v, want ErrSnapshotNotFound", err)
	}
}

func TestS3ListIsNewestOffsetFirstAcrossStateVersions(t *testing.T) {
	s, _ := s3Store(t)
	ctx := context.Background()
	for _, w := range []struct {
		version uint32
		offset  int64
	}{{1, 30}, {2, 20}, {1, 10}} {
		if err := s.Put(ctx, sim.SnapshotKey("village", w.version, sim.Tick(w.offset), w.offset), []byte("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	keys, err := s.List(ctx, "village")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		sim.SnapshotKey("village", 1, 30, 30),
		sim.SnapshotKey("village", 2, 20, 20),
		sim.SnapshotKey("village", 1, 10, 10),
	}
	if len(keys) != len(want) {
		t.Fatalf("List = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("List = %v, want %v", keys, want)
		}
	}
}

func TestS3ListUnknownZoneIsEmpty(t *testing.T) {
	s, _ := s3Store(t)
	keys, err := s.List(context.Background(), "nowhere")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("List = %v, want empty", keys)
	}
}

// A Zone whose ID is a prefix of another's must not appear in its listing —
// the trailing slash on the prefix is what keeps "town" and "townsquare"
// apart, and a prefix scan is exactly where that goes wrong.
func TestS3ListDoesNotLeakAcrossZonePrefixes(t *testing.T) {
	s, _ := s3Store(t)
	ctx := context.Background()
	if err := s.Put(ctx, sim.SnapshotKey("town", 1, 1, 1), []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Put(ctx, sim.SnapshotKey("townsquare", 1, 2, 2), []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	keys, err := s.List(ctx, "town")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != sim.SnapshotKey("town", 1, 1, 1) {
		t.Fatalf("List(town) = %v, want only town's own object", keys)
	}
}

// Keys that are not {zone}/{version}/{offset} are skipped rather than
// returned: a bucket may hold anything, and List's contract is snapshots.
func TestS3ListSkipsForeignKeys(t *testing.T) {
	s, bucket := s3Store(t)
	ctx := context.Background()
	if err := s.Put(ctx, sim.SnapshotKey("village", 1, 5, 5), []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for _, foreign := range []string{
		"village/notaversion/00000000000000000001",
		"village/1/notanoffset",
		"village/1/2/too-deep",
		"village/stray",
	} {
		if err := s.Put(ctx, foreign, []byte("x")); err != nil {
			t.Fatalf("Put %q into %s: %v", foreign, bucket, err)
		}
	}
	keys, err := s.List(ctx, "village")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != sim.SnapshotKey("village", 1, 5, 5) {
		t.Fatalf("List = %v, want only the one real snapshot", keys)
	}
}

// The whole path the server uses: encode a real Snapshot, Put it, and Decode
// it back off the object store.
func TestS3CarriesAnEncodedSnapshot(t *testing.T) {
	s, _ := s3Store(t)
	ctx := context.Background()
	e, err := newSnapshotFixture()
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	for _, snap := range e.SnapshotAll(1758500000000000000) {
		wire, err := snap.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if err := s.Put(ctx, snap.Key(), wire); err != nil {
			t.Fatalf("Put: %v", err)
		}
		back, err := s.Get(ctx, snap.Key())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		_, body, err := decodeBody(back)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if got, err := sim.BodyStateHash(body); err != nil || got != snap.StateHash() {
			t.Errorf("zone %s: round-tripped through S3 with a different hash", snap.Zone)
		}
	}
}

// NewS3 must not reach the network: a snapshot store that is down should
// degrade a process, not stop it from starting.
func TestNewS3DoesNotConnect(t *testing.T) {
	t.Parallel()
	s, err := NewS3(S3Options{Bucket: "b", Endpoint: "127.0.0.1:1"}) // nothing listens there
	if err != nil {
		t.Fatalf("NewS3 against a dead endpoint failed at construction: %v", err)
	}
	if s == nil {
		t.Fatal("NewS3 returned no store and no error")
	}
}

func TestNewS3RequiresABucket(t *testing.T) {
	t.Parallel()
	if _, err := NewS3(S3Options{Endpoint: "localhost:9000"}); err == nil {
		t.Fatal("NewS3 accepted an empty bucket")
	}
}

// A scheme on the endpoint is the likeliest operator mistake, and minio-go's
// own error for it is opaque.
func TestNewS3AcceptsASchemeOnTheEndpoint(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{"http://localhost:9000", "https://s3.example.com", "s3.example.com"} {
		if _, err := NewS3(S3Options{Bucket: "b", Endpoint: endpoint}); err != nil {
			t.Errorf("NewS3(%q): %v", endpoint, err)
		}
	}
}
