// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package roster is the Gateway's side of AW-SRV-014: an Account's
// Characters, and the binding of one to a Session — the step between an
// authenticated Session and one that is in the World.
//
// Identity is Account state (auth.Store); the body is World state (the
// sim, reached only through the log). What lives here is the seam: the
// one-live rule as Session state in process memory (correct under
// ADR-0001's single process; the sharding story moves it into the log),
// the routing table entry made before the BindCharacter is produced so
// the arrival is routed to the Session, and the teardown that produces the
// UnbindCharacter when the Session ends, whichever way it ends.
package roster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/ingress"
	"github.com/valesordev/andara/server/sim"
)

// Accounts is the roster's view of the account store: what auth.Store
// implements. Every write is under its single-writer lock.
type Accounts interface {
	MaxCharacters() int
	Characters(accountID string) []*accountsv1.CharacterRef
	Character(accountID, characterID string) (*accountsv1.CharacterRef, error)
	CreateCharacter(ctx context.Context, accountID, name, zone, room string) (*accountsv1.CharacterRef, error)
	SetCharacterPosition(ctx context.Context, accountID, characterID, zone, room string) error
}

// Options configures a Roster.
type Options struct {
	Accounts Accounts
	// Bindings is the routing table (AW-SRV-010): Bind before the
	// produce, so the arrival is routed to the Session.
	Bindings *ingress.Bindings
	// Log is the Command log; the same producer Submit uses.
	Log command.Producer
	// SpawnRoom is character.spawn_room: where a never-bound Character
	// is placed. The boot has resolved it against the loaded content.
	SpawnRoom sim.RoomRef
	// ProduceDeadline bounds the teardown's UnbindCharacter produce, which
	// runs on its own context: ingress.produce_deadline. Zero means 2s.
	ProduceDeadline time.Duration
	// PositionWrites bounds the roster-position writes in flight behind
	// ObserveMove. Zero means 32; a move observed with none free is
	// dropped, which costs a re-route at worst (see ObserveMove).
	PositionWrites int
	// Linkdead is what a linkdead teardown marks the body with, in Ticks
	// (config.LinkdeadTicks), and how long the roster holds a linkdead flag
	// at most whatever the sim says (session.linkdead_max). Zero Ticks make
	// every lost stream a quit, as AW-SRV-014 had it.
	Linkdead LinkdeadTicks

	// TickRate is sim.tick_rate, for linkdead durations in seconds. Zero
	// means 10.
	TickRate int

	Metrics *Metrics
	Logger  *slog.Logger
	Tracer  trace.Tracer
	Now     func() time.Time
}

// LinkdeadTicks is session.linkdead_* as MarkLinkdead carries it.
type LinkdeadTicks struct {
	Grace, Extension, Max uint64
}

// Roster implements gateway.Roster.
type Roster struct {
	opts    Options
	metrics *Metrics
	log     *slog.Logger
	tracer  trace.Tracer
	now     func() time.Time

	mu        sync.Mutex
	byAccount map[string]*live
	bySession map[string]*live
	// releases counts the work the roster started in the background — the
	// teardown produces and the position writes — so a drain or a test can
	// wait for it. writes bounds the position writes in flight.
	releases sync.WaitGroup
	writes   chan struct{}
	// linkdeadBodies is every body the sim has marked linkdead and not yet
	// reconnected or despawned, with whether combat extended it: the
	// andara_sessions_linkdead gauge's source. Loop goroutine only.
	linkdeadBodies map[sim.EntityID]bool
	// recovered is the linkdead bodies recovery left (SeedLinkdead): no
	// Session survives a restart, so their lines name none. Loop goroutine
	// only.
	recovered map[sim.EntityID]bool
}

