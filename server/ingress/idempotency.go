// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"time"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
)

// entry is one Submit's idempotency record, keyed by (Session, client_ref)
// (AW-SRV-031). It is made before the Submit queues, so a retry arriving
// while the original is in flight finds it and waits on done; it is
// resolved by the pipeline's outcome or, when the outcome was unknown at
// the deadline, by the producer's settlement of the record's fate.
//
// kept says whether the outcome is one the Command's fate turns on — an
// offset, or a rejection of the Intent — and so is remembered for the
// window; a transient refusal (rate, queue, read-only, the caller giving
// up) is not, and a waiter on such an entry runs the Command itself.
type entry struct {
	ref  string
	raw  string
	done chan struct{}
	resp *gamev1.SubmitResponse
	err  error
	kept bool
	// at is when the entry was made and then, once resolved, when: the
	// window counts from the latter, and a never-settled entry cannot
	// outlive it either.
	at time.Time
}

// table is one Session's keys, oldest first.
type table struct {
	keys  map[string]*entry
	order []*entry
}

// lookup finds ref's entry or makes one. It reports a hit, or
// ErrDuplicateClientRef when the ref is known for a different Intent.
// Expired entries are swept first and, at max keys, the oldest is
// evicted to make room. Under the Session's lock; n is the change in
// held keys for the gauge.
func (t *table) lookup(ref, raw string, now time.Time, window time.Duration, max int) (e *entry, hit bool, n int, err error) {
	if t.keys == nil {
		t.keys = map[string]*entry{}
	}
	n = t.sweep(now, window)
	if e, ok := t.keys[ref]; ok {
		if e.raw != raw {
			return nil, false, n, ErrDuplicateClientRef
		}
		return e, true, n, nil
	}
	for len(t.keys) >= max && len(t.order) > 0 {
		n += t.drop(t.order[0])
		t.order = t.order[1:]
	}
	e = &entry{ref: ref, raw: raw, done: make(chan struct{}), at: now}
	t.keys[ref] = e
	t.order = append(t.order, e)
	return e, false, n + 1, nil
}

// sweep removes entries past the window, and from the order those a
// transient outcome already dropped, and returns the change.
func (t *table) sweep(now time.Time, window time.Duration) (n int) {
	kept := t.order[:0]
	for _, e := range t.order {
		if t.keys[e.ref] != e || now.Sub(e.at) > window {
			n += t.drop(e)
			continue
		}
		kept = append(kept, e)
	}
	clear(t.order[len(kept):])
	t.order = kept
	return n
}

// drop removes e from the keys if it is still the one held for its ref;
// the order is the caller's to maintain.
func (t *table) drop(e *entry) int {
	if t.keys[e.ref] != e {
		return 0
	}
	delete(t.keys, e.ref)
	return -1
}

// resolve records the outcome and wakes waiters. A transient outcome
// leaves the keys now; its order slot goes with the next sweep.
func (t *table) resolve(e *entry, resp *gamev1.SubmitResponse, err error, kept bool, now time.Time) (n int) {
	e.resp, e.err, e.kept, e.at = resp, err, kept, now
	if !kept {
		n = t.drop(e)
	}
	close(e.done)
	return n
}

// forget empties the table and returns the change.
func (t *table) forget() int {
	n := -len(t.keys)
	t.keys, t.order = nil, nil
	return n
}
