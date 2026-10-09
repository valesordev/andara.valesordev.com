// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/valesordev/andara/server/auth"
)

// AW-SRV-039: `andara-act-as` on an Admin call.

// actAsVerifier authenticates every token as acct-op and resolves acting-as
// the way auth.Store does: err refuses it, otherwise the result carries the
// actor's identity and the target's roles.
type actAsVerifier struct {
	roles []auth.Role
	err   error
	calls []string
	// methods is the Admin method each ActAs call was made for.
	methods []string
}

func (v *actAsVerifier) Verify(context.Context, string) (Principal, error) {
	return Principal{AccountID: "acct-op", Roles: v.roles}, nil
}

func (v *actAsVerifier) ActAs(ctx context.Context, p Principal, target string) (Principal, error) {
	v.calls = append(v.calls, p.AccountID+"->"+target)
	v.methods = append(v.methods, auth.ActAsMethodFrom(ctx))
	if v.err != nil {
		return Principal{}, v.err
	}
	return Principal{AccountID: p.AccountID, ActingAs: target, Roles: []auth.Role{auth.RoleBuilder}}, nil
}

type actAsRun struct {
	err     error
	reached bool
	got     Principal
	span    sdktrace.ReadOnlySpan
}

// callAdmin runs an Admin or Game call through the auth interceptor with the
// given extra headers, under a span as the trace interceptor would start.
func callAdmin(t *testing.T, ic *authInterceptor, procedure string, header http.Header) actAsRun {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("test").Start(context.Background(), procedure)

	var run actAsRun
	next := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		run.reached = true
		run.got, _ = PrincipalFrom(ctx)
		return nil, nil
	}
	if header.Get("Authorization") == "" {
		header.Set("Authorization", "Bearer tok")
	}
	_, run.err = ic.WrapUnary(next)(ctx, fakeRequest{spec: connect.Spec{Procedure: procedure}, header: header})
	span.End()
	if ended := rec.Ended(); len(ended) == 1 {
		run.span = ended[0]
	}
	return run
}

func attr(span sdktrace.ReadOnlySpan, key string) (string, bool) {
	if span == nil {
		return "", false
	}
	i := slices.IndexFunc(span.Attributes(), func(kv attribute.KeyValue) bool { return string(kv.Key) == key })
	if i < 0 {
		return "", false
	}
	return span.Attributes()[i].Value.AsString(), true
}

const publish = "/andara.admin.v1.Admin/PublishVersion"

func actAsCount(m *Metrics, outcome string) float64 {
	return testutil.ToFloat64(m.ActAsTotal.WithLabelValues(outcome))
}

// AC-1 (gateway half): an Operator's `andara-act-as` becomes the Principal
// the handler reads, the real actor first. The outcome is counted and put on
// the RPC span, and the verifier is told which method it is acting for.
func TestAuthInterceptor_ActAsResolvesIntoThePrincipal(t *testing.T) {
	v := &actAsVerifier{roles: []auth.Role{auth.RoleOperator}}
	m := NewMetrics(nil)
	run := callAdmin(t, &authInterceptor{verifier: v, metrics: m}, publish, http.Header{"Andara-Act-As": {"acct-builder"}})

	if run.err != nil || !run.reached {
		t.Fatalf("err=%v reached=%v", run.err, run.reached)
	}
	if run.got.AccountID != "acct-op" || run.got.ActingAs != "acct-builder" || run.got.EffectiveAccountID() != "acct-builder" {
		t.Errorf("principal = %+v", run.got)
	}
	if want := []string{"acct-op->acct-builder"}; !slices.Equal(v.calls, want) {
		t.Errorf("ActAs calls = %v, want %v", v.calls, want)
	}
	if want := []string{"andara.admin.v1.Admin/PublishVersion"}; !slices.Equal(v.methods, want) {
		t.Errorf("ActAs methods = %v, want %v", v.methods, want)
	}
	if got := actAsCount(m, "ok"); got != 1 {
		t.Errorf("act_as_total{ok} = %v, want 1", got)
	}
	if got, ok := attr(run.span, "auth.acting_as"); !ok || got != "acct-builder" {
		t.Errorf("span auth.acting_as = %q, %v", got, ok)
	}
	if got, ok := attr(run.span, "auth.act_as_outcome"); !ok || got != "ok" {
		t.Errorf("span auth.act_as_outcome = %q, %v", got, ok)
	}
}

