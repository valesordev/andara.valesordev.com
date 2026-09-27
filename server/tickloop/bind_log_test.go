// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-014 Observability: a BindCharacter that applies logs at info with
// the account, the Character, the Session, the trace, and where the body
// is and what the bind did to it. One that is rejected logs no such line.
func TestLoop_LogsTheAppliedBind(t *testing.T) {
	var recs *spanRecords
	h := newHarness(t, func(o *Options) { recs = recordSpans(o) })
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "lane"))
	h.source.Push(simtest.Unbind("town", "ch-1"))
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	h.source.Push(simtest.Bind("town", "ch-2", "Brenna", "nowhere"))
	// No actor_id on the envelope: the sim applies it to the payload's
	// character_id, and the line names that (review of #98).
	noActor := simtest.Bind("town", "ch-3", "Corin", "plaza")
	noActor.ActorId = ""
	h.source.Push(noActor)
	if err := h.runFor(time.Second); err != nil {
		t.Fatal(err)
	}
	lines := findLogs(t, h.logs, "character bind applied")
	want := []struct{ room, body string }{{"lane", "spawned"}, {"lane", "woken"}, {"lane", "present"}}
	if len(lines) != len(want)+1 {
		t.Fatalf("got %d bind-applied lines, want %d (the rejected bind logs none):\n%s", len(lines), len(want)+1, h.logs.String())
	}
	if l := lines[len(want)]; l["character_id"] != "ch-3" || l["account_id"] != "acct-ch-3" || l["body"] != "spawned" {
		t.Errorf("the bind with no actor_id: %v, want character_id ch-3", l)
	}
	for i, w := range want {
		l := lines[i]
		if l["level"] != "INFO" || l["account_id"] != "acct-ch-1" || l["character_id"] != "ch-1" || l["session_id"] != "s-ch-1" ||
			l["zone"] != "town" || l["room"] != w.room || l["body"] != w.body {
			t.Errorf("line %d: %v, want room %s body %s", i, l, w.room, w.body)
		}
		if tid, _ := l["trace_id"].(string); len(tid) != 32 {
			t.Errorf("line %d: trace_id %q", i, l["trace_id"])
		}
		if _, ok := l["tick"]; !ok {
			t.Errorf("line %d: no tick", i)
		}
	}

	// The record's trace context, which the OTel log bridge exports and
	// Loki indexes, is the command.apply span's: the same trace the
	// attribute names, and one Tempo holds. The loop's own context would
	// name sim.tick, exported one tick in a hundred (§8 pass on #98, item 3).
	applies := map[trace.SpanID]string{}
	for _, sp := range h.spans.Ended() {
		if sp.Name() == "command.apply" {
			applies[sp.SpanContext().SpanID()] = sp.SpanContext().TraceID().String()
		}
	}
	for _, msg := range []string{"character bind applied", "command applied"} {
		got := recs.of(msg)
		if len(got) == 0 {
			t.Fatalf("no %q records", msg)
		}
		for i, sc := range got {
			tid, ok := applies[sc.SpanID()]
			if !ok {
				t.Errorf("%q record %d: span %s is not a command.apply span", msg, i, sc.SpanID())
				continue
			}
			if tid != sc.TraceID().String() {
				t.Errorf("%q record %d: trace %s, its span's is %s", msg, i, sc.TraceID(), tid)
			}
		}
	}
	for i, l := range findLogs(t, h.logs, "character bind applied") {
		if l["trace_id"] != recs.of("character bind applied")[i].TraceID().String() {
			t.Errorf("line %d: attribute trace_id %v disagrees with the record's", i, l["trace_id"])
		}
	}
}

