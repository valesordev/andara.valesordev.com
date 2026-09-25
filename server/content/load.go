// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// Source names where content comes from.
const (
	SourceKafka = "kafka"
	SourceDir   = "dir"
)

// DirPack is the pack ID a content.source=dir World is recorded under. A
// directory is one unit of content with no versions, so its ContentSwap
// carries version 0 and the digest, and a directory changed while the server
// was down halts recovery on that digest rather than replaying silently over
// different content (AW-SRV-012, 2026-09-25).
const DirPack = "dir"

// Options is everything the content adapters need, flattened out of the
// content.* configuration keys the way store.Options is out of snapshot.*.
type Options struct {
	Source string // content.source
	Path   string // content.path, dir source only

	// Kafka source.
	Brokers      []string      // the broker list the rest of the server uses
	Packs        []string      // content.packs; a single "*" follows every pointer
	CacheDir     string        // content.cache_dir
	MaxBlobBytes int64         // content.max_blob_bytes
	Debounce     time.Duration // content.reload_debounce

	StrictOrphans bool
	// SpawnRoom is character.spawn_room: a version whose World lacks it,
	// when the World in effect has it, is refused (spawn_room_removed).
	SpawnRoom sim.RoomRef
	Metrics   *Metrics
	Log       *slog.Logger
	Tracer    trace.Tracer
}

// Content is the content source, whichever it is: what the World would run on
// (Candidates, for the boot's validation), how a ContentSwap's topology is
// prepared (sim.ContentSource, live and on replay), what is in effect
// (Applied, Versions), and how the World is brought to what the source names
// (Reconcile, Follow). The log is the source of the content in effect, so
// every one of those goes through a swap.
type Content struct {
	opts     Options
	loader   *Loader
	resolver *KafkaResolver
	dir      *dirContent
}

