// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/valesordev/andara/server/recovery"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// `andara-server recover` is --verify only: recovery at boot is the server.
// Its flags are refused before anything is opened.
func TestRecover_RefusesWhatItCannotRun(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no --verify", []string{"recover"}, "--verify is required"},
		{"--round without a tick", []string{"recover", "--verify", "--round"}, "--round needs a tick"},
		{"--round 0", []string{"recover", "--verify", "--round", "0"}, "--round must be a tick above 0"},
		{"--round=abc", []string{"recover", "--verify", "--round=abc"}, "--round must be a tick above 0"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(c.args, emptyEnv, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: exit %d, stderr %q, want exit 1 naming %q", c.name, code, stderr.String(), c.want)
		}
		if stdout.Len() != 0 {
			t.Errorf("%s: wrote to stdout: %q", c.name, stdout.String())
		}
	}
}

func TestPrintVerify_SaysMatchOrMismatchWithBothHashes(t *testing.T) {
	h1, h2 := [32]byte{0xaa}, [32]byte{0xbb}
	rep := recovery.Report{Round: store.Round{Tick: 4200}, Tick: 9000, Replayed: 4800, Actual: h1, Expected: h1,
		Phases: map[string]time.Duration{recovery.PhaseTotal: time.Second}}

	var out bytes.Buffer
	printVerify(&out, rep, nil)
	if !strings.HasPrefix(out.String(), "match\n") || !strings.Contains(out.String(), "round_tick=4200") || !strings.Contains(out.String(), "phase_total=1s") {
		t.Errorf("match:\n%s", out.String())
	}

	out.Reset()
	rep.MismatchTick, rep.Expected, rep.Actual = 4321, h1, h2
	printVerify(&out, rep, &recovery.HashMismatchError{Tick: 4321, Expected: h1, Actual: h2, Round: 4200})
	for _, want := range []string{"mismatch\n", "reason=hash", "tick=4321", "recorded_hash=aa", "replayed_hash=bb"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("replay mismatch lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	printVerify(&out, rep, &sim.SeedMismatch{RoundTick: 4200, Recorded: 7, Configured: 8})
	if !strings.HasPrefix(out.String(), "mismatch\nreason=seed") {
		t.Errorf("restore mismatch is a mismatch with its reason:\n%s", out.String())
	}

	out.Reset()
	printVerify(&out, rep, &sim.ErrRoundIncomplete{Tick: 4200, Cause: sim.RoundHash})
	if !strings.HasPrefix(out.String(), "refused\nexit=7") {
		t.Errorf("an incomplete round is a refusal, not a mismatch:\n%s", out.String())
	}
}
