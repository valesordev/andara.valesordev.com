// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.opentelemetry.io/otel/attribute"

	"github.com/valesordev/andara/server/sim"
)

// The reasons an activation is refused before the pointer moves (AW-SRV-013
// AC-14). They're the Loader's own, so a Builder hears at activation what the
// swap would have refused after the pointer moved.
const (
	RefusalZoneRemoved      = string(sim.ErrZoneRemoved)
	RefusalSpawnRoomRemoved = string(sim.ErrSpawnRoomRemoved)
	RefusalCoreVersion      = ReasonCoreVersion
	// RefusalValidation is a version the World in effect would refuse for its
	// findings: valid when published, and made invalid since by another
	// pack's activation. Not one of AC-14's reasons; see
	// docs/feedback/AW-SRV-013-publish-path.md, For architecture 3.
	RefusalValidation = ReasonValidation
)

// Refusal is why an activation would be refused.
type Refusal struct {
	Reason string
	// Subjects name what the refusal is about: the Zones removed, the spawn
	// Room as zone/room, or each pack@version a core move would strand.
	Subjects []string
	// Findings are the refusing findings, for RefusalValidation.
	Findings []sim.ValidationError
	Err      error
}

func (r *Refusal) Error() string { return r.Err.Error() }

// CheckPublish is the publish gate (AW-SRV-013 AC-1): the validator run over
// a version about to be published, against the packs in effect other than
// its own. The transitions a version is judged on at activation are left
// out, because at publish there's no transition yet: a version that removes
// a Zone is a legal version, and refusing to activate it is AC-14's.
//
// It returns the findings that refuse the version and the warnings that
// don't, in the loader's taxonomy, so the same content gets the same
// findings here, at boot, and at load.
func (l *Loader) CheckPublish(ctx context.Context, candidate *Resolved) (refusing, warnings []sim.ValidationError) {
	base := l.servingSnapshot()
	delete(base, candidate.Pack)
	vctx, span := l.tracer.Start(ctx, "content.validate")
	defer span.End()
	_, refusing, warnings = l.build(vctx, base, candidate, false)
	span.SetAttributes(attribute.Int("error_count", len(refusing)), attribute.Int("warning_count", len(warnings)))
	return refusing, warnings
}

// CheckActivation decides pack@version against the World in effect, exactly
// as the Loader will when the pointer moves, and returns nil if it would be
// accepted. A store that can't answer is an error, not a refusal.
func (l *Loader) CheckActivation(ctx context.Context, pack string, version uint64) (*Refusal, error) {
	base := l.servingSnapshot()
	_, _, rej := l.evaluate(ctx, pack, version, base)
	if rej == nil {
		return nil, nil
	}
	err := rej.Err
	if IsStoreFault(err) {
		return nil, err
	}
	var (
		cr *ErrCoreRollback
		cv *ErrCoreVersion
	)
	switch {
	case errors.As(err, &cr):
		subjects := make([]string, 0, len(cr.Holding))
		for _, h := range cr.Holding {
			v := uint64(0)
			if r := base[h.Pack]; r != nil {
				v = r.Version
			}
			subjects = append(subjects, fmt.Sprintf("%s@%d", h.Pack, v))
		}
		sort.Strings(subjects)
		return &Refusal{Reason: RefusalCoreVersion, Subjects: subjects, Err: err}, nil
	case errors.As(err, &cv):
		// A pack compiled against a core newer than the one in effect.
		return &Refusal{Reason: RefusalCoreVersion, Subjects: []string{fmt.Sprintf("%s@%d", CorePack, cv.Compiled)}, Err: err}, nil
	}
	findings := Findings(err)
	var zones, spawn []string
	for _, f := range findings {
		switch f.Code {
		case sim.ErrZoneRemoved:
			zones = append(zones, string(f.Zone))
		case sim.ErrSpawnRoomRemoved:
			spawn = append(spawn, fmt.Sprintf("%s/%s", f.Zone, f.Room))
		}
	}
	switch {
	case len(zones) > 0:
		sort.Strings(zones)
		return &Refusal{Reason: RefusalZoneRemoved, Subjects: zones, Findings: findings, Err: err}, nil
	case len(spawn) > 0:
		return &Refusal{Reason: RefusalSpawnRoomRemoved, Subjects: spawn, Findings: findings, Err: err}, nil
	}
	return &Refusal{Reason: RefusalValidation, Findings: findings, Err: err}, nil
}
