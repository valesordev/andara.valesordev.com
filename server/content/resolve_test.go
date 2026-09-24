// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/sim"
)

// fakeStore is the content store without a broker. Every rejection in AC-4
// through AC-11 is a property of a manifest and its blobs, not of Kafka, so
// they are unit tests here and the broker is exercised once, in the
// integration test, for the thing only a broker can show: that a compacted
// topic scans to the same answer.
type fakeStore struct {
	active    map[string]uint64
	manifests map[string]*contentv1.ContentVersion
	blobs     map[string][]byte // hex(hash) -> body
	// dropBlob makes Blobs behave as if the blob topic never carried this
	// hash, which is AC-6.
	dropBlob string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		active:    map[string]uint64{},
		manifests: map[string]*contentv1.ContentVersion{},
		blobs:     map[string][]byte{},
	}
}

func (f *fakeStore) Active(context.Context) (map[string]uint64, error) {
	out := make(map[string]uint64, len(f.active))
	for k, v := range f.active {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStore) Manifest(_ context.Context, pack string, version uint64) (*contentv1.ContentVersion, error) {
	mf, ok := f.manifests[ManifestKey(pack, version)]
	if !ok {
		return nil, fmt.Errorf("content: no manifest for %s", ManifestKey(pack, version))
	}
	return mf, nil
}

func (f *fakeStore) Blobs(_ context.Context, refs []*contentv1.BlobRef) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, ref := range refs {
		h := hex.EncodeToString(ref.GetHash())
		if h == f.dropBlob {
			return nil, &ErrBlobMissing{Hash: ref.GetHash(), Path: ref.GetPath()}
		}
		body, ok := f.blobs[h]
		if !ok {
			return nil, &ErrBlobMissing{Hash: ref.GetHash(), Path: ref.GetPath()}
		}
		out[ref.GetPath()] = body
	}
	return out, nil
}

// publish writes a version into the fake store and makes it active.
func (f *fakeStore) publish(pack string, version, coreVersion uint64, files map[string]string) {
	mf := &contentv1.ContentVersion{PackId: pack, Version: version, CoreVersion: coreVersion}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		body := []byte(files[p])
		sum := sha256.Sum256(body)
		f.blobs[hex.EncodeToString(sum[:])] = body
		mf.Blobs = append(mf.Blobs, &contentv1.BlobRef{
			Path: p, Hash: sum[:], SizeBytes: uint64(len(body)),
		})
	}
	f.manifests[ManifestKey(pack, version)] = mf
	f.active[pack] = version
}

func zoneJSON(id, room string) string {
	return fmt.Sprintf(`{"formatVersion":1,"id":%q,"name":"Zone %s",
		"rooms":[{"id":%q,"title":"A Room","description":"Somewhere."}]}`, id, id, room)
}

// --- AC-11: a Template published under the wrong pack -----------------------

func TestResolve_PackMismatchIsRefusedNamingBothPacks(t *testing.T) {
	s := newFakeStore()
	// town publishes a blob whose name claims andara.core's namespace. Whether
	// this would collide with core's own Npc or quietly stand in for it depends
	// on load order, which is exactly why it cannot be allowed.
	s.publish("town", 9, 1, map[string]string{
		"town.json": zoneJSON("town", "square"),
		"templates/andara.core.Npc.json": `{"formatVersion":1,"name":"andara.core.Npc",
			"kind":"ENTITY","chain":["andara.core.Npc"],"resolved":true}`,
	})

	_, err := Resolve(context.Background(), s, "town", 9)
	var pm *ErrPackMismatch
	if !errors.As(err, &pm) {
		t.Fatalf("err = %v, want ErrPackMismatch", err)
	}
	if pm.NamePack != "andara.core" || pm.PublishedPack != "town" {
		t.Errorf("got name pack %q published %q", pm.NamePack, pm.PublishedPack)
	}
	if !strings.Contains(pm.Blob, "andara.core.Npc.json") {
		t.Errorf("blob %q should name the offending path", pm.Blob)
	}
	if Reason(err) != ReasonPackMismatch {
		t.Errorf("reason = %q", Reason(err))
	}
	// The finding carries the code errors.md assigns, so a refused publish and
	// a refused load say the same word.
	fs := Findings(err)
	if len(fs) != 1 || fs[0].Code != sim.ErrPackMismatch {
		t.Fatalf("findings = %+v", fs)
	}
}

func TestResolve_TemplateInItsOwnPackIsAccepted(t *testing.T) {
	s := newFakeStore()
	s.publish("town", 9, 1, map[string]string{
		"town.json": zoneJSON("town", "square"),
		"templates/town.Merchant.json": `{"formatVersion":1,"name":"town.Merchant",
			"kind":"ENTITY","chain":["town.Merchant"],"resolved":true}`,
	})
	res, err := Resolve(context.Background(), s, "town", 9)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Templates) != 1 || res.Templates[0].Def.GetName() != "town.Merchant" {
		t.Fatalf("templates = %+v", res.Templates)
	}
}

// --- AC-4: format_version skew ---------------------------------------------

