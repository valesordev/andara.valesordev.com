// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/types/known/durationpb"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/server/gateway"
)

// world is both seams behind the test gateway: Submit answers by a
// script, and Subscribe streams what the script and the test push. It
// stands in for AW-SRV-010's ingress and AW-SRV-011's egress with the
// wire shapes they emit — codes, ErrorInfo reasons, RetryInfo, the Resync
// frame on a resume it cannot honor — so that play is tested against the
// Protocol rather than against the server's packages.
type world struct {
	mu      sync.Mutex
	submits []*gamev1.SubmitRequest
	subs    []*gamev1.SubscribeRequest
	answer  func(w *world, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error)
	frames  chan frame
	nextID  atomic.Uint64
	offset  atomic.Int64
	streams atomic.Int32
}

type frame struct {
	env *gamev1.EventEnvelope
	err error
}

func newWorld() *world {
	w := &world{frames: make(chan frame, 64)}
	w.answer = defaultAnswer
	return w
}

func (w *world) Submit(_ context.Context, _ *gateway.Session, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
	w.mu.Lock()
	w.submits = append(w.submits, req)
	answer := w.answer
	w.mu.Unlock()
	return answer(w, req)
}

func (w *world) Subscribe(ctx context.Context, _ *gateway.Session, req *gamev1.SubscribeRequest, stream *connect.ServerStream[gamev1.EventEnvelope]) error {
	w.mu.Lock()
	w.subs = append(w.subs, req)
	w.mu.Unlock()
	w.streams.Add(1)
	defer w.streams.Add(-1)
	if req.GetLastEventId() != 0 {
		// Nothing is retained across a test server's restart, and this
		// fake retains nothing at all: every resume is a Resync.
		if err := stream.Send(&gamev1.EventEnvelope{Tick: 7, Payload: &gamev1.EventEnvelope_Resync{Resync: &gamev1.Resync{LastEventId: req.GetLastEventId(), Reason: "no_history"}}}); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-w.frames:
			if f.err != nil {
				return f.err
			}
			if err := stream.Send(f.env); err != nil {
				return err
			}
		}
	}
}

// push sends an Event to the stream with a fresh event_id.
func (w *world) push(ref string, tick uint64, payload any) {
	env := &gamev1.EventEnvelope{EventId: w.nextID.Add(1), Tick: tick, ClientRef: ref}
	switch p := payload.(type) {
	case *gamev1.RoomDescribed:
		env.Payload = &gamev1.EventEnvelope_RoomDescribed{RoomDescribed: p}
	case *gamev1.CharacterArrived:
		env.Payload = &gamev1.EventEnvelope_CharacterArrived{CharacterArrived: p}
	case *gamev1.CharacterLeft:
		env.Payload = &gamev1.EventEnvelope_CharacterLeft{CharacterLeft: p}
	case *gamev1.CommandRejected:
		env.Payload = &gamev1.EventEnvelope_CommandRejected{CommandRejected: p}
	case *gamev1.Heartbeat:
		env.EventId, env.Payload = 0, &gamev1.EventEnvelope_Heartbeat{Heartbeat: p}
	default:
		panic("push: unsupported payload")
	}
	w.frames <- frame{env: env}
}

// end ends the stream with err.
func (w *world) end(err error) { w.frames <- frame{err: err} }

func (w *world) accepted() (*gamev1.SubmitResponse, error) {
	return &gamev1.SubmitResponse{Partition: 3, AcceptedOffset: w.offset.Add(1)}, nil
}

var (
	square = &gamev1.RoomDescribed{ZoneId: "town", RoomId: "square", Title: "Town Square", Description: "A wide square of worn flagstones.", Exits: []string{"north", "west"}, Occupants: []string{"Mara"}}
	hall   = &gamev1.RoomDescribed{ZoneId: "town", RoomId: "hall", Title: "Old Hall", Description: "Dust and long tables.", Exits: []string{"south"}}
)