// Open opens the configured content source. It reads no content: the boot
// asks for Candidates, recovery prepares what the log names, and Reconcile
// brings the World to the source.
func Open(ctx context.Context, o Options) (*Content, []sim.ValidationError) {
	if o.Metrics == nil {
		o.Metrics = NewMetrics(nil)
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	switch o.Source {
	case SourceDir:
		return &Content{opts: o, dir: &dirContent{opts: o, applied: make(chan struct{}), refused: make(chan sim.SwapRefused, 1)}}, nil
	case SourceKafka:
		return openKafka(ctx, o)
	default:
		return nil, []sim.ValidationError{{
			Code:   sim.ErrMalformed,
			Detail: `content.source "` + o.Source + `" is not kafka or dir`,
		}}
	}
}

func openKafka(ctx context.Context, o Options) (*Content, []sim.ValidationError) {
	resolver, err := NewKafkaResolver(KafkaOptions{
		Brokers:      o.Brokers,
		Cache:        BlobCache{Dir: o.CacheDir},
		MaxBlobBytes: o.MaxBlobBytes,
		Metrics:      o.Metrics,
		Log:          o.Log,
	})
	if err != nil {
		return nil, []sim.ValidationError{{Code: sim.ErrMalformed, Detail: err.Error()}}
	}
	loader := NewLoader(LoaderOptions{
		Store:         resolver,
		Packs:         o.Packs,
		Metrics:       o.Metrics,
		Log:           o.Log,
		Tracer:        o.Tracer,
		StrictOrphans: o.StrictOrphans,
		SpawnRoom:     o.SpawnRoom,
		// The bounded wait for a swap to apply (review of #88).
		ApplyWait: applyWait(o.Debounce),
	})
	c := &Content{opts: o, loader: loader, resolver: resolver}
	// Pin the pointer topic before reading it. A pointer moved while the
	// boot's reconcile is in flight has to be seen by the watch afterwards,
	// and a watch that only takes its position when it first polls would
	// treat that move as history.
	if err := resolver.Pin(ctx); err != nil {
		return c, []sim.ValidationError{{Code: sim.ErrMalformed,
			Detail: "cannot read the content store: " + err.Error()}}
	}
	return c, nil
}

// Candidates is what the World would run on if the source were brought into
// effect now, validated, for the boot's findings and --validate-only. For
// Kafka a refused pack is logged and skipped, and only a source with no Zones
// at all is fatal: loadFindings.
func (c *Content) Candidates(ctx context.Context) ([]sim.Input, []sim.TemplateInput, []sim.ValidationError) {
	if c.dir != nil {
		zones, zerrs := LoadDir(c.opts.Path)
		templates, terrs := LoadTemplatesDir(c.opts.Path)
		return zones, templates, append(zerrs, terrs...)
	}
	zones, templates, rejects, err := c.loader.Candidates(ctx)
	if err != nil {
		// The store itself is unreachable. Distinct from a refused version:
		// nothing was rejected, nothing was read.
		return nil, nil, []sim.ValidationError{{Code: sim.ErrMalformed,
			Detail: "cannot read the content store: " + err.Error()}}
	}
	return zones, templates, loadFindings(rejects, len(zones))
}

// loadFindings decides which boot findings a set of per-pack rejections
// produces.
//
// The answer is usually none. Runtime.LoadContent exits 1 on any fatal
// finding, and every finding a rejection carries is fatal — so returning them
// would mean one malformed Builder pack takes the whole server down, undoing
// at the boot layer exactly what the Loader's retained-version rule
// guarantees. The rejections are not lost: the Loader logs each one, with its
// findings, at error.
//
// The one case that is fatal is a boot with no Zones at all. There is no
// previous version to retain, and a World with nowhere to stand is not a World
// (AW-SRV-001). Then the rejections are worth returning too, because they say
// why there is nothing.
func loadFindings(rejects []Rejection, zones int) []sim.ValidationError {
	if zones > 0 {
		return nil
	}
	var findings []sim.ValidationError
	for _, r := range rejects {
		findings = append(findings, Findings(r.Err)...)
	}
	return append(findings, sim.ValidationError{
		Code:   sim.ErrEmptyContent,
		Detail: "no Zones were found in kafka: no followed pack has a loadable Active Pointer",
	})
}

// SetProducer sets where ContentSwaps are written: the Gateway's producer.
func (c *Content) SetProducer(p SwapProducer) {
	if c.dir != nil {
		c.dir.setProducer(p)
		return
	}
	c.loader.SetProducer(p)
}

// SetBarrier sets how a load waits for the World Partition to be consumed
// past its current end: before reconcile decides anything, and after a swap
// whose produce had an unknown outcome.
func (c *Content) SetBarrier(b func(context.Context) error) {
	if c.dir != nil {
		c.dir.mu.Lock()
		c.dir.barrier = b
		c.dir.mu.Unlock()
		return
	}
	c.loader.SetBarrier(b)
}

// Refused is the Engine reporting swaps it consumed and refused.
func (c *Content) Refused(refusals []sim.SwapRefused) {
	if len(refusals) == 0 {
		return
	}
	if c.dir != nil {
		c.dir.Refused(refusals)
		return
	}
	c.loader.Refused(refusals)
}

// Prepare implements sim.ContentSource.
func (c *Content) Prepare(inEffect map[string]uint64, swap *logv1.ContentSwap) (sim.Topology, error) {
	if c.dir != nil {
		return c.dir.Prepare(inEffect, swap)
	}
	return c.loader.Prepare(inEffect, swap)
}

// Applied is the Engine reporting a tick's swaps: those versions are now in
// effect. Called for replayed swaps as well as live ones.
func (c *Content) Applied(swaps []sim.SwapApplied) {
	if len(swaps) == 0 {
		return
	}
	if c.dir != nil {
		c.dir.Applied(swaps)
		return
	}
	c.loader.Applied(swaps)
}

// Reconcile brings the World to what the source names, through the log: on an
// empty log that is genesis, and after a restart it is whatever moved while
// the process was down. It returns when every accepted version is in effect.
func (c *Content) Reconcile(ctx context.Context) ([]Rejection, error) {
	if c.dir != nil {
		return nil, c.dir.Reconcile(ctx)
	}
	return c.loader.LoadAll(ctx)
}

// InEffect is the content in effect as the source last learned it from the
// Engine: the pack versions and their world_digest.
func (c *Content) InEffect() (map[string]uint64, [32]byte) {
	if c.dir != nil {
		c.dir.mu.Lock()
		defer c.dir.mu.Unlock()
		if !c.dir.inEffect {
			return nil, [32]byte{}
		}
		return map[string]uint64{DirPack: 0}, c.dir.digest
	}
	return c.loader.InEffect()
}

// Versions reports the content version each pack has in effect, for
// andara_build_info and Admin.GetServerInfo. Empty for the dir source, which
// has no versions: a directory is whatever is in it.
func (c *Content) Versions() map[string]uint64 {
	if c == nil {
		return nil
	}
	if c.dir != nil {
		return c.dir.versions()
	}
	return c.loader.Versions()
}

// ZoneVersions names the packID@version each Zone in effect came from. Empty
// for the dir source, for the reason Versions is.
func (c *Content) ZoneVersions() map[sim.ZoneID]string {
	if c == nil {
		return nil
	}
	if c.dir != nil {
		return c.dir.zoneVersions()
	}
	return c.loader.ZoneVersions()
}

// Follow watches the Active Pointer topic and applies moves until ctx ends.
// A no-op for the dir source, which has nothing to watch: reloading a
// directory under a running World is not a thing this story adds.
func (c *Content) Follow(ctx context.Context, onReject func(Rejection)) error {
	if c.loader == nil || c.resolver == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return c.loader.Follow(ctx, c.resolver, c.opts.Debounce, onReject)
}

// Close releases the content store.
func (c *Content) Close() error {
	if c.resolver == nil {
		return nil
	}
	return c.resolver.Close()
}

// dirContent is content.source=dir brought into effect through the log: one
// pack, DirPack, at version 0.
type dirContent struct {
	opts Options

	mu       sync.Mutex
	producer SwapProducer
	barrier  func(context.Context) error
	inEffect bool
	digest   [32]byte
	zones    []sim.ZoneID
	applied  chan struct{}
	refused  chan sim.SwapRefused
}

// versions is dir@0 once the directory is in effect, as andara_build_info
// reports it.
func (d *dirContent) versions() map[string]uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inEffect {
		return nil
	}
	return map[string]uint64{DirPack: 0}
}

