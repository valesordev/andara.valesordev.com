// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// seqHarness records what boundarySeq hands the producer, and resolves each
// boundary's fate when the test says, as franz-go's promise goroutine does.
type seqHarness struct {
	mu       sync.Mutex
	produced []sim.Tick
	lost     []sim.Tick
	acked    []sim.Tick
	seq      *boundarySeq
}

func newSeqHarness() *seqHarness {
	h := &seqHarness{}
	h.seq = &boundarySeq{
		produce: func(b pendingBoundary) {
			h.mu.Lock()
			h.produced = append(h.produced, b.tick)
			h.mu.Unlock()
		},
		onLost:  func(t sim.Tick, _ error) { h.lost = append(h.lost, t) },
		onAcked: func(b pendingBoundary) { h.acked = append(h.acked, b.tick) },
	}
	return h
}

func (h *seqHarness) publish(t sim.Tick) error {
	return h.seq.publish(pendingBoundary{tick: t})
}

func (h *seqHarness) producedTicks() []sim.Tick {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.produced)
}

// AW-SRV-026 AC-2, the review of #352's race: franz-go reports a failed
// boundary on its promise goroutine after it has already emptied the
// Partition's buffer, so a boundary handed to it in between is written to a
// fresh buffer and delivered — the topic then reads N-1, [gap], N+1 and
// recovery refuses it. A boundary is therefore handed over only once every
// earlier one is acknowledged, and a loss drops what was held.
func TestBoundarySeq_NoBoundaryAfterAnUnresolvedOne(t *testing.T) {
	h := newSeqHarness()
	for tick := sim.Tick(1); tick <= 3; tick++ {
		if err := h.publish(tick); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1}) {
		t.Fatalf("produced %v while tick 1 was unresolved, want [1]", got)
	}
	h.seq.resolve(1, nil)
	// 2 and 3 go together, in order.
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1, 2, 3}) {
		t.Fatalf("after tick 1's ack: produced %v", got)
	}
	_ = h.publish(4) // 2 and 3 unresolved: held
	h.seq.resolve(2, nil)
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1, 2, 3}) {
		t.Fatalf("tick 4 handed over while tick 3 was unresolved: %v", got)
	}
	// 3 is lost: 4, held, is never handed over, and later publishes refuse.
	h.seq.resolve(3, errors.New("timed out"))
	if err := h.publish(5); !errors.Is(err, ErrBoundaryLost) {
		t.Fatalf("publish after the loss: %v", err)
	}
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1, 2, 3}) {
		t.Fatalf("produced %v after the loss of 3, want [1 2 3]", got)
	}
	if !slices.Equal(h.lost, []sim.Tick{3}) || !slices.Equal(h.acked, []sim.Tick{1, 2}) {
		t.Fatalf("lost %v acked %v", h.lost, h.acked)
	}
}

// The loss is reported once, for the first boundary that failed; the ones
// franz-go fails behind it on the same Partition are not reported again.
func TestBoundarySeq_LossReportedOnce(t *testing.T) {
	h := newSeqHarness()
	_ = h.publish(1)
	h.seq.resolve(1, nil)
	_ = h.publish(2)
	_ = h.publish(3)
	h.seq.resolve(2, nil)
	_ = h.publish(4)
	_ = h.publish(5)
	h.seq.resolve(3, nil)
	// 4 and 5 were handed over together; both fail.
	h.seq.resolve(4, errors.New("timed out"))
	h.seq.resolve(5, errors.New("timed out"))
	if !slices.Equal(h.lost, []sim.Tick{4}) {
		t.Fatalf("lost reported %v, want [4]", h.lost)
	}
	if lost, at := h.seq.lostAt(); !lost || at != 4 {
		t.Fatalf("lostAt %v %d", lost, at)
	}
}

