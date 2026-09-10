package telemetry

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/config"
)

// Traces must carry the same identity the log lines carry. Tempo recorded these
// spans as `unknown_service:andara-server` — the Go SDK's fallback when no
// resource is set — so they were present, correct, and unfindable by the service
// name every dashboard and TraceQL query uses.
//
// Nothing in the boot tests could catch it: a SpanRecorder observes spans before
// export, and resource attributes are attached on export. This test is the cheap
// guard that keeps the expensive verification from having to be repeated.
func TestBootResource_CarriesServiceIdentity(t *testing.T) {
	cfg := config.Config{ServiceName: "andara-server", Environment: "local"}
	got := map[string]string{}
	for _, a := range bootResource(cfg).Attributes() {
		got[string(a.Key)] = a.Value.AsString()
	}

	for key, want := range map[string]string{
		"service.name":                "andara-server",
		"deployment.environment.name": "local",
	} {
		if got[key] != want {
			t.Errorf("resource %s = %q, want %q (have %v)", key, got[key], want, got)
		}
	}
	if bootResource(cfg).SchemaURL() == "" {
		t.Error("resource has no schema URL; a backend cannot interpret the attribute names")
	}
}

// The identity follows configuration rather than being hardcoded, so a second
// service or a non-local environment is distinguishable in the same backend.
func TestBootResource_FollowsConfig(t *testing.T) {
	r := bootResource(config.Config{ServiceName: "andara-gateway", Environment: "staging"})
	got := map[string]string{}
	for _, a := range r.Attributes() {
		got[string(a.Key)] = a.Value.AsString()
	}
	if got["service.name"] != "andara-gateway" {
		t.Errorf("service.name = %q, want andara-gateway", got["service.name"])
	}
	if got["deployment.environment.name"] != "staging" {
		t.Errorf("deployment.environment.name = %q, want staging", got["deployment.environment.name"])
	}
}

// The resource must reach what an exporter sees, down both code paths: with no
// OTLP endpoint (the path tests and --validate-only take, and the one the gap
// survived on) and with one. The SDK does not expose a provider's resource, so
// this reads it off an exported span, which is exactly where it failed.
func TestNewTracerProvider_SpansCarryTheResource(t *testing.T) {
	for _, endpoint := range []string{"", "localhost:4317"} {
		rec := tracetest.NewSpanRecorder()
		tp := newTracerProvider(config.Config{
			ServiceName:  "andara-server",
			Environment:  "test",
			OTLPEndpoint: endpoint,
		}, sdktrace.WithSpanProcessor(rec))
		_, span := tp.Tracer("test").Start(context.Background(), "content.load")
		span.End()
		// The recorder is its own span processor and captures on End, so there is
		// nothing to flush. Shutting down with an unbounded context would instead
		// make this test wait out the OTLP exporter's retries against an endpoint
		// that is not listening, and `make check` has to stay fast enough that
		// nobody is tempted to skip it.
		shutCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = tp.Shutdown(shutCtx)
		cancel()

		ended := rec.Ended()
		if len(ended) != 1 {
			t.Fatalf("OTLPEndpoint=%q: recorded %d spans, want 1", endpoint, len(ended))
		}
		got := map[string]string{}
		for _, a := range ended[0].Resource().Attributes() {
			got[string(a.Key)] = a.Value.AsString()
		}
		if got["service.name"] != "andara-server" {
			t.Errorf("OTLPEndpoint=%q: exported span resource service.name = %q, want andara-server (have %v)",
				endpoint, got["service.name"], got)
		}
		if got["deployment.environment.name"] != "test" {
			t.Errorf("OTLPEndpoint=%q: deployment.environment.name = %q, want test",
				endpoint, got["deployment.environment.name"])
		}
	}
}
