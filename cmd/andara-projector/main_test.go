// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

// A projection is named; anything else is a usage error, exit 1, before any
// broker is touched.
func TestRun_ProjectionIsRequiredAndKnown(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "usage: andara-projector"},
		{[]string{"redis"}, "unknown projection redis"},
		{[]string{"state"}, "kafka.brokers"},
	} {
		var stderr bytes.Buffer
		if code := run(tc.args, noEnv, &stderr); code != 1 {
			t.Errorf("%v: exit %d, want 1", tc.args, code)
		}
		if !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%v: stderr %q does not name %q", tc.args, stderr.String(), tc.want)
		}
	}
}