// defaultAnswer is a small world: look describes the square, north moves
// to the hall, west is refused after the log, and anything else is
// refused before it.
func defaultAnswer(w *world, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
	switch strings.TrimSpace(req.GetRaw()) {
	case "look":
		resp, _ := w.accepted()
		w.push(req.GetClientRef(), 10, square)
		return resp, nil
	case "north":
		resp, _ := w.accepted()
		w.push(req.GetClientRef(), 11, &gamev1.CharacterLeft{ZoneId: "town", RoomId: "square", CharacterName: "you", ToDirection: "north"})
		w.push(req.GetClientRef(), 11, hall)
		return resp, nil
	case "west":
		resp, _ := w.accepted()
		w.push(req.GetClientRef(), 12, &gamev1.CommandRejected{Code: "no_such_exit", Message: "There is no exit west."})
		return resp, nil
	}
	return nil, preLog(connect.CodeInvalidArgument, "unknown_verb", "parse", "I don't know how to "+strings.Fields(req.GetRaw())[0]+".")
}

// preLog is a pre-log rejection as the ingress words it on the wire.
func preLog(code connect.Code, reason, stage, detail string) error {
	ce := connect.NewError(code, errors.New(detail))
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: "andara.command", Metadata: map[string]string{"stage": stage, "pre_log": "true"}}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

// ingressError is an ingress error with its reason and, when given, the
// RetryInfo.
func ingressError(code connect.Code, reason, msg string, retryAfter time.Duration) error {
	ce := connect.NewError(code, errors.New(msg))
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: "andara.command"}); err == nil {
		ce.AddDetail(d)
	}
	if retryAfter > 0 {
		if d, err := connect.NewErrorDetail(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)}); err == nil {
			ce.AddDetail(d)
		}
	}
	return ce
}

func streamError(code connect.Code, reason, msg string) error {
	ce := connect.NewError(code, errors.New(msg))
	if d, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Reason: reason, Domain: "andara.stream"}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}

// newTestGameClient is a second client of the test server, for what a
// test does behind play's back.
func newTestGameClient(t *testing.T, s *liveServer) (gamev1connect.GameClient, error) {
	t.Helper()
	pem, err := os.ReadFile(s.ca)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	hc := &http.Client{Transport: &http2.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	return gamev1connect.NewGameClient(hc, "https://"+s.addr, connect.WithGRPC()), nil
}

// playServer is a test gateway with the world behind its seams, and a
// logged-in credential file for it.
func playServer(t *testing.T, w *world, adjust func(*gateway.Options)) (*liveServer, map[string]string) {
	t.Helper()
	s := startServerWith(t, nil, func(o *gateway.Options) {
		o.Ingress, o.Egress = w, w
		if adjust != nil {
			adjust(o)
		}
	})
	env := s.env(t)
	login(t, env)
	return s, env
}

func login(t *testing.T, env map[string]string) {
	t.Helper()
	res := runWithStdin(t, []string{"auth", "login", "--username", "oper", "--password-stdin"}, env, "operator-password\n")
	if res.exit != 0 {
		t.Fatalf("login: exit=%d stderr=%q", res.exit, res.stderr)
	}
}

// play runs the command with stdin, returning the transcript.
func play(t *testing.T, env map[string]string, stdin io.Reader, args ...string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args:      append([]string{"play"}, args...),
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookupFrom(env),
		stdin:     stdin,
	})
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

// script is a stdin the test feeds line by line, waiting for the
// transcript to show what it expects between lines.
type script struct {
	pr *io.PipeReader
	pw *io.PipeWriter
}

func newScript() *script {
	pr, pw := io.Pipe()
	return &script{pr: pr, pw: pw}
}

func (s *script) line(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.pw, line+"\n"); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
}

func (s *script) close() { _ = s.pw.Close() }

// syncBuffer is a goroutine-safe transcript the test can wait on.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// await waits for the transcript to contain want.
func (b *syncBuffer) await(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transcript never showed %q:\n%s", want, b.String())
}

// playLive runs play on a scripted stdin, returning the live transcript
// and a wait for the exit code.
func playLive(t *testing.T, env map[string]string, sc *script, args ...string) (*syncBuffer, *syncBuffer, func() int) {
	t.Helper()
	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- execute(&runtime{
			args:      append([]string{"play"}, args...),
			stdout:    stdout,
			stderr:    stderr,
			lookupEnv: lookupFrom(env),
			stdin:     sc.pr,
		})
	}()
	return stdout, stderr, func() int {
		select {
		case code := <-done:
			return code
		case <-time.After(15 * time.Second):
			t.Fatalf("play did not exit\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
			return -1
		}
	}
}

