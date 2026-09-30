// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	contentv1 "github.com/valesordev/andara/gen/go/andara/content/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/sim"
)

// The accounts the publish-path tests act as. alice and bob are Builders
// holding town; carol is a Builder holding nothing; brian is the Operator.
const (
	alice = "acct-alice"
	bob   = "acct-bob"
	carol = "acct-carol"
	brian = "acct-brian"
)

type packHolders map[string][]string

func (p packHolders) BuilderPacks(id string) []string { return p[id] }

// pubHarness is the publish path over in-memory topics, with a real Loader
// and Engine behind it following andara.core and town: what a
// content.source=kafka server has, without a broker.
type pubHarness struct {
	t                              *testing.T
	blobs, versions, active, audit *recordlog.Memory
	cache                          BlobCache
	reg                            *Registry
	loader                         *Loader
	admin                          *Admin
	m                              *PublishMetrics
	logs                           *bytes.Buffer
}

// registryStore is the read side over what the Registry wrote: the Loader's
// Store in these tests, as the KafkaResolver is in the server.
type registryStore struct {
	r     *Registry
	cache BlobCache
}

func (s registryStore) Active(context.Context) (map[string]uint64, error) { return s.r.Pointers(), nil }
func (s registryStore) Manifest(_ context.Context, pack string, v uint64) (*contentv1.ContentVersion, error) {
	cv, ok := s.r.Manifest(pack, v)
	if !ok {
		return nil, &ErrManifestMissing{Pack: pack, Version: v}
	}
	return cv, nil
}
func (s registryStore) Blobs(_ context.Context, refs []*contentv1.BlobRef) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, ref := range refs {
		body, err := s.cache.Get(ref.GetHash())
		if err != nil {
			return nil, &ErrBlobMissing{Hash: ref.GetHash(), Path: ref.GetPath()}
		}
		out[ref.GetPath()] = body
	}
	return out, nil
}

func newPubHarness(t *testing.T, mutate func(*AdminOptions, *LoaderOptions)) *pubHarness {
	t.Helper()
	h := &pubHarness{
		t:     t,
		blobs: recordlog.NewMemory(), versions: recordlog.NewMemory(), active: recordlog.NewMemory(), audit: recordlog.NewMemory(),
		cache: BlobCache{Dir: t.TempDir()},
		logs:  &bytes.Buffer{},
	}
	var err error
	h.reg, err = OpenRegistry(context.Background(), RegistryOptions{Blobs: h.blobs, Versions: h.versions, Active: h.active, Audit: h.audit, Cache: h.cache})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	lo := LoaderOptions{Store: registryStore{h.reg, h.cache}, Packs: []string{CorePack, "town"}, Metrics: NewMetrics(nil)}
	h.m = NewPublishMetrics(nil)
	ao := AdminOptions{
		Registry: h.reg,
		Blobs:    registryStore{h.reg, h.cache},
		Accounts: packHolders{alice: {"town"}, bob: {"town"}, carol: {}},
		Auditor:  auth.NewAuditor(h.audit, log, nil, nil),
		Metrics:  h.m,
		Log:      log,
		// The contract's defaults.
		MaxBlobBytes: 8 << 20, MaxPackBytes: 256 << 20, OperatorSelfApproval: true,
	}
	if mutate != nil {
		mutate(&ao, &lo)
	}
	h.loader = NewLoader(lo)
	attachEngine(h.loader)
	ao.Loader = h.loader
	h.admin, err = NewAdmin(ao)
	if err != nil {
		t.Fatal(err)
	}
	h.seedCore(1)
	return h
}

func as(id string, roles ...auth.Role) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{AccountID: id, Roles: roles})
}

func builder(id string) context.Context { return as(id, auth.RoleBuilder) }
func operator() context.Context         { return as(brian, auth.RoleOperator, auth.RoleBuilder) }

// seedCore writes andara.core@v as the server's boot will, and brings it into
// effect.
func (h *pubHarness) seedCore(v uint64) {
	h.t.Helper()
	files := map[string][]byte{}
	for _, name := range []string{"Entity", "Character", "Npc", "Item"} {
		files["templates/andara.core."+name+".json"] = readFile(h.t, filepath.Join("..", "..", "content", "core", "templates", "andara.core."+name+".json"))
	}
	refs := h.putBlobs(files)
	cv, err := h.reg.Publish(context.Background(), &contentv1.ContentVersion{PackId: CorePack, Blobs: refs, Author: "server"}, h.reg.Newest(CorePack), "server")
	if err != nil || cv.GetVersion() != v {
		h.t.Fatalf("seed core@%d: %v %v", v, cv, err)
	}
	if _, err := h.reg.MovePointer(context.Background(), CorePack, v, "server"); err != nil {
		h.t.Fatal(err)
	}
	h.follow(CorePack, v)
}

// follow is the pointer watch: the Loader brings the pack into effect.
func (h *pubHarness) follow(pack string, v uint64) {
	h.t.Helper()
	if rej := h.loader.Apply(context.Background(), PointerMove{Pack: pack, Version: v}); len(rej) > 0 {
		h.t.Fatalf("load %s@%d: %v", pack, v, rej[0].Err)
	}
	if got := h.loader.Versions()[pack]; got != v {
		h.t.Fatalf("%s serving %d, want %d", pack, got, v)
	}
}

