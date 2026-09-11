package gateway

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/trace/noop"
)

func newTestStore() *sessionStore {
	return newSessionStore(NewMetrics(nil), slog.New(slog.DiscardHandler), noop.NewTracerProvider().Tracer("t"))
}

// AC-7's race: the connection closes while OpenSession is still running.
// ConnState fires when the serve loop returns, not when handlers do, so an
// open that lands after the close must be refused — otherwise the Session
// is bound to a dead connection and never torn down.
func TestSessionStore_RefusesOpenOnClosedConn(t *testing.T) {
	st := newTestStore()
	st.connOpened(7)
	st.connClosed(7)

	_, err := st.open(context.Background(), 7, "late/0", 1, "1.2.3.4:5", Principal{Subject: "stub"})
	if !errors.Is(err, errConnGone) {
		t.Fatalf("open on a closed connection: err = %v, want errConnGone", err)
	}
	if got := testutil.ToFloat64(st.metrics.SessionsActive); got != 0 {
		t.Errorf("sessions_active = %v after a refused open", got)
	}
	if st.count() != 0 {
		t.Errorf("store holds %d sessions after a refused open", st.count())
	}

	// A connection never announced is not live either.
	if _, err := st.open(context.Background(), 99, "unknown/0", 1, "", Principal{}); !errors.Is(err, errConnGone) {
		t.Errorf("open on an unannounced connection: %v", err)
	}
}

func TestSessionStore_OpenThenConnClosedDrops(t *testing.T) {
	st := newTestStore()
	st.connOpened(1)
	a, err := st.open(context.Background(), 1, "a/0", 1, "", Principal{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.open(context.Background(), 1, "b/0", 1, "", Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(st.metrics.SessionsActive); got != 2 {
		t.Fatalf("sessions_active = %v", got)
	}

	st.connClosed(1)
	for _, s := range []*Session{a, b} {
		if s.Context().Err() == nil {
			t.Errorf("session %s context still live after its connection closed", s.ID)
		}
	}
	if got := testutil.ToFloat64(st.metrics.SessionsActive); got != 0 {
		t.Errorf("sessions_active = %v", got)
	}
	if got := testutil.ToFloat64(st.metrics.SessionsTotal.WithLabelValues(OutcomeDropped)); got != 2 {
		t.Errorf("sessions_total{dropped} = %v", got)
	}

	// Closing twice — a CloseSession racing the drop — is one decrement.
	st.close(context.Background(), a, OutcomeClosed, "again")
	if got := testutil.ToFloat64(st.metrics.SessionsActive); got != 0 {
		t.Errorf("sessions_active = %v after a double close", got)
	}
	if got := testutil.ToFloat64(st.metrics.SessionsTotal.WithLabelValues(OutcomeClosed)); got != 0 {
		t.Errorf("a second close was counted: sessions_total{closed} = %v", got)
	}
}