func TestResolve_FormatVersionSkewNamesBothVersions(t *testing.T) {
	s := newFakeStore()
	future := sim.MaxFormatVersion + 1
	s.publish("town", 1, 1, map[string]string{
		"town.json": fmt.Sprintf(`{"formatVersion":%d,"id":"town","name":"Town",
			"rooms":[{"id":"square","title":"Square","description":"d"}]}`, future),
	})
	_, err := Resolve(context.Background(), s, "town", 1)
	var fv *ErrFormatVersion
	if !errors.As(err, &fv) {
		t.Fatalf("err = %v, want ErrFormatVersion", err)
	}
	if fv.Have != future || fv.Max != sim.MaxFormatVersion {
		t.Errorf("have %d, supported %d..%d", fv.Have, fv.Min, fv.Max)
	}
	msg := fv.Error()
	if !strings.Contains(msg, fmt.Sprint(future)) || !strings.Contains(msg, fmt.Sprint(sim.MaxFormatVersion)) {
		t.Errorf("message %q must name both versions (AC-4)", msg)
	}
	if Reason(err) != ReasonFormatVersion {
		t.Errorf("reason = %q", Reason(err))
	}
}

// --- AC-6: a manifest naming a blob the store does not have -----------------

func TestResolve_MissingBlobNamesHashAndPath(t *testing.T) {
	s := newFakeStore()
	s.publish("town", 1, 1, map[string]string{"town.json": zoneJSON("town", "square")})
	mf := s.manifests[ManifestKey("town", 1)]
	want := hex.EncodeToString(mf.Blobs[0].GetHash())
	s.dropBlob = want

	_, err := Resolve(context.Background(), s, "town", 1)
	var bm *ErrBlobMissing
	if !errors.As(err, &bm) {
		t.Fatalf("err = %v, want ErrBlobMissing", err)
	}
	if bm.Path != "town.json" || hex.EncodeToString(bm.Hash) != want {
		t.Errorf("got path %q hash %x", bm.Path, bm.Hash)
	}
	if !strings.Contains(bm.Error(), want) {
		t.Errorf("message %q must name the hash", bm.Error())
	}
}

// --- Source blobs are retained, never loaded --------------------------------

func TestResolve_SourceIsRetainedAndNotLoadedAsAZone(t *testing.T) {
	s := newFakeStore()
	s.publish("town", 1, 1, map[string]string{
		"town.json": zoneJSON("town", "square"),
		// The Content Language the version was compiled from. If the resolver
		// tried to parse this as a Zone Definition it would fail, and a server
		// that compiled .aw would be a second implementation of the compiler.
		"town.aw": "zone town \"Town\" {\n  room square \"Square\" {}\n}\n",
		"README":  "not content",
	})
	res, err := Resolve(context.Background(), s, "town", 1)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(res.Zones) != 1 {
		t.Fatalf("zones = %d, the .aw and the README must not become Zones", len(res.Zones))
	}
	if len(res.Source) != 1 || res.Source[0].GetPath() != "town.aw" {
		t.Fatalf("source = %+v", res.Source)
	}
}

// --- AC-7: cold and cached resolve to the same bytes ------------------------

func TestResolve_ColdAndCachedAreByteIdentical(t *testing.T) {
	s := newFakeStore()
	s.publish("town", 3, 1, map[string]string{
		"town.json":                    zoneJSON("town", "square"),
		"templates/town.Merchant.json": `{"formatVersion":1,"name":"town.Merchant","kind":"ENTITY","chain":["town.Merchant"],"resolved":true}`,
	})
	a, err := Resolve(context.Background(), s, "town", 3)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(context.Background(), s, "town", 3)
	if err != nil {
		t.Fatal(err)
	}
	if encodeAll(t, a) != encodeAll(t, b) {
		t.Fatal("the same version resolved twice must decode to identical definitions (AC-7)")
	}
}

func encodeAll(t *testing.T, r *Resolved) string {
	t.Helper()
	opts := proto.MarshalOptions{Deterministic: true}
	var sb strings.Builder
	for _, z := range r.Zones {
		b, err := opts.Marshal(z.Def)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%s=%x\n", z.File, b)
	}
	for _, tm := range r.Templates {
		b, err := opts.Marshal(tm.Def)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "%s=%x\n", tm.File, b)
	}
	return sb.String()
}

// Manifest order is the publisher's, and a manifest is data from outside this
// process. Resolution sorts it again, so findings come out in the same order
// however the publisher happened to write it.
func TestResolve_LoadOrderDoesNotDependOnManifestOrder(t *testing.T) {
	files := map[string]string{
		"a.json": zoneJSON("a", "one"),
		"b.json": zoneJSON("b", "two"),
		"c.json": zoneJSON("c", "three"),
	}
	s1 := newFakeStore()
	s1.publish("p", 1, 1, files)
	s2 := newFakeStore()
	s2.publish("p", 1, 1, files)
	// Reverse the manifest the publisher wrote.
	mf := s2.manifests[ManifestKey("p", 1)]
	for i, j := 0, len(mf.Blobs)-1; i < j; i, j = i+1, j-1 {
		mf.Blobs[i], mf.Blobs[j] = mf.Blobs[j], mf.Blobs[i]
	}

	a, err := Resolve(context.Background(), s1, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Resolve(context.Background(), s2, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	var fa, fb []string
	for _, z := range a.Zones {
		fa = append(fa, z.File)
	}
	for _, z := range b.Zones {
		fb = append(fb, z.File)
	}
	if strings.Join(fa, ",") != strings.Join(fb, ",") {
		t.Fatalf("%v vs %v", fa, fb)
	}
}