func (h *pubHarness) putBlobs(files map[string][]byte) []*contentv1.BlobRef {
	h.t.Helper()
	var refs []*contentv1.BlobRef
	for p, body := range files {
		sum := sha256.Sum256(body)
		if _, err := h.reg.PutBlob(context.Background(), sum[:], "application/json", body); err != nil {
			h.t.Fatal(err)
		}
		refs = append(refs, &contentv1.BlobRef{Path: p, Hash: sum[:], SizeBytes: uint64(len(body))})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].GetPath() < refs[j].GetPath() })
	return refs
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// townFiles is the valid fixture as pack town, requiring andara.core@1.
func townFiles(t *testing.T) map[string][]byte {
	t.Helper()
	valid := filepath.Join("..", "..", "testdata", "content", "valid")
	files := map[string][]byte{"src/pack.aw": []byte("pack town requires andara.core@1\n")}
	for _, z := range []string{"town", "docks", "wilds", "purgatory"} {
		files[z+".json"] = readFile(t, filepath.Join(valid, z+".json"))
	}
	for _, n := range []string{"Guard", "Lantern", "Merchant"} {
		files["templates/town."+n+".json"] = readFile(t, filepath.Join(valid, "templates", "town."+n+".json"))
	}
	return files
}

// fakeBlobStream is PublishBlob's stream from a slice.
type fakeBlobStream struct{ msgs []*adminv1.PublishBlobRequest }

func (s *fakeBlobStream) Receive() (*adminv1.PublishBlobRequest, error) {
	if len(s.msgs) == 0 {
		return nil, io.EOF
	}
	m := s.msgs[0]
	s.msgs = s.msgs[1:]
	return m, nil
}

func blobStream(pack, path string, body []byte, chunk int) *fakeBlobStream {
	sum := sha256.Sum256(body)
	s := &fakeBlobStream{msgs: []*adminv1.PublishBlobRequest{{Chunk: &adminv1.PublishBlobRequest_Header{Header: &adminv1.PublishBlobHeader{
		PackId: pack, Path: path, MediaType: "application/json", SizeBytes: uint64(len(body)), Hash: sum[:],
	}}}}}
	for off := 0; off < len(body); off += chunk {
		s.msgs = append(s.msgs, &adminv1.PublishBlobRequest{Chunk: &adminv1.PublishBlobRequest_Data{Data: body[off:min(off+chunk, len(body))]}})
	}
	return s
}

// publish runs what `andara-cli content publish` will: every blob, then the
// version, against the pack's newest.
func (h *pubHarness) publish(ctx context.Context, pack string, files map[string][]byte) (*adminv1.PublishVersionResponse, error) {
	h.t.Helper()
	var refs []*contentv1.BlobRef
	for p, body := range files {
		if _, err := h.admin.PublishBlob(ctx, blobStream(pack, p, body, 1<<20)); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		refs = append(refs, &contentv1.BlobRef{Path: p, Hash: sum[:], SizeBytes: uint64(len(body))})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].GetPath() < refs[j].GetPath() })
	return h.admin.PublishVersion(ctx, &adminv1.PublishVersionRequest{PackId: pack, Blobs: refs, ParentVersion: h.reg.Newest(pack)})
}

// live publishes town as alice, approves it as bob, and activates it: the
// version in effect the activation tests move away from.
func (h *pubHarness) live(files map[string][]byte) uint64 {
	h.t.Helper()
	pv, err := h.publish(builder(alice), "town", files)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: pv.GetVersion()}); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: pv.GetVersion()}); err != nil {
		h.t.Fatal(err)
	}
	h.follow("town", pv.GetVersion())
	return pv.GetVersion()
}

func (h *pubHarness) auditRecords() []*auditv1.AuditRecord {
	h.t.Helper()
	var out []*auditv1.AuditRecord
	for _, r := range h.audit.Records() {
		var ar auditv1.AuditRecord
		if err := proto.Unmarshal(r.Value, &ar); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, &ar)
	}
	return out
}

// auditSince is the audit records written after the first n.
func (h *pubHarness) auditSince(n int) []*auditv1.AuditRecord {
	h.t.Helper()
	return h.auditRecords()[n:]
}

func adminError(t *testing.T, err error, code Code, reason string) *AdminError {
	t.Helper()
	var ae *AdminError
	if !errors.As(err, &ae) {
		t.Fatalf("want an AdminError %d/%s, got %v", code, reason, err)
	}
	if ae.Code != code || ae.Reason != reason {
		t.Fatalf("got %d/%s (%v), want %d/%s", ae.Code, ae.Reason, ae.Err, code, reason)
	}
	return ae
}

// --- AC-1, AC-2 --------------------------------------------------------------

