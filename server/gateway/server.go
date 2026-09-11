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

	// Seams. Nil selects the stub for each.
	Verifier TokenVerifier
	Ingress  Ingress
	Egress   Egress

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
		opts.Verifier = StubVerifier{}
	}
	if opts.Ingress == nil {
		opts.Ingress = UnimplementedIngress{}
	}
	if opts.Egress == nil {
		opts.Egress = HoldingEgress{}
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
	s.sessions = newSessionStore(s.metrics, s.log, s.tracer)
	s.drainCtx, s.drainStop = context.WithCancel(context.Background())

	handlerOpts := []connect.HandlerOption{
		s.interceptors(),
		connect.WithReadMaxBytes(opts.MaxRecvBytes),
	}
	mux := http.NewServeMux()
	mux.Handle(gamev1connect.NewGameHandler(&gameService{s: s}, handlerOpts...))
	mux.Handle(adminv1connect.NewAdminHandler(&adminService{s: s}, handlerOpts...))
	// Server reflection, so `grpcurl ... list` works against the local
	// stack (ADR-0003: debugging is grpcurl, not nc). The schema is public
	// in this repository; exposing it costs nothing.
	reflector := grpcreflect.NewStaticReflector(gamev1connect.GameName, adminv1connect.AdminName)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	s.http = &http.Server{
		Handler: mux,
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

// connContext stamps a connection identity into every request context on
// that connection — HTTP/1.1 and HTTP/2 both derive request contexts from
// it — so OpenSession can bind the Session to the connection.
func (s *Server) connContext(ctx context.Context, c net.Conn) context.Context {
	s.conns.Lock()
	s.conns.next++
	id := s.conns.next
	s.conns.ids[c] = id
	s.conns.Unlock()
	s.sessions.connOpened(id)
	return context.WithValue(ctx, connIDKey{}, id)
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
