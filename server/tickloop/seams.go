// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"sort"
	"sync"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// Source feeds the loop records from andara.commands.v1. The Kafka
// implementation buffers off the tick goroutine so a broker stall is not a
// tick stall; Poll never blocks.
type Source interface {
	// Poll returns up to max buffered records, taken round-robin across
	// Partitions in offset order so one busy Zone cannot starve another,
	// skipping the frozen Partitions. ErrStarved when the broker is
	// unreachable and nothing is buffered; ErrOffsetGap when the broker
	// delivered a Partition out of sequence.
	Poll(frozen []int32, max int) ([]sim.Record, error)
	// Requeue puts records back at the front of their Partitions' buffers,
	// in the order given: what Step did not apply.
	Requeue([]sim.Record)
	// Commit checkpoints next-to-read offsets, the TickCompleted convention.
	Commit(ctx context.Context, offsets map[int32]int64) error
	// Lag is the broker's end offset minus the loop's next-to-read, per
	// Partition, as last observed.
	Lag() map[int32]int64
	// Pending is how many records are buffered — the deferred backlog.
	Pending() int
	// Close releases the connection.
	Close() error
}

// Publisher carries what a tick produced out of the process. Events and
// TickCompleted go to andara.events.v1; cross-Zone Commands go to
// andara.commands.v1 on their target Partition.
type Publisher interface {
	Publish(ctx context.Context, events []sim.Event, completed sim.TickCompleted) error
	Produce(ctx context.Context, commands []*logv1.LoggedCommand) error
	Close() error
}

// Errors the seams return.
var (
	// ErrStarved: the broker is unreachable and nothing is buffered. The
	// tick continues applying nothing (AC-9).
	ErrStarved = errors.New("input starved: broker unreachable")
)

// MemorySource is a Source over in-process buffers: the loop's tests, and
// sim.source=memory, where the World ticks with no input.
type MemorySource struct {
	mu          sync.Mutex
	buffers     map[int32][]sim.Record
	next        map[int32]int64 // next offset to assign per partition
	committed   map[int32]int64
	end         map[int32]int64 // highest offset + 1 pushed per partition
	unavailable bool
	commits     int
}

// NewMemorySource returns an empty source.
func NewMemorySource() *MemorySource {
	return &MemorySource{buffers: map[int32][]sim.Record{}, next: map[int32]int64{}, committed: map[int32]int64{}, end: map[int32]int64{}}
}

// Push appends a Command to its Zone's Partition and returns its offset.
func (m *MemorySource) Push(cmd *logv1.LoggedCommand) sim.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
	r := sim.Record{Partition: p, Offset: m.next[p], Command: cmd}
	m.next[p]++
	m.end[p] = m.next[p]
	m.buffers[p] = append(m.buffers[p], r)
	return r
}

// SetUnavailable simulates the broker going away (AC-9).
func (m *MemorySource) SetUnavailable(v bool) {
	m.mu.Lock()
	m.unavailable = v
	m.mu.Unlock()
}

// Poll implements Source.
func (m *MemorySource) Poll(frozen []int32, max int) ([]sim.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := selectRoundRobin(m.buffers, frozen, max)
	if len(out) == 0 && m.unavailable {
		return nil, ErrStarved
	}
	return out, nil
}

// selectRoundRobin takes records from the buffers one Partition at a time
// in Partition order until max is reached, skipping frozen Partitions, and
// removes what it took. Pure over the buffers, so the deferral rule is one
// tested function.
func selectRoundRobin(buffers map[int32][]sim.Record, frozen []int32, max int) []sim.Record {
	skip := map[int32]bool{}
	for _, p := range frozen {
		skip[p] = true
	}
	parts := make([]int, 0, len(buffers))
	for p, recs := range buffers {
		if len(recs) > 0 && !skip[p] {
			parts = append(parts, int(p))
		}
	}
	sort.Ints(parts)
	taken := map[int32]int{}
	var out []sim.Record
	for len(out) < max && len(parts) > 0 {
		progress := false
		for _, pi := range parts {
			if len(out) >= max {
				break
			}
			p := int32(pi)
			if taken[p] < len(buffers[p]) {
				out = append(out, buffers[p][taken[p]])
				taken[p]++
				progress = true
			}
		}
		if !progress {
			break
		}
	}
	for p, n := range taken {
		buffers[p] = buffers[p][n:]
	}
	// Step wants each Partition's records contiguous and ascending; the
	// round-robin interleaved them.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Partition != out[j].Partition {
			return out[i].Partition < out[j].Partition
		}
		return out[i].Offset < out[j].Offset
	})
	return out
}

// Requeue implements Source.
func (m *MemorySource) Requeue(recs []sim.Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	requeue(m.buffers, recs)
}

func requeue(buffers map[int32][]sim.Record, recs []sim.Record) {
	byPart := map[int32][]sim.Record{}
	for _, r := range recs {
		byPart[r.Partition] = append(byPart[r.Partition], r)
	}
	for p, back := range byPart {
		buffers[p] = append(back, buffers[p]...)
	}
}

// Commit implements Source.
func (m *MemorySource) Commit(_ context.Context, offsets map[int32]int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unavailable {
		return ErrStarved
	}
	for p, o := range offsets {
		m.committed[p] = o
	}
	m.commits++
	return nil
}

// Committed returns the last committed offsets, for tests.
func (m *MemorySource) Committed() (map[int32]int64, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int32]int64, len(m.committed))
	for p, o := range m.committed {
		out[p] = o
	}
	return out, m.commits
}

// Lag implements Source: pushed but not yet handed out.
func (m *MemorySource) Lag() map[int32]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int32]int64{}
	for p, recs := range m.buffers {
		if len(recs) > 0 {
			out[p] = int64(len(recs))
		}
	}
	return out
}

// Pending implements Source.
func (m *MemorySource) Pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, recs := range m.buffers {
		n += len(recs)
	}
	return n
}

// Close implements Source.
func (m *MemorySource) Close() error { return nil }

// MemoryPublisher records what the loop published, for tests and for
// sim.source=memory.
type MemoryPublisher struct {
	mu        sync.Mutex
	Events    []sim.Event
	Completed []sim.TickCompleted
	Produced  []*logv1.LoggedCommand
	Fail      error
}

// Publish implements Publisher.
func (m *MemoryPublisher) Publish(_ context.Context, events []sim.Event, tc sim.TickCompleted) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	m.Events = append(m.Events, events...)
	m.Completed = append(m.Completed, tc)
	return nil
}

// Produce implements Publisher.
func (m *MemoryPublisher) Produce(_ context.Context, cmds []*logv1.LoggedCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fail != nil {
		return m.Fail
	}
	m.Produced = append(m.Produced, cmds...)
	return nil
}

// Close implements Publisher.
func (m *MemoryPublisher) Close() error { return nil }

// Snapshot returns copies of what was published.
func (m *MemoryPublisher) Snapshot() ([]sim.Event, []sim.TickCompleted, []*logv1.LoggedCommand) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sim.Event(nil), m.Events...), append([]sim.TickCompleted(nil), m.Completed...), append([]*logv1.LoggedCommand(nil), m.Produced...)
}
