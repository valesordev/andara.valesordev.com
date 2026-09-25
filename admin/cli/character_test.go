// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/server/gateway"
)

// rosterRefusal is a roster error as AW-SRV-014 puts it on the wire.
func rosterRefusal(code connect.Code, reason, msg string) error {
	ce := connect.NewError(code, errors.New(msg))
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: rosterDomain}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

func summaryOf(id, name string, live bool, zone, room string) *gamev1.CharacterSummary {
	return &gamev1.CharacterSummary{CharacterId: id, Name: name, Status: accountsv1.CharacterStatus_CHARACTER_STATUS_ACTIVE, Live: live, ZoneId: zone, RoomId: room}
}

func brin() *gamev1.CharacterSummary { return summaryOf("ch-brin", "Brin", false, "town", "hall") }

func errorCode(t *testing.T, stdout string) string {
	t.Helper()
	var env jsonErrorEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an error envelope: %v\n%s", err, stdout)
	}
	return env.Error.Code
}

// indexOf is where call first appears in calls, or -1.
func indexOf(calls []string, call string) int { return slices.Index(calls, call) }

// The flag resolution table (Test plan: unit): none, one, or several
// Characters, with --character given or not.
func TestResolveCharacter(t *testing.T) {
	deleted := summaryOf("ch-old", "Old", false, "town", "square")
	deleted.Status = accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED
	cases := []struct {
		name    string
		chars   []*gamev1.CharacterSummary
		flag    string
		wantID  string
		wantErr string
		exit    int
	}{
		{name: "none, no flag", flag: "", wantErr: CodeNoCharacter, exit: ExitUsage},
		{name: "none, flag", flag: "Aldric", wantErr: CodeNoSuchCharacter, exit: ExitFail},
		{name: "one, no flag", chars: []*gamev1.CharacterSummary{aldric()}, wantID: "ch-aldric"},
		{name: "one, flag", chars: []*gamev1.CharacterSummary{aldric()}, flag: "Aldric", wantID: "ch-aldric"},
		{name: "one, flag in another case", chars: []*gamev1.CharacterSummary{aldric()}, flag: "aLDRIC", wantID: "ch-aldric"},
		{name: "one, flag names another", chars: []*gamev1.CharacterSummary{aldric()}, flag: "Brin", wantErr: CodeNoSuchCharacter, exit: ExitFail},
		{name: "several, no flag", chars: []*gamev1.CharacterSummary{brin(), aldric()}, wantErr: CodeCharacterRequired, exit: ExitUsage},
		{name: "several, flag", chars: []*gamev1.CharacterSummary{brin(), aldric()}, flag: "brin", wantID: "ch-brin"},
		{name: "one and a deleted one, no flag", chars: []*gamev1.CharacterSummary{deleted, aldric()}, wantID: "ch-aldric"},
		{name: "a deleted one named", chars: []*gamev1.CharacterSummary{deleted, aldric()}, flag: "Old", wantErr: CodeNoSuchCharacter, exit: ExitFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCharacter(tc.chars, tc.flag)
			if tc.wantErr == "" {
				if err != nil || got.GetCharacterId() != tc.wantID {
					t.Fatalf("got %v, %v; want %s", got, err, tc.wantID)
				}
				return
			}
			var ae *AppError
			if !errors.As(err, &ae) || ae.Code != tc.wantErr || ae.Exit != tc.exit {
				t.Fatalf("got %v, %v; want %s exit %d", got, err, tc.wantErr, tc.exit)
			}
		})
	}
	// The messages say what to do.
	_, err := resolveCharacter(nil, "")
	if !strings.Contains(err.Error(), "andara-cli character create") {
		t.Errorf("no_character does not name the command: %q", err)
	}
	_, err = resolveCharacter([]*gamev1.CharacterSummary{brin(), aldric()}, "")
	if !strings.Contains(err.Error(), "Aldric, Brin") || !strings.Contains(err.Error(), "--character") {
		t.Errorf("character_required does not list them: %q", err)
	}
}