func TestPublishVersion_ADanglingExitIsRefusedWithTheLoadersFindings(t *testing.T) {
	h := newPubHarness(t, nil)
	files := townFiles(t)
	files["town.json"] = bytes.Replace(files["town.json"], []byte(`"toRoom": "hall"`), []byte(`"toRoom": "nowhere"`), 1)
	before := len(h.auditRecords())

	_, err := h.publish(builder(alice), "town", files)
	ae := adminError(t, err, CodeInvalidArgument, ErrReasonValidation)
	pf, ok := ae.Detail.(*adminv1.PublishFindings)
	if !ok || len(pf.GetFindings()) == 0 {
		t.Fatalf("detail %v", ae.Detail)
	}
	// The same findings the loader gives the same content (AW-SRV-001).
	zones, _ := h.loader.Inputs()
	var in []sim.Input
	for _, z := range zones {
		in = append(in, z)
	}
	for _, name := range []string{"town", "docks", "wilds", "purgatory"} {
		var def contentv1.ZoneDefinition
		if verr := parseInto(files[name+".json"], &def); verr != nil {
			t.Fatal(verr)
		}
		in = append(in, sim.Input{File: name + ".json", Def: &def})
	}
	_, want := sim.BuildWorld(in, sim.Options{})
	var wantErrors []string
	for _, f := range want {
		if !sim.IsWarning(f, false) {
			wantErrors = append(wantErrors, string(f.Code)+" "+f.Detail)
		}
	}
	var got []string
	for _, d := range pf.GetFindings() {
		if d.GetSeverity() == contentv1.Severity_ERROR {
			got = append(got, d.GetCode()+" "+d.GetMessage())
		}
	}
	if !slices.Equal(got, wantErrors) || !strings.HasPrefix(got[0], "unknown_room") {
		t.Fatalf("findings %v, want the loader's %v", got, wantErrors)
	}
	if h.reg.Newest("town") != 0 {
		t.Error("a refused publish wrote a manifest")
	}
	sum := sha256.Sum256(files["town.json"])
	if !h.reg.HasBlob(sum[:]) {
		t.Error("the blobs already written were removed")
	}
	recs := h.auditSince(before)
	if len(recs) != 1 || recs[0].GetAction() != auth.ActionReject || recs[0].GetOutcome() != "rejected" || recs[0].GetFindingsCount() != uint32(len(wantErrors)) {
		t.Fatalf("audit %v", recs)
	}
	if got := testutil.ToFloat64(h.m.Publishes.WithLabelValues(PublishRejected)); got != 1 {
		t.Errorf("publishes_total{rejected} = %v", got)
	}
	if got := testutil.ToFloat64(h.m.ValidationFailures.WithLabelValues(string(sim.ErrUnknownRoom))); got != 1 {
		t.Errorf("validation_failures_total{unknown_room} = %v", got)
	}
}

func parseInto(b []byte, def *contentv1.ZoneDefinition) error {
	d, verr := parseZoneJSON("x.json", b)
	if verr != nil {
		return verr
	}
	proto.Merge(def, d)
	return nil
}

func TestPublishVersion_AValidVersionIsWrittenUnapprovedAndInactive(t *testing.T) {
	h := newPubHarness(t, nil)
	pv, err := h.publish(builder(alice), "town", townFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	if pv.GetVersion() != 1 || pv.GetCoreVersion() != 1 {
		t.Fatalf("published %v", pv)
	}
	// Purgatory's one-way Exit is a warning, not a refusal.
	if len(pv.GetWarnings()) != 1 || pv.GetWarnings()[0].GetCode() != string(sim.ErrMissingReverseExit) {
		t.Errorf("warnings %v", pv.GetWarnings())
	}
	cv, ok := h.reg.Manifest("town", 1)
	if !ok || cv.GetParentVersion() != 0 || cv.GetApprovedBy() != "" || cv.GetAuthor() != alice || cv.GetCoreVersion() != 1 {
		t.Fatalf("manifest %v", cv)
	}
	for _, ref := range cv.GetBlobs() {
		if !h.reg.HasBlob(ref.GetHash()) {
			t.Errorf("%s not keyed by its hash", ref.GetPath())
		}
	}
	if _, ok := h.reg.Pointer("town"); ok {
		t.Error("publish moved the Active Pointer")
	}
	// The next publish's parent is the newest.
	files := townFiles(t)
	files["town.json"] = bytes.Replace(files["town.json"], []byte("A dusty square"), []byte("A dustier square"), 1)
	pv2, err := h.publish(builder(bob), "town", files)
	if err != nil {
		t.Fatal(err)
	}
	if cv2, _ := h.reg.Manifest("town", pv2.GetVersion()); cv2.GetVersion() != 2 || cv2.GetParentVersion() != 1 {
		t.Fatalf("second manifest %v", cv2)
	}
}

func TestPublishVersion_AStaleParentIsRefused(t *testing.T) {
	h := newPubHarness(t, nil)
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	cv, _ := h.reg.Manifest("town", 1)
	_, err := h.admin.PublishVersion(builder(bob), &adminv1.PublishVersionRequest{PackId: "town", Blobs: cv.GetBlobs(), ParentVersion: 0})
	adminError(t, err, CodeFailedPrecondition, ErrReasonStaleParent)
	if got := testutil.ToFloat64(h.m.Publishes.WithLabelValues(PublishStaleParent)); got != 1 {
		t.Errorf("publishes_total{stale_parent} = %v", got)
	}
}

// --- AC-3 to AC-6 ------------------------------------------------------------

func TestActivateVersion_UnapprovedIsRefusedForAnyoneWithoutOverride(t *testing.T) {
	h := newPubHarness(t, nil)
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{builder(alice), builder(bob), operator()} {
		before := len(h.auditRecords())
		_, err := h.admin.ActivateVersion(ctx, &adminv1.ActivateVersionRequest{PackId: "town", Version: 1})
		ae := adminError(t, err, CodeFailedPrecondition, ErrReasonUnapproved)
		if !strings.Contains(ae.Error(), "approval") {
			t.Errorf("%q doesn't name the missing approval", ae.Error())
		}
		if recs := h.auditSince(before); len(recs) != 1 || recs[0].GetOutcome() != "refused" {
			t.Fatalf("audit %v", recs)
		}
	}
	if _, ok := h.reg.Pointer("town"); ok {
		t.Error("the pointer moved")
	}
	if got := testutil.ToFloat64(h.m.ActivationsRefused.WithLabelValues(RefusedUnapproved)); got != 3 {
		t.Errorf("activations_refused_total{unapproved} = %v", got)
	}
}

func TestApproveVersion_ABuilderCannotApproveTheirOwnAndAnotherCan(t *testing.T) {
	h := newPubHarness(t, nil)
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	_, err := h.admin.ApproveVersion(builder(alice), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1})
	adminError(t, err, CodePermissionDenied, ErrReasonSelfApproval)
	if got := testutil.ToFloat64(h.m.Approvals.WithLabelValues(ApprovalSelf)); got != 1 {
		t.Errorf("approvals_total{self} = %v", got)
	}

	before := len(h.auditRecords())
	ap, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1})
	if err != nil || ap.GetApprovedBy() != bob || ap.GetSelfApproval() {
		t.Fatalf("approve %v %v", ap, err)
	}
	cv, _ := h.reg.Manifest("town", 1)
	if cv.GetApprovedBy() != bob || cv.GetApprovedAtUnixNano() == 0 {
		t.Fatalf("manifest %v", cv)
	}
	if recs := h.auditSince(before); len(recs) != 1 || recs[0].GetAction() != auth.ActionApprove || recs[0].GetActorAccountId() != bob {
		t.Fatalf("audit %v", recs)
	}
}

// The real actor behind an acting-as publish is the publisher too: an
// Operator who published as alice, and a Builder account alice acted as.
func TestApproveVersion_ActingAsDoesNotHideTheSamePerson(t *testing.T) {
	h := newPubHarness(t, func(o *AdminOptions, _ *LoaderOptions) { o.OperatorSelfApproval = false })
	asAlice := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: brian, ActingAs: alice, Roles: []auth.Role{auth.RoleBuilder}})
	if _, err := h.publish(asAlice, "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	cv, _ := h.reg.Manifest("town", 1)
	if cv.GetAuthor() != alice || h.reg.PublishedBy("town", 1) != brian {
		t.Fatalf("author %s, published by %s", cv.GetAuthor(), h.reg.PublishedBy("town", 1))
	}
	_, err := h.admin.ApproveVersion(operator(), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1})
	adminError(t, err, CodePermissionDenied, ErrReasonSelfApproval)
}

func TestActivateVersion_AnApprovedVersionMovesThePointer(t *testing.T) {
	h := newPubHarness(t, nil)
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1}); err != nil {
		t.Fatal(err)
	}
	pointerBefore, auditBefore := len(h.active.Records()), len(h.auditRecords())
	av, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: 1})
	if err != nil || av.GetPreviousVersion() != 0 || av.GetRollback() {
		t.Fatalf("activate %v %v", av, err)
	}
	recs := h.active.Records()[pointerBefore:]
	if len(recs) != 1 {
		t.Fatalf("%d pointer records", len(recs))
	}
	var ptr contentv1.ActiveVersion
	_ = proto.Unmarshal(recs[0].Value, &ptr)
	if ptr.GetPackId() != "town" || ptr.GetVersion() != 1 || ptr.GetActivatedBy() != alice {
		t.Fatalf("pointer %v", &ptr)
	}
	if a := h.auditSince(auditBefore); len(a) != 1 || a[0].GetAction() != auth.ActionActivate || a[0].GetOutcome() != auth.AuditOK {
		t.Fatalf("audit %v", a)
	}
	if got := testutil.ToFloat64(h.m.PointerMoves.WithLabelValues(DirectionForward, "false")); got != 1 {
		t.Errorf("pointer_moves_total{forward,false} = %v", got)
	}
}

func TestActivateVersion_RollbackNeedsNoFreshApprovalAndLeavesNewerIntact(t *testing.T) {
	h := newPubHarness(t, nil)
	var last uint64
	for i := range 4 {
		files := townFiles(t)
		files["town.json"] = bytes.Replace(files["town.json"], []byte("A dusty square"), []byte("A dusty square "+strings.Repeat("!", i)), 1)
		last = h.live(files)
	}
	target := last - 3
	approvalsBefore := testutil.ToFloat64(h.m.Approvals.WithLabelValues(ApprovalOK))
	pointerBefore := len(h.active.Records())
	av, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: target})
	if err != nil || !av.GetRollback() || av.GetPreviousVersion() != last {
		t.Fatalf("rollback %v %v", av, err)
	}
	if n := len(h.active.Records()) - pointerBefore; n != 1 {
		t.Errorf("%d pointer moves", n)
	}
	if testutil.ToFloat64(h.m.Approvals.WithLabelValues(ApprovalOK)) != approvalsBefore {
		t.Error("the rollback took a fresh approval")
	}
	if got := testutil.ToFloat64(h.m.PointerMoves.WithLabelValues(DirectionRollback, "false")); got != 1 {
		t.Errorf("pointer_moves_total{rollback,false} = %v", got)
	}
	h.follow("town", target)
	// N is intact and can come back.
	if _, ok := h.reg.Manifest("town", last); !ok {
		t.Fatal("the newer version is gone")
	}
	if av, err := h.admin.ActivateVersion(builder(bob), &adminv1.ActivateVersionRequest{PackId: "town", Version: last}); err != nil || av.GetRollback() {
		t.Fatalf("re-activate %v %v", av, err)
	}
	list, err := h.admin.ListVersions(builder(carol), &adminv1.ListVersionsRequest{PackId: CorePack})
	if err != nil || list.GetActiveVersion() != 1 {
		t.Fatalf("a Builder without core reads core's versions: %v %v", list, err)
	}
	list, err = h.admin.ListVersions(builder(alice), &adminv1.ListVersionsRequest{PackId: "town"})
	if err != nil || list.GetActiveVersion() != last || len(list.GetVersions()) != 4 || list.GetVersions()[0].GetVersion() != last || len(list.GetActivations()) != 6 {
		t.Fatalf("list %v %v", list, err)
	}
}

// --- AC-7, AC-8 ----------------------------------------------------------------

func TestPublishVersion_ABuilderWithoutThePackIsDenied(t *testing.T) {
	h := newPubHarness(t, nil)
	refs := h.putBlobs(townFiles(t))
	before := len(h.auditRecords())
	_, err := h.admin.PublishVersion(builder(carol), &adminv1.PublishVersionRequest{PackId: "town", Blobs: refs})
	adminError(t, err, CodePermissionDenied, ErrReasonPackNotHeld)
	if recs := h.auditSince(before); len(recs) != 1 || recs[0].GetOutcome() != auth.AuditDenied || recs[0].GetActorAccountId() != carol {
		t.Fatalf("audit %v", recs)
	}
}

func TestActivateVersion_AnOperatorOverridesApprovalWithAReason(t *testing.T) {
	h := newPubHarness(t, nil)
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	_, err := h.admin.ActivateVersion(operator(), &adminv1.ActivateVersionRequest{PackId: "town", Version: 1, Override: true})
	adminError(t, err, CodeInvalidArgument, ErrReasonValidation)
	_, err = h.admin.ActivateVersion(builder(bob), &adminv1.ActivateVersionRequest{PackId: "town", Version: 1, Override: true, Reason: "3 am"})
	adminError(t, err, CodePermissionDenied, ErrReasonOperatorOnly)

	before := len(h.auditRecords())
	if _, err := h.admin.ActivateVersion(operator(), &adminv1.ActivateVersionRequest{PackId: "town", Version: 1, Override: true, Reason: "broken exit at 3 am"}); err != nil {
		t.Fatal(err)
	}
	recs := h.auditSince(before)
	if len(recs) != 1 || recs[0].GetAction() != auth.ActionOverride || !recs[0].GetOverride() || recs[0].GetReason() != "broken exit at 3 am" {
		t.Fatalf("audit %v", recs)
	}
	if got := testutil.ToFloat64(h.m.PointerMoves.WithLabelValues(DirectionForward, "true")); got != 1 {
		t.Errorf("pointer_moves_total{forward,true} = %v", got)
	}
	if !strings.Contains(h.logs.String(), `"reason":"broken exit at 3 am"`) {
		t.Error("the override's warn line lacks its reason")
	}
}

// --- AC-9, AC-12 -------------------------------------------------------------

func TestPublishBlob_APresentBlobIsReportedAndDeduplicated(t *testing.T) {
	h := newPubHarness(t, nil)
	body := []byte(`{"formatVersion":1}`)
	sum := sha256.Sum256(body)
	other := sha256.Sum256([]byte("absent"))
	if r, err := h.admin.PublishBlob(builder(alice), blobStream("town", "a.json", body, 4)); err != nil || r.GetDeduplicated() {
		t.Fatalf("first %v %v", r, err)
	}
	has, err := h.admin.HasBlobs(builder(alice), &adminv1.HasBlobsRequest{PackId: "town", Hashes: [][]byte{sum[:], other[:]}})
	if err != nil || !slices.Equal(has.GetPresent(), []bool{true, false}) {
		t.Fatalf("HasBlobs %v %v", has, err)
	}
	n := len(h.blobs.Records())
	if r, err := h.admin.PublishBlob(builder(bob), blobStream("town", "b.json", body, 1<<20)); err != nil || !r.GetDeduplicated() {
		t.Fatalf("again %v %v", r, err)
	}
	if len(h.blobs.Records()) != n {
		t.Error("a present blob was written again")
	}
	if got := testutil.ToFloat64(h.m.BlobBytes); got != float64(len(body)) {
		t.Errorf("blob_bytes_total = %v, want %d once", got, len(body))
	}
}

func TestPublishBlob_ABlobOverTheLimitIsRefusedBeforeAnythingIsProduced(t *testing.T) {
	h := newPubHarness(t, func(o *AdminOptions, _ *LoaderOptions) { o.MaxBlobBytes = 1024 })
	n := len(h.blobs.Records())
	// The header says so.
	_, err := h.admin.PublishBlob(builder(alice), blobStream("town", "big.json", make([]byte, 2048), 512))
	adminError(t, err, CodeResourceExhausted, ErrReasonBlobTooLarge)
	// The header lies, and the bytes say so.
	s := blobStream("town", "big.json", make([]byte, 2048), 512)
	s.msgs[0].GetHeader().SizeBytes = 100
	_, err = h.admin.PublishBlob(builder(alice), s)
	adminError(t, err, CodeResourceExhausted, ErrReasonBlobTooLarge)
	if len(h.blobs.Records()) != n {
		t.Error("an oversized blob was produced")
	}
	// A body that doesn't hash to its header is refused too.
	s = blobStream("town", "a.json", []byte("abc"), 1)
	s.msgs[1].Chunk = &adminv1.PublishBlobRequest_Data{Data: []byte("x")}
	_, err = h.admin.PublishBlob(builder(alice), s)
	adminError(t, err, CodeInvalidArgument, ErrReasonHashMismatch)
}

func TestPublishVersion_APackOverTheLimitIsRefused(t *testing.T) {
	h := newPubHarness(t, func(o *AdminOptions, _ *LoaderOptions) { o.MaxPackBytes = 1024 })
	refs := h.putBlobs(townFiles(t))
	_, err := h.admin.PublishVersion(builder(alice), &adminv1.PublishVersionRequest{PackId: "town", Blobs: refs})
	adminError(t, err, CodeResourceExhausted, ErrReasonPackTooLarge)
}

// --- AC-11 -------------------------------------------------------------------

func TestCore_NoRPCPublishesItAndOnlyAnOperatorMovesIt(t *testing.T) {
	h := newPubHarness(t, nil)
	for _, ctx := range []context.Context{builder(alice), operator()} {
		before := len(h.auditRecords())
		_, err := h.admin.PublishBlob(ctx, blobStream(CorePack, "templates/x.json", []byte("{}"), 8))
		adminError(t, err, CodePermissionDenied, ErrReasonCorePublish)
		_, err = h.admin.PublishVersion(ctx, &adminv1.PublishVersionRequest{PackId: CorePack})
		adminError(t, err, CodePermissionDenied, ErrReasonCorePublish)
		if n := len(h.auditSince(before)); n != 2 {
			t.Errorf("%d audit records for two refused core publishes", n)
		}
	}
	_, err := h.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: CorePack, Version: 1})
	adminError(t, err, CodePermissionDenied, ErrReasonOperatorOnly)

	h.seedCore(2)
	before := len(h.auditRecords())
	// Back to 1, unapproved (core never is), by the Operator.
	av, err := h.admin.ActivateVersion(operator(), &adminv1.ActivateVersionRequest{PackId: CorePack, Version: 1})
	if err != nil || !av.GetRollback() {
		t.Fatalf("operator activating core@1: %v %v", av, err)
	}
	if recs := h.auditSince(before); len(recs) != 1 || recs[0].GetActorAccountId() != brian || recs[0].GetOverride() {
		t.Fatalf("audit %v", recs)
	}
}

// --- AC-13 -------------------------------------------------------------------

func TestApproveVersion_AnOperatorApprovesTheirOwnPublish(t *testing.T) {
	cases := []struct {
		name string
		pub  context.Context
	}{
		{"published as another Builder", auth.WithPrincipal(context.Background(), auth.Principal{AccountID: brian, ActingAs: alice, Roles: []auth.Role{auth.RoleBuilder}})},
		{"published directly", operator()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPubHarness(t, nil)
			if _, err := h.publish(tc.pub, "town", townFiles(t)); err != nil {
				t.Fatal(err)
			}
			before := len(h.auditRecords())
			ap, err := h.admin.ApproveVersion(operator(), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1})
			if err != nil || ap.GetApprovedBy() != brian || !ap.GetSelfApproval() {
				t.Fatalf("approve %v %v", ap, err)
			}
			recs := h.auditSince(before)
			if len(recs) != 1 || !recs[0].GetSelfApproval() || recs[0].GetActorAccountId() != brian {
				t.Fatalf("audit %v", recs)
			}
			if got := testutil.ToFloat64(h.m.Approvals.WithLabelValues(ApprovalSelfOperator)); got != 1 {
				t.Errorf("approvals_total{self_operator} = %v", got)
			}
			if !strings.Contains(h.logs.String(), `"self_approval":true`) || !strings.Contains(h.logs.String(), `"level":"WARN"`) {
				t.Error("no warn line with self_approval=true")
			}
		})
	}
	t.Run("switched off", func(t *testing.T) {
		h := newPubHarness(t, func(o *AdminOptions, _ *LoaderOptions) { o.OperatorSelfApproval = false })
		if _, err := h.publish(operator(), "town", townFiles(t)); err != nil {
			t.Fatal(err)
		}
		_, err := h.admin.ApproveVersion(operator(), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1})
		adminError(t, err, CodePermissionDenied, ErrReasonSelfApproval)
		if got := testutil.ToFloat64(h.m.Approvals.WithLabelValues(ApprovalSelf)); got != 1 {
			t.Errorf("approvals_total{self} = %v", got)
		}
	})
}