func (w *world) submitted() []*gamev1.SubmitRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*gamev1.SubmitRequest(nil), w.submits...)
}

func (w *world) subscribed() []*gamev1.SubscribeRequest {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]*gamev1.SubscribeRequest(nil), w.subs...)
}

// AC-1, AC-2, AC-4, AC-5, AC-9, AC-11: the transcript of a short session
// on a piped stdin, and what is not in it.
func TestPlay_Transcript(t *testing.T) {
	w := newWorld()
	s, env := playServer(t, w, nil)

	// Two heartbeats waiting before the first look; the transcript must
	// not mention them (AC-9).
	w.push("", 9, &gamev1.Heartbeat{})
	w.push("", 9, &gamev1.Heartbeat{})

	res := play(t, env, strings.NewReader("north\nwest\nfrobnicate\n\n"))
	if res.exit != 0 {
		t.Fatalf("exit=%d\nstdout:\n%s\nstderr:\n%s", res.exit, res.stdout, res.stderr)
	}
	want := []string{
		"-- Connected to " + s.addr + " as oper (session ",
		"Town Square\nA wide square of worn flagstones.\nExits: north, west\nHere: Mara\n",
		"you leaves north.\nOld Hall\nDust and long tables.\nExits: south\n",
		"There is no exit west.\n",
		"I don't know how to frobnicate.\n",
	}
	rest := res.stdout
	for _, s := range want {
		i := strings.Index(rest, s)
		if i < 0 {
			t.Fatalf("transcript lacks %q in order:\n%s", s, res.stdout)
		}
		rest = rest[i+len(s):]
	}
	// The ack is not game output, and nothing says where a rejection
	// happened (AC-2, AC-4, AC-5).
	for _, no := range []string{"offset", "partition", "stage", "pre_log", "post-log", "Heartbeat", "»", "«"} {
		if strings.Contains(res.stdout, no) {
			t.Errorf("transcript mentions %q:\n%s", no, res.stdout)
		}
	}
	if res.stderr != "" {
		t.Errorf("stderr is not empty: %q", res.stderr)
	}

	// Every line went out with its own client_ref, the look first (AC-1).
	subs := w.submitted()
	if len(subs) != 4 {
		t.Fatalf("submits = %d, want 4", len(subs))
	}
	seen := map[string]bool{}
	for i, want := range []string{"look", "north", "west", "frobnicate"} {
		if subs[i].GetRaw() != want {
			t.Errorf("submit %d raw = %q, want %q", i, subs[i].GetRaw(), want)
		}
		if ref := subs[i].GetClientRef(); ref == "" || seen[ref] {
			t.Errorf("submit %d client_ref %q is empty or reused", i, ref)
		} else {
			seen[ref] = true
		}
	}
	// A clean quit closed the Session (AC-11).
	if got := s.counter(t, "andara_sessions_total", map[string]string{"outcome": "closed"}); got != 1 {
		t.Errorf("sessions closed = %v, want 1", got)
	}
	if got := s.counter(t, "andara_sessions_active", nil); got != 0 {
		t.Errorf("sessions active = %v, want 0", got)
	}
}

// AC-10: under --output json, stdout is the Event stream and nothing else;
// prose and diagnostics go to stderr.
func TestPlay_JSONOutput(t *testing.T) {
	w := newWorld()
	_, env := playServer(t, w, nil)
	w.push("", 9, &gamev1.Heartbeat{})

	res := play(t, env, strings.NewReader("west\nfrobnicate\n"), "--output", "json", "--log-level", "info")
	if res.exit != 0 {
		t.Fatalf("exit=%d\nstdout:\n%s\nstderr:\n%s", res.exit, res.stdout, res.stderr)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		var env map[string]any
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("stdout line is not JSON: %q\nstdout:\n%s", line, res.stdout)
		}
		for _, k := range []string{"heartbeat", "room_described", "command_rejected"} {
			if _, ok := env[k]; ok {
				types = append(types, k)
			}
		}
	}
	if got := strings.Join(types, ","); got != "heartbeat,room_described,command_rejected" {
		t.Errorf("stdout carried %s", got)
	}
	// The pre-log refusal has no envelope: it is prose, so it is on
	// stderr as a log line, as is the connection notice.
	for _, want := range []string{`"msg":"I don't know how to frobnicate."`, `"msg":"Connected to `} {
		if !strings.Contains(res.stderr, want) {
			t.Errorf("stderr lacks %s:\n%s", want, res.stderr)
		}
	}
	if strings.Contains(res.stdout, "Connected") || strings.Contains(res.stdout, "frobnicate") {
		t.Errorf("prose on stdout:\n%s", res.stdout)
	}
}

