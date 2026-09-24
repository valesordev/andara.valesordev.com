// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Findings from the review of PR #63. Each one is reproduced here before it is
// fixed, so the fix is pinned by a test that fails without it.
package content

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/sim"
)

// One rejected pack must not fail the boot.
//
// The Loader deliberately retains every pack that did load — that rule has a
// test. What it did not have was a test one layer up: openKafka turned every
// rejection into a fatal validation finding, and Runtime.LoadContent exits 1 on
// any fatal finding. So a valid andara.core plus one malformed Builder pack
// took the whole server down, undoing at the boot layer exactly what the
// Loader was written to guarantee.
func TestBootFindings_OneRejectedPackIsNotFatalWhenOthersLoaded(t *testing.T) {
	rejects := []Rejection{{
		Pack: "town", Version: 8, Reason: ReasonValidation,
		Err: &ErrValidation{Findings: []sim.ValidationError{
			{File: "town.json", Code: sim.ErrUnknownRoom, Detail: "dangling exit"},
		}},
	}}
	findings := loadFindings(rejects, 3 /* zones from packs that did load */)
	for _, f := range findings {
		if !sim.IsWarning(f, false) {
			t.Fatalf("finding %+v is fatal; a retained World must still boot", f)
		}
	}
}

// ...but a boot where nothing loaded is still exit 1, because there is no
// previous version to retain.
func TestBootFindings_NothingLoadableIsFatal(t *testing.T) {
	findings := loadFindings(nil, 0)
	fatal := false
	for _, f := range findings {
		if !sim.IsWarning(f, false) {
			fatal = true
		}
	}
	if !fatal {
		t.Fatal("a boot with no loadable Zones must fail")
	}
}

// A core rollback must not strand a pack that pinned a newer core.
//
// The skew check ran only for non-core candidates, so core moving *backwards*
// was accepted unconditionally. With core@4 and a pack pinned to core@4 both
// serving, rolling core back to 3 left the World serving a combination that
// the loader would refuse to assemble from scratch.
func TestLoader_CoreRollbackIsRefusedWhileAPackPinsANewerCore(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 8, 4, map[string]string{"town.json": zoneJSON("town", "square")})
	s.active["andara.core"] = 4

	l, m := testLoader(t, s, AllPacks)
	if rejects, err := l.LoadAll(context.Background()); err != nil || len(rejects) != 0 {
		t.Fatalf("boot: %+v %v", rejects, err)
	}
	if v := l.Versions(); v["andara.core"] != 4 || v["town"] != 8 {
		t.Fatalf("versions = %v", v)
	}

	rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 3})
	if len(rejects) != 1 || rejects[0].Reason != ReasonCoreVersion {
		t.Fatalf("rejects = %+v, want the rollback refused for core_version", rejects)
	}
	if msg := rejects[0].Err.Error(); !strings.Contains(msg, "town") {
		t.Errorf("message %q must name the pack that pins the newer core", msg)
	}
	if v := l.Versions(); v["andara.core"] != 4 {
		t.Errorf("core is serving %d; an incompatible rollback must retain 4", v["andara.core"])
	}
	if v := l.Versions(); v["town"] != 8 {
		t.Errorf("town is serving %d; it must not be stranded", v["town"])
	}
	if n := testutil.ToFloat64(m.LoadFailures.WithLabelValues(ReasonCoreVersion)); n != 1 {
		t.Errorf("core_version failures = %v", n)
	}
}

// A rollback that strands nobody still rolls back.
func TestLoader_CoreRollbackIsAcceptedWhenNoPackPinsANewerCore(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 3, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("andara.core", 4, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	s.publish("town", 8, 2, map[string]string{"town.json": zoneJSON("town", "square")})
	s.active["andara.core"] = 4

	l, _ := testLoader(t, s, AllPacks)
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 3}); len(rejects) != 0 {
		t.Fatalf("rejects = %+v", rejects)
	}
	if v := l.Versions(); v["andara.core"] != 3 {
		t.Errorf("core = %d, want the rollback applied", v["andara.core"])
	}
}

