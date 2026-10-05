// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"testing"
	"time"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// Admin.VerifySnapshotRound answers a mismatch as a response and a refusal as
// a status (the pinned admin.proto).
func TestVerifyResponse_MapsEveryOutcome(t *testing.T) {
	h1, h2 := [32]byte{1}, [32]byte{2}
	rep := recovery.Report{Tick: 9000, Actual: h1, Expected: h1}
	resp := func(err error) *adminv1.VerifySnapshotRoundResponse {
		t.Helper()
		r, rerr := verifyResponse(recovery.Classify(err), rep, 4200, 2, time.Minute)
		if rerr != nil {
			t.Fatalf("%v: refused with %v", err, rerr)
		}
		return r
	}
	if r, err := verifyResponse(nil, rep, 4200, 2, time.Minute); err != nil || !r.GetMatch() || r.GetOutcome() != adminv1.VerifyOutcome_VERIFY_OUTCOME_MATCH || r.GetComparedTick() != 9000 {
		t.Fatalf("match: %v %v", r, err)
	}
	if r := resp(&recovery.HashMismatchError{Tick: 4321, Expected: h1, Actual: h2}); r.GetMatch() || r.GetOutcome() != adminv1.VerifyOutcome_VERIFY_OUTCOME_REPLAY_MISMATCH || r.GetComparedTick() != 4321 || r.GetActualHash()[0] != 2 {
		t.Errorf("replay mismatch: %v", r)
	}
	if r := resp(&sim.RestoreMismatch{RoundTick: 4200, Recorded: []byte{1}, Restored: []byte{2}}); r.GetOutcome() != adminv1.VerifyOutcome_VERIFY_OUTCOME_RESTORE_MISMATCH || r.GetComparedTick() != 4200 {
		t.Errorf("restore mismatch: %v", r)
	}
	if r := resp(&sim.SeedMismatch{RoundTick: 4200, Recorded: 7, Configured: 8}); r.GetOutcome() != adminv1.VerifyOutcome_VERIFY_OUTCOME_SEED_MISMATCH || r.GetRecordedSeed() != 7 || r.GetConfiguredSeed() != 8 || r.GetComparedTick() != 0 {
		t.Errorf("seed mismatch: %v", r)
	}
	if r := resp(&sim.ContentDigestError{}); r.GetOutcome() != adminv1.VerifyOutcome_VERIFY_OUTCOME_CONTENT_MISMATCH {
		t.Errorf("content mismatch: %v", r)
	}
	if r := resp(&sim.ErrRoundZoneUnknown{Tick: 4200, Zone: "x"}); r.GetOutcome() != adminv1.VerifyOutcome_VERIFY_OUTCOME_CONTENT_MISMATCH {
		t.Errorf("unknown zone: %v", r)
	}
}

func TestVerifyResponse_RefusalsNameTheirStatus(t *testing.T) {
	rep := recovery.Report{}
	for _, c := range []struct {
		name   string
		err    error
		code   uint32
		reason string
	}{
		{"no object at all", &sim.ErrRoundIncomplete{Tick: 7, Cause: sim.RoundMissing, Zones: []sim.ZoneID{"a", "b"}}, codeNotFound, "round_not_found"},
		{"one zone missing", &sim.ErrRoundIncomplete{Tick: 7, Cause: sim.RoundMissing, Zones: []sim.ZoneID{"a"}}, codeFailedPrecondition, "round_incomplete"},
		{"duplicate", &sim.ErrRoundZoneDuplicate{Tick: 7, Zone: "a"}, codeFailedPrecondition, "round_incomplete"},
		{"hash", &sim.ErrRoundIncomplete{Tick: 7, Cause: sim.RoundHash, Zones: []sim.ZoneID{"a", "b"}}, codeFailedPrecondition, "round_incomplete"},
		{"log gap", &tickloop.LogGapError{Topic: "t", Partition: 1, Need: 5, Have: 9}, codeFailedPrecondition, "log_gap"},
		{"newer version", &sim.ErrStateVersion{Have: 3, Want: 2}, codeFailedPrecondition, "state_version"},
		{"timeout", context.DeadlineExceeded, codeDeadlineExceeded, "verify_timeout"},
	} {
		_, err := verifyResponse(recovery.Classify(c.err), rep, 7, 2, time.Minute)
		var ss *snapshotStatus
		if !errors.As(err, &ss) || ss.code != c.code || ss.reason != c.reason {
			t.Errorf("%s: %v, want code %d reason %s", c.name, err, c.code, c.reason)
		}
	}
	if _, err := verifyResponse(recovery.Classify(errors.New("store down")), rep, 7, 2, time.Minute); err == nil {
		t.Error("an unclassified failure must be an error")
	} else if errors.As(err, new(*snapshotStatus)) {
		t.Errorf("an unclassified failure must not name a status: %v", err)
	}
}

func TestScratchOptions_ReportToNothingLive(t *testing.T) {
	live := recovery.NewMetrics(nil)
	o := recovery.Options{Metrics: live, After: func(sim.StepResult) error { return nil }, OnEngine: func(*sim.Engine) {}, OnRestored: func([]sim.SwapApplied) {}}
	s := scratchOptions(o, 4200, live)
	if s.Metrics == live || s.Restores != live.Restores || s.After != nil || s.OnEngine != nil || s.OnRestored != nil || !s.Verify || s.Round == nil || *s.Round != 4200 {
		t.Fatalf("scratch options %+v", s)
	}
}
