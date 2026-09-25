// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/sim"
)

// CorePack is the pack every other pack compiles against (ADR-0010). Its
// version gates every Builder pack: a pack pins the core it was compiled
// against, and a pack compiled against a core newer than the one running is
// held rather than refused for good (AC-8).
const CorePack = "andara.core"

// AllPacks is the content.packs value that means "follow every Active Pointer".
const AllPacks = "*"

// SwapProducer writes a ContentSwap to the command log: the Gateway's producer,
// which puts a World-scoped Command on Partition 0 (AW-SRV-012).
type SwapProducer interface {
	ProduceSwap(ctx context.Context, cmd *logv1.LoggedCommand) error
}

// Loader owns the retained-version rule, and follows the content in effect.
//
// The rule in one sentence: a version that does not load changes nothing. Not
// the World, not the previous version's place in it, not the process. That is
// what makes a Builder's mistake a Builder's problem instead of an outage, and
// it is why every method here reports a rejection and leaves `serving`
// untouched rather than returning an error that some caller might treat as
// fatal.
//
// Serving means applied (AW-SRV-012, 2026-09-25): the log is the source of the
// content in effect, so a version the Loader accepts is not serving until the
// Engine applies the ContentSwap it produced, and `serving` moves only in
// Applied. The Loader validates, builds and digests a version off-tick,
// produces the swap, and waits for it to apply before it handles the next, so
// what it validates against is always what the World is running. It is also
// the sim.ContentSource the Engine prepares a swap through, live and on replay.
type Loader struct {
	store   Store
	packs   []string
	metrics *Metrics
	log     *slog.Logger
	tracer  trace.Tracer
	now     func() time.Time
	// strictOrphans promotes orphan_room from a warning to a refusal, the
	// same policy knob the dir loader has (content.strict_orphans).
	strictOrphans bool

	mu       sync.Mutex
	producer SwapProducer
	// serving is the content in effect: what the Engine last applied, per pack.
	serving map[string]*Resolved
	// held is a version that resolved cleanly but was refused for core skew,
	// waiting for core to catch up. Re-evaluated on every core pointer move,
	// which is what makes AC-8 a state machine rather than a check.
	held map[string]uint64
	// resolved caches pack@version, so a version resolved to be validated is
	// not read again to be prepared or to become serving.
	resolved map[string]*Resolved
	// staged is a topology built off-tick, keyed by the versions in effect it
	// assumes, waiting for the Engine to prepare the swap that produces it.
	staged map[string]sim.Topology
	// waiting is a load waiting for its swap to apply or be refused, by
	// pack@version.
	waiting map[string][]chan swapResult
	// digest is the world_digest of the content in effect: a swap is built
	// on it (ContentSwap.base_digest), so one built on a World the log has
	// since moved past is refused rather than applied.
	digest [32]byte
	// barrier waits until the World Partition is consumed past its current
	// end: what reconcile runs first, and what an ambiguous produce waits for.
	barrier func(context.Context) error
	// applyWait bounds a load's wait for its swap to apply.
	applyWait time.Duration
	// spawn is character.spawn_room; a version whose World lacks it, when
	// the World in effect has it, is refused.
	spawn sim.RoomRef
	// startRetries is every pack reconcile could not load for the store's
	// sake: Follow starts by retrying them, since the watch starts past the
	// pointer that named them.
	startRetries map[string]uint64
	// pointer is the newest version each pack's Active Pointer named, and
	// pendingSince when it moved to one that is neither serving nor refused
	// for a Builder's reason (andara_content_pending_seconds).
	pointer      map[string]uint64
	pendingSince map[string]time.Time
}

// LoaderOptions configures a Loader.
type LoaderOptions struct {
	Store Store
	// Packs to follow. Empty means andara.core alone; a single "*" means
	// every Active Pointer on the topic.
	Packs         []string
	Metrics       *Metrics
	Log           *slog.Logger
	Tracer        trace.Tracer
	StrictOrphans bool
	// Producer writes the swaps. It may be set later, with SetProducer: the
	// Gateway's producer is built after content is opened.
	Producer SwapProducer
	// Now is the clock for the pending gauge; time.Now when nil.
	Now func() time.Time
	// ApplyWait bounds the wait for a produced swap to apply
	// (content.reload_debounce × 15); 30s when zero.
	ApplyWait time.Duration
	// SpawnRoom is character.spawn_room. Zero skips the check.
	SpawnRoom sim.RoomRef
}

// swapResult is how a waiting load learns its swap's fate.
type swapResult struct{ refused *sim.SwapRefused }