// An aggregate with no Zones was accepted, because the build was skipped when
// there was nothing to build. A pointer moving the last serving pack to a
// manifest with no Zone JSON therefore discarded a working World and advanced
// the gauge, with an empty topology behind it.
func TestLoader_AVersionThatEmptiesTheWorldIsRefused(t *testing.T) {
	s := newFakeStore()
	s.publish("andara.core", 1, 0, map[string]string{"core.json": zoneJSON("core", "void")})
	l, _ := testLoader(t, s, "andara.core")
	if _, err := l.LoadAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Version 2 carries a Template and its source, and no Zone at all.
	s.publish("andara.core", 2, 0, map[string]string{
		"core.aw": "pack andara.core\n",
		"templates/andara.core.Thing.json": `{"formatVersion":1,"name":"andara.core.Thing",
			"kind":"ENTITY","chain":["andara.core.Thing"],"resolved":true}`,
	})
	rejects := l.Apply(context.Background(), PointerMove{Pack: "andara.core", Version: 2})
	if len(rejects) != 1 {
		t.Fatalf("rejects = %+v, want the empty World refused", rejects)
	}
	if v := l.Versions()["andara.core"]; v != 1 {
		t.Errorf("serving %d; the previous version must be retained", v)
	}
	zones, _ := l.Inputs()
	if len(zones) != 1 {
		t.Errorf("the World has %d Zones; it must still be the one that loaded", len(zones))
	}
}

// A Template written by a newer compiler is format skew, and must be counted
// as format_version. It reached BuildTemplates instead, whose finding is
// wrapped as ErrValidation — so the reason said the Builder's content was
// invalid when in fact this binary was too old to read it.
func TestResolve_TemplateFormatSkewIsFormatVersionNotValidation(t *testing.T) {
	s := newFakeStore()
	s.publish("town", 1, 0, map[string]string{
		"town.json": zoneJSON("town", "square"),
		"templates/town.Merchant.json": fmt.Sprintf(`{"formatVersion":%d,"name":"town.Merchant",
			"kind":"ENTITY","chain":["town.Merchant"],"resolved":true}`, sim.TemplateFormatVersion+1),
	})
	_, err := Resolve(context.Background(), s, "town", 1)
	if err == nil {
		t.Fatal("want a rejection")
	}
	if got := Reason(err); got != ReasonFormatVersion {
		t.Fatalf("reason = %q, want %q (err: %v)", got, ReasonFormatVersion, err)
	}
	var fv *ErrFormatVersion
	if !asFormatErr(err, &fv) {
		t.Fatalf("err = %T", err)
	}
	if !strings.Contains(fv.Path, "town.Merchant.json") {
		t.Errorf("path = %q", fv.Path)
	}
}

func asFormatErr(err error, target **ErrFormatVersion) bool {
	e, ok := err.(*ErrFormatVersion)
	if ok {
		*target = e
	}
	return ok
}

// content.max_blob_bytes was enforced against the manifest's own size_bytes,
// which is data the publisher wrote. A manifest that underreports a blob still
// got the real body read, cached and parsed, as long as the hash matched — so
// the limit protected against honest mistakes and not against the case it
// exists for.
func TestBlobs_LimitIsEnforcedAgainstTheActualBody(t *testing.T) {
	body := strings.Repeat("x", 4096)
	s := newFakeStore()
	s.publish("town", 1, 0, map[string]string{"town.json": body})
	// The publisher claims the blob is tiny.
	s.manifests[ManifestKey("town", 1)].Blobs[0].SizeBytes = 1

	r := &KafkaResolver{maxBlob: 1024, metrics: NewMetrics(nil), cache: BlobCache{}}
	if err := r.checkBlobSize("town.json", len(body)); err == nil {
		t.Fatal("a body over content.max_blob_bytes must be refused whatever the manifest claims")
	}
	if err := r.checkBlobSize("town.json", 512); err != nil {
		t.Fatalf("a body under the limit must pass: %v", err)
	}
}
