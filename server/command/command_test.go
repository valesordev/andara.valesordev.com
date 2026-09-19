// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package command_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	auditv1 "github.com/valesordev/andara/gen/go/andara/audit/v1"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/command"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/sim"
)

// fakeLog is the Producer a harness stands in with: a slice per Partition.
// Nothing reaches it that parse or authorize rejected, which is what the
// pre-log ACs assert.
type fakeLog struct {
	records map[int32][]*logv1.LoggedCommand
	calls   int
}

func newFakeLog() *fakeLog { return &fakeLog{records: map[int32][]*logv1.LoggedCommand{}} }

func (f *fakeLog) Produce(_ context.Context, cmd *logv1.LoggedCommand) (command.Accepted, error) {
	f.calls++
	p := sim.PartitionFor(sim.ZoneID(cmd.GetZoneId()))
	f.records[p] = append(f.records[p], cmd)
	return command.Accepted{Partition: p, Offset: int64(len(f.records[p]) - 1)}, nil
}

func (f *fakeLog) total() int {
	n := 0
	for _, r := range f.records {
		n += len(r)
	}
	return n
}

type fixture struct {
	p        *command.Pipeline
	log      *fakeLog
	audit    *recordlog.Memory
	reg      *prometheus.Registry
	spans    *tracetest.InMemoryExporter
	logs     *bytes.Buffer
	bindings map[string]command.Binding
}

func newFixture(t *testing.T, roles auth.VerbRoles) *fixture {
	t.Helper()
	f := &fixture{log: newFakeLog(), audit: recordlog.NewMemory(), reg: prometheus.NewRegistry(), spans: tracetest.NewInMemoryExporter(), logs: &bytes.Buffer{}}
	f.bindings = map[string]command.Binding{"s-alice": {Actor: "alice", Zone: "town"}}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(f.spans))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	table := command.Builtin()
	if roles != nil {
		verbs := table.Verbs()
		for i := range verbs {
			if r, ok := roles[verbs[i].Name]; ok {
				verbs[i].Role = r
			}
		}
		var err error
		if table, err = command.NewVerbTable(verbs); err != nil {
			t.Fatal(err)
		}
	}
	names := make([]string, 0)
	for _, v := range table.Verbs() {
		names = append(names, v.Name)
	}
	f.p = &command.Pipeline{
		Table:      table,
		Authorizer: &auth.Authorizer{Table: table.Roles(), Audit: auth.NewAuditor(f.audit, nil, nil, nil)},
		Bindings:   command.BinderFunc(func(_ context.Context, id string) (command.Binding, error) { return f.binding(id) }),
		Log:        f.log,
		Metrics:    command.NewMetrics(f.reg, names),
		Tracer:     tp.Tracer("test"),
		Logger:     slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	return f
}

var player = auth.Principal{AccountID: "acct-alice", Roles: []auth.Role{auth.RolePlayer}}

func (f *fixture) binding(id string) (command.Binding, error) {
	b, ok := f.bindings[id]
	if !ok {
		return command.Binding{}, command.ErrNoBinding
	}
	return b, nil
}

func (f *fixture) submit(t *testing.T, session, raw string) (command.Accepted, error) {
	t.Helper()
	return f.p.Submit(context.Background(), command.Intent{SessionID: session, Raw: raw, ClientRef: "ref"}, player)
}

func (f *fixture) rejected(stage command.Stage, code string) float64 {
	return testutil.ToFloat64(f.p.Metrics.Rejected.WithLabelValues(string(stage), code, map[bool]string{true: "true", false: "false"}[stage.PreLog()]))
}

// --- parse ---------------------------------------------------------------

