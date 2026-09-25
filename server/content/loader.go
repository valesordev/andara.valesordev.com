// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
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
	// waiting is a load waiting for its swap to apply, by pack@version.
	waiting map[string][]chan struct{}
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
}

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
		waiting:       map[string][]chan struct{}{},
		pointer:       map[string]uint64{},
		pendingSince:  map[string]time.Time{},
	}
	m.pending(ld.Pending)
	return ld
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
	start := time.Now()
	active, err := l.store.Active(ctx)
	if err != nil {
		return nil, err
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
		}
	}
	return rejects, nil
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

// load accepts or refuses pack@version and, accepted, brings it into effect:
// build off-tick, stage, produce the swap, and wait for the Engine to apply it.
func (l *Loader) load(ctx context.Context, pack string, version uint64) *Rejection {
	if version == 0 {
		return nil // no Active Pointer for this pack; nothing to load
	}
	if v, ok := l.servingVersion(pack); ok && v == version {
		l.settled(pack, version)
		return nil
	}
	ctx, span := l.tracer.Start(ctx, "content.load", trace.WithAttributes(
		attribute.String("pack", pack), attribute.Int64("version", int64(version))))
	defer span.End()

	start := time.Now()
	res, topo, rej := l.evaluate(ctx, pack, version, l.servingSnapshot())
	if rej != nil {
		if pack != CorePack {
			if _, skew := rej.Err.(*ErrCoreVersion); skew {
				l.hold(pack, version)
			}
		}
		return l.reject(*rej)
	}

	// Stage the topology under exactly the versions it assumes, so the Engine
	// preparing the swap picks up this build and not one for some other
	// combination that happens to digest alike.
	l.mu.Lock()
	after := servingVersions(l.serving)
	after[pack] = version
	key := stageKey(after)
	l.staged[key] = topo
	done := make(chan struct{})
	pv := ManifestKey(pack, version)
	l.waiting[pv] = append(l.waiting[pv], done)
	producer := l.producer
	l.mu.Unlock()

	digest := sim.ContentDigest(topo)
	cmd := &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: &logv1.ContentSwap{
		PackId: pack, Version: version, WorldDigest: digest[:],
	}}}
	var perr error
	if producer == nil {
		perr = fmt.Errorf("no producer: the swap has nowhere to go")
	} else {
		perr = producer.ProduceSwap(ctx, cmd)
	}
	if perr != nil {
		l.mu.Lock()
		delete(l.staged, key)
		l.unwait(pv, done)
		l.mu.Unlock()
		return l.reject(Rejection{Pack: pack, Version: version, Err: &ErrProduce{Pack: pack, Version: version, Err: perr}})
	}
	l.log.Info("content version accepted; swap produced",
		"pack", pack, "version", version, "core_version", res.CoreVersion,
		"zones", len(res.Zones), "templates", len(res.Templates),
		"world_digest", fmt.Sprintf("%x", digest[:8]),
		"duration_ms", time.Since(start).Milliseconds())

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		l.mu.Lock()
		l.unwait(pv, done)
		l.mu.Unlock()
		return nil
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
	topo, findings := l.build(vctx, base, res)
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
	bstart := time.Now()
	defer func() { l.metrics.LoadDuration.WithLabelValues(PhaseBuild).Observe(time.Since(bstart).Seconds()) }()

	topo := sim.Topology{World: sim.EmptyWorld()}
	var findings []sim.ValidationError
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
	return topo, findings
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
		delete(l.held, s.Pack)
		if l.pointer[s.Pack] == s.Version {
			delete(l.pendingSince, s.Pack)
		}
		pv := ManifestKey(s.Pack, s.Version)
		for _, ch := range l.waiting[pv] {
			close(ch)
		}
		delete(l.waiting, pv)
		l.prune()
		versions := servingVersions(l.serving)
		l.mu.Unlock()

		l.metrics.ActiveVersion.WithLabelValues(s.Pack).Set(float64(s.Version))
		l.metrics.buildInfo(versions)
		for _, r := range s.Relocations {
			l.metrics.Relocations.WithLabelValues(string(r.Zone)).Inc()
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
		for _, z := range s.Stranded {
			l.log.Warn("zone stranded: a content swap removed it while Entities stand in it; its state is kept and its Commands refused until content brings it back",
				"pack", s.Pack, "version", s.Version, "zone", string(z))
		}
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

func (l *Loader) unwait(pv string, done chan struct{}) {
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
