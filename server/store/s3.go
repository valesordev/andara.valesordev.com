// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/valesordev/andara/server/sim"
)

// S3Options configures the S3-compatible store.
type S3Options struct {
	// Bucket is snapshot.s3_bucket. Required.
	Bucket string
	// Endpoint is snapshot.s3_endpoint — host[:port], no scheme. Empty means
	// AWS S3 (s3.amazonaws.com); MinIO locally.
	Endpoint string
	// Region is optional; S3 resolves it when empty and MinIO ignores it.
	Region string
	// AccessKey and SecretKey are optional: empty falls back to the
	// environment and the IAM chain, which is how the cluster supplies them
	// (AWS_ACCESS_KEY_ID, a web-identity token, or the instance role). They
	// exist for the local stack, where MinIO wants a static pair.
	AccessKey string
	SecretKey string
	// UseSSL defaults to true. MinIO over plain HTTP locally sets it false.
	UseSSL *bool
}

// S3 is a sim.WorldStore over an S3-compatible object store: the cluster's
// snapshot.store=s3, and MinIO in the local stack, so the cluster's path is
// exercised before the cluster exists.
//
// PutObject is atomic on S3 and on MinIO — an object is visible with complete
// contents or not at all, and an interrupted upload leaves nothing to list —
// so AC-5 needs no tmp-and-rename dance here, unlike the filesystem store.
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 builds the store. It does not reach the network: a constructor that
// fails on an unreachable endpoint would make the server's start depend on the
// object store being up, and a snapshot store that is down is a degraded
// process, not a dead one — SnapshotStale is the alert for it.
func NewS3(o S3Options) (*S3, error) {
	if o.Bucket == "" {
		return nil, errors.New("store: snapshot.s3_bucket is required when snapshot.store=s3")
	}
	endpoint := o.Endpoint
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	// A scheme here is the most likely operator mistake, and minio-go's own
	// error for it is opaque. Accept it and say what was done.
	secure := true
	if after, ok := strings.CutPrefix(endpoint, "https://"); ok {
		endpoint = after
	} else if after, ok := strings.CutPrefix(endpoint, "http://"); ok {
		endpoint, secure = after, false
	}
	if o.UseSSL != nil {
		secure = *o.UseSSL
	}
	var creds *credentials.Credentials
	if o.AccessKey != "" {
		creds = credentials.NewStaticV4(o.AccessKey, o.SecretKey, "")
	} else {
		// Environment first, then the IAM chain: the cluster supplies a role
		// or a web-identity token, and neither is an env var.
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.FileAWSCredentials{},
			&credentials.IAM{},
		})
	}
	c, err := minio.New(endpoint, &minio.Options{Creds: creds, Secure: secure, Region: o.Region})
	if err != nil {
		return nil, fmt.Errorf("store: s3 client for %s: %w", endpoint, err)
	}
	return &S3{client: c, bucket: o.Bucket}, nil
}

// Put writes envelope at key. Implements sim.WorldStore.
func (s *S3) Put(ctx context.Context, key string, envelope []byte) error {
	if key == "" {
		return fmt.Errorf("store: empty key")
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(envelope), int64(len(envelope)),
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("%w: put %s: %w", sim.ErrStoreUnavailable, key, err)
	}
	return nil
}

// Get reads the object at key. Implements sim.WorldStore.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s.readErr(key, err)
	}
	defer func() { _ = obj.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(obj); err != nil {
		// GetObject is lazy: a missing key surfaces here, not above.
		return nil, s.readErr(key, err)
	}
	return buf.Bytes(), nil
}

func (s *S3) readErr(key string, err error) error {
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return fmt.Errorf("%w: %s", sim.ErrSnapshotNotFound, key)
	}
	return fmt.Errorf("%w: get %s: %w", sim.ErrStoreUnavailable, key, err)
}

// List returns a Zone's keys, newest first. Implements sim.WorldStore.
//
// Ordered by tick descending, the same as the filesystem store and for the same
// reason: a lexical sort would rank state_version above recency.
func (s *S3) List(ctx context.Context, zone sim.ZoneID) ([]string, error) {
	if zone == "" {
		return nil, fmt.Errorf("store: empty zone")
	}
	var found []snapshotEntry
	// A prefix scan under the Zone, which is one call's worth of pagination
	// rather than a listing of the whole bucket. The trailing slash is what
	// keeps a Zone whose ID is a prefix of this one's out of the results.
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    string(zone) + "/",
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("%w: list %s: %w", sim.ErrStoreUnavailable, zone, obj.Err)
		}
		e, ok := newSnapshotEntry(obj.Key)
		if !ok {
			continue // not a snapshot key; a bucket may hold anything
		}
		if z, _, _, _, _ := sim.ParseSnapshotKey(obj.Key); z != zone {
			continue
		}
		found = append(found, e)
	}
	return sortSnapshotKeys(found), nil
}
