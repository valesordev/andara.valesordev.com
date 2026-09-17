// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package sim

import (
	"errors"
	"fmt"
	"sort"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// Record is one Command as the log delivered it: which Partition, which
// offset, and the typed Command. The core is handed these and never learns
// where they came from (AC-13).
type Record struct {
	Partition int32
	Offset    int64
	Command   *logv1.LoggedCommand
}

// TickInput is what the loop hands Step: the records to apply this tick,
// ordered by offset within each Partition, and the Partitions they cover.
// Step derives the applied range from the records themselves; a Partition
// listed with no records advances nothing.
type TickInput struct {
	Records []Record
}

// TickCompleted is the Tick Boundary Record (ADR-0002 §4): the tick, the
// next-to-read offset on every owned Partition when it ended, and the State
// Hash it left. Replay reads these rather than re-deciding boundaries.
type TickCompleted struct {
	Tick            Tick
	Offsets         map[int32]int64
	StateHash       [32]byte
	StateVersion    uint32
	EventsEmitted   uint64
	CommandsApplied uint64
}

// Proto renders the record for andara.events.v1.
func (tc TickCompleted) Proto() *logv1.TickCompleted {
	out := &logv1.TickCompleted{
		Tick:            uint64(tc.Tick),
		StateHash:       tc.StateHash[:],
		StateVersion:    tc.StateVersion,
		EventsEmitted:   tc.EventsEmitted,
		CommandsApplied: tc.CommandsApplied,
	}
	parts := make([]int, 0, len(tc.Offsets))
	for p := range tc.Offsets {
		parts = append(parts, int(p))
	}
	sort.Ints(parts)
	for _, p := range parts {
		out.Offsets = append(out.Offsets, &logv1.PartitionOffset{Partition: int32(p), Offset: tc.Offsets[int32(p)]})
	}
	return out
}

// TickCompletedFromProto reads a record back.
func TickCompletedFromProto(p *logv1.TickCompleted) TickCompleted {
	tc := TickCompleted{
		Tick:            Tick(p.GetTick()),
		Offsets:         make(map[int32]int64, len(p.GetOffsets())),
		StateVersion:    p.GetStateVersion(),
		EventsEmitted:   p.GetEventsEmitted(),
		CommandsApplied: p.GetCommandsApplied(),
	}
	copy(tc.StateHash[:], p.GetStateHash())
	for _, o := range p.GetOffsets() {
		tc.Offsets[o.GetPartition()] = o.GetOffset()
	}
	return tc
}

// StepResult is what one tick produced.
type StepResult struct {
	Tick   Tick
	Events []Event
	// Outbound is every Command a handler produced for another Zone — the
	// cross-Zone effect of ADR-0001 §4, which resolves on a later tick on
	// the target's Partition, never as a call, including when this process
	// owns both Zones (AC-11).
	Outbound []*logv1.LoggedCommand
	// Unapplied is every record handed in that this tick did not apply: the
	// record whose apply panicked and everything after it on that
	// Partition. The loop puts them back at the front of the Partition's
	// buffer; the Partition is frozen until the process restarts (AC-12).
	Unapplied []Record
	// Faults is every Zone that panicked this tick, for the loop's error
	// line and counter.
	Faults    []Fault
	Completed TickCompleted
}

// CommandKind names the arm of LoggedCommand.command a handler serves.
type CommandKind string

// KindOf names a Command's arm, or "" for an empty oneof.
func KindOf(cmd *logv1.LoggedCommand) CommandKind {
	switch cmd.GetCommand().(type) {
	case *logv1.LoggedCommand_Look:
		return "look"
	case *logv1.LoggedCommand_Move:
		return "move"
	}
	return ""
}

// Apply is the handler seam: AW-SRV-003 registers one per verb. It runs
// inside the tick with the Zone's state and an Emitter, and must be a pure
// function of (state, command, RNG) — no clock, no I/O, no goroutines.
type Apply func(a *ApplyContext, cmd *logv1.LoggedCommand) error

// ApplyContext is what a handler may touch.
type ApplyContext struct {
	Tick      Tick
	World     *World
	Templates *TemplateRegistry
	Zone      *ZoneState
	State     *WorldState
	RNG       *RNG
	Record    Record
	emit      func(ZoneID, string, *gamev1.EventEnvelope)
	outbound  *[]*logv1.LoggedCommand
}

// Emit publishes an Event from this Zone, echoing the Command's client_ref.
func (a *ApplyContext) Emit(env *gamev1.EventEnvelope) {
	a.emit(a.Zone.ID, a.Record.Command.GetClientRef(), env)
}

// Produce sends a Command to another Zone's Partition. It is applied on a
// later tick, after the log has ordered it (ADR-0001 §4).
func (a *ApplyContext) Produce(cmd *logv1.LoggedCommand) {
	*a.outbound = append(*a.outbound, cmd)
}

// ZoneTimer lets the loop attribute tick time to Zones without the core
// reading a clock: Begin is called before a record is applied to a Zone and
// the returned func after. The loop's implementation observes the per-Zone
// histogram and span (ADR-0001's starving-Zone signal).
type ZoneTimer interface {
	Begin(zone ZoneID) (end func())
}

// Config configures an Engine.
type Config struct {
	Seed       uint64
	Partitions []int32
	// ZoneTimer is optional.
	ZoneTimer ZoneTimer
	// Handlers is the verb table's apply column. A Command with no handler
	// advances its offset and is rejected with unsupported_command: the log
	// only ever carries parsed, authorized Commands (AW-SRV-010), so this is
	// a binary behind its content, not a Builder's mistake.
	Handlers map[CommandKind]Apply
}

// Engine holds one World's mutable state and advances it one tick at a
// time. Step is a pure function of (state, input); the loop that schedules
// it and the consumer that feeds it live outside this package.
type Engine struct {
	world     *World
	templates *TemplateRegistry
	cfg       Config
	state     *WorldState
	sinks     []EventSink
}

// NewEngine builds an Engine at tick 0 with every Zone empty.
func NewEngine(w *World, templates *TemplateRegistry, cfg Config) *Engine {
	if cfg.Seed == 0 {
		cfg.Seed = DeriveSeed(w)
	}
	parts := append([]int32(nil), cfg.Partitions...)
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })
	cfg.Partitions = parts
	return &Engine{world: w, templates: templates, cfg: cfg, state: NewWorldState(w, cfg.Seed, parts)}
}

