// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"fmt"
	"testing"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/sim"
)

// AW-SRV-007 AC-16: a missing manifest is the sim's ErrContentVersionUnknown, so
// a round recording a version this store never had is a content mismatch, and a
// registry that merely failed to answer is not.
func TestManifestMissingIsAContentVersionUnknown(t *testing.T) {
	var unknown *sim.ErrContentVersionUnknown
	err := fmt.Errorf("resolve: %w", &ErrManifestMissing{Pack: "town", Version: 4, Topic: "t"})
	if !errors.As(err, &unknown) || unknown.Pack != "town" || unknown.Version != 4 {
		t.Fatalf("errors.As: %v", unknown)
	}
	if errors.As(fmt.Errorf("resolve: %w", context.DeadlineExceeded), &unknown) {
		t.Fatal("a timeout is not a version the source lacks")
	}
}

// content.source=dir cannot prepare a pack version: that is a version it lacks.
func TestDirSourceNamesAVersionItLacks(t *testing.T) {
	c, errs := Open(context.Background(), Options{Source: SourceDir, Path: t.TempDir()})
	if c == nil {
		t.Skipf("no dir source here: %v", errs)
	}
	_, err := c.Prepare(nil, &logv1.ContentSwap{PackId: "town", Version: 7})
	var unknown *sim.ErrContentVersionUnknown
	if !errors.As(err, &unknown) || unknown.Pack != "town" || unknown.Version != 7 {
		t.Fatalf("err %v", err)
	}
}