// AC-6: a read-only world. The client holds the prompt for what RetryInfo
// says and tries the same line again; when the world is still read-only
// the player reads the server's statement, the Session stays open, and
// Events keep arriving.
func TestPlay_ReadOnly(t *testing.T) {
	w := newWorld()
	const readOnly = "The world is read-only for a moment: your command was not taken. Try it again shortly."
	w.answer = func(w *world, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
		if req.GetRaw() == "north" {
			return nil, ingressError(connect.CodeUnavailable, "world_read_only", readOnly, 10*time.Millisecond)
		}
		return defaultAnswer(w, req)
	}
	_, env := playServer(t, w, nil)

	sc := newScript()
	stdout, _, wait := playLive(t, env, sc)
	stdout.await(t, "Here: Mara")
	sc.line(t, "north")
	stdout.await(t, readOnly)
	// Still here: the world speaks and the client hears it.
	w.push("", 20, &gamev1.CharacterArrived{ZoneId: "town", RoomId: "square", CharacterName: "Mara", FromDirection: "west"})
	stdout.await(t, "Mara arrives from the west.")
	sc.close()
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d\n%s", code, stdout.String())
	}
	// One line, one client_ref, retried after RetryInfo until the hold
	// ran out.
	var norths []string
	for _, s := range w.submitted() {
		if s.GetRaw() == "north" {
			norths = append(norths, s.GetClientRef())
		}
	}
	if len(norths) != maxHoldAttempts+1 {
		t.Errorf("north was submitted %d times, want %d", len(norths), maxHoldAttempts+1)
	}
	for _, ref := range norths {
		if ref != norths[0] {
			t.Errorf("a retry changed the client_ref: %v", norths)
		}
	}
	if n := strings.Count(stdout.String(), readOnly); n != 1 {
		t.Errorf("the read-only statement appeared %d times, want 1:\n%s", n, stdout.String())
	}
}