// Each server reason becomes error.code, exit 1, with the server's message.
func TestRosterErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		code   connect.Code
		reason string
	}{
		{connect.CodeResourceExhausted, "roster_full"},
		{connect.CodeAlreadyExists, "name_taken"},
		{connect.CodeInvalidArgument, "name_invalid"},
		{connect.CodeFailedPrecondition, "already_live"},
		{connect.CodeNotFound, "no_such_character"},
	} {
		err := rosterError(rosterRefusal(tc.code, tc.reason, "the server's words for "+tc.reason))
		var ae *AppError
		if !errors.As(err, &ae) || ae.Exit != ExitFail || ae.Code != tc.reason || ae.Message != "the server's words for "+tc.reason {
			t.Errorf("%s: got %#v", tc.reason, err)
		}
	}
	// Anything else is the ordinary RPC mapping.
	var ae *AppError
	if err := rosterError(connect.NewError(connect.CodeUnavailable, errors.New("down"))); !errors.As(err, &ae) || ae.Code != CodeConnect || ae.Exit != ExitConnect {
		t.Errorf("UNAVAILABLE: got %#v", err)
	}
}

// list's human form, golden (Test plan: unit).
func TestFormatRosterGolden(t *testing.T) {
	cs := byName([]*gamev1.CharacterSummary{
		summaryOf("ch-3", "brin", false, "town", "hall"),
		summaryOf("ch-1", "Aldric", true, "town", "square"),
		summaryOf("ch-2", "Carys", false, "wilds", "ford"),
	})
	got := formatRoster(cs)
	path := filepath.Join("testdata", "character", "list.txt")
	if *updateGoldens || os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `make goldens`)", path, err)
	}
	if string(want) != got {
		t.Errorf("list mismatch\nwant:\n%s\ngot:\n%s", want, got)
	}
}

// AC-1: create on an empty roster; the Session opened for it is closed.
func TestCharacter_Create(t *testing.T) {
	w := newWorld()
	w.chars = nil
	s, env := playServer(t, w, nil)
	closed := s.counter(t, "andara_sessions_total", map[string]string{"outcome": "closed"})

	res := runWithStdin(t, []string{"character", "create", "Aldric"}, env, "")
	if res.exit != ExitOK || res.stdout != "Aldric (1 of 5)\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	if got := s.counter(t, "andara_sessions_total", map[string]string{"outcome": "closed"}); got != closed+1 {
		t.Errorf("sessions closed %v -> %v, want one more", closed, got)
	}

	res = runWithStdin(t, []string{"character", "create", "Brin", "--output", "json"}, env, "")
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	var out struct {
		Character struct {
			CharacterID string `json:"character_id"`
			Name        string `json:"name"`
			Status      string `json:"status"`
			ZoneID      string `json:"zone_id"`
			RoomID      string `json:"room_id"`
		} `json:"character"`
		Count int `json:"count"`
		Max   int `json:"max_per_account"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("%v\n%s", err, res.stdout)
	}
	if out.Character.CharacterID != "ch-brin" || out.Character.Name != "Brin" || out.Character.Status != "CHARACTER_STATUS_ACTIVE" ||
		out.Character.ZoneID != "town" || out.Character.RoomID != "square" || out.Count != 2 || out.Max != 5 {
		t.Errorf("json = %+v", out)
	}
}

// AC-2: a refused name prints the server's message, exits 1 with the
// reason, and nothing follows the refused CreateCharacter.
func TestCharacter_CreateRefused(t *testing.T) {
	for _, tc := range []struct {
		code   connect.Code
		reason string
		msg    string
	}{
		{connect.CodeResourceExhausted, "roster_full", "this account already has 5 characters"},
		{connect.CodeAlreadyExists, "name_taken", "that name is taken"},
		{connect.CodeInvalidArgument, "name_invalid", "a name is 3 to 16 letters"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			w := newWorld()
			w.createAnswer = func(string) error { return rosterRefusal(tc.code, tc.reason, tc.msg) }
			_, env := playServer(t, w, nil)

			res := runWithStdin(t, []string{"character", "create", "Aldric"}, env, "")
			if res.exit != ExitFail || !strings.Contains(res.stderr, tc.msg) || res.stdout != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
			}
			res = runWithStdin(t, []string{"character", "create", "Aldric", "-o", "json"}, env, "")
			if res.exit != ExitFail || errorCode(t, res.stdout) != tc.reason {
				t.Fatalf("exit=%d stdout=%q", res.exit, res.stdout)
			}
			calls := w.called()
			if calls[len(calls)-1] != "CreateCharacter:Aldric" {
				t.Errorf("something was attempted after the refusal: %v", calls)
			}
		})
	}
}

// AC-3: one line each, sorted by name; JSON carries the summaries.
func TestCharacter_List(t *testing.T) {
	w := newWorld()
	w.chars = []*gamev1.CharacterSummary{brin(), summaryOf("ch-aldric", "Aldric", true, "town", "square")}
	_, env := playServer(t, w, nil)

	res := runWithStdin(t, []string{"character", "list"}, env, "")
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	if want := "Aldric  live     town/square\nBrin    dormant  town/hall\n"; res.stdout != want {
		t.Errorf("list =\n%s\nwant\n%s", res.stdout, want)
	}

	res = runWithStdin(t, []string{"character", "list", "-o", "json"}, env, "")
	var out struct {
		Characters []struct {
			CharacterID string `json:"character_id"`
			Name        string `json:"name"`
			Live        bool   `json:"live"`
		} `json:"characters"`
		Max int `json:"max_per_account"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
		t.Fatalf("%v\n%s", err, res.stdout)
	}
	if len(out.Characters) != 2 || out.Characters[0].Name != "Aldric" || !out.Characters[0].Live || out.Characters[1].CharacterID != "ch-brin" || out.Max != 5 {
		t.Errorf("json = %+v", out)
	}

	// Asked every time: nothing is cached.
	w.mu.Lock()
	w.chars = w.chars[:1]
	w.mu.Unlock()
	res = runWithStdin(t, []string{"character", "list"}, env, "")
	if res.stdout != "Brin  dormant  town/hall\n" {
		t.Errorf("second list = %q", res.stdout)
	}
}

