// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"

	"github.com/valesordev/andara/content/core"
	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/recordlog"
)

type coreStore struct {
	blobs, versions, active, audit *recordlog.Memory
	reg                            *Registry
}

func newCoreStore(t *testing.T) *coreStore {
	t.Helper()
	s := &coreStore{blobs: recordlog.NewMemory(), versions: recordlog.NewMemory(), active: recordlog.NewMemory(), audit: recordlog.NewMemory()}
	s.reopen(t)
	return s
}

// reopen is a restart: the index rebuilt from the topics.
func (s *coreStore) reopen(t *testing.T) {
	t.Helper()
	reg, err := OpenRegistry(context.Background(), RegistryOptions{Blobs: s.blobs, Versions: s.versions, Active: s.active, Audit: s.audit, Cache: BlobCache{Dir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	s.reg = reg
}

func (s *coreStore) boot(t *testing.T, version uint64, blobs map[string][]byte) (CoreBoot, *PublishMetrics, string, error) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	m := NewPublishMetrics(nil)
	res, err := BootCore(context.Background(), CoreBootOptions{
		Registry: s.reg, Auditor: auth.NewAuditor(s.audit, log, nil, nil), Metrics: m, Log: log,
		Pack: CorePack, Version: version, Blobs: blobs, Build: "v0.9.0",
	})
	return res, m, logs.String(), err
}

func (s *coreStore) auditRecords(t *testing.T) []*auditv1.AuditRecord {
	t.Helper()
	var out []*auditv1.AuditRecord
	for _, r := range s.audit.Records() {
		var ar auditv1.AuditRecord
		if err := proto.Unmarshal(r.Value, &ar); err != nil {
			t.Fatal(err)
		}
		out = append(out, &ar)
	}
	return out
}

// AC-15: a store without core gets the build's, published and activated by
// the server, one audit record per step; a second boot of the same build
// publishes nothing and moves nothing.
func TestBootCore_AnEmptyStoreGetsTheBuildsCore(t *testing.T) {
	s := newCoreStore(t)
	res, m, logs, err := s.boot(t, core.Version(), core.Blobs())
	if err != nil || !res.Published || !res.Activated || res.Active != core.Version() || res.ActiveBy != ServerPrincipal {
		t.Fatalf("boot: %+v %v", res, err)
	}
	cv, ok := s.reg.Manifest(CorePack, core.Version())
	if !ok || cv.GetAuthor() != ServerPrincipal || cv.GetPublisher() != ServerPrincipal || !bytes.Equal(BlobHashesDigest(cv.GetBlobs()), core.Digest(core.Blobs())) {
		t.Fatalf("manifest %v", cv)
	}
	av, _ := s.reg.Pointer(CorePack)
	if av.GetActivatedBy() != ServerPrincipal {
		t.Fatalf("pointer %v", av)
	}
	recs := s.auditRecords(t)
	if len(recs) != 2 || recs[0].GetAction() != auth.ActionPublish || recs[1].GetAction() != auth.ActionActivate {
		t.Fatalf("audit %v", recs)
	}
	for _, r := range recs {
		if r.GetActorAccountId() != ServerPrincipal || r.GetReason() != "boot v0.9.0" {
			t.Errorf("audit record %v", r)
		}
	}
	if got := testutil.ToFloat64(m.PointerMoves.WithLabelValues(DirectionForward, "false")); got != 1 {
		t.Errorf("pointer_moves_total{forward,false} = %v", got)
	}
	if !strings.Contains(logs, "content core: andara.core@1 published; activated") {
		t.Errorf("no core line: %s", logs)
	}

	s.reopen(t)
	n, p := len(s.versions.Records()), len(s.active.Records())
	res, _, logs, err = s.boot(t, core.Version(), core.Blobs())
	if err != nil || res.Published || res.Activated {
		t.Fatalf("second boot: %+v %v", res, err)
	}
	if len(s.versions.Records()) != n || len(s.active.Records()) != p || len(s.auditRecords(t)) != 2 {
		t.Error("a second boot of the same build wrote something")
	}
	if !strings.Contains(logs, "content core: andara.core@1 present; active andara.core@1 by server") {
		t.Errorf("no core line: %s", logs)
	}
}

// AC-16: two builds disagreeing about core N stop the second, which writes
// nothing and names both digests.
func TestBootCore_AnotherDigestForTheSameVersionExitsAndWritesNothing(t *testing.T) {
	s := newCoreStore(t)
	if _, _, _, err := s.boot(t, 1, core.Blobs()); err != nil {
		t.Fatal(err)
	}
	other := core.Blobs()
	for p := range other {
		other[p] = append(other[p], ' ')
		break
	}
	n, p, a := len(s.blobs.Records()), len(s.versions.Records()), len(s.audit.Records())
	_, _, logs, err := s.boot(t, 1, other)
	var cd *ErrCoreDigest
	if !errors.As(err, &cd) || bytes.Equal(cd.Stored, cd.Built) {
		t.Fatalf("err %v", err)
	}
	if len(s.blobs.Records()) != n || len(s.versions.Records()) != p || len(s.audit.Records()) != a {
		t.Error("a mismatched core wrote something")
	}
	if !strings.Contains(logs, `"level":"ERROR"`) || !strings.Contains(logs, "stored_digest") || !strings.Contains(logs, "built_digest") {
		t.Errorf("no error line naming both digests: %s", logs)
	}
}

// AC-17: the server moves core forward over its own pointer, leaves an
// Account's, and never moves backwards.
func TestBootCore_WhoseCorePointerMoves(t *testing.T) {
	v2 := func() map[string][]byte {
		b := core.Blobs()
		b["templates/andara.core.Entity.json"] = append(b["templates/andara.core.Entity.json"], '\n')
		return b
	}
	cases := []struct {
		name string
		// setup leaves core@1 active, moved by by.
		by      string
		boot    uint64
		blobs   map[string][]byte
		active  uint64
		level   string
		mention []string
	}{
		{"the server's own pointer moves forward", ServerPrincipal, 2, v2(), 2, "INFO", []string{"activated"}},
		{"an Account's pointer is left", "acct-brian", 2, v2(), 1, "WARN", []string{"andara.core@2", "active andara.core@1 by acct-brian"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newCoreStore(t)
			if _, _, _, err := s.boot(t, 1, core.Blobs()); err != nil {
				t.Fatal(err)
			}
			if tc.by != ServerPrincipal {
				if _, err := s.reg.MovePointer(context.Background(), CorePack, 1, tc.by, ""); err != nil {
					t.Fatal(err)
				}
			}
			res, _, logs, err := s.boot(t, tc.boot, tc.blobs)
			if err != nil || !res.Published || res.Active != tc.active {
				t.Fatalf("boot: %+v %v", res, err)
			}
			if !strings.Contains(logs, `"level":"`+tc.level+`"`) {
				t.Errorf("want a %s line: %s", tc.level, logs)
			}
			for _, m := range tc.mention {
				if !strings.Contains(logs, m) {
					t.Errorf("the line doesn't name %q: %s", m, logs)
				}
			}
		})
	}
	t.Run("an older build leaves a newer core", func(t *testing.T) {
		s := newCoreStore(t)
		if _, _, _, err := s.boot(t, 1, core.Blobs()); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := s.boot(t, 2, v2()); err != nil {
			t.Fatal(err)
		}
		p := len(s.active.Records())
		res, _, logs, err := s.boot(t, 1, core.Blobs())
		if err != nil || res.Activated || res.Active != 2 || len(s.active.Records()) != p {
			t.Fatalf("boot: %+v %v", res, err)
		}
		if !strings.Contains(logs, `"level":"INFO"`) || !strings.Contains(logs, "active andara.core@2 by server") {
			t.Errorf("want an info line naming core@2: %s", logs)
		}
	})
	t.Run("a newer build over an older one names its parent", func(t *testing.T) {
		s := newCoreStore(t)
		if _, _, _, err := s.boot(t, 1, core.Blobs()); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := s.boot(t, 3, v2()); err != nil {
			t.Fatal(err)
		}
		cv, _ := s.reg.Manifest(CorePack, 3)
		if cv.GetParentVersion() != 1 {
			t.Fatalf("parent %d, want 1", cv.GetParentVersion())
		}
	})
}
