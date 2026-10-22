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

// AW-SRV-044's trace rule at the core boot: a blob write the broker refuses
// leaves content.write_blob failed with error_name = topic_unauthorized.
func TestBootCore_ARefusedBlobWriteNamesTheReasonOnItsSpan(t *testing.T) {
	s := newCoreStore(t)
	s.blobs.Fail = &kafkaclient.AuthError{Reason: kafkaclient.ReasonTopicUnauthorized, ClientID: "andara-server-content",
		Principal: "andara-server-dev", Topic: "dev.andara.content.blobs.v1", Err: errors.New("TOPIC_AUTHORIZATION_FAILED")}
	rec := tracetest.NewSpanRecorder()
	log := slog.New(slog.DiscardHandler)
	_, err := BootCore(context.Background(), CoreBootOptions{
		Registry: s.reg, Auditor: auth.NewAuditor(s.audit, log, nil, nil), Metrics: NewPublishMetrics(nil), Log: log,
		Tracer: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t"),
		Pack:   CorePack, Version: core.Version(), Blobs: core.Blobs(), Build: "v0.9.0",
	})
	if kafkaclient.ErrorName(err) != kafkaclient.ReasonTopicUnauthorized {
		t.Fatalf("BootCore = %v, want the refusal", err)
	}
	var name string
	for _, sp := range rec.Ended() {
		if sp.Name() == "content.write_blob" {
			for _, a := range sp.Attributes() {
				if a.Key == "error_name" {
					name = a.Value.AsString()
				}
			}
		}
	}
	if name != kafkaclient.ReasonTopicUnauthorized {
		t.Fatalf("content.write_blob error_name = %q", name)
	}
}
