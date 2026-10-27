// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build !fault && !sizing

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/boot"
)

const faultPkg = "github.com/valesordev/andara/server/fault"

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, out)
	}
	return strings.Fields(string(out))
}

// AW-SRV-066 AC-8: the production binary has no fault package in its
// dependencies, and a fault build does, so the first half isn't vacuous.
func TestProductionBuildHasNoFaultPackage(t *testing.T) {
	for _, dep := range goList(t, "-deps", ".") {
		if dep == faultPkg {
			t.Fatalf("%s is a dependency of the production andara-server", faultPkg)
		}
	}
	found := false
	for _, dep := range goList(t, "-tags", "fault", "-deps", ".") {
		found = found || dep == faultPkg
	}
	if !found {
		t.Fatalf("%s is not a dependency of the -tags fault build: the injector isn't wired", faultPkg)
	}
}

// AW-SRV-066 AC-8: no file the production binary compiles names a fault
// variable, so setting one has nothing to reach.
func TestProductionBuildReadsNoFaultVariable(t *testing.T) {
	const module = "github.com/valesordev/andara"
	for _, row := range goList(t, "-deps", "-f", `{{if .Module}}{{if eq .Module.Path "`+module+`"}}{{.Dir}}|{{join .GoFiles ","}}{{end}}{{end}}`, ".") {
		dir, files, _ := strings.Cut(row, "|")
		for _, f := range strings.Split(files, ",") {
			if f == "" {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "ANDARA_FAULT_") {
				t.Errorf("%s names a fault variable and is in the production build", filepath.Join(dir, f))
			}
		}
	}
}

// AW-SRV-066 AC-8: with the variable set, the production build installs
// nothing and reports no build tag.
func TestProductionBuildIgnoresTheWedgeVariable(t *testing.T) {
	rt := &boot.Runtime{}
	env := func(k string) (string, bool) {
		if k == "ANDARA_FAULT_WEDGE_TICK_AFTER" {
			return "30s", true
		}
		return "", false
	}
	if err := installFaults(rt, env, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if rt.BeforeTick != nil {
		t.Error("the production build installed a BeforeTick")
	}
	if buildTags != "" {
		t.Errorf("build_tags = %q, want empty", buildTags)
	}
}
