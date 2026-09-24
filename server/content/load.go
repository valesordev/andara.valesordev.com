// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"log/slog"
	"time"

	"github.com/valesordev/andara/server/sim"
)

// Source names where content comes from.
const (
	SourceKafka = "kafka"
	SourceDir   = "dir"
)

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
	Metrics       *Metrics
	Log           *slog.Logger
}

// Content is the loaded content, whichever source it came from. It exists so
// the boot path asks one question — "what is in the World?" — rather than
// branching on the source twice, once for Zones and once for Templates.
//
// The Kafka source resolves once, in Open. Zones and Templates then read from
// that one resolution rather than resolving again, which is what keeps a boot
// to a single pass over the content store and keeps the two halves of a World
// from being read at two different pointer values.
type Content struct {
	opts     Options
	loader   *Loader
	resolver *KafkaResolver

	zones     []sim.Input
	templates []sim.TemplateInput
}

// Open reads the configured content source. The returned findings are fatal
// unless they are warnings; a Kafka source with no loadable version at all is
// ErrEmptyContent, because there is no previous version to retain at boot.
func Open(ctx context.Context, o Options) (*Content, []sim.ValidationError) {
	switch o.Source {
	case SourceDir:
		return &Content{opts: o}, nil
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
	m := o.Metrics
	if m == nil {
		m = NewMetrics(nil)
	}
	resolver, err := NewKafkaResolver(KafkaOptions{
		Brokers:      o.Brokers,
		Cache:        BlobCache{Dir: o.CacheDir},
		MaxBlobBytes: o.MaxBlobBytes,
		Metrics:      m,
	})
	if err != nil {
		return nil, []sim.ValidationError{{Code: sim.ErrMalformed, Detail: err.Error()}}
	}
	loader := NewLoader(LoaderOptions{
		Store:         resolver,
		Packs:         o.Packs,
		Metrics:       m,
		Log:           o.Log,
		StrictOrphans: o.StrictOrphans,
	})

	c := &Content{opts: o, loader: loader, resolver: resolver}
	rejects, err := loader.LoadAll(ctx)
	if err != nil {
		// The store itself is unreachable. Distinct from a refused version:
		// nothing was rejected, nothing was read.
		return c, []sim.ValidationError{{Code: sim.ErrMalformed,
			Detail: "cannot read the content store: " + err.Error()}}
	}
	c.zones, c.templates = loader.Inputs()

	var findings []sim.ValidationError
	for _, r := range rejects {
		findings = append(findings, Findings(r.Err)...)
	}
	if len(c.zones) == 0 {
		// AC-1's failure case and the story's boot rule: a boot with nothing
		// loadable exits 1 naming the reason, because there is no previous
		// version to fall back to. Any rejection findings already say why.
		findings = append(findings, sim.ValidationError{
			Code:   sim.ErrEmptyContent,
			Detail: "no Zones were found in kafka: no followed pack has a loadable Active Pointer",
		})
	}
	return c, findings
}

// Zones returns the Zone Definitions to build the World from.
func (c *Content) Zones() ([]sim.Input, []sim.ValidationError) {
	if c.opts.Source == SourceDir {
		return LoadDir(c.opts.Path)
	}
	return c.zones, nil
}

// Templates returns the flattened Templates to register (AW-SRV-022).
func (c *Content) Templates() ([]sim.TemplateInput, []sim.ValidationError) {
	if c.opts.Source == SourceDir {
		return LoadTemplatesDir(c.opts.Path)
	}
	return c.templates, nil
}

// Versions reports the content version each pack is serving, for
// andara_build_info and Admin.GetServerInfo. Empty for the dir source, which
// has no versions: a directory is whatever is in it.
func (c *Content) Versions() map[string]uint64 {
	if c.loader == nil {
		return nil
	}
	return c.loader.Versions()
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
