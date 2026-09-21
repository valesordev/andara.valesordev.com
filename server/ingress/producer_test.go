// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/command"
)

// The fate discriminator on its own: a failed promise is not written when
// no produce request was written since the record was enqueued, and
// outcome unknown when one was — whoever's request it was. A successful
// promise is landed at the record's offset.
func TestProducer_SettleClassifiesTheFate(t *testing.T) {
	k := &KafkaProducer{}
	rec := &kgo.Record{Partition: 7, Offset: 42}
	failed := errors.New("client closed")

	before := k.written.Load()
	u := newUnsettled(ErrDeadline)
	k.settle(u, rec, failed, before)
	if _, err := u.Outcome(); !errors.Is(err, ErrNotWritten) || !errors.Is(err, failed) {
		t.Fatalf("no write since enqueue: %v", err)
	}

	before = k.written.Load()
	k.written.Add(1) // an unrelated Session's produce request left the process
	u = newUnsettled(ErrDeadline)
	k.settle(u, rec, failed, before)
	if _, err := u.Outcome(); !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrNotWritten) {
		t.Fatalf("a write since enqueue: %v", err)
	}

	u = newUnsettled(ErrDeadline)
	k.settle(u, rec, nil, before)
	if acc, err := u.Outcome(); err != nil || acc != (command.Accepted{Partition: 7, Offset: 42}) {
		t.Fatalf("landed: %v %v", acc, err)
	}
	select {
	case <-u.Settled():
	default:
		t.Fatal("Settled not closed")
	}
	if !errors.Is(u, ErrDeadline) {
		t.Fatal("the surface error is not the cause")
	}
}