// AW-SRV-031's inherited rule: produce_deadline is retried with the same
// client_ref and the retry's answer is the command's; outcome_unknown is
// terminal and tells the player to look.
func TestPlay_DeadlineRetriesTheSameRef(t *testing.T) {
	w := newWorld()
	var norths atomic.Int32
	w.answer = func(w *world, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
		switch req.GetRaw() {
		case "north":
			if norths.Add(1) == 1 {
				return nil, ingressError(connect.CodeDeadlineExceeded, "produce_deadline", "produce deadline exceeded; outcome unknown", 0)
			}
		case "dig":
			return nil, ingressError(connect.CodeDeadlineExceeded, "outcome_unknown", "outcome unknown: the command may be in the log and its fate will not be known", 0)
		}
		return defaultAnswer(w, req)
	}
	_, env := playServer(t, w, nil)

	res := play(t, env, strings.NewReader("north\ndig\n"))
	if res.exit != 0 {
		t.Fatalf("exit=%d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "Old Hall") {
		t.Errorf("the retried move did not land:\n%s", res.stdout)
	}
	if !strings.Contains(res.stdout, "the world may or may not have taken it. Use `look`") {
		t.Errorf("outcome_unknown was not explained:\n%s", res.stdout)
	}
	var refs []string
	for _, s := range w.submitted() {
		if s.GetRaw() == "north" {
			refs = append(refs, s.GetClientRef())
		}
	}
	if len(refs) != 2 || refs[0] != refs[1] {
		t.Errorf("north client_refs = %v, want two equal", refs)
	}
	digs := 0
	for _, s := range w.submitted() {
		if s.GetRaw() == "dig" {
			digs++
		}
	}
	if digs != 1 {
		t.Errorf("outcome_unknown was retried: %d submits", digs)
	}
}

// AC-7 and AC-8: the server goes away and comes back. The client says so,
// reconnects, resumes from its last event_id, is told the resume could
// not be honored, says that, and rebuilds the Room.
func TestPlay_ReconnectAndResync(t *testing.T) {
	prev := backoffInitial
	backoffInitial = 20 * time.Millisecond
	t.Cleanup(func() { backoffInitial = prev })

	w := newWorld()
	s, env := playServer(t, w, nil)

	sc := newScript()
	stdout, _, wait := playLive(t, env, sc)
	stdout.await(t, "Here: Mara")
	last := w.nextID.Load()

	s.stop(t)
	stdout.await(t, "-- Connection lost; reconnecting.")
	// A pause with nothing to connect to, then the server is back.
	time.Sleep(100 * time.Millisecond)
	w2 := newWorld()
	startServerWith(t, s, func(o *gateway.Options) { o.Ingress, o.Egress = w2, w2 })

	stdout.await(t, "You may have missed some events; the world continues from here.")
	stdout.await(t, "Town Square") // the look after the Resync — it appears once before, so wait for the count
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(stdout.String(), "Town Square") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Count(stdout.String(), "Town Square") < 2 {
		t.Fatalf("the Room was not rebuilt after the resync:\n%s", stdout.String())
	}
	sc.close()
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d\n%s", code, stdout.String())
	}
	subs := w2.subscribed()
	if len(subs) == 0 || subs[0].GetLastEventId() != last {
		t.Errorf("the resume did not carry last_event_id=%d: %v", last, subs)
	}
	out := stdout.String()
	if i, j := strings.Index(out, "Connection lost"), strings.Index(out, "You may have missed"); i < 0 || j <= i {
		t.Errorf("the loss was not announced before the resync:\n%s", out)
	}
	if strings.Count(out, "-- Connected to") != 2 {
		t.Errorf("expected two connection notices:\n%s", out)
	}
}