// live is one Account's live flag: which Session drives which Character.
// It is set tentatively before the BindCharacter is produced — the one
// window in which a second SelectCharacter loses the race rather than
// finds the Character live — and cleared by the Session's teardown once
// the UnbindCharacter has been produced, or by a failed produce.
type live struct {
	account   string
	session   string
	character string
	name      string
	zone      sim.ZoneID
	confirmed bool // the BindCharacter is in the log
	bound     bool // counted on andara_sessions_bound
	releasing bool // the teardown has begun; the flag frees when it ends
	// released is closed when the teardown has finished: the produce done,
	// and the flag freed (quit) or handed to the linkdead body (linkdead).
	released chan struct{}
	// linkdead: the Session is gone and the body is waiting out its grace
	// (AW-SRV-015). The flag stays held — another Character of the Account
	// is already_live (AC-16) — until a SelectCharacter of this one
	// reconnects it, or the body's despawn frees it (ObserveLinkdead).
	linkdead bool
	end      gateway.SessionEnd
	// reconnectOf is the linkdead hold a reconnect took over, kept until its
	// BindCharacter is in the log: a produce that fails puts it back, since
	// the body is still linkdead (review of #114). ended records that the
	// body's grace ended meanwhile, and there is nothing to put back.
	reconnectOf *live
	ended       bool
}

// New builds a Roster.
func New(o Options) (*Roster, error) {
	if o.Accounts == nil || o.Bindings == nil || o.Log == nil {
		return nil, errors.New("roster: accounts, bindings, and a log are required")
	}
	if o.SpawnRoom.Zone == "" || o.SpawnRoom.Room == "" {
		return nil, errors.New("roster: character.spawn_room is required")
	}
	if o.ProduceDeadline <= 0 {
		o.ProduceDeadline = 2 * time.Second
	}
	if o.PositionWrites <= 0 {
		o.PositionWrites = 32
	}
	if o.TickRate <= 0 {
		o.TickRate = 10
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}
	if o.Metrics == nil {
		o.Metrics = NewMetrics(nil)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Roster{
		opts: o, metrics: o.Metrics, log: o.Logger, tracer: o.Tracer, now: o.Now,
		byAccount: map[string]*live{}, bySession: map[string]*live{},
		writes:         make(chan struct{}, o.PositionWrites),
		linkdeadBodies: map[sim.EntityID]bool{},
		recovered:      map[sim.EntityID]bool{},
	}, nil
}

// Metrics returns the roster metrics.
func (r *Roster) Metrics() *Metrics { return r.metrics }

// Wait blocks until the background work the roster started — teardown
// produces, position writes — has finished. The drain calls it before the
// producer closes; tests call it to observe.
func (r *Roster) Wait() { r.releases.Wait() }

// ObserveMove is called by the routing table when a bound Session's
// Character settles in a Zone other than the one it was in — the Gateway
// already watches those arrivals to route the Session's Commands
// (AW-SRV-010), and writing the roster's position here shrinks the window
// in which a crash leaves the roster naming a Zone the body has left
// (review of PR #43). Without it the next SelectCharacter is re-routed by
// the sim, which is correct but costs a tick; with it that is a crash
// inside the arrival-to-write window only.
//
// It runs on the tick goroutine and must not block: the write happens on
// its own goroutine, bounded by PositionWrites, and a move observed with
// none free is dropped — the re-route covers it. Same-Zone Room changes
// are not written: they are every step a player takes, and the sim's
// position is authoritative for all of them.
//
// Best-effort and unordered, in both directions: nothing sequences one of
// these writes against another, or against the teardown's, so a straggler
// can leave the roster naming an older Zone than the one the Session
// ended in. That is the same stale roster the re-route handles, and
// spawn_room_id is ignored for a body that exists — but it means nothing
// may be built on the roster's position being current.
func (r *Roster) ObserveMove(sessionID string, b command.Binding) {
	r.mu.Lock()
	l, ok := r.bySession[sessionID]
	write := ok && l.confirmed && !l.releasing && b.Zone != "" && b.Room != ""
	r.mu.Unlock()
	if !write {
		return
	}
	select {
	case r.writes <- struct{}{}:
	default:
		r.log.LogAttrs(context.Background(), slog.LevelDebug, "character position not written: too many writes in flight",
			slog.String("character_id", l.character), slog.String("session_id", sessionID))
		return
	}
	r.releases.Add(1)
	go func() {
		defer func() { <-r.writes; r.releases.Done() }()
		ctx, cancel := context.WithTimeout(context.Background(), r.opts.ProduceDeadline)
		defer cancel()
		if err := r.opts.Accounts.SetCharacterPosition(ctx, l.account, l.character, string(b.Zone), string(b.Room)); err != nil {
			r.log.LogAttrs(ctx, slog.LevelWarn, "character position not recorded",
				slog.String("account_id", l.account), slog.String("character_id", l.character),
				slog.String("session_id", sessionID), slog.String("detail", err.Error()))
		}
	}()
}

// Live reports which Character an Account drives right now, and on which
// Session, or false. For tests and the readiness of AW-SRV-015.
func (r *Roster) Live(accountID string) (sessionID, characterID string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.byAccount[accountID]
	if !ok {
		return "", "", false
	}
	return l.session, l.character, true
}

// ListCharacters implements gateway.Roster: the Account's ACTIVE
// Characters, sorted by character_id, with the live one flagged.
func (r *Roster) ListCharacters(ctx context.Context, s *gateway.Session) (*gamev1.ListCharactersResponse, error) {
	acct := s.Principal.EffectiveAccountID()
	_, liveID, _ := r.Live(acct)
	out := &gamev1.ListCharactersResponse{MaxPerAccount: uint32(r.opts.Accounts.MaxCharacters())}
	for _, c := range r.opts.Accounts.Characters(acct) {
		out.Characters = append(out.Characters, summary(c, c.GetCharacterId() == liveID))
	}
	return out, nil
}

// CreateCharacter implements gateway.Roster: a new Character on the
// Account's roster, its body to be spawned at character.spawn_room the
// first time it is selected.
func (r *Roster) CreateCharacter(ctx context.Context, s *gateway.Session, name string) (*gamev1.CreateCharacterResponse, error) {
	acct := s.Principal.EffectiveAccountID()
	ctx, span := r.tracer.Start(ctx, "character.create",
		trace.WithLinks(trace.Link{SpanContext: s.SpanContext()}),
		trace.WithAttributes(attribute.String("session.id", s.ID)))
	defer span.End()
	ref, err := r.opts.Accounts.CreateCharacter(ctx, acct, name, string(r.opts.SpawnRoom.Zone), string(r.opts.SpawnRoom.Room))
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, wireError(err)
	}
	span.SetAttributes(attribute.String("character.id", ref.GetCharacterId()))
	return &gamev1.CreateCharacterResponse{Character: summary(ref, false), MaxPerAccount: uint32(r.opts.Accounts.MaxCharacters())}, nil
}

