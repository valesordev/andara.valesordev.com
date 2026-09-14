// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recordlog

import (
	"context"
	"sync"
)

// Memory is a Log that lives in the process. It is the store behind
// auth.store=memory — a development convenience that loses every Account on
// restart, and says so at boot — and the fake every unit test runs against.
type Memory struct {
	mu      sync.Mutex
	records []Record
	// Fail, when set, is returned by Append instead of writing. Tests use it
	// to prove that a failed write leaves the in-memory index untouched.
	Fail error
}

// NewMemory returns an empty Memory log.
func NewMemory() *Memory { return &Memory{} }

// Append copies the value and appends it.
func (m *Memory) Append(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	v := make([]byte, len(value))
	copy(v, value)
	m.records = append(m.records, Record{Key: key, Value: v})
	return nil
}

// Replay delivers a snapshot of the records in append order.
func (m *Memory) Replay(_ context.Context, fn func(Record) error) error {
	m.mu.Lock()
	snap := make([]Record, len(m.records))
	copy(snap, m.records)
	m.mu.Unlock()
	for _, r := range snap {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// Close is a no-op.
func (m *Memory) Close() error { return nil }

// Records returns a copy of everything appended, for tests.
func (m *Memory) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, len(m.records))
	copy(out, m.records)
	return out
}

// Len is the number of records appended.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}
