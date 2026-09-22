// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// Options configures a Server. Every field maps to a key in AW-SRV-005's
// configuration table except the seams and the telemetry handles.
type Options struct {
	Listen      string // grpc.listen
	TLSCertFile string // grpc.tls_cert_file — required
	TLSKeyFile  string // grpc.tls_key_file — required

	MaxRecvBytes      int           // grpc.max_recv_bytes
	MaxRequestTimeout time.Duration // grpc.max_request_timeout
	DrainTimeout      time.Duration // grpc.drain_timeout

	ProtocolMin uint32 // protocol.min_version
	ProtocolMax uint32 // protocol.max_version

	Build       BuildInfo
	Environment string

	// TrustInboundTraceparent makes a client's W3C traceparent the parent
	// of the RPC span — and its sampled flag the sampling decision. Off
	// (the default) the RPC span is a new root that only links to the
	// client's context, so telemetry.trace_sample_ratio applies whatever
	// the client sent: a client that flagged every request sampled would
	// otherwise hold the collector's cost lever (AW-SRV-010). On for local
	// development, where andara-cli's cli.command root is worth having.
	TrustInboundTraceparent bool // telemetry.trust_inbound_traceparent

	// Verifier is required: there is no stub that accepts any token, and
	// no code path that serves the Protocol without authentication.
	Verifier TokenVerifier
	// Auth serves andara.auth.v1.Auth on the same listener. Nil leaves the
	// service unmounted, which a test that needs only Game may want.
	Auth authv1connect.AuthHandler
	// Accounts is the account-administration half of Admin. Nil leaves
	// those methods UNIMPLEMENTED.
	Accounts AccountAdmin
	// Rechecker, with RecheckInterval, is the AC-12 loop: every open
	// Session's Principal is re-read on the interval and the Session closed
	// if its Account was disabled or its roles changed. Nil disables it.
	Rechecker       Rechecker
	RecheckInterval time.Duration

	// Seams. Nil selects the stub for each.
	Ingress Ingress
	Egress  Egress
	Roster  Roster

	// OnDrain is called once when Shutdown begins, before any connection is
	// closed, so readiness can flip to "not ready" while in-flight work
	// finishes. Optional.
	OnDrain func()

	Log      *slog.Logger
	Tracer   trace.Tracer
	Registry prometheus.Registerer
}

// Server serves Game and Admin on one TLS listener.
type Server struct {
	opts     Options
	log      *slog.Logger
	tracer   trace.Tracer
	metrics  *Metrics
	sessions *sessionStore

	http     *http.Server
	listener net.Listener
	serveErr chan error

	draining  atomic.Bool
	drainCtx  context.Context
	drainStop context.CancelFunc

	conns struct {
		sync.Mutex
		next uint64
		ids  map[net.Conn]uint64
		byID map[uint64]net.Conn
	}
}

