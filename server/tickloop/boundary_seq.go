// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// pendingBoundary is one Tick Boundary Record on its way to the broker.
type pendingBoundary struct {
	tick      sim.Tick
	body      []byte
	published time.Time // when Publish took it, for the publish-lag gauge
	handed    time.Time // when it was handed to the producer
}

// boundarySeq orders Tick Boundary Records onto the broker so that the topic
// never holds a gap (AW-SRV-026 AC-2). A boundary is handed to the producer
// only once every boundary before it has been acknowledged; the ones Publish
// takes meanwhile are held, and handed over together, in order, when the last
// outstanding one is acknowledged. A loss drops what was held, and nothing is
// handed over again.
//
// Pipelining the boundaries instead is not safe with franz-go: a record that
// times out fails every record buffered behind it on its Partition, but the
// promises reporting it run on a separate goroutine after the buffer is
// emptied, so a boundary produced in between lands in a fresh buffer and can
// be delivered — N-1, [gap], N+1 on the topic, and a World that won't boot
// (pre-PR review of AW-SRV-026). Holding costs a boundary at most one
// acknowledgement round trip, which the tick never waits on.
type boundarySeq struct {
	// produce hands one boundary to the producer; its fate comes back
	// through resolve, on any goroutine. Never called with mu held, so a
	// promise that runs inline cannot deadlock.
	produce func(pendingBoundary)
	// onAcked and onLost report each boundary's fate: every acknowledgement,
	// and the first loss only. Called with mu held, so in order.
	onAcked func(pendingBoundary)
	onLost  func(sim.Tick, error)
	// room, if set, reports whether the producer's buffer can take n more
	// records now. A batch it can't take is lost before any of it is
	// handed over: a full buffer refuses a record outright, so the next one
	// of the batch could otherwise be taken behind the refused one.
	room func(n int) bool
	// timeout, if set, is the delivery timeout: a boundary handed over and
	// unanswered for longer is lost. franz-go fails a record on its own
	// timeout only if it never sent it, or sent it and got an answer; one in
	// flight when the broker went away is retried until the broker returns
	// (measured against a stopped Redpanda, 2026-10-02).
	// Declaring it lost here is what makes an outage longer than the timeout
	// a loss (AW-SRV-026), and it stays gapless: nothing behind it is ever
	// handed over, so if its write did land, the topic ends one boundary
	// later and recovery replays to that one. now is the clock; nil is
	// time.Now.
	timeout time.Duration
	now     func() time.Time

	// stopped mirrors lost for the hand-over loop, which runs unlocked.
	stopped atomic.Bool

	mu          sync.Mutex
	outstanding []pendingBoundary // handed over, not yet resolved, in tick order
	held        []pendingBoundary
	lost        bool
	lostTick    sim.Tick
}

// publish takes a boundary: handed over now if nothing is outstanding, held
// otherwise. After a loss it is refused with ErrBoundaryLost.
func (s *boundarySeq) publish(b pendingBoundary) error {
	s.mu.Lock()
	if !s.lost && s.timeout > 0 && len(s.outstanding) > 0 {
		if waited := s.clock().Sub(s.outstanding[0].handed); waited > s.timeout {
			s.loseLocked(s.outstanding[0].tick, fmt.Errorf("%w: unanswered for %s", errUnanswered, waited.Round(time.Millisecond)))
		}
	}
	if s.lost {
		at := s.lostTick
		s.mu.Unlock()
		return fmt.Errorf("%w (since tick %d)", ErrBoundaryLost, at)
	}
	if len(s.outstanding) > 0 || len(s.held) > 0 {
		s.held = append(s.held, b)
		s.mu.Unlock()
		return nil
	}
	b.handed = s.clock()
	s.outstanding = append(s.outstanding, b)
	s.mu.Unlock()
	s.handOver([]pendingBoundary{b})
	return nil
}

// errNoRoom is the loss of a batch the producer's buffer could not take, and
// errUnanswered of a boundary the broker never answered within the delivery
// timeout.
var (
	errNoRoom     = errors.New("the producer's buffer is full")
	errUnanswered = errors.New("tick boundary not acknowledged within the delivery timeout")
)

func (s *boundarySeq) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// loseLocked records the first loss and reports it. Called with mu held.
func (s *boundarySeq) loseLocked(tick sim.Tick, err error) {
	s.lost, s.lostTick, s.held = true, tick, nil
	s.stopped.Store(true)
	if s.onLost != nil {
		s.onLost(tick, err)
	}
}

// handOver gives a batch to the producer, in order, stopping at a loss.
func (s *boundarySeq) handOver(batch []pendingBoundary) {
	if s.room != nil && !s.room(len(batch)) {
		s.resolve(batch[0].tick, errNoRoom)
		return
	}
	for _, b := range batch {
		if s.stopped.Load() {
			return
		}
		s.produce(b)
	}
}

// resolve records the fate of a boundary that was handed over: nil is its
// acknowledgement. When it was the last outstanding one, what was held is
// handed over.
func (s *boundarySeq) resolve(tick sim.Tick, err error) {
	s.mu.Lock()
	var b pendingBoundary
	for i := range s.outstanding {
		if s.outstanding[i].tick == tick {
			b = s.outstanding[i]
			s.outstanding = append(s.outstanding[:i], s.outstanding[i+1:]...)
			break
		}
	}
	if s.lost {
		// Failed behind the first loss, or a late answer: already reported.
		s.mu.Unlock()
		return
	}
	if err != nil {
		s.loseLocked(tick, err)
		s.mu.Unlock()
		return
	}
	if s.onAcked != nil {
		s.onAcked(b)
	}
	if len(s.outstanding) > 0 || len(s.held) == 0 {
		s.mu.Unlock()
		return
	}
	next := s.held
	handed := s.clock()
	for i := range next {
		next[i].handed = handed
	}
	s.outstanding, s.held = append(s.outstanding, next...), nil
	s.mu.Unlock()
	s.handOver(next)
}

// lostAt reports whether a boundary was lost, and which.
func (s *boundarySeq) lostAt() (bool, sim.Tick) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lost, s.lostTick
}

// WireBoundaries routes the Kafka publisher's report of each boundary's fate
// to the loop and the snapshot runner, as the server does (AW-SRV-026): an
// acknowledgement releases the checkpoints and the snapshot round waiting on
// it, and a loss stops the loop into exact recovery. lag, if set, receives
// each acknowledgement's delay after Publish. snap may be nil.
func WireBoundaries(kp *KafkaPublisher, loop *Loop, snap *Snapshotter, lag func(time.Duration)) {
	kp.OnBoundaryLost = func(tick sim.Tick, err error) {
		snap.OnBoundaryLost(tick, err)
		loop.Metrics().PublishFailures.WithLabelValues("boundary").Inc()
		loop.BoundaryLost(tick, err)
	}
	kp.OnBoundaryAcked = func(tick sim.Tick, d time.Duration) {
		// A snapshot round waits on this (AW-SRV-006 AC-8), and so does the
		// checkpoint of its offsets.
		snap.OnBoundaryAcked(tick)
		loop.BoundaryAcked(tick)
		if lag != nil {
			lag(d)
		}
	}
}
