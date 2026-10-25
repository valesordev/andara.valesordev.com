// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/twmb/franz-go/pkg/kadm"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	"github.com/valesordev/andara/internal/eventually"
	"github.com/valesordev/andara/internal/testpki"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/recordlog"
)

// TestActAs_ThroughTheGateway is AW-SRV-039's integration test, shipped by
// AW-SRV-062 (AC-7): the Account store and the audit log on Redpanda, the
// Gateway's auth interceptor resolving andara-act-as, and the content Admin
// behind it. An Operator publishes and activates town as a Builder and
// approves it as themselves (AW-SRV-039 AC-8: a version they published is
// theirs to self-approve, as anyone else); then a Game Master naming the
// Operator meets the Ceiling.
func TestActAs_ThroughTheGateway(t *testing.T) {
	tp, auditTopic, cl := publishTopics(t)
	ctx := context.Background()
	accountsTopic := fmt.Sprintf("andara.test.actas.accounts.%d", time.Now().UnixNano())
	adm := kadm.NewClient(cl)
	if _, err := adm.CreateTopic(ctx, 6, 1, map[string]*string{"cleanup.policy": kadm.StringPtr("compact")}, accountsTopic); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adm.DeleteTopics(context.Background(), accountsTopic) })

	b := &brokerPublish{t: t, tp: tp, auditTop: auditTopic, cl: cl}
	kr, err := auth.ParseKeyring(strings.NewReader("k1: " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := auth.Open(ctx, auth.Options{
		Accounts: b.open(accountsTopic, 0), Audit: b.open(auditTopic, 0), Keys: kr,
		Argon2:     auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1},
		SessionTTL: time.Hour, RefreshTTL: time.Hour, InviteTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	b.holder = packHolderFunc(func(a string) []string { return store.BuilderPacks(a) })
	b.start()

	bootstrap := auth.WithPrincipal(ctx, auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	ids, tokens := map[string]string{}, map[string]string{}
	for name, roles := range map[string][]auth.Role{
		"oper":    {auth.RoleOperator},
		"alice":   {auth.RoleBuilder},
		"gmaster": {auth.RoleGameMaster},
	} {
		id, err := store.CreateAccount(bootstrap, name, "correct horse battery", roles)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := store.Authenticate(ctx, name, "correct horse battery", "peer")
		if err != nil {
			t.Fatal(err)
		}
		ids[name], tokens[name] = id, pair.SessionToken
	}
	if _, err := store.SetBuilderPacks(bootstrap, ids["alice"], []string{"town"}, 0); err != nil {
		t.Fatal(err)
	}

	reg := prometheus.NewRegistry()
	pki := testpki.New(t)
	gw, err := gateway.New(gateway.Options{
		Verifier: store, ContentAdmin: b.admin,
		Listen: "127.0.0.1:0", TLSCertFile: pki.CertFile, TLSKeyFile: pki.KeyFile,
		MaxRecvBytes: 16 << 20, MaxRequestTimeout: 30 * time.Second, DrainTimeout: 5 * time.Second,
		ProtocolMin: 1, ProtocolMax: 1, Build: gateway.BuildInfo{Version: "test", Commit: "abc123"}, Environment: "test",
		Log: slog.New(slog.DiscardHandler), Registry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := gw.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = gw.Shutdown(sctx)
	})
	client := func(user, actAs string) adminv1connect.AdminClient {
		h := http.Header{"Authorization": {"Bearer " + tokens[user]}}
		if actAs != "" {
			h.Set(gateway.ActAsHeader, actAs)
		}
		tr := &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}
		t.Cleanup(tr.CloseIdleConnections)
		return adminv1connect.NewAdminClient(&http.Client{Transport: withHeaders{h, tr}}, "https://"+gw.Addr().String())
	}
	asAlice, asOper := client("oper", ids["alice"]), client("oper", "")
	okBefore := actAsTotal(t, reg, "ok")

	// Publish as alice: every blob, then the version.
	files := townFiles(t)
	paths := slices.Sorted(maps.Keys(files))
	req := &adminv1.PublishVersionRequest{PackId: "town", ParentVersion: b.reg.Newest("town")}
	for _, p := range paths {
		stream := asAlice.PublishBlob(ctx)
		for _, m := range blobStream("town", p, files[p], BlobChunkBytes).msgs {
			if err := stream.Send(m); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := stream.CloseAndReceive(); err != nil {
			t.Fatalf("PublishBlob %s: %v", p, err)
		}
		sum := sha256.Sum256(files[p])
		req.Blobs = append(req.Blobs, blobRef(p, sum[:], len(files[p])))
	}
	pv, err := asAlice.PublishVersion(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	version := pv.Msg.GetVersion()
	if _, err := asOper.ApproveVersion(ctx, connect.NewRequest(&adminv1.ApproveVersionRequest{PackId: "town", Version: version})); err != nil {
		t.Fatalf("the Operator's self-approval: %v", err)
	}
	if _, err := asAlice.ActivateVersion(ctx, connect.NewRequest(&adminv1.ActivateVersionRequest{PackId: "town", Version: version})); err != nil {
		t.Fatal(err)
	}
	got, err := asOper.GetVersion(ctx, connect.NewRequest(&adminv1.GetVersionRequest{PackId: "town", Version: version}))
	if err != nil {
		t.Fatal(err)
	}
	if m := got.Msg.GetVersion(); m.GetAuthor() != ids["alice"] || m.GetPublisher() != ids["oper"] {
		t.Fatalf("manifest author %s, publisher %s; want %s, %s", m.GetAuthor(), m.GetPublisher(), ids["alice"], ids["oper"])
	}
	// One per acted-as call: each blob's stream, the publish, the activation.
	if got, want := actAsTotal(t, reg, "ok")-okBefore, float64(len(paths)+2); got != want {
		t.Errorf("act_as_total{ok} rose by %v, want %v", got, want)
	}

	// The Ceiling on the same wiring.
	deniedBefore := actAsTotal(t, reg, "denied")
	_, err = client("gmaster", ids["oper"]).ActivateVersion(ctx, connect.NewRequest(&adminv1.ActivateVersionRequest{PackId: CorePack, Version: 1}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a Game Master as the Operator: %v, want PERMISSION_DENIED", err)
	}
	if got := actAsTotal(t, reg, "denied") - deniedBefore; got != 1 {
		t.Errorf("act_as_total{denied} rose by %v, want 1", got)
	}

	// Read back from the broker, polled per live-assertions.md: the publish
	// and activation name both identities, and the Ceiling's refusal its
	// detail.
	audit := b.open(auditTopic, 0)
	target := ManifestKey("town", version)
	eventually.Observed(t, 30*time.Second, "the acted-as publish and activation, and the Ceiling's refusal, on the audit topic", func() (bool, string) {
		seen := map[string]bool{}
		err := audit.Replay(ctx, func(r recordlog.Record) error {
			var rec auditv1.AuditRecord
			if err := proto.Unmarshal(r.Value, &rec); err != nil {
				return err
			}
			both := rec.GetActorAccountId() == ids["oper"] && rec.GetActingAsAccountId() == ids["alice"]
			switch {
			case rec.GetAction() == auth.ActionPublish && rec.GetTarget() == target && rec.GetOutcome() == auth.AuditOK && both:
				seen["publish"] = true
			case rec.GetAction() == auth.ActionActivate && rec.GetTarget() == target && rec.GetOutcome() == auth.AuditOK && both:
				seen["activate"] = true
			case rec.GetAction() == auth.ActionActAs && rec.GetOutcome() == auth.AuditDenied && rec.GetActorAccountId() == ids["gmaster"] &&
				rec.GetTarget() == ids["oper"] && rec.GetDetail() == "target holds operator; actor does not":
				seen["ceiling"] = true
			}
			return nil
		})
		if err != nil {
			return false, err.Error()
		}
		return seen["publish"] && seen["activate"] && seen["ceiling"], fmt.Sprint(seen)
	})
}

// withHeaders sets fixed headers on every request, streams included.
type withHeaders struct {
	h    http.Header
	next http.RoundTripper
}

func (w withHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range w.h {
		r.Header[k] = v
	}
	return w.next.RoundTrip(r)
}

// actAsTotal reads andara_admin_act_as_total{outcome} from reg.
func actAsTotal(t *testing.T, reg *prometheus.Registry, outcome string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "andara_admin_act_as_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" && l.GetValue() == outcome {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	t.Fatalf("no andara_admin_act_as_total{outcome=%q}", outcome)
	return 0
}
