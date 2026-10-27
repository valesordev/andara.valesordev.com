// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !sizing && !fault

package main

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
)

// AW-SRV-065 AC-3: the production binary has neither server/simtest nor the
// seeding package in its dependencies, and a sizing build has both, so the
// first half isn't vacuous.
func TestProductionBuildHasNoSeedingPackage(t *testing.T) {
	seeding := []string{"github.com/valesordev/andara/server/simtest", "github.com/valesordev/andara/server/sizing"}
	for _, dep := range goList(t, "-deps", ".") {
		for _, p := range seeding {
			if dep == p {
				t.Errorf("%s is a dependency of the production andara-server", p)
			}
		}
	}
	sized := map[string]bool{}
	for _, dep := range goList(t, "-tags", "sizing", "-deps", ".") {
		sized[dep] = true
	}
	for _, p := range seeding {
		if !sized[p] {
			t.Errorf("%s is not a dependency of the -tags sizing build: the seed isn't wired", p)
		}
	}
}

// AW-SRV-065 AC-3: nothing the production binary reads seeds anything. Every
// setting the sizing guard refuses passes it, no BeforeTick is installed, the
// seed is a no-op, and build_tags is empty.
func TestProductionBuildSeedsNothing(t *testing.T) {
	cfg := config.Config{Environment: "dev", SimSource: "kafka"}
	if err := guardBuild(&cfg, slog.New(slog.DiscardHandler)); err != nil {
		t.Errorf("the production build ran the sizing guard: %v", err)
	}
	rt := &boot.Runtime{}
	seed := installSeed(rt, slog.New(slog.DiscardHandler))
	if rt.BeforeTick != nil {
		t.Error("the production build installed a BeforeTick")
	}
	if err := seed(context.Background(), nil); err != nil {
		t.Errorf("seed: %v", err)
	}
	if buildTags != "" {
		t.Errorf("build_tags = %q, want empty", buildTags)
	}
}

// The sizing build and its tests compile. Nothing in make check builds with
// -tags sizing, so without this a broken andara-server-sizing would stay
// green until measure_tick.sh built it.
func TestSizingBuildCompiles(t *testing.T) {
	if out, err := exec.Command("go", "vet", "-tags", "sizing,integration", ".").CombinedOutput(); err != nil {
		t.Fatalf("go vet -tags sizing,integration: %v\n%s", err, out)
	}
}

// AW-SRV-065 AC-7: a build with both fault and sizing fails, naming the two
// tags as mutually exclusive.
func TestFaultAndSizingDoNotBuildTogether(t *testing.T) {
	out, err := exec.Command("go", "build", "-tags", "fault,sizing", "-o", t.TempDir()+"/andara-server", ".").CombinedOutput()
	if err == nil {
		t.Fatal("go build -tags fault,sizing succeeded")
	}
	if !strings.Contains(string(out), "build_tags_fault_and_sizing_are_mutually_exclusive") {
		t.Fatalf("the failure doesn't name the tags as mutually exclusive:\n%s", out)
	}
}
