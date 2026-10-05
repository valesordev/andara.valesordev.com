// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package recovery is the server's boot-time path back to a verified World
// (AW-SRV-007): select the newest complete snapshot round, restore it onto the
// content it recorded, seek the log to the round's tick, replay the recorded
// Tick Boundary Records to the head, and prove the State Hash at every one. It
// refuses rather than guess: a hash mismatch, a log gap, a state_version it
// can't read, a round that doesn't reproduce its own tick, and an incomplete
// round each end in their own exit code and their own failure reason.
package recovery

import (
	"errors"
	"fmt"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/tickloop"
)

// The process exit codes of a recovery that doesn't end in a serving World
// (AW-SRV-007's table). 2 is never assigned: it is Go's own exit for an
// unrecovered panic or a runtime fatal error, so on a cluster, where only the
// exit code outlives the process, it can't name a hash mismatch. 5 is not
// recovery's: it is the running server's exit on a lost Tick Boundary Record
// (AW-SRV-026).
const (
	ExitOK           = 0
	ExitConfig       = 1 // configuration or store error before recovery began
	ExitLogGap       = 3 // retention shorter than the snapshot age
	ExitStateVersion = 4 // binary older than the snapshot
	ExitRestore      = 6 // the round doesn't reproduce its own tick
	ExitRound        = 7 // no complete round, or a named round that isn't complete
	ExitHashMismatch = 8 // the alerting condition
)

// The reason label of andara_recovery_failures_total.
const (
	ReasonHash    = "hash"
	ReasonRestore = "restore"
	ReasonGap     = "gap"
	ReasonVersion = "version"
	ReasonRound   = "round"
	ReasonStore   = "store"
)

// Reasons lists every failure reason, for pre-seeding the counter.
var Reasons = []string{ReasonHash, ReasonRestore, ReasonGap, ReasonVersion, ReasonRound, ReasonStore}

// The reason a restore refuses (the `reason` of the `recovery restore
// mismatch` line): the round hashes to something other than its tick recorded,
// was written with another seed, or doesn't restore onto the content in effect.
const (
	RestoreHash    = "hash"
	RestoreSeed    = "seed"
	RestoreContent = "content"
)

// HashMismatchError is a replay that reached a boundary whose State Hash it
// did not reproduce (AW-SRV-007 AC-5): the first such boundary, where replay
// stopped. Round is the tick of the round it replayed from, 0 for a cold start.
type HashMismatchError struct {
	Tick     sim.Tick
	Expected [32]byte // the Tick Boundary Record's
	Actual   [32]byte // the replayed World's
	Round    sim.Tick
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("state hash mismatch at tick %d: recorded %x, replayed %x (replayed from the round at tick %d)", e.Tick, e.Expected, e.Actual, e.Round)
}

// Unwrap makes errors.Is(err, sim.ErrHashMismatch) hold.
func (e *HashMismatchError) Unwrap() error { return sim.ErrHashMismatch }

// Failure is a recovery that failed: the error, the exit code it maps to, the
// failure reason, and, for an exit-6 restore, its reason. It is what Recover
// returns for every refusal, so a caller needs no classification of its own.
type Failure struct {
	Err     error
	Exit    int
	Reason  string
	Restore string // RestoreHash, RestoreSeed or RestoreContent for exit 6
}

func (f *Failure) Error() string { return f.Err.Error() }
func (f *Failure) Unwrap() error { return f.Err }

// Classify maps an error recovery met to its Failure. An error it doesn't know,
// including a store that can't be read, is a configuration or store error: exit
// 1 with reason store, as an operator would act on it, not on a World.
func Classify(err error) *Failure {
	var (
		f      *Failure
		sv     *sim.ErrStateVersion
		ri     *sim.ErrRoundIncomplete
		rd     *sim.ErrRoundZoneDuplicate
		ru     *sim.ErrRoundZoneUnknown
		rm     *sim.RestoreMismatch
		sm     *sim.SeedMismatch
		cd     *sim.ContentDigestError
		hm     *HashMismatchError
		shm    *sim.HashMismatchError
		lg     *tickloop.LogGapError
		cancel = errors.Is(err, errCanceled)
	)
	switch {
	case err == nil:
		return nil
	case errors.As(err, &f):
		return f
	case cancel:
		return &Failure{Err: err, Exit: ExitConfig, Reason: ReasonStore}
	case errors.As(err, &sv):
		return &Failure{Err: err, Exit: ExitStateVersion, Reason: ReasonVersion}
	case errors.As(err, &ri):
		return &Failure{Err: err, Exit: ExitRound, Reason: ReasonRound}
	case errors.As(err, &rd):
		return &Failure{Err: &sim.ErrRoundIncomplete{Tick: rd.Tick, Cause: sim.RoundDuplicate, Zones: []sim.ZoneID{rd.Zone}}, Exit: ExitRound, Reason: ReasonRound}
	case errors.As(err, &rm):
		return &Failure{Err: err, Exit: ExitRestore, Reason: ReasonRestore, Restore: RestoreHash}
	case errors.As(err, &sm):
		return &Failure{Err: err, Exit: ExitRestore, Reason: ReasonRestore, Restore: RestoreSeed}
	case errors.As(err, &cd), errors.As(err, &ru):
		return &Failure{Err: err, Exit: ExitRestore, Reason: ReasonRestore, Restore: RestoreContent}
	case errors.As(err, &hm), errors.As(err, &shm):
		return &Failure{Err: err, Exit: ExitHashMismatch, Reason: ReasonHash}
	case errors.As(err, &lg), errors.Is(err, tickloop.ErrLogGap), errors.Is(err, sim.ErrBoundaryGap), errors.Is(err, sim.ErrOffsetGap):
		return &Failure{Err: err, Exit: ExitLogGap, Reason: ReasonGap}
	}
	return &Failure{Err: err, Exit: ExitConfig, Reason: ReasonStore}
}

// ExitCode is the exit code of err, ExitOK for nil.
func ExitCode(err error) int {
	if f := Classify(err); f != nil {
		return f.Exit
	}
	return ExitOK
}

// errCanceled marks an error that is the caller's context ending, not a
// recovery failure.
var errCanceled = errors.New("recovery canceled")
