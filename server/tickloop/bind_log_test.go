// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"strings"
	"testing"
	"time"

	"github.com/valesordev/andara/server/simtest"
)

// AW-SRV-014 Observability: a BindCharacter that applies logs at info with
// the account, the Character, the Session, the trace, and where the body
// is and what the bind did to it. One that is rejected logs no such line.
func TestLoop_LogsTheAppliedBind(t *testing.T) {
	h := newHarness(t, nil)
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "lane"))
	h.source.Push(simtest.Unbind("town", "ch-1"))
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	h.source.Push(simtest.Bind("town", "ch-1", "Aldric", "plaza"))
	h.source.Push(simtest.Bind("town", "ch-2", "Brenna", "nowhere"))
	if err := h.runFor(time.Second); err != nil {
		t.Fatal(err)
	}
	lines := findLogs(t, h.logs, "character bind applied")
	want := []struct{ room, body string }{{"lane", "spawned"}, {"lane", "woken"}, {"lane", "present"}}
	if len(lines) != len(want) {
		t.Fatalf("got %d bind-applied lines, want %d (the rejected bind logs none):\n%s", len(lines), len(want), h.logs.String())
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
