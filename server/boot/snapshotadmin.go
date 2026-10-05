// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// The gRPC status codes Admin's snapshot RPCs answer with
// (google.golang.org/grpc/codes), which connect's codes equal.
const (
	codeInvalidArgument    = 3
	codeDeadlineExceeded   = 4
	codeNotFound           = 5
	codeFailedPrecondition = 9
)

// snapshotStatus is a refusal that names its status, as the gateway's
// statusError matches it by shape.
type snapshotStatus struct {
	code   uint32
	reason string
	msg    string
}

func (e *snapshotStatus) Error() string { return e.msg }

// Status implements the gateway's statusError.
func (e *snapshotStatus) Status() (uint32, string, string, proto.Message) {
	return e.code, "andara.snapshot", e.reason, nil
}

// SnapshotAdmin is the Snapshot Round half of Admin (AW-SRV-007), over the
// store and the log the server is configured with. It never touches the live
// Engine: a verify restores a scratch one.
type SnapshotAdmin struct{ rt *Runtime }

// NewSnapshotAdmin builds the Admin seam for the gateway.
func (rt *Runtime) NewSnapshotAdmin() *SnapshotAdmin { return &SnapshotAdmin{rt: rt} }

// ListSnapshotRounds lists the rounds the store holds, newest first, judged
// against the Zones this process owns.
func (a *SnapshotAdmin) ListSnapshotRounds(ctx context.Context, req *adminv1.ListSnapshotRoundsRequest) (*adminv1.ListSnapshotRoundsResponse, error) {
	ws, err := a.rt.openSnapshotStore()
	if err != nil {
		return nil, fmt.Errorf("snapshot store: %w", err)
	}
	if ws == nil {
		return nil, &snapshotStatus{codeFailedPrecondition, "snapshots_disabled", "snapshots are disabled on this server (snapshot.interval is 0)"}
	}
	rounds, err := store.ListRounds(ctx, ws, a.rt.ownedZones())
	if err != nil {
		return nil, fmt.Errorf("list rounds: %w", err)
	}
	resp := &adminv1.ListSnapshotRoundsResponse{}
	for _, r := range rounds {
		zones := make([]string, 0, len(r.Zones))
		holds := req.GetZoneId() == ""
		for _, z := range r.Zones {
			zones = append(zones, string(z.Zone))
			if string(z.Zone) == req.GetZoneId() {
				holds = true
			}
		}
		if !holds {
			continue
		}
		resp.Rounds = append(resp.Rounds, &adminv1.SnapshotRound{
			Tick: uint64(r.Tick), StateVersion: r.StateVersion, ZoneIds: zones, Complete: r.Complete, TakenAtUnixNano: r.TakenAt,
		})
		if lim := int(req.GetLimit()); lim > 0 && len(resp.Rounds) >= lim {
			break
		}
	}
	return resp, nil
}

// VerifySnapshotRound restores one round into a scratch Engine, verifies it at
// its own tick, replays it to the log head, and compares. A mismatch is a
// response, not an error.
func (a *SnapshotAdmin) VerifySnapshotRound(ctx context.Context, req *adminv1.VerifySnapshotRoundRequest) (*adminv1.VerifySnapshotRoundResponse, error) {
	if req.GetTick() == 0 {
		return nil, &snapshotStatus{codeInvalidArgument, "tick_required", "tick must be above 0"}
	}
	rt := a.rt
	ctx, cancel := context.WithTimeout(ctx, rt.Cfg.RecoveryVerifyTimeout)
	defer cancel()
	inEffect := false
	o, release, err := rt.RecoverOptions(ctx, rt.EngineConfig(), &inEffect)
	if err != nil {
		return nil, err
	}
	defer release()
	if o.Store == nil {
		return nil, &snapshotStatus{codeFailedPrecondition, "snapshots_disabled", "snapshots are disabled on this server (snapshot.interval is 0)"}
	}
	tick := sim.Tick(req.GetTick())
	// A scratch recovery: it reports nothing to the live Engine, the content
	// source, or the recovery gauges that RecoveryStateMismatch pages on.
	o.Round, o.Verify = &tick, true
	o.After, o.OnEngine, o.OnRestored = nil, nil, nil
	o.Metrics = recovery.NewMetrics(nil)
	_, rep, rerr := recovery.Recover(ctx, o)
	resp, err := verifyResponse(rerr, rep, tick, len(rt.ownedZones()), rt.Cfg.RecoveryVerifyTimeout)
	// Counted as andara_restore_total{caller="verify"}: ok, or the restore's
	// own refusal.
	outcome := "ok"
	if rerr != nil {
		outcome = sim.RestoreOutcome(recovery.Classify(rerr).Err)
	}
	if outcome != "" {
		rt.RecoveryMetrics().Restores.WithLabelValues(recovery.CallerVerify, outcome).Inc()
	}
	return resp, err
}

