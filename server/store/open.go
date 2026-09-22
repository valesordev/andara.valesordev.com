// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package store

import (
	"fmt"

	"github.com/valesordev/andara/server/sim"
)

// Kinds snapshot.store accepts. The same strings server/config validates;
// duplicated as constants here rather than imported, because the store does not
// depend on the configuration package — it is handed values, not a Config.
const (
	KindFS = "fs"
	KindS3 = "s3"
)

// Options is everything Open needs, flattened out of the snapshot.* keys.
type Options struct {
	Kind       string // snapshot.store
	FSPath     string // snapshot.fs_path
	S3Bucket   string // snapshot.s3_bucket
	S3Endpoint string // snapshot.s3_endpoint
	S3Region   string
	AccessKey  string
	SecretKey  string
	UseSSL     *bool
}

// Open builds the store snapshot.store names.
//
// Neither implementation touches its backend here: the filesystem store creates
// its directories on the first Put, and the S3 client does not connect until it
// is used. A snapshot store that is down should degrade a process, not stop it
// from starting — the World is still authoritative in memory and still
// recoverable from the log, and SnapshotStale is the alert that says recovery
// would be slow.
func Open(o Options) (sim.WorldStore, error) {
	switch o.Kind {
	case KindFS:
		if o.FSPath == "" {
			return nil, fmt.Errorf("store: snapshot.store=fs requires snapshot.fs_path")
		}
		return NewFS(o.FSPath), nil
	case KindS3:
		return NewS3(S3Options{
			Bucket:    o.S3Bucket,
			Endpoint:  o.S3Endpoint,
			Region:    o.S3Region,
			AccessKey: o.AccessKey,
			SecretKey: o.SecretKey,
			UseSSL:    o.UseSSL,
		})
	default:
		return nil, fmt.Errorf("store: snapshot.store must be %s or %s, got %q", KindFS, KindS3, o.Kind)
	}
}