// NewLoader builds a Loader over a store.
func NewLoader(o LoaderOptions) *Loader {
	packs := o.Packs
	if len(packs) == 0 {
		packs = []string{CorePack}
	}
	m := o.Metrics
	if m == nil {
		m = NewMetrics(nil)
	}
	l := o.Log
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	tr := o.Tracer
	if tr == nil {
		tr = noop.NewTracerProvider().Tracer("andara-server")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	wait := o.ApplyWait
	if wait <= 0 {
		wait = 30 * time.Second
	}
	ld := &Loader{
		store:         o.Store,
		packs:         packs,
		metrics:       m,
		log:           l,
		tracer:        tr,
		now:           now,
		strictOrphans: o.StrictOrphans,
		producer:      o.Producer,
		serving:       map[string]*Resolved{},
		held:          map[string]uint64{},
		resolved:      map[string]*Resolved{},
		staged:        map[string]sim.Topology{},
		waiting:       map[string][]chan swapResult{},
		applyWait:     wait,
		spawn:         o.SpawnRoom,
		startRetries:  map[string]uint64{},
		pointer:       map[string]uint64{},
		pendingSince:  map[string]time.Time{},
	}
	m.pending(ld.Pending)
	return ld
}

// SetBarrier sets how the Loader waits for the World Partition to be
// consumed past its current end.
func (l *Loader) SetBarrier(b func(context.Context) error) {
	l.mu.Lock()
	l.barrier = b
	l.mu.Unlock()
}

// SetProducer sets where swaps are written.
func (l *Loader) SetProducer(p SwapProducer) {
	l.mu.Lock()
	l.producer = p
	l.mu.Unlock()
}

// Rejection is one refused version, with the reason and the findings.
type Rejection struct {
	Pack    string
	Version uint64
	Reason  string
	Err     error
}

// LoadAll brings every followed pack to its Active Pointer: the reconcile a
// boot runs after recovery. On an empty log it is genesis — one swap per pack,
// core first — and after a restart it is whatever moved while the process was
// down, applied through the log like any other move.
//
// Core is loaded first and on its own, because every other pack's acceptance
// depends on which core version is serving. A pack that fails is reported and
// skipped; the rest still load, since one Builder's bad pack must not keep
// every other Builder's good one off the World.
func (l *Loader) LoadAll(ctx context.Context) ([]Rejection, error) {
	// A swap produced by a previous process, or one left ambiguous, may be
	// on the World Partition past the last boundary. Let it apply before
	// deciding anything, so nothing is built on a World about to change.
	barrier := l.waitBarrier(ctx)
	var bt *ErrBarrierTimeout
	if barrier != nil && !errors.As(barrier, &bt) {
		return nil, barrier
	}
	start := time.Now()
	active, err := l.store.Active(ctx)
	if err != nil {
		return nil, err
	}
	if bt != nil {
		// The World Partition is frozen or far behind: nothing produced now
		// would apply. Every pointer not in effect is store_unavailable,
		// retried by Follow, and the boot carries on with what the log
		// recorded rather than hang (review of #91).
		l.log.Warn("the world partition did not catch up; reconcile defers every pending pack to the retry",
			"wait", bt.Wait.String())
		var rejects []Rejection
		for _, pack := range l.order(active) {
			if v, ok := l.servingVersion(pack); active[pack] == 0 || (ok && v == active[pack]) {
				continue
			}
			l.moved(pack, active[pack])
			rejects = append(rejects, *l.reject(Rejection{Pack: pack, Version: active[pack], Err: bt}))
			l.mu.Lock()
			l.startRetries[pack] = active[pack]
			l.mu.Unlock()
		}
		return rejects, nil
	}
	l.metrics.LoadDuration.WithLabelValues(PhaseResolve).Observe(time.Since(start).Seconds())

	var rejects []Rejection
	for _, pack := range l.order(active) {
		if active[pack] == 0 {
			// A configured pack with no Active Pointer is not an error — it
			// may not have been published yet — but it is never what the
			// operator meant, and silence here looks exactly like a pack that
			// loaded fine.
			l.log.Warn("configured content pack has no Active Pointer; nothing to load",
				"pack", pack, "topic", TopicActive)
			continue
		}
		l.moved(pack, active[pack])
		if r := l.load(ctx, pack, active[pack]); r != nil {
			rejects = append(rejects, *r)
			if r.Reason == ReasonStoreUnavailable {
				l.mu.Lock()
				l.startRetries[pack] = active[pack]
				l.mu.Unlock()
			}
		}
	}
	for p, v := range l.Versions() {
		if !l.follows(p) {
			l.log.Warn("a pack in effect is no longer followed; it stays as it is and its pointer is not watched",
				"pack", p, "version", v, "content.packs", strings.Join(l.packs, ","))
		}
	}
	return rejects, nil
}

// waitBarrier runs the barrier, bounded by applyWait: a frozen World
// Partition never catches up, and nothing may wait on it forever. Past the
// bound it is ErrBarrierTimeout.
func (l *Loader) waitBarrier(ctx context.Context) error {
	l.mu.Lock()
	b := l.barrier
	l.mu.Unlock()
	if b == nil {
		return nil
	}
	bctx, cancel := context.WithTimeout(ctx, l.applyWait)
	defer cancel()
	err := b(bctx)
	if err != nil && ctx.Err() == nil && bctx.Err() != nil {
		return &ErrBarrierTimeout{Wait: l.applyWait}
	}
	return err
}

// Candidates resolves and validates every followed pack at its Active Pointer
// without producing anything: what the World would be, for
// `andara-server --validate-only`. Nothing is marked serving.
func (l *Loader) Candidates(ctx context.Context) ([]sim.Input, []sim.TemplateInput, []Rejection, error) {
	active, err := l.store.Active(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	base := map[string]*Resolved{}
	var rejects []Rejection
	for _, pack := range l.order(active) {
		if active[pack] == 0 {
			continue
		}
		res, _, rej := l.evaluate(ctx, pack, active[pack], base)
		if rej != nil {
			rejects = append(rejects, *rej)
			continue
		}
		base[pack] = res
	}
	zones, templates := inputsOf(base)
	return zones, templates, rejects, nil
}

// order is the followed packs present in active: core first, then by name, so
// findings come out the same way on every boot.
func (l *Loader) order(active map[string]uint64) []string {
	wanted := l.wanted(active)
	order := make([]string, 0, len(wanted))
	for _, p := range wanted {
		if p != CorePack {
			order = append(order, p)
		}
	}
	sort.Strings(order)
	if _, ok := active[CorePack]; ok && contains(wanted, CorePack) {
		order = append([]string{CorePack}, order...)
	}
	return order
}

// Apply handles one Active Pointer move. A rejection is returned, not raised:
// the caller logs it and the World carries on with what it had.
func (l *Loader) Apply(ctx context.Context, move PointerMove) []Rejection {
	if !l.follows(move.Pack) {
		return nil
	}
	l.moved(move.Pack, move.Version)
	var out []Rejection
	if r := l.load(ctx, move.Pack, move.Version); r != nil {
		out = append(out, *r)
	}
	// AC-8: core moving is what releases packs held for core skew. Re-evaluate
	// them here rather than waiting for their own pointer to move again — a
	// Builder who published against a core that had not shipped yet should not
	// have to publish a second time once it has.
	if move.Pack == CorePack {
		out = append(out, l.releaseHeld(ctx)...)
	}
	return out
}

// releaseHeld retries every pack held for core skew.
func (l *Loader) releaseHeld(ctx context.Context) []Rejection {
	l.mu.Lock()
	retry := make(map[string]uint64, len(l.held))
	for p, v := range l.held {
		retry[p] = v
	}
	l.mu.Unlock()

	packs := make([]string, 0, len(retry))
	for p := range retry {
		packs = append(packs, p)
	}
	sort.Strings(packs)

	var out []Rejection
	for _, p := range packs {
		if r := l.load(ctx, p, retry[p]); r != nil {
			out = append(out, *r)
		}
	}
	return out
}

// staleRetries bounds how often a load re-evaluates after its swap was
// refused as stale; past it the load is store_unavailable, and retried.
const staleRetries = 3

// load accepts or refuses pack@version and, accepted, brings it into effect:
// build off-tick, stage, produce the swap, and wait for the Engine to apply or
// refuse it. A swap refused as stale — built on a World the log had moved
// past — is evaluated again against what is in effect now.
func (l *Loader) load(ctx context.Context, pack string, version uint64) *Rejection {
	if version == 0 {
		return nil // no Active Pointer for this pack; nothing to load
	}
	for attempt := 0; ; attempt++ {
		if v, ok := l.servingVersion(pack); ok && v == version {
			l.settled(pack, version)
			return nil
		}
		refused, rej := l.loadOnce(ctx, pack, version)
		if rej != nil || refused == nil {
			return rej
		}
		if refused.Reason != sim.SwapStaleBase || attempt+1 >= staleRetries {
			return l.reject(Rejection{Pack: pack, Version: version, Err: &ErrSwapRefused{
				Pack: pack, Version: version, Reason: refused.Reason, Detail: refused.Detail}})
		}
		l.log.Info("content swap was stale; evaluating again against the content in effect",
			"pack", pack, "version", version, "detail", refused.Detail)
	}
}

// loadOnce is one evaluate-stage-produce-wait. It returns the Engine's refusal
// when there was one, a rejection when the version did not come into effect
// for any other reason, and neither when it applied.
func (l *Loader) loadOnce(ctx context.Context, pack string, version uint64) (*sim.SwapRefused, *Rejection) {
	ctx, span := l.tracer.Start(ctx, "content.load", trace.WithAttributes(
		attribute.String("pack", pack), attribute.Int64("version", int64(version))))
	defer span.End()
	traceID := ""
	if sc := span.SpanContext(); sc.HasTraceID() {
		traceID = sc.TraceID().String()
	}
	// The record carries the W3C traceparent, which is what the tick parses
	// to parent content.swap under this span; logs carry the bare trace ID.
	traceParent := command.TraceParent(ctx)

	start := time.Now()
	res, topo, rej := l.evaluate(ctx, pack, version, l.servingSnapshot())
	if rej != nil {
		if pack != CorePack {
			if _, skew := rej.Err.(*ErrCoreVersion); skew {
				l.hold(pack, version)
			}
		}
		return nil, l.reject(*rej)
	}
	digest := sim.ContentDigest(topo)

	// Stage the topology under exactly the versions it assumes, so the Engine
	// preparing the swap picks up this build and not one for some other
	// combination that happens to digest alike.
	l.mu.Lock()
	after := servingVersions(l.serving)
	var base []byte
	if len(after) > 0 {
		base = append([]byte(nil), l.digest[:]...)
	}
	after[pack] = version
	key := stageKey(after)
	l.staged[key] = topo
	done := make(chan swapResult, 1)
	pv := ManifestKey(pack, version)
	l.waiting[pv] = append(l.waiting[pv], done)
	producer := l.producer
	l.mu.Unlock()
	drop := func(unstage bool) {
		l.mu.Lock()
		if unstage {
			delete(l.staged, key)
		}
		l.unwait(pv, done)
		l.mu.Unlock()
	}

	cmd := &logv1.LoggedCommand{TraceId: traceParent, Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: &logv1.ContentSwap{
		PackId: pack, Version: version, WorldDigest: digest[:], BaseDigest: base,
	}}}
	var perr error
	if producer == nil {
		perr = fmt.Errorf("no producer: the swap has nowhere to go")
	} else {
		perr = producer.ProduceSwap(ctx, cmd)
	}
	var pending *SwapPending
	if errors.As(perr, &pending) {
		// The produce's wait ended with the record possibly live. It is never
		// treated as not written until it settles that way.
		select {
		case <-pending.Settled:
		case <-ctx.Done():
			drop(false)
			return nil, nil
		}
		switch err := pending.Outcome(); {
		case err == nil:
			perr = nil // written: wait for it to apply
		case errors.Is(err, ErrSwapOutcomeUnknown):
			// It may be in the log. Evaluate nothing else until the World
			// Partition is consumed past it: then it has applied, been
			// refused, or was never there.
			if berr := l.waitBarrier(ctx); berr != nil {
				drop(false)
				return nil, l.reject(Rejection{Pack: pack, Version: version, Err: &ErrProduce{Pack: pack, Version: version, Err: berr}})
			}
			select {
			case r := <-done:
				return r.refused, nil
			default:
				drop(true)
				return nil, l.reject(Rejection{Pack: pack, Version: version, Err: &ErrProduce{Pack: pack, Version: version, Err: err}})
			}
		default:
			perr = err
		}
	}
	if perr != nil {
		drop(true)
		return nil, l.reject(Rejection{Pack: pack, Version: version, Err: &ErrProduce{Pack: pack, Version: version, Err: perr}})
	}
	l.log.Info("content version accepted; swap produced",
		"pack", pack, "version", version, "core_version", res.CoreVersion,
		"zones", len(res.Zones), "templates", len(res.Templates),
		"world_digest", fmt.Sprintf("%x", digest[:8]),
		"duration_ms", time.Since(start).Milliseconds(), "trace_id", traceID)

	timer := time.NewTimer(l.applyWait)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.refused, nil
	case <-timer.C:
		// Partition 0 faulted, or far behind. The swap is in the log and may
		// still apply — Applied needs no waiter — but this move stops
		// blocking every other.
		drop(false)
		l.log.Warn("content swap produced but not applied within the bounded wait; retrying later",
			"pack", pack, "version", version, "wait", l.applyWait.String(), "trace_id", traceID)
		return nil, l.reject(Rejection{Pack: pack, Version: version, Err: &ErrApplyTimeout{Pack: pack, Version: version, Wait: l.applyWait}})
	case <-ctx.Done():
		drop(false)
		return nil, nil
	}
}