// Under -race: resolutions on another goroutine while the loop publishes
// keep the topic order and never hand over a boundary past an unresolved one.
func TestBoundarySeq_ConcurrentResolve(t *testing.T) {
	h := newSeqHarness()
	done := make(chan struct{})
	go func() {
		defer close(done)
		next := sim.Tick(1)
		for next <= 200 {
			got := h.producedTicks()
			if sim.Tick(len(got)) >= next {
				h.seq.resolve(next, nil)
				next++
			}
		}
	}()
	for tick := sim.Tick(1); tick <= 200; tick++ {
		if err := h.publish(tick); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	got := h.producedTicks()
	for i, tick := range got {
		if tick != sim.Tick(i+1) {
			t.Fatalf("produced out of order: %v", got)
		}
	}
	if len(got) != 200 {
		t.Fatalf("produced %d of 200", len(got))
	}
}

// A batch the producer's buffer can't take is lost whole, before any of it
// is handed over, so no boundary is taken behind a refused one.
func TestBoundarySeq_NoRoomLosesTheBatch(t *testing.T) {
	h := newSeqHarness()
	full := false
	h.seq.room = func(int) bool { return !full }
	_ = h.publish(1)
	_ = h.publish(2)
	_ = h.publish(3)
	full = true
	h.seq.resolve(1, nil)
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1}) {
		t.Fatalf("produced %v into a full buffer", got)
	}
	if !slices.Equal(h.lost, []sim.Tick{2}) {
		t.Fatalf("lost %v, want [2]", h.lost)
	}
}

// A loss reported while a batch is being handed over stops the rest of it.
func TestBoundarySeq_LossMidHandOverStopsTheBatch(t *testing.T) {
	h := newSeqHarness()
	h.seq.produce = func(b pendingBoundary) {
		h.mu.Lock()
		h.produced = append(h.produced, b.tick)
		h.mu.Unlock()
		if b.tick == 2 {
			// Refused on the producer's goroutine as soon as it is taken.
			h.seq.resolve(2, errors.New("refused"))
		}
	}
	_ = h.publish(1)
	_ = h.publish(2)
	_ = h.publish(3)
	h.seq.resolve(1, nil)
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1, 2}) {
		t.Fatalf("produced %v, want [1 2]: 3 was handed over behind a lost 2", got)
	}
}

// The server's wiring (boot.StartTickLoop calls WireBoundaries): an
// acknowledgement reaches the loop's checkpoint gate and the lag gauge, and a
// loss stops the loop and counts a boundary publish failure.
func TestWireBoundaries(t *testing.T) {
	h := newHarness(t, nil)
	kp := &KafkaPublisher{}
	var lag time.Duration
	WireBoundaries(kp, h.loop, nil, func(d time.Duration) { lag = d })

	kp.OnBoundaryAcked(4, 7*time.Millisecond)
	if got := h.loop.acked.Load(); got != 4 || lag != 7*time.Millisecond {
		t.Fatalf("after the ack: loop acked %d, lag %v", got, lag)
	}
	kp.OnBoundaryLost(5, errBrokerGone)
	lost := h.loop.lost.Load()
	if lost == nil || lost.Lost != 5 || !errors.Is(lost.Err, errBrokerGone) {
		t.Fatalf("after the loss: %+v", lost)
	}
	if got := counter(h.loop.Metrics().PublishFailures.WithLabelValues("boundary")); got != 1 {
		t.Fatalf("publish failures{boundary} %v", got)
	}
}

// A boundary handed over and unanswered past the delivery timeout is lost,
// whether or not franz-go ever fails it: a request it sent and got no answer
// for is retried until the broker returns, so a stopped broker would
// otherwise never be a loss (measured, AW-SRV-026). The check runs on the
// next publish, and what was held behind it is never handed over.
func TestBoundarySeq_UnansweredPastTheTimeoutIsLost(t *testing.T) {
	h := newSeqHarness()
	now := time.Unix(1759400000, 0)
	h.seq.timeout = time.Minute
	h.seq.now = func() time.Time { return now }
	_ = h.publish(1)
	h.seq.resolve(1, nil)
	_ = h.publish(2) // handed over, never answered
	now = now.Add(59 * time.Second)
	if err := h.publish(3); err != nil {
		t.Fatalf("at 59 s: %v", err)
	}
	now = now.Add(2 * time.Second)
	if err := h.publish(4); !errors.Is(err, ErrBoundaryLost) {
		t.Fatalf("at 61 s: %v, want ErrBoundaryLost", err)
	}
	if !slices.Equal(h.lost, []sim.Tick{2}) {
		t.Fatalf("lost %v, want [2]", h.lost)
	}
	// The broker answers late: neither the ack nor the held 3 goes anywhere.
	h.seq.resolve(2, nil)
	if got := h.producedTicks(); !slices.Equal(got, []sim.Tick{1, 2}) || !slices.Equal(h.acked, []sim.Tick{1}) {
		t.Fatalf("after the late answer: produced %v acked %v", got, h.acked)
	}
}