// SetZoneTimer attaches the per-Zone timer after construction; the loop
// that implements it is built around the engine.
func (e *Engine) SetZoneTimer(t ZoneTimer) { e.cfg.ZoneTimer = t }

// State exposes the mutable state, for snapshots and tests. Callers must
// not mutate it outside a handler.
func (e *Engine) State() *WorldState { return e.state }

// Tick is the current tick.
func (e *Engine) Tick() Tick { return e.state.Tick }

// StateHash is the State Hash of the current state.
func (e *Engine) StateHash() [32]byte { return e.state.Hash() }

// Subscribe attaches a sink. AW-SRV-004 owns the buffering and drop rules;
// here a sink is called in order and must return promptly.
func (e *Engine) Subscribe(s EventSink) { e.sinks = append(e.sinks, s) }

// FaultedPartitions is every Partition frozen by a Zone fault, sorted. The
// loop must not hand Step a record on one of them.
func (e *Engine) FaultedPartitions() []int32 {
	seen := map[int32]bool{}
	for _, z := range e.state.Zones {
		if z.Faulted {
			seen[PartitionFor(z.ID)] = true
		}
	}
	out := make([]int32, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Errors Step and Replay return.
var (
	// ErrZoneFaulted: a record targets a Zone quarantined by a panic.
	ErrZoneFaulted = errors.New("zone faulted")
	// ErrOffsetGap: a record's offset is not the next one expected on its
	// Partition. Applying it would skip history silently; refuse instead.
	ErrOffsetGap = errors.New("offset gap")
	// ErrUnownedPartition: a record on a Partition this process is not
	// assigned.
	ErrUnownedPartition = errors.New("partition not assigned to this process")
	// ErrBoundaryGap: the recorded boundaries skip a tick, so the batching
	// decision for that tick is lost and exact replay is impossible.
	ErrBoundaryGap = errors.New("tick boundary gap")
	// ErrHashMismatch: replay reached a boundary with a different State
	// Hash than the record says (ADR-0002 §4). The World is not the one that
	// was running; halt rather than serve it.
	ErrHashMismatch = errors.New("state hash mismatch")
)

// Step advances exactly one tick over in and returns what it produced. It
// validates the input first — offsets contiguous with the state, Partitions
// owned, none faulted — and applies nothing if that fails, so a refused
// Step leaves state and hash untouched.
func (e *Engine) Step(in TickInput) (StepResult, error) {
	s := e.state
	tick := s.Tick + 1

	// Validate: records grouped by Partition, ascending and contiguous from
	// the next expected offset.
	byPart := map[int32][]Record{}
	for _, r := range in.Records {
		byPart[r.Partition] = append(byPart[r.Partition], r)
	}
	parts := make([]int32, 0, len(byPart))
	for p := range byPart {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })
	faulted := map[int32]bool{}
	for _, p := range e.FaultedPartitions() {
		faulted[p] = true
	}
	for _, p := range parts {
		next, owned := s.Offsets[p]
		if !owned {
			return StepResult{}, fmt.Errorf("%w: partition %d", ErrUnownedPartition, p)
		}
		if faulted[p] {
			return StepResult{}, fmt.Errorf("%w: partition %d is frozen", ErrZoneFaulted, p)
		}
		for _, r := range byPart[p] {
			if r.Offset != next {
				return StepResult{}, fmt.Errorf("%w: partition %d expected offset %d, got %d", ErrOffsetGap, p, next, r.Offset)
			}
			next++
		}
	}

	res := StepResult{Tick: tick}
	emit := func(zone ZoneID, clientRef string, env *gamev1.EventEnvelope) {
		env.EventId = s.NextEventID
		env.Tick = uint64(tick)
		env.ClientRef = clientRef
		s.NextEventID++
		res.Events = append(res.Events, Event{ID: env.EventId, Tick: tick, Zone: zone, Type: typeOf(env), Envelope: env})
	}

	for _, p := range parts {
		records := byPart[p]
		for i, r := range records {
			zone := s.Zones[ZoneID(r.Command.GetZoneId())]
			if zone == nil {
				// A Zone the World does not have. Content moved under the
				// log (AW-SRV-012 owns that transition); the record is
				// consumed and rejected so the Partition keeps moving.
				emit("", r.Command.GetClientRef(), rejected("unknown_zone", "that place is not in this world"))
				res.Completed.CommandsApplied++
				s.Offsets[p] = r.Offset + 1
				continue
			}
			if PartitionFor(zone.ID) != p {
				emit(zone.ID, r.Command.GetClientRef(), rejected("misrouted", "the command reached the wrong partition"))
				res.Completed.CommandsApplied++
				s.Offsets[p] = r.Offset + 1
				continue
			}
			if !e.applyOne(tick, zone, r, emit, &res) {
				// The Zone faulted on this record. It and everything after
				// it on this Partition go back to the loop; the offset stops
				// here (AC-12).
				res.Unapplied = append(res.Unapplied, records[i:]...)
				break
			}
			res.Completed.CommandsApplied++
			s.Offsets[p] = r.Offset + 1
		}
	}

	s.Tick = tick
	res.Completed.Tick = tick
	res.Completed.StateVersion = s.Version
	res.Completed.EventsEmitted = uint64(len(res.Events))
	res.Completed.Offsets = make(map[int32]int64, len(s.Offsets))
	for p, o := range s.Offsets {
		res.Completed.Offsets[p] = o
	}
	res.Completed.StateHash = s.Hash()
	for _, ev := range res.Events {
		for _, sink := range e.sinks {
			sink.Publish(ev)
		}
	}
	return res, nil
}

// applyOne dispatches one record to its handler inside a recover. It
// returns false if the Zone faulted.
func (e *Engine) applyOne(tick Tick, zone *ZoneState, r Record, emit func(ZoneID, string, *gamev1.EventEnvelope), res *StepResult) (ok bool) {
	kind := KindOf(r.Command)
	handler, known := e.cfg.Handlers[kind]
	if !known {
		emit(zone.ID, r.Command.GetClientRef(), rejected("unsupported_command", "this server cannot act on that yet"))
		return true
	}
	defer func() {
		if p := recover(); p != nil {
			zone.Faulted = true
			zone.FaultedTick = tick
			emit(zone.ID, "", &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_ZoneFaulted{ZoneFaulted: &gamev1.ZoneFaulted{ZoneId: string(zone.ID)}}})
			res.Faults = append(res.Faults, Fault{Zone: zone.ID, Tick: tick, Partition: r.Partition, Offset: r.Offset, Panic: fmt.Sprint(p)})
			ok = false
		}
	}()
	actx := &ApplyContext{
		Tick: tick, World: e.world, Templates: e.templates, Zone: zone, State: e.state, RNG: e.state.RNG, Record: r,
		emit: emit, outbound: &res.Outbound,
	}
	if e.cfg.ZoneTimer != nil {
		defer e.cfg.ZoneTimer.Begin(zone.ID)()
	}
	if err := handler(actx, r.Command); err != nil {
		// A handler's error is a rejection the player sees, never a fault:
		// the Command was legitimately ordered and turned out to be illegal
		// (AW-SRV-003 AC-8).
		code, msg := "rejected", err.Error()
		var re *RejectError
		if errors.As(err, &re) {
			code, msg = re.Code, re.Message
		}
		emit(zone.ID, r.Command.GetClientRef(), rejected(code, msg))
	}
	return true
}