// evaluate resolves pack@version and decides it against base, the versions it
// would join: core skew, core rollback, and the whole World it would build. On
// success it returns the Topology, built, which is what a swap moves the World
// to. Nothing here touches serving.
func (l *Loader) evaluate(ctx context.Context, pack string, version uint64, base map[string]*Resolved) (*Resolved, sim.Topology, *Rejection) {
	reject := func(err error) (*Resolved, sim.Topology, *Rejection) {
		return nil, sim.Topology{}, &Rejection{Pack: pack, Version: version, Err: err}
	}
	rstart := time.Now()
	_, rspan := l.tracer.Start(ctx, "content.resolve")
	res, err := l.resolve(ctx, pack, version)
	rspan.End()
	l.metrics.LoadDuration.WithLabelValues(PhaseResolve).Observe(time.Since(rstart).Seconds())
	if err != nil {
		return reject(err)
	}

	// Core skew (AC-8). Checked after resolution rather than before, because
	// the pinned core version is in the manifest, and because a pack that is
	// also malformed should say so rather than waiting for a core it would be
	// refused against anyway.
	if pack != CorePack {
		if core, ok := base[CorePack]; ok && res.CoreVersion > core.Version {
			return reject(&ErrCoreVersion{Pack: pack, Compiled: res.CoreVersion, Active: core.Version})
		}
	} else if holding := strandedBy(base, res.Version); len(holding) > 0 {
		// Core moving *backwards* is the same compatibility question asked
		// from the other side. Accepting it would leave the World serving a
		// combination the loader would refuse to assemble from scratch: a
		// pack pinned to core@4 on top of core@3. The Loader has no way to
		// unload a pack, so the answer is to retain core and name every pack
		// holding it there (feedback §9b).
		from := uint64(0)
		if c := base[CorePack]; c != nil {
			from = c.Version
		}
		return reject(&ErrCoreRollback{From: from, To: res.Version, Holding: holding})
	}

	vctx, vspan := l.tracer.Start(ctx, "content.validate")
	vstart := time.Now()
	topo, findings := l.build(vctx, base, res)
	l.metrics.LoadDuration.WithLabelValues(PhaseValidate).Observe(time.Since(vstart).Seconds())
	vspan.SetAttributes(attribute.Int("error_count", len(findings)))
	vspan.End()
	if len(findings) > 0 {
		return reject(refusal(findings))
	}
	return res, topo, nil
}