// AC-4: --character selects before Subscribe's first look; the ack is
// protocol visibility only; the first thing the player reads is the Room.
func TestPlay_SelectsBeforeSubscribe(t *testing.T) {
	w := newWorld()
	w.chars = []*gamev1.CharacterSummary{brin(), aldric()}
	_, env := playServer(t, w, nil)

	res := play(t, env, strings.NewReader(""), "--character", "aldric")
	if res.exit != 0 {
		t.Fatalf("exit=%d\nstdout:\n%s\nstderr:\n%s", res.exit, res.stdout, res.stderr)
	}
	calls := w.called()
	sel, sub, look := indexOf(calls, "SelectCharacter:ch-aldric"), indexOf(calls, "Subscribe"), indexOf(calls, "Submit:look")
	if sel < 0 || sub < 0 || look < 0 || sel >= sub || sub >= look {
		t.Fatalf("calls out of order: %v", calls)
	}
	var player []string
	for _, l := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		if !strings.HasPrefix(l, "-- ") {
			player = append(player, l)
		}
	}
	if len(player) == 0 || player[0] != "Town Square" {
		t.Errorf("the first thing read is not the Room:\n%s", res.stdout)
	}
	for _, no := range []string{"SelectCharacter", "accepted_offset", "partition"} {
		if strings.Contains(res.stdout, no) {
			t.Errorf("the ack reached the player's transcript (%q):\n%s", no, res.stdout)
		}
	}

	res = play(t, env, strings.NewReader(""), "--character", "Aldric", "--show-protocol")
	if res.exit != 0 {
		t.Fatalf("exit=%d\n%s", res.exit, res.stderr)
	}
	for _, re := range []string{
		`(?m)^  » SelectCharacter session_id=[0-9a-f]+ character_id=ch-aldric$`,
		`(?m)^  « SelectCharacterResponse session_id=[0-9a-f]+ partition=3 accepted_offset=\d+$`,
	} {
		if !regexp.MustCompile(re).MatchString(res.stdout) {
			t.Errorf("protocol visibility lacks %s:\n%s", re, res.stdout)
		}
	}
}

