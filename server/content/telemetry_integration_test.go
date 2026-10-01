// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
)

// TestPublishPath_TelemetryAgainstABroker is the backend assertion SRE's §8
// check of AW-SRV-013 asks for: the RPC path driven against the local
// Redpanda, through a publish the validator refuses, a valid publish, an
// unapproved activation refused, a Builder's approval and an Operator's
// self-approval, two activations and a rollback. It asserts the metric
// objects and, on a recording tracer, the span tree.
func TestPublishPath_TelemetryAgainstABroker(t *testing.T) {
	tp, audit, cl := publishTopics(t)
	rec := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	b := &brokerPublish{t: t, tp: tp, auditTop: audit, cl: cl,
		pm: NewPublishMetrics(prometheus.NewRegistry()), tracer: provider.Tracer("test")}
	b.start()

	// Every blob goes through PublishBlob, and the bytes the store took
	// after deduplication are summed from its answers.
	var accepted int64
	publish := func(ctx context.Context, files map[string][]byte) (*adminv1.PublishVersionResponse, error) {
		t.Helper()
		var paths []string
		for p := range files {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		req := &adminv1.PublishVersionRequest{PackId: "town", ParentVersion: b.reg.Newest("town")}
		for _, p := range paths {
			resp, err := b.admin.PublishBlob(ctx, blobStream("town", p, files[p], BlobChunkBytes).Receive)
			if err != nil {
				t.Fatal(err)
			}
			if !resp.GetDeduplicated() {
				accepted += int64(len(files[p]))
			}
			sum := sha256.Sum256(files[p])
			req.Blobs = append(req.Blobs, blobRef(p, sum[:], len(files[p])))
		}
		return b.admin.PublishVersion(ctx, req)
	}

	// 1. A publish the validator refuses: an Exit to a Room town lacks.
	bad := townFiles(t)
	bad["town.json"] = bytes.Replace(bad["town.json"], []byte(`"toRoom": "hall"`), []byte(`"toRoom": "nowhere"`), 1)
	if _, err := publish(builder(alice), bad); reasonOf(err) != ErrReasonValidation {
		t.Fatalf("the bad publish: %v", err)
	}
	// 2. A valid publish by alice.
	v1, err := publish(builder(alice), townFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	// 3. Activating it unapproved is refused.
	if _, err := b.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: v1.GetVersion()}); reasonOf(err) != ErrReasonUnapproved {
		t.Fatalf("the unapproved activation: %v", err)
	}
	// 4. bob approves; alice activates.
	if _, err := b.admin.ApproveVersion(builder(bob), &adminv1.ApproveVersionRequest{PackId: "town", Version: v1.GetVersion()}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.admin.ActivateVersion(builder(alice), &adminv1.ActivateVersionRequest{PackId: "town", Version: v1.GetVersion()}); err != nil {
		t.Fatal(err)
	}
	b.follow("town")
	// 5. The Operator publishes a version and approves it themselves, and
	// activates it; then rolls back to 1.
	files := townFiles(t)
	files["README"] = []byte("a second version\n")
	v2, err := publish(operator(), files)
	if err != nil {
		t.Fatal(err)
	}
	if ap, err := b.admin.ApproveVersion(operator(), &adminv1.ApproveVersionRequest{PackId: "town", Version: v2.GetVersion()}); err != nil || !ap.GetSelfApproval() {
		t.Fatalf("the self-approval: %v %v", ap, err)
	}
	if _, err := b.admin.ActivateVersion(operator(), &adminv1.ActivateVersionRequest{PackId: "town", Version: v2.GetVersion()}); err != nil {
		t.Fatal(err)
	}
	b.follow("town")
	if av, err := b.admin.ActivateVersion(operator(), &adminv1.ActivateVersionRequest{PackId: "town", Version: v1.GetVersion()}); err != nil || !av.GetRollback() {
		t.Fatalf("the rollback: %v %v", av, err)
	}

	// The metric objects.
	m := b.pm
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"publishes_total{ok}", testutil.ToFloat64(m.Publishes.WithLabelValues(PublishOK)), 2},
		{"publishes_total{rejected}", testutil.ToFloat64(m.Publishes.WithLabelValues(PublishRejected)), 1},
		{"approvals_total{ok}", testutil.ToFloat64(m.Approvals.WithLabelValues(ApprovalOK)), 1},
		{"approvals_total{self_operator}", testutil.ToFloat64(m.Approvals.WithLabelValues(ApprovalSelfOperator)), 1},
		{"pointer_moves_total{forward,false}", testutil.ToFloat64(m.PointerMoves.WithLabelValues(DirectionForward, "false")), 2},
		{"pointer_moves_total{rollback,false}", testutil.ToFloat64(m.PointerMoves.WithLabelValues(DirectionRollback, "false")), 1},
		{"activations_refused_total{unapproved}", testutil.ToFloat64(m.ActivationsRefused.WithLabelValues(RefusedUnapproved)), 1},
		{"validation_failures_total{unknown_room}", testutil.ToFloat64(m.ValidationFailures.WithLabelValues("unknown_room")), 1},
		{"blob_bytes_total", testutil.ToFloat64(m.BlobBytes), float64(accepted)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if accepted == 0 {
		t.Error("no bytes were accepted; the dedup sum is untested")
	}

	// The span tree.
	spans := rec.Ended()
	children := func(parent sdktrace.ReadOnlySpan) []string {
		var out []string
		for _, s := range spans {
			if s.Parent().SpanID() == parent.SpanContext().SpanID() {
				out = append(out, s.Name())
			}
		}
		return out
	}
	want := map[string][]string{
		"content.publish":      {"content.validate", "content.write_manifest", "audit.write"},
		"content.approve":      {"content.write_manifest", "audit.write"},
		"content.activate":     {"content.write_pointer", "audit.write"},
		"content.publish_blob": {"content.write_blob"},
	}
	seen := map[string]bool{}
	for _, s := range spans {
		need, ok := want[s.Name()]
		if !ok {
			continue
		}
		got := children(s)
		if s.Name() == "content.publish_blob" && !slices.Contains(got, "content.write_blob") {
			// A deduplicated blob is still written through the registry,
			// so every stream has its write.
			t.Errorf("content.publish_blob without content.write_blob: %v", got)
		}
		if s.Name() == "content.publish" && slices.Contains(got, "content.validate") && !slices.Contains(got, "content.write_manifest") {
			continue // the refused publish: validated, nothing written
		}
		if s.Name() == "content.activate" && !slices.Contains(got, "content.write_pointer") {
			continue // the refused activation
		}
		for _, n := range need {
			if !slices.Contains(got, n) {
				t.Errorf("%s lacks child %s: %v", s.Name(), n, got)
			}
		}
		seen[s.Name()] = true
		if s.Name() == "content.activate" {
			for _, c := range spans {
				if c.Name() == "content.write_pointer" && c.Parent().SpanID() == s.SpanContext().SpanID() {
					dir := ""
					for _, kv := range c.Attributes() {
						if kv.Key == "direction" {
							dir = kv.Value.AsString()
						}
					}
					if dir == "" {
						t.Error("content.write_pointer carries no direction")
					}
				}
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("no complete %s span", name)
		}
	}
}