// refusal types a set of refusing findings: fallback_missing when that is all
// they are (AC-10), validation otherwise.
func refusal(findings []sim.ValidationError) error {
	for _, f := range findings {
		if f.Code != sim.ErrFallbackMissing {
			return &ErrValidation{Findings: findings}
		}
	}
	return &ErrFallbackMissing{Zone: string(findings[0].Zone), Room: string(findings[0].Room), Findings: findings}
}

// build builds the World that candidate plus base would produce, and returns
// it with the findings that refuse it. A pack is not validated alone: an Exit
// from town into a Zone that only core declares is valid only in the whole.
func (l *Loader) build(ctx context.Context, base map[string]*Resolved, candidate *Resolved) (sim.Topology, []sim.ValidationError) {
	with := make(map[string]*Resolved, len(base)+1)
	for p, r := range base {
		with[p] = r
	}
	if candidate != nil {
		with[candidate.Pack] = candidate
	}
	zones, templates := inputsOf(with)

	// A version that would leave the World with no Zones at all, when the
	// World currently has some, is refused. Everyone standing in it would be
	// standing nowhere, and the previous version is right there. This is a
	// rule about the *transition*: a pack with no Zones is perfectly legal on
	// its own — andara.core is exactly that, Templates and nothing else —
	// which is why the test is against what is serving rather than against
	// the candidate alone.
	if len(zones) == 0 && countZones(base) > 0 && candidate != nil {
		return sim.Topology{}, []sim.ValidationError{{
			Code: sim.ErrEmptyContent,
			Detail: fmt.Sprintf("%s@%d would leave the World with no Zones; the previous version keeps serving",
				candidate.Pack, candidate.Version),
		}}
	}

	_, span := l.tracer.Start(ctx, "content.build")
	defer span.End()
	// phase="build" is building the World and the Template registry, which
	// is also where their findings come from; phase="validate" is the
	// checks against what is in effect, around it (evaluate).
	bstart := time.Now()
	defer func() { l.metrics.LoadDuration.WithLabelValues(PhaseBuild).Observe(time.Since(bstart).Seconds()) }()

	topo := sim.Topology{World: sim.EmptyWorld()}
	var findings []sim.ValidationError
	if candidate != nil {
		// Transitions the version alone cannot be judged on (review of
		// #86–#88): removing a Zone the World has — an evacuation policy is
		// a later story — and losing character.spawn_room.
		now := map[string]bool{}
		for _, z := range zones {
			now[z.Def.GetId()] = true
		}
		baseZones, _ := inputsOf(base)
		for _, z := range baseZones {
			if id := z.Def.GetId(); !now[id] {
				findings = append(findings, sim.ValidationError{Zone: sim.ZoneID(id), Code: sim.ErrZoneRemoved,
					Detail: fmt.Sprintf("%s@%d removes Zone %s, which is in effect; removing a Zone is not supported", candidate.Pack, candidate.Version, id)})
			}
		}
	}
	if len(templates) > 0 {
		reg, errs := sim.BuildTemplates(templates, sim.TemplateOptions{})
		findings = append(findings, l.fatalOnly(errs)...)
		topo.Templates = reg
	}
	if len(zones) > 0 {
		opts := sim.Options{Source: SourceKafka, StrictOrphans: l.strictOrphans}
		w, errs := sim.BuildWorld(zones, opts)
		findings = append(findings, l.fatalOnly(errs)...)
		if w != nil {
			topo.World = w
		}
	}
	if candidate != nil && l.spawn.Zone != "" && len(findings) == 0 {
		if _, had := spawnIn(base, l.spawn); had {
			if _, ok := topo.World.Resolve(l.spawn); !ok {
				findings = append(findings, sim.ValidationError{Zone: l.spawn.Zone, Room: l.spawn.Room, Code: sim.ErrSpawnRoomRemoved,
					Detail: fmt.Sprintf("%s@%d has no Room %s/%s, which character.spawn_room names", candidate.Pack, candidate.Version, l.spawn.Zone, l.spawn.Room)})
			}
		}
	}
	return topo, findings
}

