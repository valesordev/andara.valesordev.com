// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// CorePack is the pack every other pack compiles against (ADR-0010). Its
// version gates every Builder pack: a pack pins the core it was compiled
// against, and a pack compiled against a core newer than the one running is
// held rather than refused for good (AC-8).
const CorePack = "andara.core"

// AllPacks is the content.packs value that means "follow every Active Pointer".
const AllPacks = "*"

// Loader owns the retained-version rule.
//
// The rule in one sentence: a version that does not load changes nothing. Not
// the World, not the previous version's place in it, not the process. That is
// what makes a Builder's mistake a Builder's problem instead of an outage, and
// it is why every method here reports a rejection and leaves `serving`
// untouched rather than returning an error that some caller might treat as
// fatal.
//
// The one exception is a boot with nothing loadable, which Runtime turns into
// exit 1 — there is no previous version to retain.
type Loader struct {
	store   Store
	packs   []string
	metrics *Metrics
	log     *slog.Logger
	// strictOrphans promotes orphan_room from a warning to a refusal, the
	// same policy knob the dir loader has (content.strict_orphans).
	strictOrphans bool

	mu sync.Mutex
	// serving is what each pack is showing the World right now.
	serving map[string]*Resolved
	// held is a version that resolved cleanly but was refused for core skew,
	// waiting for core to catch up. Re-evaluated on every core pointer move,
	// which is what makes AC-8 a state machine rather than a check.
	held map[string]uint64
}

// LoaderOptions configures a Loader.
type LoaderOptions struct {
	Store Store
	// Packs to follow. Empty means andara.core alone; a single "*" means
	// every Active Pointer on the topic.
	Packs         []string
	Metrics       *Metrics
	Log           *slog.Logger
	StrictOrphans bool
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
	return &Loader{
		store:         o.Store,
		packs:         packs,
		metrics:       m,
		log:           l,
		strictOrphans: o.StrictOrphans,
		serving:       map[string]*Resolved{},
		held:          map[string]uint64{},
	}
}

// Rejection is one refused version, with the reason and the findings.
type Rejection struct {
	Pack    string
	Version uint64
	Reason  string
	Err     error
}

// LoadAll resolves every followed pack at its Active Pointer.
//
// Core is resolved first and on its own, because every other pack's acceptance
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

	wanted := l.wanted(active)
	var rejects []Rejection
	// Core first, then the rest in name order so findings come out the same
	// way on every boot.
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

	for _, pack := range order {
		if active[pack] == 0 {
			// A configured pack with no Active Pointer is not an error — it
			// may not have been published yet — but it is never what the
			// operator meant, and silence here looks exactly like a pack that
			// loaded fine.
			l.log.Warn("configured content pack has no Active Pointer; nothing to load",
				"pack", pack, "topic", TopicActive)
			continue
		}
		if r := l.load(ctx, pack, active[pack]); r != nil {
			rejects = append(rejects, *r)
		}
	}
	return rejects, nil
}

