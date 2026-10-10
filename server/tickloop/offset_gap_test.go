// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kerr"
)

// The readers run on NoResetOffset, so an offset the broker no longer has
// reaches them as OFFSET_OUT_OF_RANGE; it is the log gap that exits 3.
func TestOffsetGap_OutOfRangeIsALogGapAndNothingElseIs(t *testing.T) {
	got := OffsetGap(kerr.OffsetOutOfRange)
	if !errors.Is(got, ErrLogGap) || !errors.Is(got, kerr.OffsetOutOfRange) {
		t.Fatalf("OffsetGap(OffsetOutOfRange) = %v", got)
	}
	other := errors.New("connection refused")
	if got := OffsetGap(other); got != other {
		t.Fatalf("offsetGap passed %v through as %v", other, got)
	}
}