// AW-SRV-015: a linkdead expiry has no Command behind it, so its despawn
// line names the sim.tick trace that applied it. That tick is kept, or the
// line would point at a trace exported one tick in a hundred.
func TestLoop_KeepsTheTickThatExpiresALinkdeadBody(t *testing.T) {
	var mu sync.Mutex
	var ended []sim.LinkdeadChange
	h := newHarness(t, func(o *Options) {
		o.OnTick = func(res sim.StepResult, _ time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			for _, c := range res.Linkdead {
				if c.Kind == sim.LinkdeadEnded {
					ended = append(ended, c)
				}
			}
		}
	})
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "lane"))
	h.source.Push(simtest.MarkLinkdead("town", "ch-1", 3, 3, 6))
	if err := h.runFor(2 * time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ended) != 1 {
		t.Fatalf("got %d expiries, want 1", len(ended))
	}
	sc := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(),
		propagation.MapCarrier{"traceparent": ended[0].TraceID}))
	if !sc.IsValid() {
		t.Fatalf("the expiry's trace %q is not a traceparent", ended[0].TraceID)
	}
	for _, sp := range h.spans.Ended() {
		if sp.Name() != "sim.tick" || sp.SpanContext().SpanID() != sc.SpanID() {
			continue
		}
		for _, a := range sp.Attributes() {
			if a.Key == attribute.Key("andara.keep") {
				if !a.Value.AsBool() {
					t.Fatal("the tick that applied the expiry is not kept: its despawn line names a trace nobody exports")
				}
				return
			}
		}
		t.Fatal("the expiry's sim.tick has no andara.keep")
	}
	t.Fatalf("no sim.tick span %s", sc.SpanID())
}

// Only an expiry keeps its tick. A combat extension has no trace either,
// but logs no line, and keeping every combat tick would undo the sampling;
// a despawn by a Command carries that Command's trace (review of #132).
func TestLoop_OnlyAnExpiryKeepsItsTick(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    sim.LinkdeadChange
		want bool
	}{
		{"expiry", sim.LinkdeadChange{Kind: sim.LinkdeadEnded}, true},
		{"combat extension", sim.LinkdeadChange{Kind: sim.LinkdeadExtended}, false},
		{"despawn by a Command", sim.LinkdeadChange{Kind: sim.LinkdeadEnded, TraceID: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}, false},
		{"entered", sim.LinkdeadChange{Kind: sim.LinkdeadEntered, TraceID: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}, false},
	} {
		if got := expired([]sim.LinkdeadChange{tc.c}); got != tc.want {
			t.Errorf("%s: kept %v, want %v", tc.name, got, tc.want)
		}
	}
}

// spanRecords is the span context each log record was handled with, by
// message: what the OTel log bridge stamps on the exported record.
type spanRecords struct {
	mu    sync.Mutex
	byMsg map[string][]trace.SpanContext
}

func (r *spanRecords) of(msg string) []trace.SpanContext {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byMsg[msg]
}

// recordSpans wraps o.Log so each record's span context is kept.
func recordSpans(o *Options) *spanRecords {
	r := &spanRecords{byMsg: map[string][]trace.SpanContext{}}
	o.Log = slog.New(spanHandler{next: o.Log.Handler(), r: r})
	return r
}

type spanHandler struct {
	next slog.Handler
	r    *spanRecords
}

func (h spanHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h spanHandler) Handle(ctx context.Context, rec slog.Record) error {
	h.r.mu.Lock()
	h.r.byMsg[rec.Message] = append(h.r.byMsg[rec.Message], trace.SpanContextFromContext(ctx))
	h.r.mu.Unlock()
	return h.next.Handle(ctx, rec)
}

func (h spanHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return spanHandler{next: h.next.WithAttrs(as), r: h.r}
}

func (h spanHandler) WithGroup(g string) slog.Handler {
	return spanHandler{next: h.next.WithGroup(g), r: h.r}
}

func findLogs(t *testing.T, logs *syncBuffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, `"msg":"`+msg+`"`) {
			continue
		}
		var m map[string]any
		if err := jsonUnmarshal([]byte(line), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}