// Fault records a Zone fault for the loop's log line and counter.
type Fault struct {
	Zone      ZoneID
	Tick      Tick
	Partition int32
	Offset    int64
	Panic     string
}

// RejectError is how a handler rejects a Command with a stable code and a
// player-safe message (AW-SRV-003's taxonomy).
type RejectError struct {
	Code    string
	Message string
}

func (e *RejectError) Error() string { return e.Code + ": " + e.Message }

func rejected(code, msg string) *gamev1.EventEnvelope {
	return &gamev1.EventEnvelope{Payload: &gamev1.EventEnvelope_CommandRejected{CommandRejected: &gamev1.CommandRejected{Code: code, Message: msg}}}
}

// Stop emits SimulationStopped through the sinks. It is a lifecycle
// notification, not World history: it carries event_id 0, consumes no ID,
// does not advance the tick, and does not move the hash — a recovered
// process replays to the last boundary and the next real Event takes the
// ID it would have taken had nothing stopped.
func (e *Engine) Stop(reason string) Event {
	env := &gamev1.EventEnvelope{
		EventId: 0, Tick: uint64(e.state.Tick),
		Payload: &gamev1.EventEnvelope_SimulationStopped{SimulationStopped: &gamev1.SimulationStopped{Reason: reason}},
	}
	ev := Event{ID: 0, Tick: e.state.Tick, Type: EvSimulationStopped, Envelope: env}
	for _, s := range e.sinks {
		s.Publish(ev)
	}
	return ev
}

