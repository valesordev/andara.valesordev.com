// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lang_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestImportsNothingFromServerButSim is AC-8.
//
// content/lang is imported by admin/cli, by server/content for AW-SRV-013's
// publish gate, and by the conformance harness. It may reach into server/sim —
// for the Component registry, the Direction set, and the ErrCode taxonomy,
// quoted rather than copied so that the compiler and the loader speak one
// vocabulary (errors.md §2) — and nowhere else under server/.
//
// The reason is the publish gate. server/content imports this package, so
// anything this package imported from server/content would be a cycle, and
// anything it imported from the transport or storage packages would drag a
// Kafka client into a Builder's offline `content compile`.
//
// The rule is about what this package *imports*, not about what server/sim
// pulls in behind it: sim's own dependencies are sim's business, and it already
// reaches server/canonical for the hashing the State Hash is defined over.
//
// depguard carries the same rule at lint time (.golangci.yml). This test is
// the exhaustive half: depguard names the packages that exist today, and
// `go list` catches the one added next month.
func TestImportsNothingFromServerButSim(t *testing.T) {
	const self = "github.com/valesordev/andara/content/lang"
	const serverRoot = "github.com/valesordev/andara/server/"
	const allowed = "github.com/valesordev/andara/server/sim"

	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, self).Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var offenders []string
	for _, pkg := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasPrefix(pkg, serverRoot) || pkg == allowed {
			continue
		}
		offenders = append(offenders, pkg)
	}
	if len(offenders) > 0 {
		t.Errorf("content/lang reaches into server/ beyond sim: %s", strings.Join(offenders, ", "))
	}
}

// TestTheLoaderAcceptsWhatTheCompilerEmits is the other half of AC-8, and the
// seed AW-SRV-013's three-way equivalence test grows from: the compiler's
// output goes through the loader unchanged, which is what "after emit, output
// goes through sim.BuildWorld unchanged" means in the story's Out of scope.
//
// It is also the loader checking the compiler. chain_mismatch,
// invalid_provenance and unflattened_template exist for exactly this — they are
// findings only a compiler bug can produce (errors.md §3.5) — and a compiler
// whose output tripped one would be emitting content nothing can load.
func TestTheLoaderAcceptsWhatTheCompilerEmits(t *testing.T) {
	assertLoads(t, "valid/core", nil)
	core := compileCore(t)
	for _, c := range []string{"valid/town", "valid/merge-three-deep", "valid/components-on-carriers", "valid/exit-all-directions"} {
		assertLoads(t, c, core)
	}
}