// zoneVersions names dir@0 for every Zone of the directory in effect.
func (d *dirContent) zoneVersions() map[sim.ZoneID]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.inEffect {
		return nil
	}
	out := make(map[sim.ZoneID]string, len(d.zones))
	for _, z := range d.zones {
		out[z] = ManifestKey(DirPack, 0)
	}
	return out
}

// Refused hands a refusal of the genesis swap to Reconcile.
func (d *dirContent) Refused(refusals []sim.SwapRefused) {
	for _, r := range refusals {
		select {
		case d.refused <- r:
		default:
		}
	}
}

func (d *dirContent) setProducer(p SwapProducer) {
	d.mu.Lock()
	d.producer = p
	d.mu.Unlock()
}

// Prepare builds the directory as it is now. Recovery compares the digest the
// log recorded, so a directory that changed while the server was down halts
// it.
func (d *dirContent) Prepare(inEffect map[string]uint64, swap *logv1.ContentSwap) (sim.Topology, error) {
	if swap.GetPackId() != DirPack || swap.GetVersion() != 0 {
		return sim.Topology{}, fmt.Errorf("content.source=dir cannot prepare %s: the log was written with another content source", ManifestKey(swap.GetPackId(), swap.GetVersion()))
	}
	for p := range inEffect {
		if p != DirPack {
			return sim.Topology{}, fmt.Errorf("content.source=dir cannot build on %s in effect: the log was written with another content source", p)
		}
	}
	zones, zerrs := LoadDir(d.opts.Path)
	templates, terrs := LoadTemplatesDir(d.opts.Path)
	findings := fatal(append(zerrs, terrs...), d.opts.StrictOrphans)
	topo := sim.Topology{World: sim.EmptyWorld()}
	if len(templates) > 0 {
		reg, errs := sim.BuildTemplates(templates, sim.TemplateOptions{})
		findings = append(findings, fatal(errs, d.opts.StrictOrphans)...)
		topo.Templates = reg
	}
	if len(zones) > 0 {
		w, errs := sim.BuildWorld(zones, sim.Options{Source: SourceDir + ":" + d.opts.Path, StrictOrphans: d.opts.StrictOrphans})
		findings = append(findings, fatal(errs, d.opts.StrictOrphans)...)
		if w != nil {
			topo.World = w
		}
	}
	ids := make([]sim.ZoneID, 0, len(topo.World.Zones))
	for id := range topo.World.Zones {
		ids = append(ids, id)
	}
	d.mu.Lock()
	d.zones = ids
	d.mu.Unlock()
	if len(findings) > 0 {
		return sim.Topology{}, &ErrValidation{Findings: findings}
	}
	return topo, nil
}

