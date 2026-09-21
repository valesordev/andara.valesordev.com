// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/term"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/encoding/protojson"

	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// The Protocol version this client speaks. One value, not a range: the
// server declares the range and either accepts this or names what it
// would have taken (ADR-0003, AW-SRV-005).
const clientProtocolVersion uint32 = 1

// error.code values play adds. Additive-only.
const (
	CodeProtocolVersion = "protocol_version"
	CodeDisconnected    = "disconnected"
)

// Reasons and details read off the wire. The strings are the server's
// contract (AW-SRV-010, AW-SRV-011, AW-SRV-031), repeated here rather than
// imported so that the client depends on the Protocol and not on the
// server's packages.
const (
	reasonProduceDeadline = "produce_deadline"
	reasonOutcomeUnknown  = "outcome_unknown"
	reasonReadOnly        = "world_read_only"
	reasonInTransit       = "in_transit"
	reasonBufferFull      = "buffer_full"
)

// Submit retry bounds. A produce_deadline retry carries the same client_ref
// and is answered with the original outcome (AW-SRV-031), so the only
// question is the client's patience; an UNAVAILABLE retry waits what
// RetryInfo says, or holdDelay when it says nothing (in_transit).
const (
	maxSubmitAttempts = 4
	maxHoldAttempts   = 3
	holdDelay         = 500 * time.Millisecond
	maxRetryAfter     = 5 * time.Second
)

// The client's own system-voice lines: what it says when the server has
// said nothing a player could read. Listed under the story's Open
// questions for Brian, as AW-SRV-010's ReadOnlyMessage was.
const (
	outcomeUnknownMessage = "No answer for that: the world may or may not have taken it. Use `look` to see where things stand."
	notAnsweredMessage    = "The world has not answered that yet; it may still take it. Use `look` to see where things stand."
	notSentMessage        = "That was not sent: "
	lostMessage           = "Connection lost; reconnecting."
	behindMessage         = "You fell behind the world's events; the stream is being reopened."
)

// Reconnect backoff (AC-7). The initial delay is a variable so a test can
// reconnect in milliseconds.
var backoffInitial = time.Second

const backoffMax = 15 * time.Second

// answerWait is how long play waits for the Events of an accepted Submit:
// before the first prompt, for the Room (AC-1); between the lines of a
// pipe, so a script's transcript reads in the order it was typed; and at
// the pipe's end, before leaving. A person's lines are not held — and a
// person who leaves, leaves.
const answerWait = 3 * time.Second

// player is one play session: the Game client, the Session it holds, the
// stream reading it, and the console the player sees.
type player struct {
	rt   *runtime
	o    playOptions
	game gamev1connect.GameClient
	cred *storedCredential
	con  *console

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	sessionID string
	// streamCancel ends the current Subscribe stream, so a Submit that
	// learns the Session is gone can hand the reconnect to the stream
	// loop rather than reconnecting itself.
	streamCancel context.CancelFunc

	// lastEvent is the resume point: the last event_id seen. Stream frames
	// carry 0 and do not move it.
	lastEvent atomic.Uint64
	// refs numbers client_refs; refPrefix keeps them unique across the
	// Sessions one process opens, so a reconnect cannot reuse one.
	refs      atomic.Uint64
	refPrefix string
	// looked is whether the current Session has been asked for its Room.
	looked atomic.Bool
	// pending is every client_ref the server accepted and no Event has
	// yet carried, and lastFrame when the stream last delivered: what a
	// scripted run waits on between lines.
	pending   map[string]struct{}
	lastFrame atomic.Int64

	proto atomic.Bool

	// fatal carries the error that ends play from the stream loop.
	fatal chan error
	wg    sync.WaitGroup
	// ready is closed once the first look has been answered, so typed
	// lines follow the Room rather than race it.
	ready     chan struct{}
	readyOnce sync.Once
}

// play runs the Text Interface until the player quits or the connection
// is lost for good.
func (rt *runtime) play(o playOptions) error {
	game, cred, err := rt.gameClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(rt.ctx)
	defer cancel()
	p := &player{
		rt: rt, o: o, game: game, cred: cred,
		ctx: ctx, cancel: cancel,
		refPrefix: fmt.Sprintf("%08x", rand.Uint32()),
		fatal:     make(chan error, 1),
		ready:     make(chan struct{}),
		pending:   map[string]struct{}{},
	}
	p.proto.Store(o.showProtocol)

	con, restore, err := rt.newConsole(o)
	if err != nil {
		return err
	}
	defer restore()
	p.con = con

	if err := p.connect(ctx); err != nil {
		return err
	}
	p.wg.Add(1)
	go p.streamLoop()

	err = p.repl()
	// Leaving: end the stream, close the Session so no linkdead Character
	// is left behind (AC-11), and wait briefly for the stream to go.
	cancel()
	p.closeSession()
	p.wait(2 * time.Second)
	if con.interactive() {
		// Leave the shell on a fresh line, not after the prompt.
		_, _ = io.WriteString(rt.stdout, "\r\n")
	}
	return err
}

// wait blocks until the stream loop has ended or d has passed; a goroutine
// still blocked in a read is the process's to take with it.
func (p *player) wait(d time.Duration) {
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// connect opens a Session (AC-1, AC-12) bounded by --timeout, and remembers
// it. A version outside the server's range names both and is exit 3.
//
// A session token expires an hour after login, not after play started, so
// a reconnect late in a long session may be refused UNAUTHENTICATED with
// nothing wrong but the clock. The stored refresh token is exchanged once
// and the credential file updated — what `auth refresh` does — before that
// is taken as an answer.
func (p *player) connect(ctx context.Context) error {
	refreshed := false
	for {
		cctx, cancel := context.WithTimeout(ctx, p.rt.settings.Timeout)
		p.protof("» OpenSession protocol_version=%d client_name=%q act_as=%q", clientProtocolVersion, clientName(), p.o.as)
		resp, err := p.game.OpenSession(cctx, connect.NewRequest(&gamev1.OpenSessionRequest{
			ProtocolVersion: clientProtocolVersion,
			AuthToken:       p.cred.SessionToken,
			ClientName:      clientName(),
			ActAsAccountId:  p.o.as,
		}))
		cancel()
		if err == nil {
			return p.opened(resp.Msg)
		}
		p.protof("« error %s", describeError(err))
		if ve := versionError(err); ve != nil {
			return ve
		}
		if connect.CodeOf(err) == connect.CodeUnauthenticated && !refreshed && p.cred.RefreshToken != "" {
			refreshed = true
			if rerr := p.refresh(ctx); rerr == nil {
				continue
			} else {
				p.rt.log("debug", "refresh: "+rerr.Error())
			}
		}
		return rpcError(err)
	}
}

// refresh exchanges the refresh token for a fresh pair and stores it.
func (p *player) refresh(ctx context.Context) error {
	client, err := p.rt.authClient()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, p.rt.settings.Timeout)
	defer cancel()
	p.protof("» Auth.Refresh")
	resp, err := client.Refresh(cctx, connect.NewRequest(&authv1.RefreshRequest{RefreshToken: p.cred.RefreshToken}))
	if err != nil {
		p.protof("« error %s", describeError(err))
		return err
	}
	p.protof("« RefreshResponse")
	pair := resp.Msg.GetTokens()
	p.cred.SessionToken, p.cred.SessionExpires = pair.GetSessionToken(), unixTime(pair.GetSessionExpiresUnix())
	p.cred.RefreshToken, p.cred.RefreshExpires = pair.GetRefreshToken(), unixTime(pair.GetRefreshExpiresUnix())
	if err := p.rt.storeCredential(*p.cred); err != nil {
		return err
	}
	p.con.notice("info", "Session token refreshed.")
	return nil
}

// opened records a new Session.
func (p *player) opened(msg *gamev1.OpenSessionResponse) error {
	p.protof("« OpenSessionResponse session_id=%s negotiated_version=%d server_range=%d..%d",
		msg.GetSessionId(), msg.GetNegotiatedVersion(), msg.GetServerMinVersion(), msg.GetServerMaxVersion())
	p.mu.Lock()
	p.sessionID = msg.GetSessionId()
	// What the old Session accepted, no Event on this one will answer.
	clear(p.pending)
	p.mu.Unlock()
	p.looked.Store(false)
	who := p.cred.Username
	if p.o.as != "" {
		who += " acting as " + p.o.as
	}
	p.con.notice("info", fmt.Sprintf("Connected to %s as %s (session %s, protocol %d).", p.rt.settings.ServerAddress, who, msg.GetSessionId(), msg.GetNegotiatedVersion()))
	return nil
}

func clientName() string { return "andara-cli/" + version }

func (p *player) session() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sessionID
}

// closeSession tells the server the player left. It runs on a fresh
// context: p.ctx is already canceled by the time we get here.
func (p *player) closeSession() {
	id := p.session()
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p.protof("» CloseSession session_id=%s", id)
	if _, err := p.game.CloseSession(ctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: id})); err != nil {
		p.protof("« error %s", describeError(err))
		p.rt.log("debug", "CloseSession: "+err.Error())
		return
	}
	p.protof("« CloseSessionResponse")
}

// repl reads lines until the player quits or the stream loop reports a
// fatal error, and returns what play should. Input is read on its own
// goroutine so that a person's Ctrl-C reaches us while a Submit is being
// retried; a pipe's end of input instead lets the last Submit finish and
// lingers for its Events.
func (p *player) repl() error {
	lines := make(chan string)
	go func() {
		defer close(lines)
		for {
			line, err := p.con.readLine()
			if err != nil {
				if p.con.interactive() {
					p.cancel()
				}
				return
			}
			select {
			case lines <- line:
			case <-p.ctx.Done():
				return
			}
		}
	}()
	// A signal ends play from its own goroutine: the loop below may be
	// inside a Submit being retried, and signal.Notify has taken the
	// default action away, so nothing else would.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	go func() {
		select {
		case <-sig:
			p.cancel()
		case <-p.ctx.Done():
		}
	}()

	// The Room first (AC-1); what is typed follows it.
	select {
	case <-p.ready:
	case err := <-p.fatal:
		return err
	case <-p.ctx.Done():
		return nil
	}
	for {
		select {
		case <-p.ctx.Done():
			return nil
		case err := <-p.fatal:
			return err
		case line, ok := <-lines:
			if !ok {
				if !p.con.interactive() {
					p.drain(answerWait)
				}
				return nil
			}
			line = strings.TrimSpace(line)
			switch {
			case line == "":
			case strings.HasPrefix(line, "/"):
				if p.local(line) {
					return nil
				}
			default:
				p.submit(line)
				if !p.con.interactive() {
					p.drain(answerWait)
				}
			}
		}
	}
}

// local handles a /command; true means quit.
func (p *player) local(line string) bool {
	fields := strings.Fields(line)
	switch fields[0] {
	case "/quit", "/exit":
		return true
	case "/help":
		p.con.system("/protocol [on|off]   show the Intents sent and the Events received")
		p.con.system("/help                this list")
		p.con.system("/quit                leave (Ctrl-C and Ctrl-D at an empty line do too)")
	case "/protocol", "/proto":
		on := !p.proto.Load()
		if len(fields) > 1 {
			switch fields[1] {
			case "on":
				on = true
			case "off":
				on = false
			default:
				p.con.system("usage: /protocol [on|off]")
				return false
			}
		}
		p.proto.Store(on)
		if on {
			p.con.system(fmt.Sprintf("Protocol visibility on (session %s, last event %d).", p.session(), p.lastEvent.Load()))
		} else {
			p.con.system("Protocol visibility off.")
		}
	default:
		p.con.system("Unknown client command " + fields[0] + "; /help lists them.")
	}
	return false
}

// quietFor is how long the stream must be silent, once every accepted
// Submit has been answered, before drain considers the answer complete:
// one Command's Events are emitted in one tick and arrive together, so a
// pause this long after the first is the rest having arrived.
const quietFor = 50 * time.Millisecond

// drain waits until every accepted Submit has been answered by an Event
// and the stream has then been quiet for quietFor, or d has passed.
func (p *player) drain(d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		n := len(p.pending)
		p.mu.Unlock()
		if n == 0 && time.Since(time.Unix(0, p.lastFrame.Load())) > quietFor {
			return
		}
		if !p.sleep(10 * time.Millisecond) {
			return
		}
	}
}

// expect marks ref as awaiting an Event. It is set before the Submit goes
// out: the Event can arrive on the stream before the ack does.
func (p *player) expect(ref string) {
	p.mu.Lock()
	p.pending[ref] = struct{}{}
	p.mu.Unlock()
}

// answered clears ref: an Event carried it, or the Submit was refused.
func (p *player) answered(ref string) {
	if ref == "" {
		return
	}
	p.mu.Lock()
	delete(p.pending, ref)
	p.mu.Unlock()
}

// nextRef is one fresh client_ref per typed line (AW-SRV-031). A line is
// retried under the same one; a different line never reuses it.
func (p *player) nextRef() string {
	return fmt.Sprintf("%s-%d", p.refPrefix, p.refs.Add(1))
}

// look asks the world for the current Room: on connect (AC-1) and after a
// Resync (AC-8), so the transcript is rebuilt rather than left gapped.
func (p *player) look() {
	p.submit("look")
}

// submit sends one line as an Intent and reports what the server said. The
// ack is not game output (AC-2): it is shown only under protocol
// visibility. A refusal is printed as the server worded it, wherever in
// the pipeline it happened (AC-5); the reason steers behavior only.
//
// A line lives and dies in the Session it was first sent on. The
// idempotency key is (Session, client_ref) — AW-SRV-031's table is per
// Session — so a retry on a Session opened since would be a fresh key, a
// new produce, and possibly the double move the retry exists to prevent.
// If the Session changed under a retry, or the old one answers that it is
// gone, the outcome of the first attempt is unknown and the player is
// told so.
func (p *player) submit(raw string) {
	ref := p.nextRef()
	p.expect(ref)
	held := 0
	id := p.session()
	for attempt := 1; ; attempt++ {
		if p.session() != id {
			p.answered(ref)
			p.con.game(outcomeUnknownMessage)
			return
		}
		ctx, cancel := context.WithTimeout(p.ctx, p.o.clientTimeout)
		p.protof("» Submit session_id=%s client_ref=%s raw=%q", id, ref, raw)
		resp, err := p.game.Submit(ctx, connect.NewRequest(&gamev1.SubmitRequest{SessionId: id, Raw: raw, ClientRef: ref}))
		cancel()
		if err == nil {
			// Accepted: the outcome arrives on the stream, carrying ref.
			p.protof("« SubmitResponse session_id=%s client_ref=%s partition=%d accepted_offset=%d", id, ref, resp.Msg.GetPartition(), resp.Msg.GetAcceptedOffset())
			return
		}
		p.protof("« error session_id=%s %s", id, describeError(err))
		if p.ctx.Err() != nil {
			p.answered(ref)
			return
		}
		reason, _ := errorInfo(err)
		switch connect.CodeOf(err) {
		case connect.CodeDeadlineExceeded:
			// produce_deadline, or our own deadline: the record may be
			// in the log, and the same client_ref asks for its fate.
			// outcome_unknown is the answer that it never will be known.
			if reason != reasonOutcomeUnknown && attempt < maxSubmitAttempts {
				continue
			}
			p.answered(ref)
			if reason == reasonOutcomeUnknown {
				p.con.game(outcomeUnknownMessage)
			} else {
				p.con.game(notAnsweredMessage)
			}
			return
		case connect.CodeUnavailable:
			// Read-only or in transit: hold the prompt and try again
			// after what RetryInfo says, then say so (AC-6).
			if held < maxHoldAttempts && (reason == reasonReadOnly || reason == reasonInTransit) {
				held++
				if p.sleep(retryDelay(err)) {
					continue
				}
				p.answered(ref)
				return
			}
			p.answered(ref)
			p.con.game(connectMessage(err))
			return
		case connect.CodeUnauthenticated:
			// The Session is gone. On the first attempt the line was
			// not taken; on a retry, the first attempt may have been.
			// Either way the stream loop reconnects.
			p.answered(ref)
			if attempt > 1 {
				p.con.game(outcomeUnknownMessage)
			} else {
				p.con.game(notSentMessage + "your session ended.")
			}
			p.endStream()
			return
		}
		p.answered(ref)
		var ce *connect.Error
		if errors.As(err, &ce) {
			// Refused, as the server worded it (AC-5).
			p.con.game(ce.Message())
			return
		}
		// Not a Protocol answer: the connection itself. The stream loop
		// will notice and say so.
		p.con.game(notSentMessage + firstLine(err.Error()))
		return
	}
}

// sleep waits d or until play ends; false means play ended.
func (p *player) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// endStream ends the current stream so streamLoop re-evaluates the
// Session.
func (p *player) endStream() {
	p.mu.Lock()
	cancel := p.streamCancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// streamLoop owns the Subscribe stream for the life of play: it opens it,
// reads it, and when it ends decides between resubscribing on the same
// Session, reopening a Session (AC-7), or giving up.
func (p *player) streamLoop() {
	defer p.wg.Done()
	backoff := backoffInitial
	lost := false
	for {
		err := p.stream()
		if p.ctx.Err() != nil {
			return
		}
		reason, _ := errorInfo(err)
		code := connect.CodeOf(err)
		var ce *connect.Error
		typed := errors.As(err, &ce)
		switch {
		case err == nil:
			// The server ended the stream cleanly. Nothing is wrong with
			// the Session; open another — after a beat, so a server that
			// keeps doing it is not hammered.
			if !p.sleep(100 * time.Millisecond) {
				return
			}
			continue
		case code == connect.CodeResourceExhausted && reason == reasonBufferFull:
			p.con.notice("warn", behindMessage)
			continue
		case code == connect.CodePermissionDenied:
			p.fatal <- &AppError{Exit: ExitFail, Code: CodePermissionDenied, Message: connectMessage(err), Detail: map[string]any{"grpc_code": code.String()}}
			return
		case code == connect.CodeUnauthenticated, code == connect.CodeUnavailable, code == connect.CodeCanceled, !typed:
			// The Session or the connection is gone.
			if !p.o.reconnect {
				p.fatal <- &AppError{Exit: ExitConnect, Code: CodeDisconnected, Message: "connection lost: " + connectMessage(err), Detail: map[string]any{}}
				return
			}
			if !lost {
				lost = true
				p.con.notice("warn", lostMessage)
			}
			if !p.sleep(jitter(backoff)) {
				return
			}
			backoff = min(backoff*2, backoffMax)
			if err := p.connect(p.ctx); err != nil {
				var ae *AppError
				if errors.As(err, &ae) && ae.Exit != ExitConnect && ae.Exit != ExitTimeout {
					p.fatal <- err
					return
				}
				if ae != nil && (ae.Code == CodeUnauthenticated || ae.Code == CodeProtocolVersion) {
					p.fatal <- err
					return
				}
				p.rt.log("debug", "reconnect: "+err.Error())
				continue
			}
			lost = false
			backoff = backoffInitial
		default:
			// A typed refusal this client has no answer for; try again
			// after a while rather than spin.
			p.rt.log("warn", "event stream ended: "+describeError(err))
			if !p.sleep(jitter(backoff)) {
				return
			}
			backoff = min(backoff*2, backoffMax)
		}
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// stream runs one Subscribe until it ends and returns why. The first
// stream of a Session asks for the Room once the server has accepted the
// subscription; a later one resumes from lastEvent, and a Resync frame on
// it triggers the look instead.
func (p *player) stream() error {
	sctx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	p.mu.Lock()
	p.streamCancel = cancel
	id := p.sessionID
	p.mu.Unlock()

	last := p.lastEvent.Load()
	p.protof("» Subscribe session_id=%s last_event_id=%d world=%t", id, last, p.o.world)
	st, err := p.game.Subscribe(sctx, connect.NewRequest(&gamev1.SubscribeRequest{SessionId: id, LastEventId: last, World: p.o.world}))
	if err != nil {
		p.protof("« error session_id=%s %s", id, describeError(err))
		return err
	}
	defer func() { _ = st.Close() }()
	// The headers are back, so the gateway has the stream; the look's
	// RoomDescribed is addressed to this Session's subscription.
	if p.looked.CompareAndSwap(false, true) && last == 0 {
		go func() {
			p.look()
			p.drain(answerWait)
			p.readyOnce.Do(func() { close(p.ready) })
		}()
	}
	for st.Receive() {
		p.handle(st.Msg())
	}
	err = st.Err()
	switch {
	case p.ctx.Err() != nil:
		p.protof("« stream ended session_id=%s: leaving", id)
	case err != nil:
		p.protof("« error session_id=%s %s", id, describeError(err))
	default:
		p.protof("« stream closed by the server session_id=%s", id)
	}
	return err
}

// handle is one frame off the stream: advance the resume point, show it,
// and act on the two frames that ask something of the client.
func (p *player) handle(env *gamev1.EventEnvelope) {
	if id := env.GetEventId(); id != 0 {
		p.lastEvent.Store(id)
	}
	p.lastFrame.Store(time.Now().UnixNano())
	p.answered(env.GetClientRef())
	p.protof("« %s session_id=%s event_id=%d tick=%d client_ref=%s", eventName(env), p.session(), env.GetEventId(), env.GetTick(), env.GetClientRef())
	p.con.event(env)
	if _, ok := env.GetPayload().(*gamev1.EventEnvelope_Resync); ok {
		// The server could not resume: the player was told (the
		// rendered line), and the Room is rebuilt rather than guessed.
		p.looked.Store(true)
		go p.look()
	}
}

// protof writes a protocol-visibility line when it is on.
func (p *player) protof(format string, args ...any) {
	if p.proto.Load() {
		p.con.proto(fmt.Sprintf(format, args...))
	}
}

// versionError turns the FAILED_PRECONDITION of a Protocol version outside
// the server's range into exit 3 naming both ranges (AC-12), or nil.
func versionError(err error) *AppError {
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
		return nil
	}
	var smin, smax uint32
	found := false
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		pf, ok := msg.(*errdetails.PreconditionFailure)
		if !ok {
			continue
		}
		for _, v := range pf.GetViolations() {
			if v.GetType() == "PROTOCOL_VERSION" {
				if _, serr := fmt.Sscanf(v.GetDescription(), "server supports %d..%d", &smin, &smax); serr == nil {
					found = true
				}
			}
		}
	}
	if !found {
		var client uint32
		if _, serr := fmt.Sscanf(ce.Message(), "protocol version %d is outside the supported range %d..%d", &client, &smin, &smax); serr != nil {
			return nil
		}
	}
	return &AppError{
		Exit:    ExitConnect,
		Code:    CodeProtocolVersion,
		Message: fmt.Sprintf("protocol version mismatch: this client speaks %d; the server supports %d..%d", clientProtocolVersion, smin, smax),
		Detail: map[string]any{
			"client_min_version": clientProtocolVersion, "client_max_version": clientProtocolVersion,
			"server_min_version": smin, "server_max_version": smax,
		},
	}
}

// errorInfo reads the ErrorInfo detail off a wire error: the reason the
// client switches on, and its metadata.
func errorInfo(err error) (string, map[string]string) {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return "", nil
	}
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		if info, ok := msg.(*errdetails.ErrorInfo); ok {
			return info.GetReason(), info.GetMetadata()
		}
	}
	return "", nil
}

// retryDelay is what RetryInfo asks for, holdDelay when it carries none,
// and never more than maxRetryAfter.
func retryDelay(err error) time.Duration {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return holdDelay
	}
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		if ri, ok := msg.(*errdetails.RetryInfo); ok && ri.GetRetryDelay() != nil {
			return min(max(ri.GetRetryDelay().AsDuration(), 0), maxRetryAfter)
		}
	}
	return holdDelay
}

// connectMessage is the server's message for a wire error, or the error's
// first line.
func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return firstLine(err.Error())
}

// describeError is the protocol-visibility form: code, reason, message.
func describeError(err error) string {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return fmt.Sprintf("transport: %s", firstLine(err.Error()))
	}
	reason, meta := errorInfo(err)
	s := "code=" + ce.Code().String()
	if reason != "" {
		s += " reason=" + reason
	}
	for _, k := range []string{"stage", "pre_log"} {
		if v, ok := meta[k]; ok {
			s += " " + k + "=" + v
		}
	}
	return s + fmt.Sprintf(" message=%q", ce.Message())
}

// console is where play writes and reads. Human output goes to stdout —
// through the terminal when stdin is one, so a line arriving mid-edit is
// placed above the prompt — and under --output json stdout carries only
// the Event stream while everything else is a stderr log line (AC-10).
type console struct {
	rt   *runtime
	json bool
	term *term.Terminal
	in   *bufio.Scanner
	mu   sync.Mutex
}