// Apply handles one Active Pointer move. A rejection is returned, not raised:
// the caller logs it and the World carries on with what it had.
func (l *Loader) Apply(ctx context.Context, move PointerMove) []Rejection {
	if !l.follows(move.Pack) {
		return nil
	}
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

// load resolves one pack at one version and accepts or refuses it.
func (l *Loader) load(ctx context.Context, pack string, version uint64) *Rejection {
	if version == 0 {
		return nil // no Active Pointer for this pack; nothing to load
	}
	start := time.Now()
	res, err := Resolve(ctx, l.store, pack, version)
	l.metrics.LoadDuration.WithLabelValues(PhaseResolve).Observe(time.Since(start).Seconds())
	if err != nil {
		return l.reject(pack, version, err)
	}

	// Core skew (AC-8). Checked after resolution rather than before, because
	// the pinned core version is in the manifest, and because a pack that is
	// also malformed should say so rather than waiting for a core it would be
	// refused against anyway.
	if pack != CorePack {
		if core, ok := l.servingVersion(CorePack); ok && res.CoreVersion > core {
			l.hold(pack, version)
			return l.reject(pack, version, &ErrCoreVersion{
				Pack: pack, Compiled: res.CoreVersion, Active: core,
			})
		}
	} else if stranded, pin := l.strandedBy(res.Version); stranded != "" {
		// Core moving *backwards* is the same compatibility question asked
		// from the other side. Accepting it would leave the World serving a
		// combination the loader would refuse to assemble from scratch: a
		// pack pinned to core@4 on top of core@3. The Loader has no way to
		// unload a pack — every path it has either accepts a version or
		// retains the previous one — so the consistent answer is to retain
		// core and say which pack is holding it there.
		return l.reject(pack, version, &ErrCoreRollback{
			From: mustServing(l, CorePack), To: res.Version, Pack: stranded, Pin: pin,
		})
	}

	// Validate the World this version would produce, together with every other
	// pack that is serving. A pack is not validated alone: an Exit from town
	// into a Zone that only core declares is valid only in the whole.
	vstart := time.Now()
	if findings := l.validateWith(res); len(findings) > 0 {
		l.metrics.LoadDuration.WithLabelValues(PhaseValidate).Observe(time.Since(vstart).Seconds())
		return l.reject(pack, version, &ErrValidation{Findings: findings})
	}
	l.metrics.LoadDuration.WithLabelValues(PhaseValidate).Observe(time.Since(vstart).Seconds())

	l.mu.Lock()
	l.serving[pack] = res
	delete(l.held, pack)
	l.mu.Unlock()
	l.metrics.ActiveVersion.WithLabelValues(pack).Set(float64(version))
	l.log.Info("content loaded",
		"pack", pack, "version", version, "core_version", res.CoreVersion,
		"zones", len(res.Zones), "templates", len(res.Templates),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

// validateWith builds the World that `candidate` plus everything already
// serving would produce, and returns the findings that refuse it.
func (l *Loader) validateWith(candidate *Resolved) []sim.ValidationError {
	zones, templates := l.inputsWith(candidate)
	var findings []sim.ValidationError

	// A version that would leave the World with no Zones at all, when the
	// World currently has some, is refused. Everyone standing in it would be
	// standing nowhere, and the previous version is right there. This is a
	// rule about the *transition*: a pack with no Zones is perfectly legal on
	// its own — andara.core is exactly that, Templates and nothing else —
	// which is why the test is against what is serving rather than against
	// the candidate alone.
	if len(zones) == 0 && l.servingZones() > 0 {
		return []sim.ValidationError{{
			Code: sim.ErrEmptyContent,
			Detail: fmt.Sprintf("%s@%d would leave the World with no Zones; the previous version keeps serving",
				candidate.Pack, candidate.Version),
		}}
	}

	bstart := time.Now()
	if len(templates) > 0 {
		if _, errs := sim.BuildTemplates(templates, sim.TemplateOptions{}); len(errs) > 0 {
			findings = append(findings, l.fatalOnly(errs)...)
		}
	}
	if len(zones) > 0 {
		opts := sim.Options{Source: SourceKafka, StrictOrphans: l.strictOrphans}
		if _, errs := sim.BuildWorld(zones, opts); len(errs) > 0 {
			findings = append(findings, l.fatalOnly(errs)...)
		}
	}
	l.metrics.LoadDuration.WithLabelValues(PhaseBuild).Observe(time.Since(bstart).Seconds())
	return findings
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

// inputsWith returns every serving pack's inputs, with candidate standing in
// for its own pack's current version.
func (l *Loader) inputsWith(candidate *Resolved) ([]sim.Input, []sim.TemplateInput) {
	l.mu.Lock()
	defer l.mu.Unlock()
	packs := make([]string, 0, len(l.serving)+1)
	for p := range l.serving {
		packs = append(packs, p)
	}
	if candidate != nil {
		if _, ok := l.serving[candidate.Pack]; !ok {
			packs = append(packs, candidate.Pack)
		}
	}
	sort.Strings(packs)

	var (
		zones     []sim.Input
		templates []sim.TemplateInput
	)
	for _, p := range packs {
		res := l.serving[p]
		if candidate != nil && p == candidate.Pack {
			res = candidate
		}
		if res == nil {
			continue
		}
		zones = append(zones, res.Zones...)
		templates = append(templates, res.Templates...)
	}
	return zones, templates
}

// Inputs returns what every serving pack contributes to the World, in pack
// name order so the build is deterministic.
func (l *Loader) Inputs() ([]sim.Input, []sim.TemplateInput) {
	return l.inputsWith(nil)
}

// Versions reports the version each pack is serving.
func (l *Loader) Versions() map[string]uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]uint64, len(l.serving))
	for p, r := range l.serving {
		out[p] = r.Version
	}
	return out
}

// ZoneVersions names, for each Zone being served, the packID@version it came
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

// strandedBy reports the first retained pack, in name order, that pinned a
// core version newer than the candidate core, and the version it pinned.
func (l *Loader) strandedBy(core uint64) (string, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	packs := make([]string, 0, len(l.serving))
	for p := range l.serving {
		if p != CorePack {
			packs = append(packs, p)
		}
	}
	sort.Strings(packs)
	for _, p := range packs {
		if l.serving[p].CoreVersion > core {
			return p, l.serving[p].CoreVersion
		}
	}
	return "", 0
}

// servingZones counts the Zones the World currently has.
func (l *Loader) servingZones() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.serving {
		n += len(r.Zones)
	}
	return n
}