// AC-5: `n`, `north`, `move north`, `mov n`, `go N` all resolve to the same
// Command.
func TestParse_Abbreviation(t *testing.T) {
	table := command.Builtin()
	want, verb, err := command.Parse(command.Intent{Raw: "move north"}, table, 0)
	if err != nil || verb != "move" {
		t.Fatalf("move north: %v %s", err, verb)
	}
	for _, raw := range []string{"n", "north", "mov n", "go N", "MOVE NORTH", "  north  ", "north please"} {
		got, _, err := command.Parse(command.Intent{Raw: raw}, table, 0)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if !proto.Equal(got, want) {
			t.Errorf("%q = %v, want %v", raw, got, want)
		}
	}
	for raw, dir := range map[string]string{"ne": "northeast", "sw": "southwest", "u": "up", "d": "down", "in": "in", "out": "out", "move se": "southeast", "go w": "west"} {
		got, _, err := command.Parse(command.Intent{Raw: raw}, table, 0)
		if err != nil || got.GetMove().GetDirection() != dir {
			t.Errorf("%q = %v (%v), want direction %s", raw, got, err, dir)
		}
	}
	for _, raw := range []string{"l", "lo", "loo", "look"} {
		if got, verb, err := command.Parse(command.Intent{Raw: raw}, table, 0); err != nil || verb != "look" || got.GetLook() == nil {
			t.Fatalf("%s = %v %s %v", raw, got, verb, err)
		}
	}
	if got, _, _ := command.Parse(command.Intent{Raw: "look around"}, table, 0); got.GetLook() == nil {
		t.Fatal("tokens past the last argument are ignored")
	}
}

// A prefix two verbs claim resolves to neither, and says which.
func TestParse_AmbiguousPrefix(t *testing.T) {
	_, _, err := command.Parse(command.Intent{Raw: "no"}, command.Builtin(), 0)
	if !errors.Is(err, command.ErrUnknownVerb) {
		t.Fatalf("err = %v", err)
	}
	e, _ := command.AsError(err)
	if e.Detail != `"no" could be north or northeast or northwest` {
		t.Fatalf("detail = %q", e.Detail)
	}
	if _, verb, err := command.Parse(command.Intent{Raw: "northe"}, command.Builtin(), 0); err != nil || verb != "northeast" {
		t.Fatalf("northe: %s %v", verb, err)
	}
}

// AC-4: an unknown verb is rejected at parse.
func TestParse_UnknownVerb(t *testing.T) {
	for _, raw := range []string{"frobnicate", "", "   ", "arrive", "xyzzy north"} {
		_, _, err := command.Parse(command.Intent{Raw: raw}, command.Builtin(), 0)
		if !errors.Is(err, command.ErrUnknownVerb) {
			t.Errorf("%q: err = %v", raw, err)
		}
		if e, _ := command.AsError(err); e.Stage != command.StageParse || !e.Stage.PreLog() {
			t.Errorf("%q: stage = %v", raw, e)
		}
	}
}

// AC-6: a missing argument names the argument.
func TestParse_MissingArgument(t *testing.T) {
	_, _, err := command.Parse(command.Intent{Raw: "move"}, command.Builtin(), 0)
	if !errors.Is(err, command.ErrMissingArgument) {
		t.Fatalf("err = %v", err)
	}
	e, _ := command.AsError(err)
	if e.Arg != "direction" || !strings.Contains(e.Detail, "direction") {
		t.Fatalf("%+v", e)
	}
}