// --- AC-14 -------------------------------------------------------------------

func TestActivateVersion_RefusesWhatTheLoaderWouldRefuse(t *testing.T) {
	approved := func(h *pubHarness, files map[string][]byte) uint64 {
		t.Helper()
		pv, err := h.publish(builder(alice), "town", files)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: pv.GetVersion()}); err != nil {
			t.Fatal(err)
		}
		return pv.GetVersion()
	}
	refused := func(t *testing.T, h *pubHarness, pack string, v uint64, reason string, subjects ...string) {
		t.Helper()
		before, pointer := len(h.auditRecords()), h.reg.Pointers()[pack]
		for _, ctx := range []context.Context{builder(alice), operator()} {
			req := &adminv1.ActivateVersionRequest{PackId: pack, Version: v}
			if ctx != nil && pack != CorePack && ctxIsOperator(ctx) {
				req.Override, req.Reason = true, "override doesn't bypass this"
			}
			if pack == CorePack && !ctxIsOperator(ctx) {
				continue
			}
			_, err := h.admin.ActivateVersion(ctx, req)
			ae := adminError(t, err, CodeFailedPrecondition, reason)
			ar, ok := ae.Detail.(*adminv1.ActivationRefusal)
			if !ok || ar.GetReason() != reason || !slices.Equal(ar.GetSubjects(), subjects) {
				t.Fatalf("detail %v, want %s %v", ae.Detail, reason, subjects)
			}
		}
		if h.reg.Pointers()[pack] != pointer {
			t.Error("the pointer moved")
		}
		for _, r := range h.auditSince(before) {
			if r.GetOutcome() != "refused" {
				t.Errorf("audit %v", r)
			}
		}
		if got := testutil.ToFloat64(h.m.ActivationsRefused.WithLabelValues(reason)); got == 0 {
			t.Errorf("activations_refused_total{%s} = 0", reason)
		}
	}

	t.Run("zone_removed", func(t *testing.T) {
		h := newPubHarness(t, nil)
		h.live(townFiles(t))
		files := townFiles(t)
		delete(files, "docks.json")
		files["town.json"] = bytes.Replace(files["town.json"], []byte(`,
        {
          "direction": "south",
          "toZone": "docks",
          "toRoom": "pier"
        }`), nil, 1)
		v := approved(h, files)
		refused(t, h, "town", v, RefusalZoneRemoved, "docks")
	})
	t.Run("spawn_room_removed", func(t *testing.T) {
		h := newPubHarness(t, func(_ *AdminOptions, l *LoaderOptions) { l.SpawnRoom = sim.RoomRef{Zone: "purgatory", Room: "start"} })
		h.live(townFiles(t))
		files := townFiles(t)
		files["purgatory.json"] = bytes.ReplaceAll(files["purgatory.json"], []byte(`"start"`), []byte(`"waiting"`))
		v := approved(h, files)
		refused(t, h, "town", v, RefusalSpawnRoomRemoved, "purgatory/start")
	})
	t.Run("core_version", func(t *testing.T) {
		h := newPubHarness(t, nil)
		h.seedCore(2)
		files := townFiles(t)
		files["src/pack.aw"] = []byte("pack town requires andara.core@2\n")
		v := h.live(files)
		refused(t, h, CorePack, 1, RefusalCoreVersion, "town@"+itoa(v))
	})
}