// spawnIn reports whether the content base names the spawn Room.
func spawnIn(base map[string]*Resolved, spawn sim.RoomRef) (string, bool) {
	for p, r := range base {
		for _, z := range r.Zones {
			if z.Def.GetId() != string(spawn.Zone) {
				continue
			}
			for _, room := range z.Def.GetRooms() {
				if room.GetId() == string(spawn.Room) {
					return p, true
				}
			}
		}
	}
	return "", false
}

// fatalOnly drops warnings. A warning is advisory by construction — an
// unconnected Room, a one-way chute — and refusing a version for one would
// make the loader useless to the Builder it is meant to help (AW-SRV-001).
func (l *Loader) fatalOnly(errs []sim.ValidationError) []sim.ValidationError {
	var out []sim.ValidationError
	for _, e := range errs {
		if sim.IsWarning(e, l.strictOrphans) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Prepare implements sim.ContentSource: the Topology the World has after swap,
// given the versions in effect before it. Live it is the build load staged;
// on replay, or a swap this process did not produce, it resolves every
// version named from the store and builds it. The Engine checks the digest.
func (l *Loader) Prepare(inEffect map[string]uint64, swap *logv1.ContentSwap) (sim.Topology, error) {
	after := make(map[string]uint64, len(inEffect)+1)
	for p, v := range inEffect {
		after[p] = v
	}
	after[swap.GetPackId()] = swap.GetVersion()
	l.mu.Lock()
	topo, ok := l.staged[stageKey(after)]
	l.mu.Unlock()
	if ok {
		return topo, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), prepareTimeout)
	defer cancel()
	base := map[string]*Resolved{}
	for p, v := range after {
		res, err := l.resolve(ctx, p, v)
		if err != nil {
			return sim.Topology{}, err
		}
		base[p] = res
	}
	topo, findings := l.build(ctx, base, nil)
	if len(findings) > 0 {
		// A version the log says was applied no longer builds: a binary whose
		// validator has changed under it. Halt rather than serve something
		// else.
		return sim.Topology{}, refusal(findings)
	}
	return topo, nil
}

// prepareTimeout bounds a replay's resolution of one swap's content.
const prepareTimeout = 2 * time.Minute

// Applied is the Engine reporting the swaps a tick applied: those versions are
// now in effect. It is the only place serving moves, and it is called for
// replayed swaps as well as live ones, so after recovery the Loader follows
// the World the log built.
func (l *Loader) Applied(swaps []sim.SwapApplied) {
	for _, s := range swaps {
		res, err := l.resolve(context.Background(), s.Pack, s.Version)
		l.mu.Lock()
		if err == nil {
			l.serving[s.Pack] = res
		}
		if s.Digest != ([32]byte{}) {
			l.digest = s.Digest
		}
		delete(l.held, s.Pack)
		if l.pointer[s.Pack] == s.Version {
			delete(l.pendingSince, s.Pack)
		}
		pv := ManifestKey(s.Pack, s.Version)
		waiters := l.waiting[pv]
		delete(l.waiting, pv)
		l.prune()
		versions := servingVersions(l.serving)
		l.mu.Unlock()

		// The gauges move before any waiter is released: serving means
		// applied, for what /metrics says too, so a caller that waited on
		// the swap reads the new version (review of #91).
		l.metrics.ActiveVersion.WithLabelValues(s.Pack).Set(float64(s.Version))
		l.metrics.buildInfo(versions)
		for _, r := range s.Relocations {
			l.metrics.Relocations.WithLabelValues(string(r.Zone)).Inc()
		}
		for _, ch := range waiters {
			ch <- swapResult{}
		}
		if err != nil {
			// Unreachable in practice: the Engine prepared this version
			// through the same resolution a moment ago.
			l.log.Error("content in effect could not be read back; the Loader no longer knows what is serving",
				"pack", s.Pack, "version", s.Version, "error", err.Error())
			continue
		}
		l.log.Info("content in effect",
			"pack", s.Pack, "version", s.Version, "core_version", res.CoreVersion,
			"zones", len(res.Zones), "templates", len(res.Templates),
			"relocations", len(s.Relocations), "world_digest", fmt.Sprintf("%x", s.Digest[:8]))
		for _, r := range s.Relocations {
			l.log.Warn("entity relocated: its Room was removed by a content swap",
				"pack", s.Pack, "version", s.Version, "zone", string(r.Zone),
				"entity_id", string(r.Entity), "from", string(r.From), "to", string(r.To), "dormant", r.Dormant)
		}
	}
}

// Refused is the Engine reporting the swaps a tick consumed and refused:
// deterministic no-ops. A load waiting on one learns why, and re-evaluates
// when it was stale.
//
// Waiters are matched by pack@version only, so a refusal of an older swap for
// the same version — one a previous attempt or process produced — reaches the
// load waiting now. That is benign: it costs at most one more evaluation, and
// the swap this load produced still applies or is refused on its own.
func (l *Loader) Refused(refusals []sim.SwapRefused) {
	for _, r := range refusals {
		l.log.Warn("content swap refused by the engine; nothing changed",
			"pack", r.Pack, "version", r.Version, "reason", r.Reason, "detail", r.Detail)
		pv := ManifestKey(r.Pack, r.Version)
		l.mu.Lock()
		for _, ch := range l.waiting[pv] {
			r := r
			ch <- swapResult{refused: &r}
		}
		delete(l.waiting, pv)
		l.mu.Unlock()
	}
}

// prune drops cached resolutions and staged builds nothing in effect names.
// Called with l.mu held.
func (l *Loader) prune() {
	keep := map[string]bool{}
	for p, r := range l.serving {
		keep[ManifestKey(p, r.Version)] = true
	}
	for pv := range l.waiting {
		keep[pv] = true
	}
	for pv := range l.resolved {
		if !keep[pv] {
			delete(l.resolved, pv)
		}
	}
	current := stageKey(servingVersions(l.serving))
	for k := range l.staged {
		if k == current || len(l.waiting) == 0 {
			delete(l.staged, k)
		}
	}
}

func (l *Loader) unwait(pv string, done chan swapResult) {
	ws := l.waiting[pv]
	for i, ch := range ws {
		if ch == done {
			l.waiting[pv] = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	if len(l.waiting[pv]) == 0 {
		delete(l.waiting, pv)
	}
}

// resolve is Resolve, cached by pack@version: a version resolved to be
// validated is the one prepared and made serving.
func (l *Loader) resolve(ctx context.Context, pack string, version uint64) (*Resolved, error) {
	pv := ManifestKey(pack, version)
	l.mu.Lock()
	res, ok := l.resolved[pv]
	l.mu.Unlock()
	if ok {
		return res, nil
	}
	res, err := Resolve(ctx, l.store, pack, version)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.resolved[pv] = res
	l.mu.Unlock()
	return res, nil
}

// moved records that pack's Active Pointer now names version, and starts the
// pending clock if that is not what is serving. A newer move while one is
// pending keeps the older start.
func (l *Loader) moved(pack string, version uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pointer[pack] = version
	if r, ok := l.serving[pack]; ok && r.Version == version {
		delete(l.pendingSince, pack)
		return
	}
	if _, pending := l.pendingSince[pack]; !pending {
		l.pendingSince[pack] = l.now()
	}
}

// settled clears the pending clock when what the pointer names is serving.
func (l *Loader) settled(pack string, version uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pointer[pack] == version {
		delete(l.pendingSince, pack)
	}
}

// Pending is andara_content_pending_seconds: per followed pack whose pointer
// has moved, the seconds since it moved to a version that is neither serving
// nor refused for a Builder's reason, and 0 otherwise.
func (l *Loader) Pending() map[string]float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]float64, len(l.pointer))
	now := l.now()
	for p := range l.pointer {
		out[p] = 0
		if since, ok := l.pendingSince[p]; ok {
			out[p] = now.Sub(since).Seconds()
		}
	}
	return out
}

// Inputs returns what every pack in effect contributes to the World, in pack
// name order so the build is deterministic.
func (l *Loader) Inputs() ([]sim.Input, []sim.TemplateInput) {
	return inputsOf(l.servingSnapshot())
}

// InEffect is the versions in effect and their world_digest.
func (l *Loader) InEffect() (map[string]uint64, [32]byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return servingVersions(l.serving), l.digest
}

// Versions reports the version each pack has in effect.
func (l *Loader) Versions() map[string]uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return servingVersions(l.serving)
}

// ZoneVersions names, for each Zone in effect, the packID@version it came
// from. The state projector stamps it on Room and Zone records so a runtime
// object traces to authored source (AW-SRV-019 AC-7).
func (l *Loader) ZoneVersions() map[sim.ZoneID]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[sim.ZoneID]string{}
	for pack, r := range l.serving {
		v := pack + "@" + strconv.FormatUint(r.Version, 10)
		for _, z := range r.Zones {
			out[sim.ZoneID(z.Def.GetId())] = v
		}
	}
	return out
}

// Held reports the versions waiting on a newer core (AC-8).
func (l *Loader) Held() map[string]uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]uint64, len(l.held))
	for p, v := range l.held {
		out[p] = v
	}
	return out
}

func (l *Loader) servingSnapshot() map[string]*Resolved {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]*Resolved, len(l.serving))
	for p, r := range l.serving {
		out[p] = r
	}
	return out
}