// --reconnect=false: a dropped connection is exit 3, for scripts.
func TestPlay_NoReconnect(t *testing.T) {
	w := newWorld()
	s, env := playServer(t, w, nil)
	sc := newScript()
	stdout, stderr, wait := playLive(t, env, sc, "--reconnect=false")
	stdout.await(t, "Here: Mara")
	s.stop(t)
	if code := wait(); code != ExitConnect {
		t.Fatalf("exit=%d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitConnect, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "connection lost") {
		t.Errorf("stderr does not say the connection was lost: %q", stderr.String())
	}
}

// The stream ends because the client fell behind (AW-SRV-011): a notice,
// and a resubscribe on the same Session from the last event_id.
func TestPlay_BufferFullResubscribes(t *testing.T) {
	w := newWorld()
	_, env := playServer(t, w, nil)
	sc := newScript()
	stdout, _, wait := playLive(t, env, sc)
	stdout.await(t, "Here: Mara")
	last := w.nextID.Load()

	w.end(streamError(connect.CodeResourceExhausted, "buffer_full", "event stream buffer full; resubscribe"))
	stdout.await(t, "-- You fell behind the world's events; the stream is being reopened.")
	// The fake answers a resume with a Resync; the client then looks.
	stdout.await(t, "You may have missed some events")
	deadline := time.Now().Add(5 * time.Second)
	for len(w.subscribed()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	subs := w.subscribed()
	if len(subs) < 2 || subs[1].GetLastEventId() != last {
		t.Fatalf("resubscribe did not resume from %d: %v", last, subs)
	}
	if subs[1].GetSessionId() != subs[0].GetSessionId() {
		t.Errorf("resubscribe changed Session: %v", subs)
	}
	sc.close()
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if strings.Contains(stdout.String(), "Connection lost") {
		t.Errorf("a stream drop was reported as a connection loss:\n%s", stdout.String())
	}
}

// AC-12: a server outside the client's Protocol range. Both ranges, exit 3.
func TestPlay_VersionMismatch(t *testing.T) {
	w := newWorld()
	_, env := playServer(t, w, func(o *gateway.Options) { o.ProtocolMin, o.ProtocolMax = 2, 3 })
	res := play(t, env, strings.NewReader(""))
	if res.exit != ExitConnect {
		t.Fatalf("exit=%d, want %d\nstdout:\n%s\nstderr:\n%s", res.exit, ExitConnect, res.stdout, res.stderr)
	}
	if want := "this client speaks 1; the server supports 2..3"; !strings.Contains(res.stderr, want) {
		t.Errorf("stderr = %q, want %q", res.stderr, want)
	}
	res = play(t, env, strings.NewReader(""), "-o", "json")
	e := jsonError(t, res.stdout)
	if e["code"] != CodeProtocolVersion {
		t.Errorf("code = %v", e["code"])
	}
	detail := e["detail"].(map[string]any)
	if detail["server_min_version"] != float64(2) || detail["server_max_version"] != float64(3) || detail["client_max_version"] != float64(1) {
		t.Errorf("detail = %v", detail)
	}
}

// No credential: usage error, nothing attempted.
func TestPlay_NotLoggedIn(t *testing.T) {
	w := newWorld()
	s := startServerWith(t, nil, func(o *gateway.Options) { o.Ingress, o.Egress = w, w })
	res := play(t, s.env(t), strings.NewReader("look\n"))
	if res.exit != ExitUsage || !strings.Contains(res.stderr, "auth login") {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	if len(w.submitted()) != 0 {
		t.Error("a Submit was attempted without a credential")
	}
}

// Protocol visibility: on at launch with --show-protocol, off and on
// again with /protocol during the session.
func TestPlay_ProtocolVisibility(t *testing.T) {
	w := newWorld()
	_, env := playServer(t, w, nil)
	res := play(t, env, strings.NewReader("west\n/protocol off\nnorth\n/protocol\nfrobnicate\n/help\n/bogus\n"), "--show-protocol")
	if res.exit != 0 {
		t.Fatalf("exit=%d\n%s\n%s", res.exit, res.stdout, res.stderr)
	}
	out := res.stdout
	for _, want := range []string{
		"» OpenSession protocol_version=1 client_name=\"andara-cli/dev\"",
		"« OpenSessionResponse session_id=",
		"» Subscribe session_id=",
		"» Submit session_id=", "raw=\"look\"",
		"« SubmitResponse session_id=", "partition=3 accepted_offset=1",
		"« RoomDescribed session_id=", "event_id=1 tick=10 client_ref=",
		"« CommandRejected session_id=", "event_id=2 tick=12",
		"Protocol visibility off.",
		"Protocol visibility on (session ",
		"« error session_id=", "code=invalid_argument reason=unknown_verb stage=parse pre_log=true message=\"I don't know how to frobnicate.\"",
		"/protocol [on|off]",
		"Unknown client command /bogus",
		"» CloseSession session_id=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript lacks %q:\n%s", want, out)
		}
	}
	// Between off and on, the north went by unseen.
	off, on := strings.Index(out, "Protocol visibility off."), strings.Index(out, "Protocol visibility on")
	if between := out[off:on]; strings.Contains(between, "»") || !strings.Contains(between, "Old Hall") {
		t.Errorf("protocol lines while off:\n%s", between)
	}
	// Under --output json the protocol lines are on stderr, verbatim.
	res = play(t, env, strings.NewReader("west\n"), "--show-protocol", "-o", "json")
	if !strings.Contains(res.stderr, "» Submit session_id=") || strings.Contains(res.stdout, "»") {
		t.Errorf("json mode: stdout=%q stderr=%q", res.stdout, res.stderr)
	}
}

// A Session the server no longer knows: the line is not sent again, the
// stream loop reopens a Session, and the next line goes out on it.
func TestPlay_SessionGoneReopens(t *testing.T) {
	prev := backoffInitial
	backoffInitial = 20 * time.Millisecond
	t.Cleanup(func() { backoffInitial = prev })

	w := newWorld()
	s, env := playServer(t, w, nil)
	sc := newScript()
	stdout, _, wait := playLive(t, env, sc)
	stdout.await(t, "Here: Mara")

	// Close the Session behind the client's back.
	first := w.subscribed()[0].GetSessionId()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	game, _ := newTestGameClient(t, s)
	if _, err := game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: first})); err != nil {
		t.Fatal(err)
	}
	stdout.await(t, "Connection lost; reconnecting.")
	stdout.await(t, "You may have missed some events")
	sc.line(t, "west")
	stdout.await(t, "There is no exit west.")
	sc.close()
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d\n%s", code, stdout.String())
	}
	subs := w.submitted()
	if got := subs[len(subs)-1].GetSessionId(); got == first {
		t.Errorf("west went out on the closed Session")
	}
}