// AC-5: no --character and exactly one: it is selected, and the notice
// names it.
func TestPlay_OnlyCharacter(t *testing.T) {
	w := newWorld()
	s, env := playServer(t, w, nil)
	res := play(t, env, strings.NewReader(""))
	if res.exit != 0 {
		t.Fatalf("exit=%d\n%s", res.exit, res.stderr)
	}
	if !strings.Contains(res.stdout, "-- Connected to "+s.addr+" as oper, playing Aldric (session ") {
		t.Errorf("the notice does not name the Character:\n%s", res.stdout)
	}
	if got := w.called(); indexOf(got, "SelectCharacter:ch-aldric") < 0 {
		t.Errorf("not selected: %v", got)
	}
}

// AC-6: none, or several with no --character: exit 2, nothing subscribed,
// and the Session opened to ask is closed.
func TestPlay_NoOrSeveralCharacters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chars []*gamev1.CharacterSummary
		code  string
		says  string
	}{
		{"none", nil, CodeNoCharacter, "andara-cli character create"},
		{"several", []*gamev1.CharacterSummary{aldric(), brin()}, CodeCharacterRequired, "Aldric, Brin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			w.chars = tc.chars
			s, env := playServer(t, w, nil)
			closed := s.counter(t, "andara_sessions_total", map[string]string{"outcome": "closed"})

			res := play(t, env, strings.NewReader("look\n"))
			if res.exit != ExitUsage || !strings.Contains(res.stderr, tc.says) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
			}
			res = play(t, env, strings.NewReader("look\n"), "-o", "json")
			if res.exit != ExitUsage || errorCode(t, res.stdout) != tc.code {
				t.Fatalf("exit=%d stdout=%q", res.exit, res.stdout)
			}
			if calls := w.called(); slices.ContainsFunc(calls, func(c string) bool {
				return c == "Subscribe" || strings.HasPrefix(c, "Submit:") || strings.HasPrefix(c, "SelectCharacter:")
			}) {
				t.Errorf("something past the roster was attempted: %v", calls)
			}
			if got := s.counter(t, "andara_sessions_total", map[string]string{"outcome": "closed"}); got != closed+2 {
				t.Errorf("sessions closed %v -> %v, want two more", closed, got)
			}
		})
	}
}

// AC-7: already_live at launch is the server's message and exit 1.
func TestPlay_AlreadyLiveAtLaunch(t *testing.T) {
	const msg = "a character is already live on this account: Aldric"
	w := newWorld()
	w.selectAnswer = func(*world, string) error {
		return rosterRefusal(connect.CodeFailedPrecondition, "already_live", msg)
	}
	_, env := playServer(t, w, nil)

	res := play(t, env, strings.NewReader("look\n"), "--character", "Aldric")
	if res.exit != ExitFail || !strings.Contains(res.stderr, msg) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	res = play(t, env, strings.NewReader("look\n"), "--character", "Aldric", "-o", "json")
	if res.exit != ExitFail || errorCode(t, res.stdout) != CodeAlreadyLive {
		t.Fatalf("exit=%d stdout=%q", res.exit, res.stdout)
	}
	if calls := w.called(); slices.Contains(calls, "Subscribe") {
		t.Errorf("subscribed after already_live: %v", calls)
	}
}