func (l *Loader) servingVersion(pack string) (uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.serving[pack]
	if !ok {
		return 0, false
	}
	return r.Version, true
}

// supersedeHeld drops a held version of pack other than version.
func (l *Loader) supersedeHeld(pack string, version uint64) {
	l.mu.Lock()
	if v, ok := l.held[pack]; ok && v != version {
		delete(l.held, pack)
	}
	l.mu.Unlock()
}

func (l *Loader) hold(pack string, version uint64) {
	l.mu.Lock()
	l.held[pack] = version
	l.mu.Unlock()
}

func (l *Loader) reject(r Rejection) *Rejection {
	r.Reason = Reason(r.Err)
	l.metrics.LoadFailures.WithLabelValues(r.Reason).Inc()
	if IsBuilderFault(r.Err) {
		// Refused for the Builder's own mistake: not the platform failing to
		// serve, so not pending (docs/specs/slo/content-freshness.md).
		l.mu.Lock()
		if l.pointer[r.Pack] == r.Version {
			delete(l.pendingSince, r.Pack)
		}
		l.mu.Unlock()
	}
	// A store fault and a refused version are both "this version is not
	// serving", but they are not the same message. Telling a Builder their
	// pack was refused when the broker was unreachable sends them looking for
	// a mistake they did not make. Both end in the suffix the runbook's query
	// matches.
	msg := "content version refused; the previous version keeps serving"
	if IsStoreFault(r.Err) {
		msg = "content could not be read from the store; the previous version keeps serving"
	}
	l.log.Error(msg,
		"pack", r.Pack, "version", r.Version, "reason", r.Reason, "error", r.Err.Error())
	for _, f := range Findings(r.Err) {
		l.log.Error("content finding", "pack", r.Pack, "version", r.Version,
			"file", f.File, "line", f.Line, "code", string(f.Code), "detail", f.Detail)
	}
	return &r
}

// wanted is the followed packs, resolved against what the topic actually has.
func (l *Loader) wanted(active map[string]uint64) []string {
	if len(l.packs) == 1 && l.packs[0] == AllPacks {
		out := make([]string, 0, len(active))
		for p := range active {
			out = append(out, p)
		}
		sort.Strings(out)
		return out
	}
	return l.packs
}

func (l *Loader) follows(pack string) bool {
	if len(l.packs) == 1 && l.packs[0] == AllPacks {
		return true
	}
	return contains(l.packs, pack)
}

// strandedBy is every pack in base, by name, compiled against a core newer
// than core.
func strandedBy(base map[string]*Resolved, core uint64) []PackPin {
	var out []PackPin
	for p, r := range base {
		if p != CorePack && r.CoreVersion > core {
			out = append(out, PackPin{Pack: p, Core: r.CoreVersion})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pack < out[j].Pack })
	return out
}

func inputsOf(packs map[string]*Resolved) ([]sim.Input, []sim.TemplateInput) {
	names := make([]string, 0, len(packs))
	for p := range packs {
		names = append(names, p)
	}
	sort.Strings(names)
	var (
		zones     []sim.Input
		templates []sim.TemplateInput
	)
	for _, p := range names {
		zones = append(zones, packs[p].Zones...)
		templates = append(templates, packs[p].Templates...)
	}
	return zones, templates
}