// AW-SRV-031's key is (Session, client_ref): a deadline retry must never
// leave the Session it started in. The first north hangs; the Session is
// closed behind the client's back and a new one opened; the hung Submit
// then answers produce_deadline — and the retry does not go out on the
// new Session as a fresh key. The player is told the outcome is unknown.
func TestPlay_DeadlineRetryStaysInItsSession(t *testing.T) {
	prev := backoffInitial
	backoffInitial = 20 * time.Millisecond
	t.Cleanup(func() { backoffInitial = prev })

	w := newWorld()
	release := make(chan struct{})
	var norths atomic.Int32
	w.answer = func(w *world, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
		if req.GetRaw() == "north" {
			if norths.Add(1) == 1 {
				<-release
				return nil, ingressError(connect.CodeDeadlineExceeded, "produce_deadline", "produce deadline exceeded; outcome unknown", 0)
			}
			return defaultAnswer(w, req)
		}
		return defaultAnswer(w, req)
	}
	s, env := playServer(t, w, nil)
	sc := newScript()
	stdout, _, wait := playLive(t, env, sc)
	stdout.await(t, "Here: Mara")
	first := w.subscribed()[0].GetSessionId()

	sc.line(t, "north")
	deadline := time.Now().Add(5 * time.Second)
	for norths.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// The Session goes away under the hung Submit, and the client has
	// reconnected — a second subscription exists — before it is answered.
	game, _ := newTestGameClient(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: first})); err != nil {
		t.Fatal(err)
	}
	for len(w.subscribed()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(w.subscribed()) < 2 {
		t.Fatal("no reconnect")
	}
	close(release)
	stdout.await(t, outcomeUnknownMessage)
	sc.close()
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d\n%s", code, stdout.String())
	}
	if n := norths.Load(); n != 1 {
		t.Errorf("north reached the world %d times, want 1", n)
	}
	for _, sub := range w.submitted() {
		if sub.GetRaw() == "north" && sub.GetSessionId() != first {
			t.Errorf("north was retried on Session %s, not %s", sub.GetSessionId(), first)
		}
	}
}

// A session token that has expired since login: OpenSession is refused,
// the refresh token is exchanged once, the credential file updated, and
// play goes on.
func TestPlay_RefreshesAnExpiredToken(t *testing.T) {
	w := newWorld()
	_, env := playServer(t, w, nil)
	credPath := filepath.Join(env["XDG_CONFIG_HOME"], "andara", "credentials.yaml")
	entries, err := readCredentialFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	for k := range entries {
		key = k
	}
	cred := entries[key]
	stale := "stale." + cred.SessionToken
	cred.SessionToken = stale
	entries[key] = cred
	if err := writeCredentialFile(credPath, entries); err != nil {
		t.Fatal(err)
	}

	res := play(t, env, strings.NewReader("west\n"), "--show-protocol")
	if res.exit != 0 {
		t.Fatalf("exit=%d\nstdout:\n%s\nstderr:\n%s", res.exit, res.stdout, res.stderr)
	}
	for _, want := range []string{"« error code=unauthenticated", "» Auth.Refresh", "-- Session token refreshed.", "There is no exit west."} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("transcript lacks %q:\n%s", want, res.stdout)
		}
	}
	after, err := readCredentialFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := after[key].SessionToken; got == stale || got == "" {
		t.Errorf("the credential file was not updated")
	}
	if strings.Contains(res.stdout, stale) || strings.Contains(res.stdout, after[key].SessionToken) {
		t.Error("a token was printed")
	}

	// No refresh token at all: the refusal stands, exit 3.
	cred.RefreshToken = ""
	cred.SessionToken = stale
	entries[key] = cred
	if err := writeCredentialFile(credPath, entries); err != nil {
		t.Fatal(err)
	}
	res = play(t, env, strings.NewReader(""))
	if res.exit != ExitConnect {
		t.Fatalf("exit=%d, want %d: %s", res.exit, ExitConnect, res.stderr)
	}
}