// Applied records the directory in effect.
func (d *dirContent) Applied(swaps []sim.SwapApplied) {
	for _, s := range swaps {
		// The gauges move before Reconcile is released: serving means
		// applied, for /metrics too (review of #91).
		d.opts.Metrics.ActiveVersion.WithLabelValues(s.Pack).Set(float64(s.Version))
		d.opts.Metrics.buildInfo(map[string]uint64{s.Pack: s.Version})
		for _, r := range s.Relocations {
			d.opts.Metrics.Relocations.WithLabelValues(string(r.Zone)).Inc()
		}
		d.mu.Lock()
		first := !d.inEffect
		d.inEffect, d.digest = true, s.Digest
		d.mu.Unlock()
		if first {
			close(d.applied)
		}
		d.opts.Log.Info("content in effect", "pack", s.Pack, "version", s.Version,
			"path", d.opts.Path, "world_digest", fmt.Sprintf("%x", s.Digest[:8]))
	}
}

// Reconcile is genesis when nothing is in effect, and nothing otherwise:
// recovery already rebuilt the directory and checked its digest.
func (d *dirContent) Reconcile(ctx context.Context) error {
	d.mu.Lock()
	done, producer, barrier := d.inEffect, d.producer, d.barrier
	d.mu.Unlock()
	if !done && barrier != nil {
		// A genesis swap a previous process produced may be past the last
		// boundary: let it apply rather than produce a second. Bounded: a
		// frozen World Partition carries on to genesis, whose own bounded
		// wait ends the boot if nothing ever applies.
		bctx, cancel := context.WithTimeout(ctx, applyWait(d.opts.Debounce))
		err := barrier(bctx)
		cancel()
		if err != nil && ctx.Err() != nil {
			return err
		}
		d.mu.Lock()
		done = d.inEffect
		d.mu.Unlock()
	}
	if done {
		return nil
	}
	swap := &logv1.ContentSwap{PackId: DirPack, Version: 0}
	topo, err := d.Prepare(nil, swap)
	if err != nil {
		return err
	}
	digest := sim.ContentDigest(topo)
	swap.WorldDigest = digest[:]
	if producer == nil {
		return fmt.Errorf("content: no producer for the genesis swap")
	}
	if err := producer.ProduceSwap(ctx, &logv1.LoggedCommand{Command: &logv1.LoggedCommand_ContentSwap{ContentSwap: swap}}); err != nil {
		var pending *SwapPending
		if !errors.As(err, &pending) {
			return &ErrProduce{Pack: DirPack, Err: err}
		}
		select {
		case <-pending.Settled:
		case <-ctx.Done():
			return ctx.Err()
		}
		if out := pending.Outcome(); out != nil && !errors.Is(out, ErrSwapOutcomeUnknown) {
			return &ErrProduce{Pack: DirPack, Err: out}
		}
		// Written, or perhaps written: wait for it to apply. A genesis swap
		// that never landed leaves nothing in effect, and the bounded wait
		// below ends the boot rather than hanging it.
	}
	wait := time.NewTimer(applyWait(d.opts.Debounce))
	defer wait.Stop()
	select {
	case <-wait.C:
		return &ErrApplyTimeout{Pack: DirPack, Wait: applyWait(d.opts.Debounce)}
	case <-d.applied:
		return nil
	case r := <-d.refused:
		return &ErrSwapRefused{Pack: r.Pack, Version: r.Version, Reason: r.Reason, Detail: r.Detail}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// applyWait is content.reload_debounce × 15, the bounded wait for a swap to
// apply and for the World Partition to catch up; 30s when the debounce is
// unset.
func applyWait(debounce time.Duration) time.Duration {
	if debounce <= 0 {
		return 30 * time.Second
	}
	return 15 * debounce
}

func fatal(errs []sim.ValidationError, strict bool) []sim.ValidationError {
	var out []sim.ValidationError
	for _, e := range errs {
		if !sim.IsWarning(e, strict) {
			out = append(out, e)
		}
	}
	return out
}