// New validates options, loads the TLS material, and builds the handler.
// It does not listen; Start does. Missing TLS material is an error here —
// there is no code path that serves the Protocol without it.
func New(opts Options) (*Server, error) {
	if opts.TLSCertFile == "" || opts.TLSKeyFile == "" {
		return nil, errors.New("gateway: TLS certificate and key are required; there is no plaintext mode")
	}
	cert, err := tls.LoadX509KeyPair(opts.TLSCertFile, opts.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("gateway: load TLS key pair: %w", err)
	}
	if opts.ProtocolMin < 1 || opts.ProtocolMin > opts.ProtocolMax {
		return nil, fmt.Errorf("gateway: invalid protocol range %d..%d", opts.ProtocolMin, opts.ProtocolMax)
	}
	if opts.MaxRecvBytes <= 0 || opts.DrainTimeout <= 0 {
		return nil, errors.New("gateway: max_recv_bytes and drain_timeout must be positive")
	}
	if opts.Verifier == nil {
		return nil, errors.New("gateway: a TokenVerifier is required; there is no accept-anything mode")
	}
	if opts.Rechecker != nil && opts.RecheckInterval <= 0 {
		return nil, errors.New("gateway: recheck_interval must be positive when a Rechecker is set")
	}
	if opts.Ingress == nil {
		opts.Ingress = UnimplementedIngress{}
	}
	if opts.Egress == nil {
		opts.Egress = HoldingEgress{}
	}
	if opts.Roster == nil {
		opts.Roster = UnimplementedRoster{}
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Tracer == nil {
		opts.Tracer = noop.NewTracerProvider().Tracer("andara-server")
	}

	s := &Server{
		opts:     opts,
		log:      opts.Log,
		tracer:   opts.Tracer,
		metrics:  NewMetrics(opts.Registry),
		serveErr: make(chan error, 1),
	}
	s.conns.ids = map[net.Conn]uint64{}
	s.conns.byID = map[uint64]net.Conn{}
	s.sessions = newSessionStore(s.metrics, s.log, s.tracer)
	if ender, ok := opts.Egress.(SessionEnder); ok {
		s.sessions.ender = ender
	}
	s.sessions.roster = opts.Roster
	s.drainCtx, s.drainStop = context.WithCancel(context.Background())

	handlerOpts := []connect.HandlerOption{
		s.interceptors(),
		connect.WithReadMaxBytes(opts.MaxRecvBytes),
	}
	mux := http.NewServeMux()
	mux.Handle(gamev1connect.NewGameHandler(&gameService{s: s}, handlerOpts...))
	mux.Handle(adminv1connect.NewAdminHandler(&adminService{s: s}, handlerOpts...))
	if opts.Auth != nil {
		mux.Handle(authv1connect.NewAuthHandler(opts.Auth, handlerOpts...))
	}
	// Server reflection, so `grpcurl ... list` works against the local
	// stack (ADR-0003: debugging is grpcurl, not nc). The schema is public
	// in this repository; exposing it costs nothing.
	reflector := grpcreflect.NewStaticReflector(gamev1connect.GameName, adminv1connect.AdminName, authv1connect.AuthName)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	s.http = &http.Server{
		Handler: withResponseController(mux),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
			NextProtos:   []string{"h2", "http/1.1"},
		},
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: a Subscribe stream writes for the life of the
		// Session. Idle is bounded so a silent client cannot hold a
		// connection forever without a Session on it.
		IdleTimeout:    5 * time.Minute,
		MaxHeaderBytes: 1 << 16,
		ConnContext:    s.connContext,
		ConnState:      s.connState,
	}
	return s, nil
}

type connIDKey struct{}

type responseControllerKey struct{}

// withResponseController puts the request's http.ResponseController where
// a streaming seam can reach it through ctx (AbortStream). Connect writes
// through the ResponseWriter the mux hands it, so this is the same one;
// nothing is wrapped, so Connect's own flushing sees the real writer.
func withResponseController(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), responseControllerKey{}, http.NewResponseController(w))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AbortStream resets the stream ctx belongs to, from any goroutine: a Send
// blocked on the client's flow-control window fails at once, and so does
// every Send after. The connection and the other streams on it are left
// alone — this is how a Subscribe whose client has stopped consuming is
// ended without waiting for it (AW-SRV-011 AC-4). Reports whether ctx
// named a stream.
//
// Mechanism: a write deadline in the past. On HTTP/2 that is RST_STREAM
// to the client; on HTTP/1.1 the connection's write fails and the server
// closes it after the handler returns.
//
// It cannot help when the client has stopped reading its socket: then
// the connection's writer is blocked in the kernel with every stream's
// frames behind it, the reset among them, and only DropConnection ends
// the Send.
func AbortStream(ctx context.Context) bool {
	rc, ok := ctx.Value(responseControllerKey{}).(*http.ResponseController)
	if !ok {
		return false
	}
	return rc.SetWriteDeadline(time.Now().Add(-time.Second)) == nil
}

// DropConnection closes the connection ctx's request arrived on, from any
// goroutine. Every stream on it fails, and every Session on it is torn
// down as if the client had dropped (AC-7). The escalation for a client
// that has stopped reading its socket, where AbortStream cannot reach.
// Reports whether ctx named an open connection.
func DropConnection(ctx context.Context) bool {
	closer, ok := ctx.Value(connCloserKey{}).(func(uint64) bool)
	if !ok {
		return false
	}
	return closer(connIDFrom(ctx))
}

