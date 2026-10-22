// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/content/core"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/kafkaclient"
)

// brokerRefusal is the error a write to a topic the broker refuses the principal
// comes back with.
func brokerRefusal(topic string) error {
	return &kafkaclient.AuthError{Reason: kafkaclient.ReasonTopicUnauthorized, ClientID: "andara-server-content",
		Principal: "andara-server-dev", Topic: topic, Err: errors.New("TOPIC_AUTHORIZATION_FAILED")}
}

// errorName is the error_name the span of that name ended with.
func errorName(rec *tracetest.SpanRecorder, span string) string {
	for _, sp := range rec.Ended() {
		if sp.Name() != span {
			continue
		}
		for _, a := range sp.Attributes() {
			if a.Key == "error_name" {
				return a.Value.AsString()
			}
		}
	}
	return ""
}

// AW-SRV-044's trace rule at the core boot: a blob write or a manifest write
// the broker refuses leaves its span failed with error_name =
// topic_unauthorized.
func TestBootCore_ARefusedWriteNamesTheReasonOnItsSpan(t *testing.T) {
	for name, c := range map[string]struct {
		refuse func(*coreStore)
		span   string
	}{
		"blob":     {func(s *coreStore) { s.blobs.Fail = brokerRefusal("dev.andara.content.blobs.v1") }, "content.write_blob"},
		"manifest": {func(s *coreStore) { s.versions.Fail = brokerRefusal("dev.andara.content.versions.v1") }, "content.write_manifest"},
	} {
		s := newCoreStore(t)
		c.refuse(s)
		rec := tracetest.NewSpanRecorder()
		log := slog.New(slog.DiscardHandler)
		_, err := BootCore(context.Background(), CoreBootOptions{
			Registry: s.reg, Auditor: auth.NewAuditor(s.audit, log, nil, nil), Metrics: NewPublishMetrics(nil), Log: log,
			Tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t"),
			Pack:   CorePack, Version: core.Version(), Blobs: core.Blobs(), Build: "v0.9.0",
		})
		if !kafkaclient.Refused(err) {
			t.Fatalf("%s: BootCore = %v, want the refusal", name, err)
		}
		if got := errorName(rec, c.span); got != kafkaclient.ReasonTopicUnauthorized {
			t.Errorf("%s: %s error_name = %q", name, c.span, got)
		}
	}
}

// The same rule on the Admin API: a PublishBlob whose write the broker
// refuses is UNAVAILABLE, and content.write_blob names the reason.
func TestPublishBlob_ARefusedWriteNamesTheReasonOnItsSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	h := newPubHarness(t, func(ao *AdminOptions, _ *LoaderOptions) {
		ao.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t")
	})
	h.blobs.Fail = brokerRefusal("dev.andara.content.blobs.v1")
	_, err := h.admin.PublishBlob(builder(alice), blobStream("town", "a.json", []byte(`{"rooms":[]}`), 1<<20).Receive)
	var ae *AdminError
	if !errors.As(err, &ae) || ae.Code != CodeUnavailable || !kafkaclient.Refused(err) {
		t.Fatalf("PublishBlob = %v, want UNAVAILABLE carrying the refusal", err)
	}
	if got := errorName(rec, "content.write_blob"); got != kafkaclient.ReasonTopicUnauthorized {
		t.Fatalf("content.write_blob error_name = %q", got)
	}
}