// verifyResponse is what Admin.VerifySnapshotRound answers for a scratch
// recovery's result: a mismatch is a response, a refusal is a status.
func verifyResponse(rerr error, rep recovery.Report, tick sim.Tick, owned int, timeout time.Duration) (*adminv1.VerifySnapshotRoundResponse, error) {
	f := recovery.Classify(rerr)
	if f == nil {
		return &adminv1.VerifySnapshotRoundResponse{
			Match: true, Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_MATCH,
			ExpectedHash: rep.Expected[:], ActualHash: rep.Actual[:], ComparedTick: uint64(rep.Tick),
		}, nil
	}
	if errors.Is(rerr, context.DeadlineExceeded) {
		return nil, &snapshotStatus{codeDeadlineExceeded, "verify_timeout", fmt.Sprintf("verify exceeded recovery.verify_timeout (%s)", timeout)}
	}
	var (
		hm *recovery.HashMismatchError
		rm *sim.RestoreMismatch
		sm *sim.SeedMismatch
		ri *sim.ErrRoundIncomplete
	)
	switch {
	case errors.As(rerr, &hm):
		return &adminv1.VerifySnapshotRoundResponse{
			Outcome:      adminv1.VerifyOutcome_VERIFY_OUTCOME_REPLAY_MISMATCH,
			ExpectedHash: hm.Expected[:], ActualHash: hm.Actual[:], ComparedTick: uint64(hm.Tick),
		}, nil
	case errors.As(rerr, &rm):
		return &adminv1.VerifySnapshotRoundResponse{
			Outcome:      adminv1.VerifyOutcome_VERIFY_OUTCOME_RESTORE_MISMATCH,
			ExpectedHash: rm.Recorded, ActualHash: rm.Restored, ComparedTick: rm.RoundTick,
		}, nil
	case errors.As(rerr, &sm):
		return &adminv1.VerifySnapshotRoundResponse{
			Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_SEED_MISMATCH, RecordedSeed: sm.Recorded, ConfiguredSeed: sm.Configured,
		}, nil
	case f.Exit == recovery.ExitRestore:
		return &adminv1.VerifySnapshotRoundResponse{Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_CONTENT_MISMATCH}, nil
	case errors.As(rerr, &ri):
		if ri.Cause == sim.RoundMissing && len(ri.Zones) == owned {
			return nil, &snapshotStatus{codeNotFound, "round_not_found", fmt.Sprintf("no snapshot object at tick %d", tick)}
		}
		return nil, &snapshotStatus{codeFailedPrecondition, "round_incomplete", ri.Error()}
	case f.Exit == recovery.ExitLogGap:
		return nil, &snapshotStatus{codeFailedPrecondition, "log_gap", f.Error()}
	case f.Exit == recovery.ExitStateVersion:
		return nil, &snapshotStatus{codeFailedPrecondition, "state_version", f.Error()}
	}
	return nil, rerr
}