// connContext stamps a connection identity into every request context on
// that connection — HTTP/1.1 and HTTP/2 both derive request contexts from
// it — so OpenSession can bind the Session to the connection.
func (s *Server) connContext(ctx context.Context, c net.Conn) context.Context {
	s.conns.Lock()
	s.conns.next++
	id := s.conns.next
	s.conns.ids[c] = id
	s.conns.byID[id] = c
	s.conns.Unlock()
	s.sessions.connOpened(id)
	ctx = context.WithValue(ctx, connIDKey{}, id)
	return context.WithValue(ctx, connCloserKey{}, s.closeConn)
}

type connCloserKey struct{}

// closeConn closes connection id, if it is still open. Its serve loop
// ends, connState observes the close, and every Session on it is torn
// down (AC-7) — the same path a client dropping takes.
func (s *Server) closeConn(id uint64) bool {
	s.conns.Lock()
	c, ok := s.conns.byID[id]
	s.conns.Unlock()
	if !ok {
		return false
	}
	// A TLS close on a wedged socket may not deliver its close_notify;
	// the connection is closed either way.
	_ = c.Close()
	return true
}

// connState tears down every Session on a connection when it closes
// (AC-7). It is the only place a dropped connection is observed. It fires
// when the connection's serve loop returns, which can be before a handler
// on that connection has — the store refuses an open on a closed
// connection for exactly that reason.
func (s *Server) connState(c net.Conn, st http.ConnState) {
	if st != http.StateClosed && st != http.StateHijacked {
		return
	}
	s.conns.Lock()
	id, ok := s.conns.ids[c]
	delete(s.conns.ids, c)
	delete(s.conns.byID, id)
	s.conns.Unlock()
	if ok {
		s.sessions.connClosed(id)
	}
}

func connIDFrom(ctx context.Context) uint64 {
	id, _ := ctx.Value(connIDKey{}).(uint64)
	return id
}

// Start binds the listener and serves in the background. It returns once
// the address is bound, so Addr is valid afterwards.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		return fmt.Errorf("gateway: listen %s: %w", s.opts.Listen, err)
	}
	s.listener = ln
	s.log.Info("grpc listen", "addr", ln.Addr().String(),
		"protocol_min_version", s.opts.ProtocolMin, "protocol_max_version", s.opts.ProtocolMax)
	go func() {
		// Certificates are already in TLSConfig; the empty paths tell
		// ServeTLS not to load any.
		err := s.http.ServeTLS(ln, "", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		s.serveErr <- err
	}()
	if s.opts.Rechecker != nil {
		go s.sessions.recheckLoop(s.drainCtx, s.opts.RecheckInterval, s.opts.Rechecker)
	}
	return nil
}

// Addr is the bound address, valid after Start.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Wait blocks until serving stops and returns the reason, nil for a
// Shutdown.
func (s *Server) Wait() error {
	return <-s.serveErr
}

// Draining reports whether Shutdown has begun.
func (s *Server) Draining() bool { return s.draining.Load() }

// Shutdown drains (AC-8): new RPCs are refused with UNAVAILABLE, every
// Session is closed with reason "draining" and every stream on one ends
// with UNAVAILABLE, in-flight unary RPCs finish, and then the listener and
// connections are closed. If in-flight work outlasts grpc.drain_timeout the
// remaining connections are closed forcibly and the error says so.
func (s *Server) Shutdown(ctx context.Context) error {
	if !s.draining.CompareAndSwap(false, true) {
		return nil
	}
	if s.opts.OnDrain != nil {
		s.opts.OnDrain()
	}
	s.log.Info("grpc drain begin", "sessions", s.sessions.count(), "drain_timeout", s.opts.DrainTimeout.String())
	s.drainStop()
	s.sessions.closeAll(OutcomeClosed, "server draining")

	dctx, cancel := context.WithTimeout(ctx, s.opts.DrainTimeout)
	defer cancel()
	err := s.http.Shutdown(dctx)
	if err != nil {
		s.log.Warn("grpc drain timeout; closing remaining connections", "detail", err.Error())
		_ = s.http.Close()
		return fmt.Errorf("gateway: drain exceeded %s: %w", s.opts.DrainTimeout, err)
	}
	s.log.Info("grpc drain complete")
	return nil
}

// SessionCount is the number of established Sessions, for tests and
// readiness reporting; the metric is the operational view.
func (s *Server) SessionCount() int { return s.sessions.count() }