// SelectCharacter implements gateway.Roster: the binding protocol.
//
//	lock(account) → live? already_live → owned & ACTIVE? else no_such_character
//	→ live flag, tentative → Bindings.Bind → produce BindCharacter → confirmed
//
// A produce failure clears the flag and the binding, and is answered as
// Submit answers it (AW-SRV-010's taxonomy). A SelectCharacter retried
// after DEADLINE_EXCEEDED therefore finds no live flag and produces
// again; the sim's BindCharacter is idempotent on a present body (AC-11),
// so the retry is safe without a client_ref.
func (r *Roster) SelectCharacter(ctx context.Context, s *gateway.Session, characterID string) (*gamev1.SelectCharacterResponse, error) {
	acct := s.Principal.EffectiveAccountID()
	ctx, span := r.tracer.Start(ctx, "character.select",
		trace.WithLinks(trace.Link{SpanContext: s.SpanContext()}),
		trace.WithAttributes(attribute.String("session.id", s.ID), attribute.String("character.id", characterID)))
	defer span.End()
	fail := func(outcome string, err error) (*gamev1.SelectCharacterResponse, error) {
		r.metrics.Bindings.WithLabelValues(outcome).Inc()
		span.SetAttributes(attribute.String("outcome", outcome))
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	ref, err := r.opts.Accounts.Character(acct, characterID)
	if err != nil {
		return fail(OutcomeNotFound, wireError(err))
	}

	r.mu.Lock()
	reconnect := false
	var prev *live
	for {
		cur, ok := r.byAccount[acct]
		if !ok {
			break
		}
		if cur.character == characterID && cur.releasing && !cur.linkdead && cur.end == gateway.EndLinkdead {
			// The linkdead teardown of the Session this one replaces is
			// still producing its MarkLinkdead: wait for it rather than
			// answer already_live to the reconnect it is making room for.
			r.mu.Unlock()
			select {
			case <-cur.released:
			case <-ctx.Done():
				return fail(OutcomeAlreadyLive, alreadyLive(cur.name))
			}
			r.mu.Lock()
			continue
		}
		if cur.linkdead && cur.character == characterID {
			// The reconnect (AC-2): this Session takes the flag over, and
			// the BindCharacter below takes the body back.
			r.drop(cur)
			reconnect = true
			prev = cur
			break
		}
		outcome, name := OutcomeAlreadyLive, cur.name
		if !cur.confirmed && !cur.releasing {
			outcome = OutcomeRaceLost
		}
		r.mu.Unlock()
		return fail(outcome, alreadyLive(name))
	}
	// The Session's teardown and this registration are ordered by this
	// lock: close sets Closing before it calls ReleaseSession, and
	// ReleaseSession takes r.mu, so a flag installed here is either
	// visible to the teardown or refused now. Reading the Session's
	// context instead would not do — it is canceled after the teardown has
	// run, so a check would pass in exactly the window that leaks the flag
	// (review of PR #43).
	if s.Closing() {
		r.mu.Unlock()
		r.log.LogAttrs(ctx, slog.LevelInfo, "character not bound: the session ended first",
			slog.String("account_id", acct), slog.String("character_id", characterID),
			slog.String("session_id", s.ID), slog.String("trace_id", traceID(ctx)))
		span.SetStatus(codes.Error, errSessionClosing.Error())
		return nil, connect.NewError(connect.CodeCanceled, errSessionClosing)
	}
	l := &live{account: acct, session: s.ID, character: characterID, name: ref.GetName(), zone: sim.ZoneID(ref.GetZoneId()), released: make(chan struct{}), reconnectOf: prev}
	r.byAccount[acct] = l
	r.bySession[s.ID] = l
	r.mu.Unlock()

	// The routing table first, with the roster's last-known position, so
	// the arrival the sim emits is routed to this Session and its stream
	// perceives from the Room it will appear in (egress.Rebind).
	r.opts.Bindings.Bind(s.ID, command.Binding{Actor: sim.EntityID(characterID), Zone: sim.ZoneID(ref.GetZoneId()), Room: sim.RoomID(ref.GetRoomId())})

	cmd := &logv1.LoggedCommand{
		ZoneId:             ref.GetZoneId(),
		ActorId:            characterID,
		SessionId:          s.ID,
		TraceId:            command.TraceParent(ctx),
		AcceptedAtUnixNano: r.now().UnixNano(),
		Command: &logv1.LoggedCommand_BindCharacter{BindCharacter: &logv1.BindCharacter{
			CharacterId: characterID,
			AccountId:   acct,
			Name:        ref.GetName(),
			// The roster's Room is the spawn Room until the first unbind
			// records where the body went dormant; the sim ignores it when
			// the body exists.
			SpawnRoomId: ref.GetRoomId(),
		}},
	}
	acc, err := r.opts.Log.Produce(ctx, cmd)
	if err != nil {
		r.opts.Bindings.Unbind(s.ID)
		r.mu.Lock()
		r.drop(l)
		if p := l.reconnectOf; p != nil && !p.ended && r.byAccount[acct] == nil {
			// The reconnect did not reach the log: the body is still
			// linkdead, and the Account still holds it.
			r.byAccount[acct] = p
		}
		r.mu.Unlock()
		r.log.LogAttrs(ctx, slog.LevelWarn, "character not bound: produce failed",
			slog.String("account_id", acct), slog.String("character_id", characterID),
			slog.String("session_id", s.ID), slog.String("detail", err.Error()), slog.String("trace_id", traceID(ctx)))
		return fail(OutcomeProduceFailed, ingress.WireError(err))
	}
	r.mu.Lock()
	l.confirmed, l.reconnectOf = true, nil
	if !l.releasing {
		l.bound = true
		r.metrics.SessionsBound.Inc()
	}
	r.mu.Unlock()
	r.metrics.Bindings.WithLabelValues(OutcomeOK).Inc()
	span.SetAttributes(attribute.String("outcome", OutcomeOK), attribute.Int64("partition", int64(acc.Partition)), attribute.Int64("offset", acc.Offset), attribute.Bool("reconnect", reconnect))
	if reconnect {
		s.AddEvent("linkdead.reconnect", attribute.String("character.id", characterID))
	}
	r.log.LogAttrs(ctx, slog.LevelInfo, "character selected",
		slog.String("account_id", acct), slog.String("character_id", characterID),
		slog.String("session_id", s.ID), slog.String("zone", ref.GetZoneId()), slog.Bool("reconnect", reconnect),
		slog.Int64("partition", int64(acc.Partition)), slog.Int64("offset", acc.Offset), slog.String("trace_id", traceID(ctx)))
	return &gamev1.SelectCharacterResponse{AcceptedOffset: acc.Offset, Partition: acc.Partition}, nil
}

// ReleaseSession implements gateway.Roster: the Session is ending. If it
// drives a Character, the teardown begins now — the routing table is read
// while it still says where the body is — and runs on its own context,
// bounded by ingress.produce_deadline. What it produces is end's
// (AW-SRV-015's teardown table):
//
//   - EndQuit: UnbindCharacter{QUIT}; the roster's position written from the
//     table's entry; the binding and the live flag cleared.
//   - EndLinkdead: MarkLinkdead with the configured durations in Ticks; the
//     binding cleared; the flag kept, marked linkdead, so the Account's next
//     SelectCharacter of this Character reconnects it and any other is
//     already_live. Only the body's despawn frees it (ObserveLinkdead).
//
// A Character mid-crossing is waited for, so the record goes to the Zone
// the body arrives in rather than the one it left.
//
// Until the teardown has run, the Character is live: a SelectCharacter from
// the same Account is already_live, except a reconnect, which waits for it.
// The returned channel is closed when it has run.
//
// A produce that fails is logged, counted, and not retried, and the flag is
// freed: the body stays present with no Session, and the next BindCharacter
// takes it where it stands (AC-11).
func (r *Roster) ReleaseSession(s *gateway.Session, end gateway.SessionEnd) <-chan struct{} {
	r.mu.Lock()
	l, ok := r.bySession[s.ID]
	if !ok || l.releasing {
		r.mu.Unlock()
		if ok {
			return l.released
		}
		return closedChan
	}
	if end == gateway.EndLinkdead && r.opts.Linkdead.Grace == 0 {
		end = gateway.EndQuit
	}
	l.releasing, l.end = true, end
	r.mu.Unlock()

	zone, room := l.zone, sim.RoomID("")
	b, bound, inTransit := r.opts.Bindings.Lookup(s.ID)
	if bound {
		zone, room = b.Zone, b.Room
	}
	if inTransit {
		// Mid-crossing the body is in neither Zone: the source has let it
		// go and the Arrive has not applied. A record produced to the
		// source now would apply to nothing, and the body would arrive
		// with no Session and no deadline (review of #114). Wait for the
		// crossing to settle — bounded by ingress.transit_hold inside
		// Binding, and by the produce deadline — while the routing entry
		// still exists: the ingress drops it when the Session's context
		// ends, which is after this returns.
		wctx, cancel := context.WithTimeout(context.Background(), r.opts.ProduceDeadline)
		settled, err := r.opts.Bindings.Binding(wctx, s.ID)
		cancel()
		if err == nil {
			zone, room = settled.Zone, settled.Room
		} else {
			r.log.LogAttrs(context.Background(), slog.LevelWarn, "character released mid-crossing: the crossing did not settle; producing to the Zone it left",
				slog.String("account_id", l.account), slog.String("character_id", l.character),
				slog.String("session_id", s.ID), slog.String("zone", string(zone)), slog.String("detail", err.Error()))
		}
	}
	if end == gateway.EndLinkdead {
		s.AddEvent("linkdead.enter", attribute.String("character.id", l.character))
	}
	sessionSpan := s.SpanContext()
	r.releases.Add(1)
	go func() {
		defer r.releases.Done()
		defer close(l.released)
		ctx, cancel := context.WithTimeout(context.Background(), r.opts.ProduceDeadline)
		defer cancel()
		name, reason := "character.unbind", ReasonQuit
		if end == gateway.EndLinkdead {
			name, reason = "character.linkdead", ReasonLinkdead
		}
		ctx, span := r.tracer.Start(ctx, name,
			trace.WithLinks(trace.Link{SpanContext: sessionSpan}),
			trace.WithAttributes(attribute.String("session.id", s.ID), attribute.String("character.id", l.character)))
		defer span.End()

		cmd := &logv1.LoggedCommand{
			ZoneId:             string(zone),
			ActorId:            l.character,
			SessionId:          s.ID,
			TraceId:            command.TraceParent(ctx),
			AcceptedAtUnixNano: r.now().UnixNano(),
			Command: &logv1.LoggedCommand_UnbindCharacter{UnbindCharacter: &logv1.UnbindCharacter{
				CharacterId: l.character, Reason: logv1.UnbindReason_QUIT,
			}},
		}
		if end == gateway.EndLinkdead {
			ld := r.opts.Linkdead
			cmd.Command = &logv1.LoggedCommand_MarkLinkdead{MarkLinkdead: &logv1.MarkLinkdead{
				CharacterId: l.character, GraceTicks: ld.Grace, ExtensionTicks: ld.Extension, MaxTicks: ld.Max,
			}}
		}
		outcome := UnbindOK
		_, err := r.opts.Log.Produce(ctx, cmd)
		if err != nil {
			outcome = UnbindProduceFailed
			span.SetStatus(codes.Error, err.Error())
			r.log.LogAttrs(ctx, slog.LevelWarn, "character not released: produce failed; the body stays present until the next select",
				slog.String("account_id", l.account), slog.String("character_id", l.character),
				slog.String("session_id", s.ID), slog.String("end", end.String()),
				slog.String("detail", err.Error()), slog.String("trace_id", traceID(ctx)))
		}
		r.metrics.Unbinds.WithLabelValues(reason, outcome).Inc()
		// The binding goes with the Session; the ingress clears it on
		// the same signal, and a Session that never submitted has no
		// ingress state to clear it from.
		r.opts.Bindings.Unbind(s.ID)
		// Where the body is, as the Gateway last knew: the next
		// BindCharacter is routed here. In transit the Room is unknown
		// and the roster keeps what it had.
		if room != "" {
			if err := r.opts.Accounts.SetCharacterPosition(ctx, l.account, l.character, string(zone), string(room)); err != nil {
				r.log.LogAttrs(ctx, slog.LevelWarn, "character position not recorded",
					slog.String("account_id", l.account), slog.String("character_id", l.character),
					slog.String("session_id", s.ID), slog.String("detail", err.Error()), slog.String("trace_id", traceID(ctx)))
			}
		}
		r.mu.Lock()
		if end == gateway.EndLinkdead && err == nil && r.byAccount[l.account] == l {
			r.holdLinkdead(l)
		} else {
			r.drop(l)
		}
		r.mu.Unlock()
		msg := "character unbound"
		if end == gateway.EndLinkdead {
			msg = "character marked linkdead"
		}
		r.log.LogAttrs(ctx, slog.LevelInfo, msg,
			slog.String("account_id", l.account), slog.String("character_id", l.character),
			slog.String("session_id", s.ID), slog.String("reason", reason), slog.String("outcome", outcome),
			slog.String("zone", string(zone)), slog.String("room", string(room)), slog.String("trace_id", traceID(ctx)))
	}()
	return l.released
}

// holdLinkdead keeps l's flag for the linkdead body, off the Session that
// left and off andara_sessions_bound, until the sim says the grace ended
// (ObserveLinkdead) or a reconnect takes it. No wall-clock bound: the sim's
// deadline starts when the mark applies and runs in Ticks, and a timer here
// could free the Account while the body is still in the World (review of
// #114). A mark that applied as a no-op leaves the body present with no
// Session; holding the flag is then right too, and the reconnect takes it.
// Caller holds mu.
func (r *Roster) holdLinkdead(l *live) {
	l.linkdead = true
	if r.bySession[l.session] == l {
		delete(r.bySession, l.session)
	}
	if l.bound {
		l.bound = false
		r.metrics.SessionsBound.Dec()
	}
}

// SeedLinkdead is the bodies recovery left linkdead, for the gauge: called
// once, before the loop runs.
func (r *Roster) SeedLinkdead(ids []sim.EntityID) {
	for _, id := range ids {
		r.linkdeadBodies[id] = false
		r.recovered[id] = true
	}
	r.setLinkdeadGauge()
}

// ObserveLinkdead is the tick's linkdead lifecycle (AW-SRV-015), on the
// loop goroutine: the metrics and the info lines, and the flag of every
// linkdead Character whose grace ended freed. A flag a reconnect already
// took over is not linkdead any more, and is left alone: the reconnect's
// BindCharacter wakes the body the despawn left.
func (r *Roster) ObserveLinkdead(tick sim.Tick, changes []sim.LinkdeadChange) {
	for _, c := range changes {
		inCombat := r.linkdeadBodies[c.Character]
		recovered := r.recovered[c.Character]
		outcome := ""
		switch c.Kind {
		case sim.LinkdeadEntered:
			r.linkdeadBodies[c.Character] = false
			outcome = "entered"
		case sim.LinkdeadExtended:
			r.linkdeadBodies[c.Character] = true
			r.metrics.CombatExtensions.Inc()
			continue
		case sim.LinkdeadReconnected:
			delete(r.linkdeadBodies, c.Character)
			delete(r.recovered, c.Character)
			outcome = LinkdeadReconnected
		case sim.LinkdeadEnded:
			delete(r.linkdeadBodies, c.Character)
			delete(r.recovered, c.Character)
			switch c.Reason {
			case sim.DespawnLinkdead:
				outcome = LinkdeadDespawned
			case sim.DespawnLinkdeadCeiling:
				outcome = LinkdeadCeiling
				r.metrics.CeilingDespawns.Inc()
			default:
				outcome = LinkdeadQuit
			}
			r.mu.Lock()
			for _, l := range r.byAccount {
				if l.linkdead && l.character == string(c.Character) {
					r.drop(l)
					if c.Session == "" {
						c.Session = l.session
					}
					break
				}
				if p := l.reconnectOf; p != nil && p.character == string(c.Character) {
					p.ended = true
					if c.Session == "" {
						c.Session = p.session
					}
				}
			}
			r.mu.Unlock()
		}
		if c.Kind != sim.LinkdeadEntered {
			r.metrics.LinkdeadOutcomes.WithLabelValues(outcome).Inc()
			secs := float64(tick-c.Since) / float64(r.opts.TickRate)
			r.metrics.LinkdeadDuration.WithLabelValues(strconv.FormatBool(inCombat)).Observe(secs)
		}
		ctx := command.ParentFrom(context.Background(), c.TraceID)
		msg := map[sim.LinkdeadKind]string{
			sim.LinkdeadEntered: "character linkdead", sim.LinkdeadReconnected: "character reconnected", sim.LinkdeadEnded: "character despawned",
		}[c.Kind]
		// An expiry names the Session that went linkdead, which the hold
		// kept. A body recovery left has none: no Session survives a
		// restart, so its line omits session_id and says recovered.
		session := slog.String("session_id", c.Session)
		if c.Session == "" && recovered {
			session = slog.Bool("recovered", true)
		}
		r.log.LogAttrs(ctx, slog.LevelInfo, msg,
			session, slog.String("character_id", string(c.Character)),
			slog.String("outcome", outcome), slog.Uint64("deadline_tick", uint64(c.Deadline)),
			slog.Uint64("tick", uint64(tick)), slog.String("zone", string(c.Zone)),
			slog.String("trace_id", traceID(ctx)))
	}
	r.setLinkdeadGauge()
}

func (r *Roster) setLinkdeadGauge() {
	var combat, calm int
	for _, in := range r.linkdeadBodies {
		if in {
			combat++
		} else {
			calm++
		}
	}
	r.metrics.Linkdead.WithLabelValues("true").Set(float64(combat))
	r.metrics.Linkdead.WithLabelValues("false").Set(float64(calm))
}

// closedChan is a teardown with nothing to wait for.
var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// drop clears l's flag if it is still the one held. Caller holds mu.
func (r *Roster) drop(l *live) {
	if r.byAccount[l.account] == l {
		delete(r.byAccount, l.account)
	}
	if r.bySession[l.session] == l {
		delete(r.bySession, l.session)
	}
	if l.bound {
		l.bound = false
		r.metrics.SessionsBound.Dec()
	}
}

func summary(c *accountsv1.CharacterRef, isLive bool) *gamev1.CharacterSummary {
	return &gamev1.CharacterSummary{
		CharacterId: c.GetCharacterId(), Name: c.GetName(), Status: c.GetStatus(),
		ZoneId: c.GetZoneId(), RoomId: c.GetRoomId(), Live: isLive, CreatedUnix: c.GetCreatedUnix(),
	}
}

// --- the error taxonomy ------------------------------------------------

// Domain is ErrorInfo.domain on every roster error.
const Domain = "andara.character"

// Reasons, the ErrorInfo.reason a client switches on.
const (
	ReasonRosterFull      = "roster_full"
	ReasonNameTaken       = "name_taken"
	ReasonNameInvalid     = "name_invalid"
	ReasonAlreadyLive     = "already_live"
	ReasonNoSuchCharacter = "no_such_character"
)

// ErrAlreadyLive: another Character is live on the Account, or this one is
// bound to another Session. FAILED_PRECONDITION.
var ErrAlreadyLive = errors.New("a character is already live on this account")

// errSessionClosing: the Session ended between the RPC resolving it and
// the roster registering the binding. CANCELED — there is nobody left to
// tell, and no outcome label: the Character was never live. It is counted
// nowhere on purpose; the log line is the record.
var errSessionClosing = errors.New("the session ended before the character was bound")

func alreadyLive(name string) error {
	return withInfo(connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%w: %s", ErrAlreadyLive, name)), ReasonAlreadyLive, map[string]string{"character": name})
}

// wireError maps a roster error to the wire with its ErrorInfo.
func wireError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	switch {
	case errors.Is(err, auth.ErrRosterFull):
		return withInfo(connect.NewError(connect.CodeResourceExhausted, err), ReasonRosterFull, nil)
	case errors.Is(err, auth.ErrNameTaken):
		return withInfo(connect.NewError(connect.CodeAlreadyExists, err), ReasonNameTaken, nil)
	case errors.Is(err, auth.ErrNameInvalid):
		return withInfo(connect.NewError(connect.CodeInvalidArgument, err), ReasonNameInvalid, nil)
	case errors.Is(err, auth.ErrNoSuchCharacter), errors.Is(err, auth.ErrNotFound):
		return withInfo(connect.NewError(connect.CodeNotFound, auth.ErrNoSuchCharacter), ReasonNoSuchCharacter, nil)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func withInfo(ce *connect.Error, reason string, meta map[string]string) *connect.Error {
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: Domain, Metadata: meta}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

var _ gateway.Roster = (*Roster)(nil)
