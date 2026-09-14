// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package recordlog is one keyed topic as the server sees it: append a
// record durably, replay what is there at boot. It is the adapter behind
// which Kafka sits (ADR-0002) for state that is not World state — Account
// records and audit records first (AW-SRV-008). The Command log has its own
// producer with partition rules of its own (AW-SRV-010); this is not that.
package recordlog

import "context"

// Record is one key/value pair on a topic.
type Record struct {
	Key   string
	Value []byte
}

// Log is a keyed, append-only topic.
type Log interface {
	// Append writes one record and returns once it is durable on the
	// declared number of replicas (acks=all), so that a response sent after
	// Append returns is a response about something that will survive.
	Append(ctx context.Context, key string, value []byte) error

	// Replay delivers every record present when the call was made, in
	// per-partition order, then returns. On a compacted topic the same key
	// may appear more than once — compaction is eventual — so a caller
	// applies last-write-wins by key. A non-nil error from fn stops the
	// replay and is returned.
	Replay(ctx context.Context, fn func(Record) error) error

	// Close releases the underlying connection. Idempotent.
	Close() error
}
