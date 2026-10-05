// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"syscall"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/telemetry"
)

// runRecover is `andara-server recover --verify [--round T]` (AW-SRV-007
// AC-10): load a round, replay to the head, print match or mismatch with both
// hashes and the phase timings, and exit. It never binds grpc.listen, never
// lingers, and never serves the Engine it built. The remaining arguments are
// the server's own configuration flags.
func runRecover(args []string, env config.EnvLookup, stdout, stderr io.Writer) int {
	var (
		verify bool
		round  uint64
		rest   []string
	)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--verify":
			verify = true
		case a == "--round" || a == "-round":
			if i+1 >= len(args) {
				_, _ = io.WriteString(stderr, "--round needs a tick\n")
				return boot.ExitFail
			}
			i++
			n, err := strconv.ParseUint(args[i], 10, 64)
			if err != nil || n == 0 {
				_, _ = fmt.Fprintf(stderr, "--round must be a tick above 0, got %q\n", args[i])
				return boot.ExitFail
			}
			round = n
		case len(a) > 8 && a[:8] == "--round=":
			n, err := strconv.ParseUint(a[8:], 10, 64)
			if err != nil || n == 0 {
				_, _ = fmt.Fprintf(stderr, "--round must be a tick above 0, got %q\n", a[8:])
				return boot.ExitFail
			}
			round = n
		default:
			rest = append(rest, a)
		}
	}
	if !verify {
		_, _ = io.WriteString(stderr, "andara-server recover: --verify is required; recovery at boot is `andara-server`\n")
		return boot.ExitFail
	}
	cfg, err := config.Parse(rest, env, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return boot.ExitOK
		}
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return boot.ExitFail
	}
	tel := telemetry.Setup(cfg, stderr)
	defer tel.Shutdown(context.Background())
	if tel.SetupErr != nil {
		tel.Log.Error("telemetry", "detail", tel.SetupErr.Error())
		return boot.ExitFail
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, cfg.RecoveryVerifyTimeout)
	defer stop()

	rt := boot.New(cfg, tel)
	rt.ContentReadOnly = true
	rt.BuildVersion = version
	if code := rt.LoadContent(ctx); code != boot.ExitOK {
		return code
	}
	if err := rt.WaitForContent(ctx); err != nil {
		tel.Log.Error("content: what the Active Pointers name does not load; a round cannot be rebuilt without it", "detail", err.Error())
		return boot.ExitFail
	}
	rt.ReadRounds = true
	inEffect := false
	o, release, err := rt.RecoverOptions(ctx, rt.EngineConfig(), &inEffect)
	if err != nil {
		tel.Log.Error("recover", "detail", err.Error())
		return boot.StartExit(err)
	}
	defer release()
	o.Verify = true
	o.Round = nil // recovery.pin_round is boot's; --round is the one-shot's
	if round > 0 {
		r := sim.Tick(round)
		o.Round = &r
	}
	_, rep, err := recovery.Recover(ctx, o)
	printVerify(stdout, rep, err)
	f := recovery.Classify(err)
	switch {
	case f == nil:
		return boot.ExitOK
	case f.Exit == recovery.ExitRestore:
		// A one-shot's output is match or mismatch, and every mismatch exits 8.
		return recovery.ExitHashMismatch
	}
	return f.Exit
}

func printVerify(w io.Writer, rep recovery.Report, err error) {
	f := recovery.Classify(err)
	switch {
	case f == nil:
		_, _ = fmt.Fprintf(w, "match\nround_tick=%d\ntick=%d\nreplayed_ticks=%d\nrecorded_hash=%x\nreplayed_hash=%x\n", rep.Round.Tick, rep.Tick, rep.Replayed, rep.Expected, rep.Actual)
	case f.Exit == recovery.ExitHashMismatch:
		_, _ = fmt.Fprintf(w, "mismatch\nreason=hash\nround_tick=%d\ntick=%d\nrecorded_hash=%x\nreplayed_hash=%x\n", rep.Round.Tick, rep.MismatchTick, rep.Expected, rep.Actual)
	case f.Exit == recovery.ExitRestore:
		_, _ = fmt.Fprintf(w, "mismatch\nreason=%s\nround_tick=%d\ndetail=%s\n", f.Restore, rep.Round.Tick, f.Error())
	default:
		_, _ = fmt.Fprintf(w, "refused\nexit=%d\nreason=%s\ndetail=%s\n", f.Exit, f.Reason, f.Error())
	}
	phases := make([]string, 0, len(rep.Phases))
	for p := range rep.Phases {
		phases = append(phases, p)
	}
	sort.Strings(phases)
	for _, p := range phases {
		_, _ = fmt.Fprintf(w, "phase_%s=%s\n", p, rep.Phases[p])
	}
}