func ctxIsOperator(ctx context.Context) bool {
	p, _ := auth.PrincipalFrom(ctx)
	return p.Has(auth.RoleOperator)
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

// --- AC-18 -------------------------------------------------------------------

func TestGetBlob_StreamsAManifestsBlobInBoundedChunks(t *testing.T) {
	h := newPubHarness(t, nil)
	files := townFiles(t)
	big := bytes.Repeat([]byte("andara "), (5<<20)/7)
	files["README"] = big
	if _, err := h.publish(builder(alice), "town", files); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(big)
	var got []byte
	chunks := 0
	err := h.admin.GetBlob(builder(bob), &adminv1.GetBlobRequest{PackId: "town", Version: 1, Hash: sum[:]}, func(r *adminv1.GetBlobResponse) error {
		if len(r.GetData()) > BlobChunkBytes {
			t.Fatalf("a %d-byte chunk", len(r.GetData()))
		}
		chunks++
		got = append(got, r.GetData()...)
		return nil
	})
	if err != nil || chunks < 5 {
		t.Fatalf("GetBlob: %d chunks, %v", chunks, err)
	}
	if s := sha256.Sum256(got); !bytes.Equal(s[:], sum[:]) {
		t.Fatal("the body doesn't hash to the request's hash")
	}
	// Core's templates are in the store, but not in town@1.
	coreCV, _ := h.reg.Manifest(CorePack, 1)
	err = h.admin.GetBlob(builder(bob), &adminv1.GetBlobRequest{PackId: "town", Version: 1, Hash: coreCV.GetBlobs()[0].GetHash()}, func(*adminv1.GetBlobResponse) error { return nil })
	adminError(t, err, CodeNotFound, ErrReasonNotFound)
	err = h.admin.GetBlob(builder(carol), &adminv1.GetBlobRequest{PackId: "town", Version: 1, Hash: sum[:]}, func(*adminv1.GetBlobResponse) error { return nil })
	adminError(t, err, CodePermissionDenied, ErrReasonPackNotHeld)
}

// --- the authorization matrix --------------------------------------------------

func TestAuthorizationMatrix(t *testing.T) {
	h := newPubHarness(t, nil)
	if _, err := h.publish(builder(alice), "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	player := as("acct-player", auth.RolePlayer)
	type call func(context.Context) error
	calls := map[string]call{
		"HasBlobs": func(ctx context.Context) error {
			_, err := h.admin.HasBlobs(ctx, &adminv1.HasBlobsRequest{PackId: "town"})
			return err
		},
		"ListVersions": func(ctx context.Context) error {
			_, err := h.admin.ListVersions(ctx, &adminv1.ListVersionsRequest{PackId: "town"})
			return err
		},
		"GetVersion": func(ctx context.Context) error {
			_, err := h.admin.GetVersion(ctx, &adminv1.GetVersionRequest{PackId: "town", Version: 1})
			return err
		},
		"ListVersions core": func(ctx context.Context) error {
			_, err := h.admin.ListVersions(ctx, &adminv1.ListVersionsRequest{PackId: CorePack})
			return err
		},
		"ReloadContent": func(ctx context.Context) error {
			_, err := h.admin.ReloadContent(ctx, &adminv1.ReloadContentRequest{})
			return err
		},
	}
	// ✓ is nil; otherwise the reason.
	want := map[string]map[string]string{
		"HasBlobs":          {"with": "", "without": ErrReasonPackNotHeld, "operator": "", "player": ErrReasonPackNotHeld},
		"ListVersions":      {"with": "", "without": ErrReasonPackNotHeld, "operator": "", "player": ErrReasonPackNotHeld},
		"GetVersion":        {"with": "", "without": ErrReasonPackNotHeld, "operator": "", "player": ErrReasonPackNotHeld},
		"ListVersions core": {"with": "", "without": "", "operator": "", "player": ErrReasonPackNotHeld},
		// No Reload is wired in this harness: an Operator gets past the
		// check and reaches UNAVAILABLE.
		"ReloadContent": {"with": ErrReasonOperatorOnly, "without": ErrReasonOperatorOnly, "operator": "unavailable", "player": ErrReasonOperatorOnly},
	}
	who := map[string]context.Context{"with": builder(bob), "without": builder(carol), "operator": operator(), "player": player}
	for name, c := range calls {
		for w, ctx := range who {
			err := c(ctx)
			var ae *AdminError
			got := ""
			switch {
			case err == nil:
			case errors.As(err, &ae) && ae.Code == CodeUnavailable:
				got = "unavailable"
			case errors.As(err, &ae):
				got = ae.Reason
			default:
				got = err.Error()
			}
			if got != want[name][w] {
				t.Errorf("%s as %s: %q, want %q", name, w, got, want[name][w])
			}
		}
	}
}

// --- the registry's history ------------------------------------------------------

// A restart rebuilds everything the publish path answers from, including what
// only the audit topic keeps: who really published, and every pointer move.
func TestRegistry_ReopenRebuildsTheIndexAndTheHistory(t *testing.T) {
	h := newPubHarness(t, nil)
	asAlice := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: brian, ActingAs: alice, Roles: []auth.Role{auth.RoleBuilder}})
	if _, err := h.publish(asAlice, "town", townFiles(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.admin.ActivateVersion(builder(bob), &adminv1.ActivateVersionRequest{PackId: "town", Version: 1}); err != nil {
		t.Fatal(err)
	}
	r, err := OpenRegistry(context.Background(), RegistryOptions{Blobs: h.blobs, Versions: h.versions, Active: h.active, Audit: h.audit})
	if err != nil {
		t.Fatal(err)
	}
	cv, ok := r.Manifest("town", 1)
	if !ok || cv.GetApprovedBy() != bob || r.Newest("town") != 1 || r.PublishedBy("town", 1) != brian {
		t.Fatalf("reopened: %v, newest %d, published by %q", cv, r.Newest("town"), r.PublishedBy("town", 1))
	}
	if av, ok := r.Pointer("town"); !ok || av.GetVersion() != 1 || av.GetActivatedBy() != bob {
		t.Fatalf("pointer %v", av)
	}
	if acts := r.Activations("town"); len(acts) != 1 || acts[0].GetActivatedBy() != bob {
		t.Fatalf("history %v", acts)
	}
	for _, ref := range cv.GetBlobs() {
		if !r.HasBlob(ref.GetHash()) {
			t.Errorf("%s not present after reopen", ref.GetPath())
		}
	}
}
