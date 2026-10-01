// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/content/lang"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/internal/contentequiv"
)

// TestPublishGateAgreesWithTheEquivalenceFixture is AW-CLI-002 AC-4's third
// runner: the publish gate, through Admin.PublishVersion, on every case in the
// fixture set that compiles (one that doesn't has nothing to publish).
//
// The gate validates compiled blobs, which carry no source position, so each
// finding is placed back on the source with the source map of the compile
// that produced the blobs — as `andara-cli`, which holds that map, places the
// gate's findings for a Builder. Its code and chain are the gate's own; the
// map only turns the chain into the position the compiler reports it at.
func TestPublishGateAgreesWithTheEquivalenceFixture(t *testing.T) {
	root := filepath.Join("..", "..")
	cases, err := contentequiv.Cases(root)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := lang.PackFromBlobs(core.Pack, uint32(core.Version()), core.Blobs())
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, c := range cases {
		if !c.Compiles {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			out, ds := lang.Compile(filepath.Join(root, c.Dir), embedded, nil)
			if out == nil {
				t.Fatalf("does not compile: %v", ds)
			}
			h := newPubHarness(t, func(ao *AdminOptions, _ *LoaderOptions) {
				ao.Accounts = packHolders{alice: {c.Pack}}
			})
			files := map[string][]byte{}
			for _, b := range out.Blobs {
				files[b.Path] = b.Bytes
			}
			resp, err := h.admin.PublishVersion(builder(alice), &adminv1.PublishVersionRequest{PackId: c.Pack, Blobs: h.putBlobs(files)})

			var found []*contentv1.Diagnostic
			var ae *AdminError
			switch {
			case err == nil:
				found = resp.GetWarnings()
			case errors.As(err, &ae):
				pf, ok := ae.Detail.(*adminv1.PublishFindings)
				if !ok {
					t.Fatalf("refused without findings: %v", err)
				}
				found = pf.GetFindings()
			default:
				t.Fatal(err)
			}
			placed := make([]lang.Diagnostic, 0, len(found))
			for _, f := range found {
				sev := lang.SeverityError
				if f.GetSeverity() == contentv1.Severity_WARNING {
					sev = lang.SeverityWarning
				}
				d, ok := out.SourceMap.Place(f.GetCode(), f.GetMessage(), f.GetChain(), sev)
				if !ok {
					t.Errorf("the source map cannot place %s %v", f.GetCode(), f.GetChain())
					continue
				}
				placed = append(placed, d)
			}
			// The gate lists findings in the order the loader finds them;
			// placed on the source, they sort as the compiler's do
			// (errors.md rule 5).
			lang.SortDiagnostics(placed)
			for _, d := range contentequiv.Diff(c.Want, contentequiv.FromDiagnostics(placed)) {
				t.Error(d)
			}
		})
	}
	if ran < 15 {
		t.Errorf("the gate ran %d cases; the corpus's valid cases alone are more", ran)
	}
}

// TestPublishGateRefusesTheBlobTwins is AW-CLI-002 AC-4's error-level half:
// each invalid/semantic case whose codes the loader raises too (errors.md
// §3.2), as blobs something other than the compiler wrote, fed straight to
// Admin.PublishVersion. The gate refuses it with the case's sidecar findings,
// on code and chain.
func TestPublishGateRefusesTheBlobTwins(t *testing.T) {
	twins, err := contentequiv.Twins(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(twins) != 19 {
		t.Fatalf("%d twins, want the 19 cases errors.md §3.2's codes cover", len(twins))
	}
	for _, tw := range twins {
		t.Run(tw.Case, func(t *testing.T) {
			h := newPubHarness(t, func(ao *AdminOptions, _ *LoaderOptions) {
				ao.Accounts = packHolders{alice: {tw.Pack}}
			})
			files := map[string][]byte{}
			for p, b := range tw.Blobs {
				files[p] = b
			}
			_, err := h.admin.PublishVersion(builder(alice), &adminv1.PublishVersionRequest{PackId: tw.Pack, Blobs: h.putBlobs(files)})
			var ae *AdminError
			if !errors.As(err, &ae) || ae.Reason != ErrReasonValidation {
				t.Fatalf("not refused for validation: %v", err)
			}
			pf, _ := ae.Detail.(*adminv1.PublishFindings)
			var got []string
			for _, f := range pf.GetFindings() {
				if f.GetSeverity() == contentv1.Severity_ERROR {
					got = append(got, contentequiv.CodeChain(f.GetCode(), f.GetChain()))
				}
			}
			slices.Sort(got)
			for _, d := range contentequiv.Diff(tw.Want, got) {
				t.Error(d)
			}
		})
	}
}