func countZones(packs map[string]*Resolved) int {
	n := 0
	for _, r := range packs {
		n += len(r.Zones)
	}
	return n
}

func servingVersions(serving map[string]*Resolved) map[string]uint64 {
	out := make(map[string]uint64, len(serving))
	for p, r := range serving {
		out[p] = r.Version
	}
	return out
}

// stageKey names a set of versions: pack@version, sorted, comma-joined.
func stageKey(versions map[string]uint64) string {
	keys := make([]string, 0, len(versions))
	for p, v := range versions {
		keys = append(keys, ManifestKey(p, v))
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Watcher is the resolver half a Loader needs to follow pointer moves.
type Watcher interface {
	Watch(ctx context.Context) (<-chan PointerMove, error)
}

// Retry bounds for a version the store could not serve (AW-SRV-012 §5): a
// store_unavailable load is retried from retryInitial, doubling to retryMax,
// until it loads or the pointer moves again. Variables so a test can retry in
// milliseconds.
var (
	retryInitial = time.Second
	retryMax     = 30 * time.Second
)

// Follow watches for Active Pointer moves and applies them, debounced, and
// retries a load the store could not serve.
//
// The debounce coalesces a burst: publishing a pack is several writes in quick
// succession when several packs are activated together, and resolving on each
// one would rebuild the World once per write. It also bounds how fast a
// flapping pointer can make the server work.
//
// The retry is what stands in for a reload command (feedback §5): without it,
// a broker blip during a load would leave the World stale until someone
// published again, and there would be nothing an operator could run to
// recover it. Every other reason holds the version until the pointer moves.
func (l *Loader) Follow(ctx context.Context, w Watcher, debounce time.Duration, onReject func(Rejection)) error {
	moves, err := w.Watch(ctx)
	if err != nil {
		return fmt.Errorf("content: follow: %w", err)
	}
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	pending := map[string]uint64{}
	type retry struct {
		version uint64
		backoff time.Duration
		at      time.Time
	}
	retries := map[string]retry{}

	var timer *time.Timer
	var fire <-chan time.Time
	var retryTimer *time.Timer
	var retryFire <-chan time.Time

	rearm := func() {
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer, retryFire = nil, nil
		}
		var next time.Time
		for _, r := range retries {
			if next.IsZero() || r.at.Before(next) {
				next = r.at
			}
		}
		if !next.IsZero() {
			retryTimer = time.NewTimer(max(time.Until(next), 0))
			retryFire = retryTimer.C
		}
	}
	// What reconcile could not load for the store's sake: the watch starts
	// past the pointers that named them, so nothing else would retry them.
	l.mu.Lock()
	for p, v := range l.startRetries {
		retries[p] = retry{version: v, backoff: retryInitial, at: time.Now().Add(retryInitial)}
	}
	l.startRetries = map[string]uint64{}
	l.mu.Unlock()
	rearm()
	report := func(rejects []Rejection, backoff map[string]time.Duration) {
		for _, r := range rejects {
			if onReject != nil {
				onReject(r)
			}
			if r.Reason == ReasonStoreUnavailable {
				b := backoff[r.Pack]
				if b == 0 {
					b = retryInitial
				} else {
					b = min(b*2, retryMax)
				}
				retries[r.Pack] = retry{version: r.Version, backoff: b, at: time.Now().Add(b)}
			}
		}
		rearm()
	}
	apply := func(batch map[string]uint64, backoff map[string]time.Duration) {
		// A held version the batch supersedes is dropped first: core in the
		// same batch would otherwise release it, and swap an obsolete
		// intermediate version into the World before the newer one.
		for p, v := range batch {
			if p != CorePack {
				l.supersedeHeld(p, v)
			}
		}
		packs := make([]string, 0, len(batch))
		for p := range batch {
			packs = append(packs, p)
		}
		sort.Strings(packs)
		// Core first: a burst that moves core and a Builder pack together must
		// evaluate the Builder pack against the new core, not the old one.
		if _, ok := batch[CorePack]; ok {
			packs = append([]string{CorePack}, without(packs, CorePack)...)
		}
		for _, p := range packs {
			report(l.Apply(ctx, PointerMove{Pack: p, Version: batch[p]}), backoff)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m, ok := <-moves:
			if !ok {
				return nil
			}
			if l.follows(m.Pack) {
				// The freshness SLI counts from the move being read, not from
				// the end of the debounce or of a load in flight.
				l.moved(m.Pack, m.Version)
			}
			pending[m.Pack] = m.Version
			// A newer move supersedes a retry of the version it replaces.
			delete(retries, m.Pack)
			rearm()
			if timer == nil {
				timer = time.NewTimer(debounce)
				fire = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounce)
				fire = timer.C
			}
		case <-fire:
			timer, fire = nil, nil
			batch := pending
			pending = map[string]uint64{}
			apply(batch, nil)
		case <-retryFire:
			retryTimer, retryFire = nil, nil
			due := map[string]uint64{}
			backoff := map[string]time.Duration{}
			now := time.Now()
			for p, r := range retries {
				if !r.at.After(now) {
					due[p] = r.version
					backoff[p] = r.backoff
					delete(retries, p)
				}
			}
			apply(due, backoff)
		}
	}
}

func without(ss []string, drop string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