// AC-8: a reconnect selects the same Character again before the resume,
// waiting out already_live — announced once, never fatal.
func TestPlay_ReconnectWaitsOutAlreadyLive(t *testing.T) {
	prev := backoffInitial
	backoffInitial = 20 * time.Millisecond
	t.Cleanup(func() { backoffInitial = prev })

	w := newWorld()
	s, env := playServer(t, w, nil)
	sc := newScript()
	stdout, stderr, wait := playLive(t, env, sc, "--character", "Aldric")
	stdout.await(t, "Here: Mara")

	var refusals atomic.Int32
	w.mu.Lock()
	w.selectAnswer = func(*world, string) error {
		if refusals.Add(1) <= 2 {
			return rosterRefusal(connect.CodeFailedPrecondition, "already_live", "a character is already live on this account: Aldric")
		}
		return nil
	}
	w.mu.Unlock()

	// Close the Session behind the client's back.
	first := w.subscribed()[0].GetSessionId()
	game, _ := newTestGameClient(t, s)
	if _, err := game.CloseSession(t.Context(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: first})); err != nil {
		t.Fatal(err)
	}
	stdout.await(t, "-- Connection lost; reconnecting.")
	stdout.await(t, "-- "+waitingMessage)
	stdout.await(t, "You may have missed some events")
	sc.line(t, "west")
	stdout.await(t, "There is no exit west.")
	sc.close()
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}

	out := stdout.String()
	if n := strings.Count(out, waitingMessage); n != 1 {
		t.Errorf("the wait was announced %d times, want 1:\n%s", n, out)
	}
	w.mu.Lock()
	selects := slices.Clone(w.selects)
	w.mu.Unlock()
	if len(selects) != 4 || slices.ContainsFunc(selects, func(id string) bool { return id != "ch-aldric" }) {
		t.Errorf("selects = %v, want ch-aldric four times (launch, two already_live, ok)", selects)
	}
	// The resume followed the successful select.
	calls := w.called()
	lastSelect, secondSub := -1, -1
	for i, c := range calls {
		switch {
		case c == "SelectCharacter:ch-aldric":
			lastSelect = i
		case c == "Subscribe" && i > indexOf(calls, "Subscribe") && secondSub < 0:
			secondSub = i
		}
	}
	if secondSub < 0 || secondSub < lastSelect {
		t.Errorf("resubscribed before the Character was selected: %v", calls)
	}
}

// The reconnect loop's other refusals are fatal with their reason.
func TestPlay_ReconnectRefusedIsFatal(t *testing.T) {
	prev := backoffInitial
	backoffInitial = 20 * time.Millisecond
	t.Cleanup(func() { backoffInitial = prev })

	w := newWorld()
	s, env := playServer(t, w, nil)
	sc := newScript()
	defer sc.close()
	stdout, stderr, wait := playLive(t, env, sc, "--character", "Aldric")
	stdout.await(t, "Here: Mara")
	w.mu.Lock()
	w.selectAnswer = func(*world, string) error {
		return rosterRefusal(connect.CodeNotFound, "no_such_character", "no such character")
	}
	w.mu.Unlock()
	game, _ := newTestGameClient(t, s)
	if _, err := game.CloseSession(t.Context(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: w.subscribed()[0].GetSessionId()})); err != nil {
		t.Fatal(err)
	}
	if code := wait(); code != ExitFail || !strings.Contains(stderr.String(), "no such character") {
		t.Fatalf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

// Observability: the cli.command root span parents the roster RPCs, as it
// does play's — the server's RPC span carries the command's trace.
func TestCharacter_SpanParentsTheRPCs(t *testing.T) {
	w := newWorld()
	var seen atomic.Value
	_, env := playServer(t, w, func(o *gateway.Options) {
		o.TrustInboundTraceparent = true
		o.Roster = tracingRoster{world: w, seen: &seen}
	})
	rec := tracetest.NewSpanRecorder()
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args:      []string{"character", "list"},
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookupFrom(env),
		tpOptions: []sdktrace.TracerProviderOption{sdktrace.WithSpanProcessor(rec)},
	})
	if exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	var root sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "cli.command" {
			root = s
		}
	}
	if root == nil {
		t.Fatal("no cli.command span")
	}
	if got, _ := seen.Load().(string); got != root.SpanContext().TraceID().String() {
		t.Errorf("ListCharacters ran in trace %q, cli.command is %s", got, root.SpanContext().TraceID())
	}
}

// tracingRoster records the trace ListCharacters was served in.
type tracingRoster struct {
	*world
	seen *atomic.Value
}

func (r tracingRoster) ListCharacters(ctx context.Context, s *gateway.Session) (*gamev1.ListCharactersResponse, error) {
	r.seen.Store(trace.SpanContextFromContext(ctx).TraceID().String())
	return r.world.ListCharacters(ctx, s)
}
