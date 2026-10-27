// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build fault

package main

import (
	"log/slog"
	"testing"

	"github.com/valesordev/andara/server/boot"
)

// AW-SRV-066 AC-8: a fault build reads ANDARA_FAULT_WEDGE_TICK_AFTER, installs
// the wedge for a positive duration, refuses anything else, and says what it is
// on andara_build_info.
func TestFaultBuildInstallsTheWedge(t *testing.T) {
	if buildTags != "fault" {
		t.Errorf("build_tags = %q, want fault", buildTags)
	}
	log := slog.New(slog.DiscardHandler)
	for _, c := range []struct {
		value   string
		set     bool
		install bool
		err     bool
	}{
		{"30s", true, true, false},
		{"", false, false, false},
		{"", true, false, false},
		{"soon", true, false, true},
		{"-1s", true, false, true},
		{"0s", true, false, true},
	} {
		rt := &boot.Runtime{}
		env := func(k string) (string, bool) {
			if k == envWedgeTickAfter && c.set {
				return c.value, true
			}
			return "", false
		}
		err := installFaults(rt, env, log)
		if (err != nil) != c.err {
			t.Errorf("%q: err = %v, want error %v", c.value, err, c.err)
		}
		if (rt.BeforeTick != nil) != c.install {
			t.Errorf("%q: installed %v, want %v", c.value, rt.BeforeTick != nil, c.install)
		}
	}
}
