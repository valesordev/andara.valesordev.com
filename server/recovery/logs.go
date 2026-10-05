// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// logRefusal writes the one `error` line a refusal gets, with the fields its
// exit code names (AW-SRV-007's table): the Partition, the round's offset and
// the log's earliest for exit 3; the round's tick and cause for exit 7; both
// versions for exit 4. A hash mismatch is logged where it is caught, with the
// tick and both hashes.
func (o *Options) logRefusal(ctx context.Context, err error, traceID string) {
	f := Classify(err)
	attrs := []any{"exit_code", f.Exit, "reason", f.Reason, "detail", err.Error(), "trace_id", traceID}
	var (
		lg *tickloop.LogGapError
		ri *sim.ErrRoundIncomplete
		sv *sim.ErrStateVersion
	)
	switch {
	case errors.As(err, &lg):
		attrs = append(attrs, "partition", lg.Partition, "round_offset", lg.Need, "earliest_offset", lg.Have)
	case errors.As(err, &ri):
		zones := make([]string, len(ri.Zones))
		for i, z := range ri.Zones {
			zones[i] = string(z)
		}
		attrs = append(attrs, "round_tick", uint64(ri.Tick), "cause", ri.Cause, "zones", strings.Join(zones, ","))
	case errors.As(err, &sv):
		attrs = append(attrs, "state_version", sv.Have, "binary_state_version", sv.Want)
	}
	o.Log.ErrorContext(ctx, "recovery refused", attrs...)
}

// logRestoreMismatch is AC-13's one `error` line, `recovery restore mismatch`.
func (o *Options) logRestoreMismatch(ctx context.Context, err error, round sim.Tick, traceID string) {
	f := Classify(err)
	attrs := []any{"round_tick", uint64(round), "reason", f.Restore, "trace_id", traceID}
	var (
		rm *sim.RestoreMismatch
		sm *sim.SeedMismatch
		cd *sim.ContentDigestError
		zu *sim.ErrRoundZoneUnknown
		rc *sim.ErrRoundContent
	)
	switch {
	case errors.As(err, &rm):
		attrs = append(attrs, "recorded_hash", fmt.Sprintf("%x", rm.Recorded), "restored_hash", fmt.Sprintf("%x", rm.Restored))
	case errors.As(err, &sm):
		attrs = append(attrs, "recorded_seed", strconv.FormatUint(sm.Recorded, 10), "configured_seed", strconv.FormatUint(sm.Configured, 10))
	case errors.As(err, &cd):
		attrs = append(attrs, "pack", cd.Pack, "recorded_digest", fmt.Sprintf("%x", cd.Recorded), "built_digest", fmt.Sprintf("%x", cd.Built))
	case errors.As(err, &zu):
		attrs = append(attrs, "zone_id", string(zu.Zone))
	case errors.As(err, &rc):
		attrs = append(attrs, "pack_versions", packVersions(rc.Versions))
	default:
		attrs = append(attrs, "detail", err.Error())
	}
	o.Log.Log(ctx, slog.LevelError, "recovery restore mismatch", attrs...)
}

func packVersions(v map[string]uint64) string {
	pv := make([]string, 0, len(v))
	for p, n := range v {
		pv = append(pv, fmt.Sprintf("%s@%d", p, n))
	}
	sort.Strings(pv)
	return strings.Join(pv, ",")
}