// RecordSource is what Replay reads records from: the range [from, to) on
// one Partition, in order. The Kafka implementation lives outside the core.
type RecordSource interface {
	Fetch(partition int32, from, to int64) ([]Record, error)
}

// Replay drives Step from recorded boundaries rather than deriving them
// (ADR-0002 §4): for each TickCompleted in order, the records between the
// current offsets and the recorded ones are fetched and applied, and the
// resulting hash must equal the recorded one. A mismatch is ErrHashMismatch
// naming the tick — the World is not the one that was running.
func (e *Engine) Replay(boundaries []TickCompleted, src RecordSource) error {
	for _, b := range boundaries {
		if b.Tick != e.state.Tick+1 {
			// A missing boundary is a tick whose batching decision was
			// lost — usually a boundary the process could not publish
			// before it died. Exact replay past it is impossible, and an
			// approximate one is a World nobody was in (ADR-0002 §4).
			// Recovery policy for this case is AW-SRV-007's; until then
			// the operator's path is a snapshot newer than the gap, or an
			// empty log.
			return fmt.Errorf("%w: boundary for tick %d follows tick %d; ticks %d..%d have no Tick Boundary Record and cannot be replayed exactly",
				ErrBoundaryGap, b.Tick, e.state.Tick, e.state.Tick+1, b.Tick-1)
		}
		if b.StateVersion != e.state.Version {
			return fmt.Errorf("replay: boundary for tick %d has state_version %d, this binary reads %d", b.Tick, b.StateVersion, e.state.Version)
		}
		var in TickInput
		parts := make([]int, 0, len(b.Offsets))
		for p := range b.Offsets {
			parts = append(parts, int(p))
		}
		sort.Ints(parts)
		for _, pi := range parts {
			p := int32(pi)
			from, owned := e.state.Offsets[p]
			if !owned {
				return fmt.Errorf("replay: %w: partition %d in boundary for tick %d", ErrUnownedPartition, p, b.Tick)
			}
			to := b.Offsets[p]
			if to < from {
				return fmt.Errorf("replay: boundary for tick %d moves partition %d backwards (%d < %d)", b.Tick, p, to, from)
			}
			if to == from {
				continue
			}
			recs, err := src.Fetch(p, from, to)
			if err != nil {
				return fmt.Errorf("replay: fetch partition %d [%d,%d): %w", p, from, to, err)
			}
			if int64(len(recs)) != to-from {
				return fmt.Errorf("replay: %w: partition %d [%d,%d) yielded %d records", ErrOffsetGap, p, from, to, len(recs))
			}
			in.Records = append(in.Records, recs...)
		}
		res, err := e.Step(in)
		if err != nil {
			return fmt.Errorf("replay: tick %d: %w", b.Tick, err)
		}
		if res.Completed.StateHash != b.StateHash {
			return fmt.Errorf("%w at tick %d: recorded %x, replayed %x", ErrHashMismatch, b.Tick, b.StateHash[:8], res.Completed.StateHash[:8])
		}
	}
	return nil
}