func mustServing(l *Loader, pack string) uint64 {
	v, _ := l.servingVersion(pack)
	return v
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

func (l *Loader) reject(pack string, version uint64, err error) *Rejection {
	reason := Reason(err)
	l.metrics.LoadFailures.WithLabelValues(reason).Inc()
	// A store fault and a refused version are both "this version is not
	// serving", but they are not the same message. Telling a Builder their
	// pack was refused when the broker was unreachable sends them looking for
	// a mistake they did not make.
	msg := "content version refused; the previous version keeps serving"
	if IsStoreFault(err) {
		msg = "content could not be read from the store; the previous version keeps serving"
	}
	l.log.Error(msg,
		"pack", pack, "version", version, "reason", reason, "error", err.Error())
	for _, f := range Findings(err) {
		l.log.Error("content finding", "pack", pack, "version", version,
			"file", f.File, "line", f.Line, "code", string(f.Code), "detail", f.Detail)
	}
	return &Rejection{Pack: pack, Version: version, Reason: reason, Err: err}
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

// Follow watches for Active Pointer moves and applies them, debounced.
//
// The debounce coalesces a burst: publishing a pack is several writes in quick
// succession when several packs are activated together, and resolving on each
// one would rebuild the World once per write. It also bounds how fast a
// flapping pointer can make the server work.
func (l *Loader) Follow(ctx context.Context, w Watcher, debounce time.Duration, onReject func(Rejection)) error {
	moves, err := w.Watch(ctx)
	if err != nil {
		return fmt.Errorf("content: follow: %w", err)
	}
	if debounce <= 0 {
		debounce = 2 * time.Second
	}
	pending := map[string]uint64{}
	var timer *time.Timer
	var fire <-chan time.Time

	flush := func() {
		packs := make([]string, 0, len(pending))
		for p := range pending {
			packs = append(packs, p)
		}
		sort.Strings(packs)
		// Core first: a burst that moves core and a Builder pack together must
		// evaluate the Builder pack against the new core, not the old one.
		if _, ok := pending[CorePack]; ok {
			packs = append([]string{CorePack}, without(packs, CorePack)...)
		}
		for _, p := range packs {
			for _, r := range l.Apply(ctx, PointerMove{Pack: p, Version: pending[p]}) {
				if onReject != nil {
					onReject(r)
				}
			}
		}
		pending = map[string]uint64{}
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
			flush()
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