// An argument outside its kind is rejected at parse, so the log never
// carries `move frobnicate`.
func TestParse_InvalidArgument(t *testing.T) {
	_, _, err := command.Parse(command.Intent{Raw: "move frobnicate"}, command.Builtin(), 0)
	if !errors.Is(err, command.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
	e, _ := command.AsError(err)
	if e.Arg != "direction" || !strings.Contains(e.Detail, "north, northeast") {
		t.Fatalf("%+v", e)
	}
}

// AC-12: an oversized Intent is rejected on its length, before anything
// proportional to it is allocated.
func TestParse_IntentTooLarge(t *testing.T) {
	huge := command.Intent{Raw: strings.Repeat("look ", 64*1024/5)}
	_, _, err := command.Parse(huge, command.Builtin(), 0)
	if !errors.Is(err, command.ErrIntentTooLarge) {
		t.Fatalf("err = %v", err)
	}
	table := command.Builtin()
	allocs := testing.AllocsPerRun(100, func() { _, _, _ = command.Parse(huge, table, 0) })
	if allocs > 4 {
		t.Fatalf("rejecting a 64 KiB intent allocated %v times; the size check must come before tokenizing", allocs)
	}
	// Exactly at the limit is fine; one over is not.
	if _, _, err := command.Parse(command.Intent{Raw: strings.Repeat("l", 4096)}, table, 4096); errors.Is(err, command.ErrIntentTooLarge) {
		t.Fatal("at the limit rejected")
	}
	if _, _, err := command.Parse(command.Intent{Raw: strings.Repeat("l", 4097)}, table, 4096); !errors.Is(err, command.ErrIntentTooLarge) {
		t.Fatal("over the limit accepted")
	}
}

// A rejection quotes the player's token cut and escaped, so a rejection
// is never a vector for what was typed.
func TestParse_QuotesSafely(t *testing.T) {
	raw := "\x1b[31mred\n" + strings.Repeat("x", 100)
	_, _, err := command.Parse(command.Intent{Raw: raw}, command.Builtin(), 0)
	e, _ := command.AsError(err)
	if strings.ContainsAny(e.Detail, "\x1b\n") || len(e.Detail) > 80 {
		t.Fatalf("detail = %q", e.Detail)
	}
}

// Parse fills only the arm and the correlation: zone and actor are the
// binding's, after authorize.
func TestParse_FillsOnlyTheArm(t *testing.T) {
	got, _, err := command.Parse(command.Intent{SessionID: "s", Raw: "look", ClientRef: "c"}, command.Builtin(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.GetZoneId() != "" || got.GetActorId() != "" || got.GetSessionId() != "s" || got.GetClientRef() != "c" || got.GetAcceptedAtUnixNano() != 0 {
		t.Fatalf("%v", got)
	}
}

// --- verb table ----------------------------------------------------------

func TestVerbTable_Validation(t *testing.T) {
	cases := map[string][]command.Verb{
		"arrive is not bindable": {{Name: "arrive", Kind: sim.KindArrive}},
		"duplicate name":         {{Name: "look", Kind: sim.KindLook}, {Name: "look", Kind: sim.KindLook}},
		"alias taken":            {{Name: "look", Kind: sim.KindLook, Aliases: []string{"l"}}, {Name: "leave", Kind: sim.KindLook, Aliases: []string{"l"}}},
		"alias is a name":        {{Name: "look", Kind: sim.KindLook}, {Name: "peek", Kind: sim.KindLook, Aliases: []string{"look"}}},
		"unknown role":           {{Name: "look", Kind: sim.KindLook, Role: "wizard"}},
		"move needs direction":   {{Name: "move", Kind: sim.KindMove}},
		"bad bind":               {{Name: "north", Kind: sim.KindMove, Bind: map[string]string{"direction": "sideways"}}},
		"unknown bind":           {{Name: "north", Kind: sim.KindMove, Bind: map[string]string{"speed": "fast"}}},
		"look takes no args":     {{Name: "look", Kind: sim.KindLook, Args: []command.ArgSpec{{Name: "direction", Kind: command.ArgDirection}}}},
		"uppercase":              {{Name: "Look", Kind: sim.KindLook}},
		"empty":                  {},
	}
	for name, verbs := range cases {
		if _, err := command.NewVerbTable(verbs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(command.Builtin().Verbs()) != 14 {
		t.Fatalf("built-in table has %d verbs, want look, move, and twelve directions", len(command.Builtin().Verbs()))
	}
}

func TestVerbTable_LoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verbs.json")
	if err := os.WriteFile(path, []byte(`{"verbs": [
		{"name": "look", "kind": "look", "abbrev": true, "aliases": ["l", "examine"]},
		{"name": "walk", "kind": "move", "role": "builder", "args": [{"name": "direction", "kind": "direction"}]},
		{"name": "north", "kind": "move", "aliases": ["n"], "bind": {"direction": "north"}}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	table, err := command.LoadVerbTable(path)
	if err != nil {
		t.Fatal(err)
	}
	if roles := table.Roles(); len(roles) != 1 || roles["walk"] != auth.RoleBuilder {
		t.Fatalf("roles = %v", roles)
	}
	if got, verb, err := command.Parse(command.Intent{Raw: "examine"}, table, 0); err != nil || verb != "look" || got.GetLook() == nil {
		t.Fatalf("examine: %v %s %v", got, verb, err)
	}
	if _, _, err := command.Parse(command.Intent{Raw: "move north"}, table, 0); !errors.Is(err, command.ErrUnknownVerb) {
		t.Fatal("the file replaces the built-in table; move should be gone")
	}
	if _, _, err := command.Parse(command.Intent{Raw: "nor"}, table, 0); !errors.Is(err, command.ErrUnknownVerb) {
		t.Fatal("north is not abbreviable in this table")
	}
	if got, _, err := command.Parse(command.Intent{Raw: "walk s"}, table, 0); err != nil || got.GetMove().GetDirection() != "south" {
		t.Fatalf("walk s: %v %v", got, err)
	}

	for name, body := range map[string]string{
		"unknown field": `{"verbs": [{"name": "look", "kind": "look", "weight": "heavy"}]}`,
		"bad kind":      `{"verbs": [{"name": "arrive", "kind": "arrive"}]}`,
		"not json":      `verbs: []`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := command.LoadVerbTable(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := command.LoadVerbTable(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
}

// --- pipeline ------------------------------------------------------------

// The happy path: parse, authorize, produce; the record carries the
// binding, the correlation, and the trace parent.
func TestSubmit_Produces(t *testing.T) {
	f := newFixture(t, nil)
	acc, err := f.submit(t, "s-alice", "north")
	if err != nil {
		t.Fatal(err)
	}
	if acc.Verb != "north" || acc.Partition != sim.PartitionFor("town") || acc.Offset != 0 {
		t.Fatalf("accepted = %+v", acc)
	}
	rec := f.log.records[acc.Partition][0]
	if rec.GetZoneId() != "town" || rec.GetActorId() != "alice" || rec.GetSessionId() != "s-alice" || rec.GetClientRef() != "ref" || rec.GetMove().GetDirection() != "north" {
		t.Fatalf("record = %v", rec)
	}
	if rec.GetAcceptedAtUnixNano() == 0 || !strings.HasPrefix(rec.GetTraceId(), "00-") {
		t.Fatalf("record diagnostics = %v", rec)
	}
	if acc2, _ := f.submit(t, "s-alice", "look"); acc2.Offset != 1 {
		t.Fatalf("second offset = %d", acc2.Offset)
	}
	if got := testutil.ToFloat64(f.p.Metrics.Commands.WithLabelValues("north")); got != 1 {
		t.Fatalf("andara_commands_total{verb=north} = %v", got)
	}
	if n := testutil.CollectAndCount(f.p.Metrics.Duration, "andara_command_duration_seconds"); n == 0 {
		t.Fatal("no duration observed")
	}

	// Spans: command.execute with parse and authorize beneath it, and the
	// record's trace_id names the execute span's trace.
	names := map[string]trace.SpanID{}
	var executeTrace trace.TraceID
	for _, s := range f.spans.GetSpans() {
		names[s.Name] = s.SpanContext.SpanID()
		if s.Name == "command.execute" && executeTrace == (trace.TraceID{}) {
			executeTrace = s.SpanContext.TraceID() // the first Submit's
		}
	}
	for _, want := range []string{"command.execute", "command.parse", "command.authorize"} {
		if _, ok := names[want]; !ok {
			t.Errorf("no span %s in %v", want, names)
		}
	}
	if !strings.Contains(rec.GetTraceId(), executeTrace.String()) {
		t.Fatalf("trace_id %q does not name trace %s", rec.GetTraceId(), executeTrace)
	}
	if sc := trace.SpanContextFromContext(command.ParentFrom(context.Background(), rec.GetTraceId())); sc.TraceID() != executeTrace {
		t.Fatalf("ParentFrom = %v", sc)
	}
	if ctx := command.ParentFrom(context.Background(), "garbage"); trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("garbage trace_id yielded a span context")
	}
	if !strings.Contains(f.logs.String(), `"raw":"\"north\""`) {
		t.Fatalf("raw intent not logged at debug, escaped: %s", f.logs.String())
	}
}

// AC-4, AC-6, AC-12: parse failures produce nothing and stop the pipeline
// before authorize.
func TestSubmit_ParseFailureProducesNothing(t *testing.T) {
	f := newFixture(t, nil)
	authorized := 0
	f.p.Bindings = command.BinderFunc(func(_ context.Context, id string) (command.Binding, error) { authorized++; return f.binding(id) })
	for raw, code := range map[string]string{
		"frobnicate": command.CodeUnknownVerb, "move": command.CodeMissingArgument,
		"move frobnicate": command.CodeInvalidArgument, strings.Repeat("l", 5000): command.CodeIntentTooLarge,
	} {
		_, err := f.submit(t, "s-alice", raw)
		e, ok := command.AsError(err)
		if !ok || e.Code != code || e.Stage != command.StageParse || !command.IsPreLog(err) {
			t.Errorf("%.20q: err = %v, want %s at parse", raw, err, code)
		}
		if got := f.rejected(command.StageParse, code); got != 1 {
			t.Errorf("%.20q: rejected{parse,%s,pre_log=true} = %v", raw, code, got)
		}
	}
	if f.log.calls != 0 || f.log.total() != 0 {
		t.Fatalf("a parse failure reached the log: %d calls", f.log.calls)
	}
	if authorized != 0 {
		t.Fatalf("authorize ran %d times after parse failed", authorized)
	}
	if f.audit.Len() != 0 {
		t.Fatal("a parse failure is not an audit event")
	}
}

// AC-7: a Session bound to no Character is not_authorized, produces
// nothing, consumes no offset, and is audited.
func TestSubmit_UnboundSession(t *testing.T) {
	f := newFixture(t, nil)
	_, err := f.submit(t, "s-nobody", "look")
	if !errors.Is(err, command.ErrNotAuthorized) || !errors.Is(err, auth.ErrNotAuthorized) {
		t.Fatalf("err = %v", err)
	}
	e, _ := command.AsError(err)
	if e.Stage != command.StageAuthorize || !command.IsPreLog(err) {
		t.Fatalf("%+v", e)
	}
	if f.log.calls != 0 {
		t.Fatal("an unauthorized Command consumed a log offset")
	}
	if got := f.rejected(command.StageAuthorize, command.CodeNotAuthorized); got != 1 {
		t.Fatalf("rejected{authorize} = %v", got)
	}
	recs := f.audit.Records()
	if len(recs) != 1 {
		t.Fatalf("audit records = %d", len(recs))
	}
	var rec auditv1.AuditRecord
	if err := proto.Unmarshal(recs[0].Value, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.GetAction() != auth.ActionAuthorize || rec.GetTarget() != "look" || rec.GetOutcome() != auth.AuditDenied || rec.GetSessionId() != "s-nobody" || rec.GetActorAccountId() != "acct-alice" {
		t.Fatalf("audit = %v", &rec)
	}
	if !strings.Contains(f.logs.String(), `"level":"INFO"`) || !strings.Contains(f.logs.String(), `"code":"not_authorized"`) {
		t.Fatalf("authorize rejection not logged at info: %s", f.logs.String())
	}
	// A bound Session with the same Intent goes through: the binding, not
	// the verb, was the problem.
	if _, err := f.submit(t, "s-alice", "look"); err != nil {
		t.Fatal(err)
	}
}

// A verb gated on a role the Principal lacks is not_authorized via
// auth.Authorizer, audited by it, and produces nothing.
func TestSubmit_RoleGate(t *testing.T) {
	f := newFixture(t, auth.VerbRoles{"move": auth.RoleBuilder})
	_, err := f.submit(t, "s-alice", "move north")
	if !errors.Is(err, command.ErrNotAuthorized) || !errors.Is(err, auth.ErrNotAuthorized) {
		t.Fatalf("err = %v", err)
	}
	if f.log.calls != 0 || f.audit.Len() != 1 {
		t.Fatalf("log calls = %d, audit = %d", f.log.calls, f.audit.Len())
	}
	// `north` is a different verb with no role: the table's role column is
	// per verb, and a shorthand is its own row.
	if _, err := f.submit(t, "s-alice", "north"); err != nil {
		t.Fatal(err)
	}
	builder := auth.Principal{AccountID: "b", Roles: []auth.Role{auth.RolePlayer, auth.RoleBuilder}}
	if _, err := f.p.Submit(context.Background(), command.Intent{SessionID: "s-alice", Raw: "move north"}, builder); err != nil {
		t.Fatal(err)
	}
}

// The produce itself failing is not a rejection: it is returned as is,
// counted under no stage, and the trace carries the error.
func TestSubmit_ProduceError(t *testing.T) {
	f := newFixture(t, nil)
	boom := errors.New("broker unreachable")
	f.p.Log = command.ProducerFunc(func(context.Context, *logv1.LoggedCommand) (command.Accepted, error) { return command.Accepted{}, boom })
	_, err := f.submit(t, "s-alice", "look")
	if !errors.Is(err, boom) || command.IsPreLog(err) {
		t.Fatalf("err = %v", err)
	}
	if n := testutil.CollectAndCount(f.p.Metrics.Rejected, "andara_command_rejected_total"); n == 0 {
		t.Fatal("pre-seeded series missing")
	}
	for stage, codes := range command.PreLogCodes {
		for _, c := range codes {
			if f.rejected(stage, c) != 0 {
				t.Fatalf("a produce error was counted as %s/%s", stage, c)
			}
		}
	}
}

// AC-10: every label combination the metric can carry is pre-seeded, and
// stage distinguishes pre-log from post-log.
func TestMetrics_PreSeeded(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := command.NewMetrics(reg, []string{"look"})
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, fam := range families {
		seen[fam.GetName()] = len(fam.GetMetric())
	}
	if seen["andara_command_rejected_total"] < 5+2*len(sim.RejectCodes()) {
		t.Fatalf("rejected series = %d", seen["andara_command_rejected_total"])
	}
	if seen["andara_commands_total"] != 1 || seen["andara_command_duration_seconds"] != 2 {
		t.Fatalf("series = %v", seen)
	}
	m.Reject(command.StageValidate, sim.CodeNoSuchExit)
	if got := testutil.ToFloat64(m.Rejected.WithLabelValues("validate", "no_such_exit", "false")); got != 1 {
		t.Fatalf("post-log rejection = %v", got)
	}
	var nilMetrics *command.Metrics
	nilMetrics.Reject(command.StageParse, command.CodeUnknownVerb) // must not panic
}

// The stepped clock feeds the pre-log histogram.
func TestSubmit_DurationUsesClock(t *testing.T) {
	f := newFixture(t, nil)
	now := time.Unix(1000, 0)
	f.p.Clock = clockFunc(func() time.Time { now = now.Add(2 * time.Millisecond); return now })
	if _, err := f.submit(t, "s-alice", "look"); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(f.p.Metrics.Duration); n == 0 {
		t.Fatal("no observation")
	}
}

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }
