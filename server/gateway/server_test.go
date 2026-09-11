package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// AC-3, AC-4, AC-5: one server, one service definition, four clients — the
// Connect protocol, gRPC and gRPC-Web through Connect's client, and a
// google.golang.org/grpc client, which is what grpcurl and every non-Connect
// stack put on the wire. Every one handshakes against the test CA with no
// insecure option and comes back with a SessionID and the negotiated version.
func TestOpenSession_AllProtocolsOneHandler(t *testing.T) {
	h := start(t, nil)

	clients := map[string]func() *gamev1.OpenSessionResponse{
		"connect":  func() *gamev1.OpenSessionResponse { return h.open(t, h.game()) },
		"grpc":     func() *gamev1.OpenSessionResponse { return h.open(t, h.game(connect.WithGRPC())) },
		"grpc-web": func() *gamev1.OpenSessionResponse { return h.open(t, h.game(connect.WithGRPCWeb())) },
		"grpc-go": func() *gamev1.OpenSessionResponse {
			conn, err := grpc.NewClient(h.srv.Addr().String(),
				grpc.WithTransportCredentials(credentials.NewTLS(h.pki.ClientTLS())))
			if err != nil {
				t.Fatal(err)
			}
			// Closing the connection would drop the Session (AC-7), and
			// this test counts established ones.
			t.Cleanup(func() { conn.Close() })
			var resp gamev1.OpenSessionResponse
			err = conn.Invoke(context.Background(), "/andara.game.v1.Game/OpenSession",
				&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: "tok", ClientName: "grpc-go/test"}, &resp)
			if err != nil {
				t.Fatalf("grpc-go OpenSession: %v", err)
			}
			return &resp
		},
	}
	ids := map[string]bool{}
	for name, call := range clients {
		resp := call()
		if resp.GetSessionId() == "" || ids[resp.GetSessionId()] {
			t.Errorf("%s: session_id %q missing or reused", name, resp.GetSessionId())
		}
		ids[resp.GetSessionId()] = true
		if resp.GetNegotiatedVersion() != 1 || resp.GetServerMinVersion() != 1 || resp.GetServerMaxVersion() != 1 {
			t.Errorf("%s: negotiated=%d range=%d..%d", name, resp.GetNegotiatedVersion(), resp.GetServerMinVersion(), resp.GetServerMaxVersion())
		}
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsActive); got != 4 {
		t.Errorf("andara_sessions_active = %v, want 4", got)
	}
	if got := testutil.ToFloat64(h.srv.metrics.RequestsTotal.WithLabelValues("andara.game.v1.Game/OpenSession", "ok")); got != 4 {
		t.Errorf("andara_grpc_requests_total{OpenSession,ok} = %v, want 4", got)
	}
}

// AC-3's negative: without the CA there is no handshake, and there is no
// option on the server that would make one. A plaintext client gets nothing.
func TestTLS_NoInsecurePath(t *testing.T) {
	h := start(t, nil)

	untrusted := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
	_, err := untrusted.Post(h.baseURL()+"/andara.game.v1.Game/OpenSession", "application/proto", nil)
	var unknown *tls.CertificateVerificationError
	if err == nil || !errors.As(err, &unknown) {
		t.Fatalf("untrusted client: err = %v, want certificate verification failure", err)
	}

	plain := &http.Client{Timeout: 2 * time.Second}
	resp, err := plain.Post("http://"+h.srv.Addr().String()+"/andara.game.v1.Game/OpenSession", "application/proto", nil)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("plaintext request was served")
		}
	}
}

// AC-6: a version outside the range is FAILED_PRECONDITION naming the
// client's version and the server's range, in the message and in a
// PreconditionFailure detail; no Session exists afterwards; the rejection
// is counted and logged at warn with both ranges.
func TestOpenSession_VersionOutsideRange(t *testing.T) {
	h := start(t, func(o *Options) { o.ProtocolMin = 2; o.ProtocolMax = 3 })
	client := h.game()

	for _, v := range []uint32{0, 1, 4, 99} {
		_, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
			ProtocolVersion: v, AuthToken: "tok", ClientName: "old-client/0",
		}))
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition {
			t.Fatalf("version %d: err = %v, want FAILED_PRECONDITION", v, err)
		}
		msg := ce.Message()
		for _, want := range []string{"2..3", itoa(v)} {
			if !strings.Contains(msg, want) {
				t.Errorf("version %d: message %q does not name %q", v, msg, want)
			}
		}
		var found bool
		for _, d := range ce.Details() {
			m, err := d.Value()
			if err != nil {
				continue
			}
			if pf, ok := m.(*errdetails.PreconditionFailure); ok && len(pf.Violations) == 1 {
				found = pf.Violations[0].Type == "PROTOCOL_VERSION" &&
					strings.Contains(pf.Violations[0].Subject, itoa(v)) &&
					strings.Contains(pf.Violations[0].Description, "2..3")
			}
		}
		if !found {
			t.Errorf("version %d: no PreconditionFailure detail naming both", v)
		}
	}
	if got := h.srv.SessionCount(); got != 0 {
		t.Errorf("sessions after rejections = %d", got)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedVersion)); got != 4 {
		t.Errorf("sessions_total{rejected_version} = %v", got)
	}
	if got := testutil.ToFloat64(h.srv.metrics.RequestsTotal.WithLabelValues("andara.game.v1.Game/OpenSession", "failed_precondition")); got != 4 {
		t.Errorf("grpc_requests_total{failed_precondition} = %v", got)
	}
	line := findLog(t, h.logs, "protocol version outside supported range")
	if line["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", line["level"])
	}
	if line["server_min_version"] != 2.0 || line["server_max_version"] != 3.0 {
		t.Errorf("log line lacks both ranges: %v", line)
	}
	if _, ok := line["client_version"]; !ok {
		t.Errorf("log line lacks client_version: %v", line)
	}

	// The gRPC wire carries the same code and detail to a non-Connect client.
	conn, err := grpc.NewClient(h.srv.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(h.pki.ClientTLS())))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	err = conn.Invoke(context.Background(), "/andara.game.v1.Game/OpenSession",
		&gamev1.OpenSessionRequest{ProtocolVersion: 99, AuthToken: "tok"}, &gamev1.OpenSessionResponse{})
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("grpc-go: %v", err)
	}
	if len(st.Details()) != 1 {
		t.Errorf("grpc-go: details = %v", st.Details())
	}
}

// The token is verified and then forgotten: an empty one is UNAUTHENTICATED
// and counted; a real one appears in no log line at any level.
func TestOpenSession_Token(t *testing.T) {
	h := start(t, nil)
	client := h.game()

	_, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("empty token: %v", err)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeRejectedAuth)); got != 1 {
		t.Errorf("sessions_total{rejected_auth} = %v", got)
	}

	const secret = "s3cr3t-token-never-logged-8f1c"
	resp, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: secret, ClientName: "andara-cli/0.1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: resp.Msg.SessionId})); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.logs.String(), secret) {
		t.Fatal("auth token appeared in a log line")
	}
	for _, s := range h.rec.Ended() {
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.String(), secret) {
				t.Fatalf("auth token appeared in span %s attribute %s", s.Name(), a.Key)
			}
		}
	}
}

// Session open and close lines carry the required fields; the trace of the
// OpenSession RPC is the CLI's when it propagates one.
func TestSession_LogsAndTraces(t *testing.T) {
	h := start(t, nil)
	client := h.game()

	// A client-side root span, propagated as W3C traceparent, becomes the
	// parent of the server's RPC span (AW-CLI-001's cli.command).
	clientTracer := h.tp.Tracer("andara-cli")
	ctx, cliSpan := clientTracer.Start(context.Background(), "cli.command")
	req := connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: "tok", ClientName: "andara-cli/0.1"})
	propagator.Inject(ctx, propagationCarrier(req.Header()))
	resp, err := client.OpenSession(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	cliSpan.End()
	sid := resp.Msg.SessionId

	opened := findLog(t, h.logs, "session opened")
	for _, key := range []string{"ts", "level", "msg", "service", "env", "session_id", "trace_id", "client_name", "negotiated_version", "remote_addr"} {
		if _, ok := opened[key]; !ok {
			t.Errorf("session opened line lacks %q: %v", key, opened)
		}
	}
	if opened["level"] != "INFO" || opened["session_id"] != sid || opened["client_name"] != "andara-cli/0.1" {
		t.Errorf("session opened = %v", opened)
	}
	if opened["trace_id"] != cliSpan.SpanContext().TraceID().String() {
		t.Errorf("trace_id = %v, want the CLI's %s", opened["trace_id"], cliSpan.SpanContext().TraceID())
	}

	rpc := spanByName(t, h.rec, "andara.game.v1.Game/OpenSession")
	if rpc.SpanKind() != trace.SpanKindServer {
		t.Errorf("rpc span kind = %v", rpc.SpanKind())
	}
	if rpc.Parent().SpanID() != cliSpan.SpanContext().SpanID() {
		t.Errorf("rpc span parent = %s, want cli.command %s", rpc.Parent().SpanID(), cliSpan.SpanContext().SpanID())
	}
	if strAttr(rpc, "rpc.service") != "andara.game.v1.Game" || strAttr(rpc, "rpc.method") != "OpenSession" {
		t.Errorf("rpc attributes = %v", rpc.Attributes())
	}

	if _, err := client.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sid})); err != nil {
		t.Fatal(err)
	}
	closed := findLog(t, h.logs, "session closed")
	if closed["session_id"] != sid || closed["outcome"] != OutcomeClosed {
		t.Errorf("session closed = %v", closed)
	}
	for _, key := range []string{"ts", "level", "msg", "service", "env", "session_id", "trace_id"} {
		if _, ok := closed[key]; !ok {
			t.Errorf("session closed line lacks %q", key)
		}
	}

	life := spanByName(t, h.rec, "session.lifetime")
	if life.Parent().IsValid() {
		t.Errorf("session.lifetime has a parent %v; want a root", life.Parent())
	}
	if len(life.Links()) != 1 || life.Links()[0].SpanContext.SpanID() != rpc.SpanContext().SpanID() {
		t.Errorf("session.lifetime links = %v, want a link to the OpenSession span", life.Links())
	}
	if strAttr(life, "session.id") != sid || strAttr(life, "session.outcome") != OutcomeClosed {
		t.Errorf("session.lifetime attributes = %v", life.Attributes())
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeClosed)); got != 1 {
		t.Errorf("sessions_total{closed} = %v", got)
	}
	if got := testutil.CollectAndCount(h.srv.metrics.SessionDuration); got != 1 {
		t.Errorf("session_duration_seconds series = %d", got)
	}
}

// AC-10 on the wire: Admin without a bearer token is refused with
// UNAUTHENTICATED; with one, GetServerInfo reports the build and the range.
func TestAdmin_RequiresToken(t *testing.T) {
	h := start(t, nil)
	c, _ := h.httpClient()
	admin := adminv1connect.NewAdminClient(c, h.baseURL())

	_, err := admin.GetServerInfo(context.Background(), connect.NewRequest(&adminv1.GetServerInfoRequest{}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no token: %v", err)
	}
	if got := testutil.ToFloat64(h.srv.metrics.RequestsTotal.WithLabelValues("andara.admin.v1.Admin/GetServerInfo", "unauthenticated")); got != 1 {
		t.Errorf("grpc_requests_total{GetServerInfo,unauthenticated} = %v", got)
	}

	req := connect.NewRequest(&adminv1.GetServerInfoRequest{})
	req.Header().Set("Authorization", "Bearer operator-token")
	resp, err := admin.GetServerInfo(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Version != "test" || resp.Msg.Commit != "abc123" || resp.Msg.Environment != "test" {
		t.Errorf("server info = %v", resp.Msg)
	}
	if resp.Msg.ProtocolMinVersion != 1 || resp.Msg.ProtocolMaxVersion != 1 {
		t.Errorf("protocol range = %d..%d", resp.Msg.ProtocolMinVersion, resp.Msg.ProtocolMaxVersion)
	}
}

// Message size: over grpc.max_recv_bytes is RESOURCE_EXHAUSTED naming the
// limit, and the handler never runs.
func TestMaxRecvBytes(t *testing.T) {
	h := start(t, func(o *Options) { o.MaxRecvBytes = 1024 })
	client := h.game()
	sess := h.open(t, client)

	_, err := client.Submit(context.Background(), connect.NewRequest(&gamev1.SubmitRequest{
		SessionId: sess.SessionId, Raw: strings.Repeat("x", 4096),
	}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeResourceExhausted {
		t.Fatalf("oversized Submit: %v", err)
	}
	if !strings.Contains(ce.Message(), "1024") {
		t.Errorf("message %q does not name the limit", ce.Message())
	}
	// Under the limit, the seam is reached: UNIMPLEMENTED until AW-SRV-010.
	_, err = client.Submit(context.Background(), connect.NewRequest(&gamev1.SubmitRequest{SessionId: sess.SessionId, Raw: "look"}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("Submit seam: %v", err)
	}
}

// An unknown Session ID is an invalid credential.
func TestSessionID_Unknown(t *testing.T) {
	h := start(t, nil)
	client := h.game()
	for name, call := range map[string]func() error{
		"Submit": func() error {
			_, err := client.Submit(context.Background(), connect.NewRequest(&gamev1.SubmitRequest{SessionId: "nope", Raw: "look"}))
			return err
		},
		"CloseSession": func() error {
			_, err := client.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: "nope"}))
			return err
		},
		"Subscribe": func() error {
			st, err := client.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: "nope"}))
			if err != nil {
				return err
			}
			for st.Receive() {
			}
			return st.Err()
		},
	} {
		if err := call(); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s with unknown session: %v", name, err)
		}
	}
}

// signallingEgress is a Subscribe seam that tells the test when the stream
// is established and then behaves like the stub: holds until ctx is done.
type signallingEgress struct{ entered chan struct{} }

func (e *signallingEgress) Subscribe(ctx context.Context, _ *Session, _ *gamev1.SubscribeRequest, _ *connect.ServerStream[gamev1.EventEnvelope]) error {
	e.entered <- struct{}{}
	<-ctx.Done()
	return nil
}

// AC-8: Shutdown stops accepting, refuses new RPCs with UNAVAILABLE, ends an
// in-flight stream with UNAVAILABLE and a reason, tears down every Session,
// and returns within the drain timeout.
func TestShutdown_DrainsInFlightStream(t *testing.T) {
	eg := &signallingEgress{entered: make(chan struct{}, 1)}
	drained := make(chan struct{})
	h := start(t, func(o *Options) {
		o.Egress = eg
		o.DrainTimeout = 3 * time.Second
		o.OnDrain = func() { close(drained) }
	})
	client := h.game()
	sess := h.open(t, client)

	stream, err := client.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: sess.SessionId}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-eg.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stream never reached the egress seam")
	}

	start := time.Now()
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Shutdown took %s, over the drain timeout", took)
	}
	select {
	case <-drained:
	default:
		t.Error("OnDrain was not called")
	}

	for stream.Receive() {
	}
	var ce *connect.Error
	if !errors.As(stream.Err(), &ce) || ce.Code() != connect.CodeUnavailable {
		t.Fatalf("stream ended with %v, want UNAVAILABLE", stream.Err())
	}
	if !strings.Contains(ce.Message(), "draining") {
		t.Errorf("stream reason %q does not say draining", ce.Message())
	}
	if got := h.srv.SessionCount(); got != 0 {
		t.Errorf("sessions after drain = %d", got)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsActive); got != 0 {
		t.Errorf("sessions_active after drain = %v", got)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeClosed)); got != 1 {
		t.Errorf("sessions_total{closed} = %v", got)
	}
	if !h.srv.Draining() {
		t.Error("Draining() false after Shutdown")
	}

	// Stopped accepting: a fresh connection is refused.
	d := net.Dialer{Timeout: time.Second}
	if c, err := d.Dial("tcp", h.srv.Addr().String()); err == nil {
		c.Close()
		t.Error("listener still accepting after Shutdown")
	}
	if err := h.srv.Wait(); err != nil {
		t.Errorf("Wait after Shutdown = %v", err)
	}
}

// The drain interceptor refuses new RPCs during a drain that is still
// waiting on in-flight work, with the retryable code.
func TestShutdown_RefusesNewRPCsWhileDraining(t *testing.T) {
	// A unary Ingress that blocks until released stands in for in-flight
	// work, so the drain window is observable.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := start(t, func(o *Options) {
		o.Ingress = blockingIngress{entered: entered, release: release}
		o.DrainTimeout = 5 * time.Second
	})
	client := h.game()
	sess := h.open(t, client)

	submitErr := make(chan error, 1)
	go func() {
		_, err := client.Submit(context.Background(), connect.NewRequest(&gamev1.SubmitRequest{SessionId: sess.SessionId, Raw: "look"}))
		submitErr <- err
	}()
	<-entered

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- h.srv.Shutdown(context.Background()) }()
	waitFor(t, 2*time.Second, h.srv.Draining, "draining flag")

	// The in-flight connection is still open, so a new RPC on it reaches
	// the interceptor and is told to go elsewhere.
	_, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: "tok"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("new RPC during drain: %v, want UNAVAILABLE", err)
	}
	close(release)

	// The in-flight Submit was allowed to finish, and its Session was closed
	// under it, which is the typed reason it reports.
	if err := <-submitErr; connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("in-flight Submit ended with %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

type blockingIngress struct {
	entered chan struct{}
	release chan struct{}
}

func (b blockingIngress) Submit(ctx context.Context, _ *Session, _ *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return &gamev1.SubmitResponse{AcceptedOffset: 1}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CloseSession while a stream is open ends the stream with a typed reason.
func TestCloseSession_EndsStream(t *testing.T) {
	eg := &signallingEgress{entered: make(chan struct{}, 1)}
	h := start(t, func(o *Options) { o.Egress = eg })
	client := h.game()
	sess := h.open(t, client)

	stream, err := client.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: sess.SessionId}))
	if err != nil {
		t.Fatal(err)
	}
	<-eg.entered
	if _, err := client.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sess.SessionId})); err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	var ce *connect.Error
	if !errors.As(stream.Err(), &ce) || ce.Code() != connect.CodeCanceled || !strings.Contains(ce.Message(), "session closed") {
		t.Fatalf("stream ended with %v", stream.Err())
	}
	// A second CloseSession is an unknown Session, not a double decrement.
	_, err = client.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sess.SessionId}))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("second close: %v", err)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsActive); got != 0 {
		t.Errorf("sessions_active = %v", got)
	}
}

// AC-7: a thousand connections each open a Session and drop. Every Session
// is torn down, the gauge returns to zero, and neither goroutines nor heap
// grow with the count.
func TestConnectionDrop_TearsDownSessions(t *testing.T) {
	const n = 1000
	// A recording tracer and a log buffer retain every span and line, and
	// the heap bound below would be measuring them rather than the server.
	h := start(t, func(o *Options) {
		o.Tracer = noop.NewTracerProvider().Tracer("andara-server")
		o.Log = slog.New(slog.DiscardHandler)
	})

	// Warm up so the baseline includes whatever the first connection
	// allocates once (TLS session caches, the h2 framer pools).
	dropOne(t, h)
	waitFor(t, 5*time.Second, func() bool { return h.srv.SessionCount() == 0 }, "warm-up teardown")
	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	// Ten clients at a time: a thousand serial handshakes is slow under the
	// race detector, and concurrent teardown is the case that matters.
	const workers = 10
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < n/workers; i++ {
				dropOne(t, h)
			}
		}()
	}
	wg.Wait()
	waitFor(t, 30*time.Second, func() bool { return h.srv.SessionCount() == 0 }, "all sessions torn down")

	if got := testutil.ToFloat64(h.srv.metrics.SessionsActive); got != 0 {
		t.Errorf("sessions_active = %v after %d drops", got, n)
	}
	if got := testutil.ToFloat64(h.srv.metrics.SessionsTotal.WithLabelValues(OutcomeDropped)); got != n+1 {
		t.Errorf("sessions_total{dropped} = %v, want %d", got, n+1)
	}
	h.srv.conns.Lock()
	tracked := len(h.srv.conns.ids)
	h.srv.conns.Unlock()
	if tracked != 0 {
		t.Errorf("%d connections still tracked", tracked)
	}

	// Goroutines: the server's per-connection goroutines exit when the
	// connection closes; allow a small slack for ones mid-exit.
	waitFor(t, 10*time.Second, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= baseGoroutines+10
	}, "goroutines to return to baseline")

	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	const bound = 8 << 20
	delta := int64(after.HeapAlloc) - int64(base.HeapAlloc)
	t.Logf("heap delta %d bytes, goroutines %d → %d, over %d connect-and-drop cycles", delta, baseGoroutines, runtime.NumGoroutine(), n)
	if delta > bound {
		t.Errorf("heap grew by %d bytes over %d connect-and-drop cycles (bound %d)", delta, n, bound)
	}
}

// dropOne opens a Session on a fresh connection and closes the connection
// without CloseSession.
func dropOne(t *testing.T, h *harness) {
	t.Helper()
	c, tr := h.httpClient()
	client := gamev1connectClient(c, h)
	_, err := client.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "tok", ClientName: "drop-test/0",
	}))
	if err != nil {
		t.Errorf("OpenSession: %v", err) // not Fatal: called from worker goroutines
		return
	}
	tr.CloseIdleConnections()
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

func gamev1connectClient(c *http.Client, h *harness) gamev1connect.GameClient {
	return gamev1connect.NewGameClient(c, h.baseURL())
}

func propagationCarrier(hdr http.Header) propagation.TextMapCarrier {
	return propagation.HeaderCarrier(hdr)
}
