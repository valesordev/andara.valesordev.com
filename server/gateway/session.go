package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Session is one established connection to the Protocol, bound to at most
// one Character (glossary). It is deliberately not World state: losing it
// must not lose the Character (AW-SRV-015 owns what survives).
//
// A Session is bound to the transport connection it was opened on. When
// that connection closes for any reason, the Session is torn down (AC-7).
type Session struct {
	ID                string
	ClientName        string
	NegotiatedVersion uint32
	RemoteAddr        string
	Principal         Principal
	OpenedAt          time.Time

	connID uint64
	ctx    context.Context
	cancel context.CancelFunc
	span   trace.Span
	once   sync.Once
}

// Context is done when the Session ends, whichever way it ends. Anything
// working on the Session's behalf — a Subscribe stream, an in-flight
// Submit — derives from it so that teardown releases it.
func (s *Session) Context() context.Context { return s.ctx }

// SpanContext is the session.lifetime span, so that command.execute
// (AW-SRV-003) can link to it and one trace runs from keystroke to Event.
func (s *Session) SpanContext() trace.SpanContext { return s.span.SpanContext() }

// sessionStore owns every live Session on this process. It is the only
// thing that increments or decrements andara_sessions_active, so the gauge
// and the map cannot disagree.
type sessionStore struct {
	mu     sync.Mutex
	byID   map[string]*Session
	byConn map[uint64]map[string]*Session

	metrics *Metrics
	log     *slog.Logger
	tracer  trace.Tracer
}

func newSessionStore(m *Metrics, log *slog.Logger, tracer trace.Tracer) *sessionStore {
	return &sessionStore{
		byID:    map[string]*Session{},
		byConn:  map[uint64]map[string]*Session{},
		metrics: m,
		log:     log,
		tracer:  tracer,
	}
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// open establishes a Session. rpcCtx is the OpenSession call's context; the
// session.lifetime span is a new root linked to it, because a Session
// outlives the RPC that created it and a child span cannot outlive its
// parent honestly.
func (st *sessionStore) open(rpcCtx context.Context, connID uint64, clientName string, version uint32, remote string, p Principal) *Session {
	id := newSessionID()
	_, span := st.tracer.Start(context.Background(), "session.lifetime",
		trace.WithNewRoot(),
		trace.WithLinks(trace.LinkFromContext(rpcCtx)),
		trace.WithAttributes(
			attribute.String("session.id", id),
			attribute.String("client.name", clientName),
			attribute.Int64("protocol.negotiated_version", int64(version)),
		),
	)
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:                id,
		ClientName:        clientName,
		NegotiatedVersion: version,
		RemoteAddr:        remote,
		Principal:         p,
		OpenedAt:          time.Now(),
		connID:            connID,
		ctx:               ctx,
		cancel:            cancel,
		span:              span,
	}

	st.mu.Lock()
	st.byID[id] = s
	conns := st.byConn[connID]
	if conns == nil {
		conns = map[string]*Session{}
		st.byConn[connID] = conns
	}
	conns[id] = s
	st.mu.Unlock()

	st.metrics.SessionsActive.Inc()
	st.log.LogAttrs(rpcCtx, slog.LevelInfo, "session opened",
		slog.String("session_id", id),
		slog.String("client_name", clientName),
		slog.Uint64("negotiated_version", uint64(version)),
		slog.String("remote_addr", remote),
		slog.String("trace_id", traceID(rpcCtx)),
		slog.String("session_trace_id", span.SpanContext().TraceID().String()),
	)
	return s
}

func (st *sessionStore) get(id string) (*Session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, ok := st.byID[id]
	return s, ok
}

// close tears one Session down. Idempotent: the first caller's outcome and
// reason win, and a later caller — a connection close racing a CloseSession,
// say — is a no-op rather than a second decrement.
func (st *sessionStore) close(ctx context.Context, s *Session, outcome, reason string) {
	s.once.Do(func() {
		st.mu.Lock()
		delete(st.byID, s.ID)
		if conns := st.byConn[s.connID]; conns != nil {
			delete(conns, s.ID)
			if len(conns) == 0 {
				delete(st.byConn, s.connID)
			}
		}
		st.mu.Unlock()

		s.cancel()
		dur := time.Since(s.OpenedAt)
		st.metrics.SessionsActive.Dec()
		st.metrics.SessionsTotal.WithLabelValues(outcome).Inc()
		st.metrics.SessionDuration.Observe(dur.Seconds())

		s.span.SetAttributes(
			attribute.String("session.outcome", outcome),
			attribute.String("session.close_reason", reason),
		)
		if outcome == OutcomeDropped {
			s.span.SetStatus(codes.Error, reason)
		}
		s.span.End()

		st.log.LogAttrs(ctx, slog.LevelInfo, "session closed",
			slog.String("session_id", s.ID),
			slog.String("client_name", s.ClientName),
			slog.Uint64("negotiated_version", uint64(s.NegotiatedVersion)),
			slog.String("remote_addr", s.RemoteAddr),
			slog.String("outcome", outcome),
			slog.String("reason", reason),
			slog.Duration("duration", dur),
			slog.String("trace_id", traceIDOr(ctx, s.span.SpanContext())),
		)
	})
}

// closeConn tears down every Session opened on a connection that has gone
// away (AC-7).
func (st *sessionStore) closeConn(connID uint64) {
	st.mu.Lock()
	conns := st.byConn[connID]
	victims := make([]*Session, 0, len(conns))
	for _, s := range conns {
		victims = append(victims, s)
	}
	st.mu.Unlock()
	for _, s := range victims {
		st.close(context.Background(), s, OutcomeDropped, "connection dropped")
	}
}

// closeAll ends every Session with one reason. Drain uses it.
func (st *sessionStore) closeAll(outcome, reason string) {
	st.mu.Lock()
	victims := make([]*Session, 0, len(st.byID))
	for _, s := range st.byID {
		victims = append(victims, s)
	}
	st.mu.Unlock()
	for _, s := range victims {
		st.close(context.Background(), s, outcome, reason)
	}
}

func (st *sessionStore) count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.byID)
}

func traceID(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return ""
	}
	return sc.TraceID().String()
}

// traceIDOr prefers the RPC's trace and falls back to the Session's own, so
// a teardown with no RPC in flight (a dropped connection) still correlates.
func traceIDOr(ctx context.Context, fallback trace.SpanContext) string {
	if id := traceID(ctx); id != "" {
		return id
	}
	if fallback.HasTraceID() {
		return fallback.TraceID().String()
	}
	return ""
}
