// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recovery

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/sim"
)

// The seeds on `recovery restore mismatch` are decimal strings: as JSON numbers
// they reach Loki rounded (#423).
func TestRestoreMismatchLogsSeedsAsStrings(t *testing.T) {
	var buf bytes.Buffer
	o := Options{Log: slog.New(slog.NewJSONHandler(&buf, nil))}
	err := &sim.SeedMismatch{RoundTick: 7, Recorded: 16406829232824261652, Configured: 16406829232824261653}
	o.logRestoreMismatch(context.Background(), err, 7, "trace")
	for _, want := range []string{`"recorded_seed":"16406829232824261652"`, `"configured_seed":"16406829232824261653"`, `"reason":"seed"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %s in %s", want, buf.String())
		}
	}
}