// newConsole builds the console and returns how to put the terminal back.
func (rt *runtime) newConsole(o playOptions) (*console, func(), error) {
	c := &console{rt: rt, json: rt.settings.Output == outputJSON}
	restore := func() {}
	interactive := rt.stdin == nil && !c.json && isTerminal(os.Stdin) && isTerminal(os.Stdout)
	if !interactive {
		c.in = bufio.NewScanner(rt.stdinReader())
		c.in.Buffer(make([]byte, 0, 64*1024), 1<<20)
		return c, restore, nil
	}
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, nil, &AppError{Exit: ExitFail, Code: CodeInvalidValue, Message: "cannot put the terminal in raw mode: " + err.Error()}
	}
	restore = func() { _ = term.Restore(fd, state) }
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}, "> ")
	if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
		_ = t.SetSize(w, h)
	}
	if !o.noHistory {
		t.History = loadHistory(rt.defaultHistoryPath())
	}
	c.term = t
	return c, restore, nil
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// interactive is whether a person is typing.
func (c *console) interactive() bool { return c.term != nil }

// readLine reads one typed line. On a terminal, Ctrl-C and Ctrl-D at an
// empty line are io.EOF: the player is leaving.
func (c *console) readLine() (string, error) {
	if c.term != nil {
		return c.term.ReadLine()
	}
	if !c.in.Scan() {
		if err := c.in.Err(); err != nil {
			return "", err
		}
		return "", io.EOF
	}
	return c.in.Text(), nil
}

// write is the one path to stdout for human output.
func (c *console) write(s string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.term != nil {
		_, _ = c.term.Write([]byte(s))
		return
	}
	_, _ = io.WriteString(c.rt.stdout, s)
}

// event shows a frame: the rendered lines, or the envelope as JSON.
func (c *console) event(env *gamev1.EventEnvelope) {
	if c.json {
		b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(env)
		if err != nil {
			c.rt.log("error", "encode event: "+err.Error())
			return
		}
		c.mu.Lock()
		_, _ = c.rt.stdout.Write(append(b, '\n'))
		c.mu.Unlock()
		return
	}
	c.game(renderEvent(env)...)
}

// game is world-facing prose: what a Room says, what a refusal says.
func (c *console) game(lines ...string) {
	if len(lines) == 0 {
		return
	}
	if c.json {
		// A refusal the Submit path printed has no envelope; it is
		// prose, and prose goes to stderr under --output json.
		for _, l := range lines {
			c.rt.log("warn", l)
		}
		return
	}
	c.write(strings.Join(lines, "\n") + "\n")
}

// notice is about the connection: connected, lost, resumed. Human output
// marks it apart from the world's voice; JSON output logs it at level.
func (c *console) notice(level, s string) {
	if c.json {
		c.rt.log(level, s)
		return
	}
	c.write("-- " + s + "\n")
}

// system answers a /command.
func (c *console) system(s string) {
	if c.json {
		c.rt.log("info", s)
		return
	}
	c.write(s + "\n")
}

// proto is a protocol-visibility line. Under --output json it goes to
// stderr as is: the player asked for it, so no log level hides it.
func (c *console) proto(s string) {
	if c.json {
		c.mu.Lock()
		_, _ = io.WriteString(c.rt.stderr, s+"\n")
		c.mu.Unlock()
		return
	}
	c.write("  " + s + "\n")
}