// AC-3, AC-4: a refusal fails PERMISSION_DENIED before the handler, and the
// outcome tells a role refusal from an unknown or inactive Account.
func TestAuthInterceptor_ActAsRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		outcome string
	}{
		{"not an operator or game master", auth.ErrPermissionDenied, "denied"},
		{"no such active account", auth.ErrActAsNoAccount, "unknown_account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &actAsVerifier{roles: []auth.Role{auth.RolePlayer}, err: tc.err}
			m := NewMetrics(nil)
			run := callAdmin(t, &authInterceptor{verifier: v, metrics: m}, publish, http.Header{"Andara-Act-As": {"acct-builder"}})

			if connect.CodeOf(run.err) != connect.CodePermissionDenied {
				t.Fatalf("err = %v, want PERMISSION_DENIED", run.err)
			}
			if run.reached {
				t.Fatal("the handler was reached after a refused act-as")
			}
			for _, o := range []string{"ok", "denied", "unknown_account"} {
				want := 0.0
				if o == tc.outcome {
					want = 1
				}
				if got := actAsCount(m, o); got != want {
					t.Errorf("act_as_total{%s} = %v, want %v", o, got, want)
				}
			}
			if got, _ := attr(run.span, "auth.act_as_outcome"); got != tc.outcome {
				t.Errorf("span auth.act_as_outcome = %q, want %q", got, tc.outcome)
			}
		})
	}
}

// AC-5: on a Game RPC the metadata is ignored. Acting-as there is
// OpenSession's field only, and a call that ignores it isn't counted.
func TestAuthInterceptor_ActAsIsIgnoredOnGame(t *testing.T) {
	v := &actAsVerifier{roles: []auth.Role{auth.RoleOperator}}
	m := NewMetrics(nil)
	run := callAdmin(t, &authInterceptor{verifier: v, metrics: m}, "/andara.game.v1.Game/Submit", http.Header{"Andara-Act-As": {"acct-builder"}})

	if run.err != nil || !run.reached {
		t.Fatalf("err=%v reached=%v", run.err, run.reached)
	}
	if len(v.calls) != 0 {
		t.Errorf("ActAs consulted on a Game RPC: %v", v.calls)
	}
	for _, o := range []string{"ok", "denied", "unknown_account"} {
		if got := actAsCount(m, o); got != 0 {
			t.Errorf("act_as_total{%s} = %v on a Game RPC", o, got)
		}
	}
}

// Without the metadata nothing is asked of ActAs and nothing is counted.
func TestAuthInterceptor_NoActAsMetadataAsksNothing(t *testing.T) {
	v := &actAsVerifier{roles: []auth.Role{auth.RoleOperator}}
	m := NewMetrics(nil)
	for _, h := range []http.Header{{}, {"Andara-Act-As": {""}}, {"Andara-Act-As": {"  "}}} {
		run := callAdmin(t, &authInterceptor{verifier: v, metrics: m}, publish, h)
		if run.err != nil || !run.reached || run.got.ActingAs != "" {
			t.Fatalf("header %v: err=%v reached=%v principal=%+v", h, run.err, run.reached, run.got)
		}
		if _, ok := attr(run.span, "auth.acting_as"); ok {
			t.Errorf("header %v: auth.acting_as set on a call that didn't act as anyone", h)
		}
	}
	if len(v.calls) != 0 {
		t.Errorf("ActAs consulted: %v", v.calls)
	}
}

// Two values for one header are not a target: it fails closed rather than
// choosing one, since a proxy in front may have added the second.
func TestAuthInterceptor_ActAsWithTwoValuesIsRefused(t *testing.T) {
	v := &actAsVerifier{roles: []auth.Role{auth.RoleOperator}}
	m := NewMetrics(nil)
	run := callAdmin(t, &authInterceptor{verifier: v, metrics: m}, publish, http.Header{"Andara-Act-As": {"acct-a", "acct-b"}})
	if connect.CodeOf(run.err) != connect.CodePermissionDenied || run.reached {
		t.Fatalf("err=%v reached=%v", run.err, run.reached)
	}
	if len(v.calls) != 0 {
		t.Errorf("ActAs consulted with an ambiguous header: %v", v.calls)
	}
	if got := actAsCount(m, "denied"); got != 1 {
		t.Errorf("act_as_total{denied} = %v, want 1", got)
	}
}

// The counter is present at 0 from the first scrape.
func TestMetrics_ActAsTotalIsPreSeeded(t *testing.T) {
	m := NewMetrics(nil)
	if got := testutil.CollectAndCount(m.ActAsTotal); got != 3 {
		t.Fatalf("series = %d, want 3 (ok, denied, unknown_account)", got)
	}
}
