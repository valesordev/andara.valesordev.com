// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package egress_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/testpki"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/egress"
	"github.com/valesordev/andara/server/events"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/simtest"
	"github.com/valesordev/andara/server/tickloop"

	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
)

// stack is a real gateway over a real Hub and Egress: what a client sees.
type stack struct {
	t   *testing.T
	pki *testpki.PKI
	srv *gateway.Server
	hub *events.Hub
	eg  *egress.Egress
	reg *prometheus.Registry
	log *logBuffer

	mu   sync.Mutex
	obs  map[string]events.Observer // by session ID
	tick uint64
	id   uint64
}

type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// verifier: any non-empty token is a player; "gm" is a Game Master.
type verifier struct{}

func (verifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	switch token {
	case "":
		return auth.Principal{}, gateway.ErrUnauthenticated
	case "gm":
		return auth.Principal{AccountID: "gm", Roles: []auth.Role{auth.RolePlayer, auth.RoleGameMaster}}, nil
	}
	return auth.Principal{AccountID: token, Roles: []auth.Role{auth.RolePlayer}}, nil
}

func (verifier) ActAs(context.Context, auth.Principal, string) (auth.Principal, error) {
	return auth.Principal{}, gateway.ErrPermissionDenied
}

func newStack(t *testing.T, buffer, window int, hubBuffer int, mutate func(*egress.Options)) *stack {
	t.Helper()
	s := &stack{t: t, pki: testpki.New(t), reg: prometheus.NewRegistry(), log: &logBuffer{}, obs: map[string]events.Observer{}}
	logger := slog.New(slog.NewJSONHandler(s.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s.hub = events.New(events.Options{Buffer: hubBuffer, Registry: s.reg, Log: logger})
	t.Cleanup(s.hub.Close)
	opts := egress.Options{
		Hub: s.hub, Buffer: buffer, ResumeWindow: window, HeartbeatInterval: time.Hour,
		Observers: egress.ObserverFunc(func(id string) (events.Observer, bool) {
			s.mu.Lock()
			defer s.mu.Unlock()
			o, ok := s.obs[id]
			return o, ok
		}),
		Log: logger, Registry: s.reg,
	}
	if mutate != nil {
		mutate(&opts)
	}
	s.eg = egress.New(opts)
	srv, err := gateway.New(gateway.Options{
		Verifier: verifier{}, Listen: "127.0.0.1:0",
		TLSCertFile: s.pki.CertFile, TLSKeyFile: s.pki.KeyFile,
		MaxRecvBytes: 1 << 20, MaxRequestTimeout: 30 * time.Second, DrainTimeout: 5 * time.Second,
		ProtocolMin: 1, ProtocolMax: 1, Environment: "test",
		Egress:  s.eg,
		OnDrain: s.eg.Drain,
		Log:     logger, Registry: s.reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	s.srv = srv
	return s
}

func (s *stack) url() string { return "https://" + s.srv.Addr().String() }

// client is a Game client on its own connection; dial, when set, wraps
// the TCP connection.
func (s *stack) client(dial func(net.Conn) net.Conn) gamev1connect.GameClient {
	tr := &http.Transport{TLSClientConfig: s.pki.ClientTLS(), ForceAttemptHTTP2: true}
	if dial != nil {
		tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			cfg := s.pki.ClientTLS().Clone()
			cfg.NextProtos = []string{"h2"}
			cfg.ServerName = "localhost"
			tc := tls.Client(dial(raw), cfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				return nil, err
			}
			return tc, nil
		}
	}
	// No gzip: the stall tests measure bytes on the wire, and padding
	// that compressed to nothing would fill no window.
	return gamev1connect.NewGameClient(&http.Client{Transport: tr}, s.url(), connect.WithAcceptCompression("gzip", nil, nil))
}

// open establishes a Session and places its Character.
func (s *stack) open(c gamev1connect.GameClient, token string, entity string, zone, room string) string {
	s.t.Helper()
	resp, err := c.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: token, ClientName: "egress-test/0"}))
	if err != nil {
		s.t.Fatalf("OpenSession: %v", err)
	}
	if entity != "" {
		s.mu.Lock()
		s.obs[resp.Msg.SessionId] = events.Observer{Entity: sim.EntityID(entity), Room: sim.RoomRef{Zone: sim.ZoneID(zone), Room: sim.RoomID(room)}}
		s.mu.Unlock()
	}
	return resp.Msg.SessionId
}

// emit publishes one RoomDescribed to the plaza, with padding bytes of
// description, and flushes the fan-out. It returns the Event ID and how
// long Publish, the tick's part, took.
func (s *stack) emit(padding int) (uint64, time.Duration) {
	s.mu.Lock()
	s.tick++
	s.id++
	id, tick := s.id, s.tick
	s.mu.Unlock()
	env := &gamev1.EventEnvelope{EventId: id, Tick: tick, Payload: &gamev1.EventEnvelope_RoomDescribed{RoomDescribed: &gamev1.RoomDescribed{RoomId: "plaza", Description: strings.Repeat("x", padding)}}}
	began := time.Now()
	s.hub.Publish(sim.Event{ID: id, Tick: sim.Tick(tick), Zone: "town", Type: sim.EvRoomDescribed, Scope: sim.ScopeRoom("town", "plaza"), Envelope: env})
	took := time.Since(began)
	s.hub.Flush()
	return id, took
}

func gauge(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	return testutil.ToFloat64(c)
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// stallConn is a connection whose reads can be frozen: the client stops
// taking bytes off the socket, so the server's writes back up through the
// kernel until they block — every stream on the connection with them.
type stallConn struct {
	net.Conn
	stalled atomic.Bool
	release chan struct{}
}

func (c *stallConn) Read(p []byte) (int, error) {
	if c.stalled.Load() {
		<-c.release
	}
	return c.Conn.Read(p)
}

// produce emits total Events of padding bytes, each acknowledged by the
// healthy stream before the next, so only a stream that is not being
// read falls behind. Returns the last ID.
func produce(t *testing.T, s *stack, healthy *connect.ServerStreamForClient[gamev1.EventEnvelope], total, padding int) uint64 {
	t.Helper()
	var last uint64
	for i := 0; i < total; i++ {
		id, _ := s.emit(padding)
		if !healthy.Receive() || healthy.Msg().GetEventId() != id {
			t.Fatalf("healthy stream at %d: %v / %v", i, healthy.Msg(), healthy.Err())
		}
		last = id
	}
	return last
}

func heapInuse() int64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapInuse)
}

// AC-4, AC-5: a client that stops consuming its stream — the connection
// stays alive and its transport keeps reading, but nothing takes the
// Events, so the stream's flow-control window closes. The stream is
// ended with buffer_full once its cursor trails by egress.buffer, the
// writer blocked in Send is reset, the Session and every other stream
// survive, and the server's heap grows by the retained window, not by
// everything produced. Sixty ticking seconds is stood in for by 3000
// Events of 32 KiB — far more than the ring and the window hold together
// — so an unbounded queue would show as ~94 MiB of heap.
func TestStalledStream_ResetKeepsSession(t *testing.T) {
	const (
		window  = 64
		padding = 32 << 10
		total   = 3000
	)
	s := newStack(t, 32, window, 256, nil)
	stalledClient := s.client(nil)
	healthyClient := s.client(nil)
	stalledID := s.open(stalledClient, "stalled", "alice", "town", "plaza")
	healthyID := s.open(healthyClient, "healthy", "bob", "town", "plaza")

	stalled, err := stalledClient.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: stalledID}))
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := healthyClient.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: healthyID}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gauge(t, s.hub.Metrics().Subscribers) == 2 }, "both subscribed")
	first, _ := s.emit(16)
	if !stalled.Receive() || !healthy.Receive() {
		t.Fatalf("first event: stalled=%v healthy=%v", stalled.Err(), healthy.Err())
	}
	// From here the stalled client never calls Receive.

	before := heapInuse()
	produce(t, s, healthy, total, padding)

	// The stalled stream was ended server-side; the Session is still
	// there, retaining.
	waitFor(t, func() bool { return gauge(t, s.eg.Metrics().Drops.WithLabelValues(egress.ReasonBufferFull)) == 1 }, "buffer_full drop")
	if got := gauge(t, s.eg.Metrics().Streams); got != 1 {
		t.Errorf("open streams = %v, want the healthy one", got)
	}
	if got := s.srv.SessionCount(); got != 2 {
		t.Errorf("sessions = %d; the stalled Session must survive its stream", got)
	}
	if got := gauge(t, s.hub.Metrics().Subscribers); got != 2 {
		t.Errorf("hub subscribers = %v; the Session keeps retaining", got)
	}
	logs := s.log.String()
	if !strings.Contains(logs, `"msg":"stream ended: client not reading, buffer full"`) || !strings.Contains(logs, `"session_id":"`+stalledID+`"`) {
		t.Errorf("warn line missing:\n%s", logs)
	}
	if strings.Contains(logs, "closing the connection") {
		t.Error("the connection was closed; a reset should have been enough")
	}

	// Retained: the ring (64) and the fan-out buffer (256) at most, each
	// a pointer to a 32 KiB envelope — 10 MiB if every slot were full and
	// distinct — plus the stream's flow-control window (4 MiB) held by
	// the client's transport in this same process. Against ~94 MiB if
	// everything produced were queued.
	if grew, bound := heapInuse()-before, int64(32<<20); grew > bound {
		t.Errorf("heap grew %d MiB with a stalled stream; bound %d MiB", grew>>20, bound>>20)
	}

	// The client reads again: what its transport had buffered, then the
	// reset. It resubscribes from the last ID it saw and is told the gap
	// cannot be resumed; the stream then runs live.
	for stalled.Receive() {
	}
	if stalled.Err() == nil {
		t.Fatal("stalled stream ended without an error")
	}
	again, err := stalledClient.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: stalledID, LastEventId: first}))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Receive() {
		t.Fatalf("resubscribe: %v", again.Err())
	}
	if r := again.Msg().GetResync(); r == nil || r.GetReason() != egress.ResyncWindowExceeded {
		t.Fatalf("expected resync, got %v", again.Msg())
	}
	id, _ := s.emit(16)
	healthy.Receive()
	if !again.Receive() || again.Msg().GetEventId() != id {
		t.Fatalf("live after resync: %v / %v", again.Msg(), again.Err())
	}
}

// AC-4, AC-5 for a client that stops reading its socket. Which way the
// stream ends depends on where the bytes stopped: if the reset reaches
// the client before the socket is full the stream ends and the Session
// stays; if the connection's writer is already wedged in the kernel, the
// reset is stuck behind it and after a heartbeat's worth of waiting the
// connection is closed and the Session with it. Either way the writer
// returns, memory stays bounded, and the other connection never notices.
// TestEscalation pins the second path deterministically.
func TestStalledSocket_EndsStream(t *testing.T) {
	const (
		window  = 64
		padding = 32 << 10
		total   = 3000
	)
	s := newStack(t, 32, window, 256, func(o *egress.Options) { o.HeartbeatInterval = 300 * time.Millisecond })
	sc := &stallConn{release: make(chan struct{})}
	var dialed atomic.Bool
	// Only the first connection is the stalled one; the client's redial
	// after it is closed is an ordinary connection.
	stalledClient := s.client(func(c net.Conn) net.Conn {
		if dialed.CompareAndSwap(false, true) {
			sc.Conn = c
			return sc
		}
		return c
	})
	healthyClient := s.client(nil)
	stalledID := s.open(stalledClient, "stalled", "alice", "town", "plaza")
	healthyID := s.open(healthyClient, "healthy", "bob", "town", "plaza")

	stalled, err := stalledClient.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: stalledID}))
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := healthyClient.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: healthyID}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gauge(t, s.hub.Metrics().Subscribers) == 2 }, "both subscribed")
	s.emit(16)
	if !stalled.Receive() || !healthy.Receive() {
		t.Fatalf("first event: stalled=%v healthy=%v", stalled.Err(), healthy.Err())
	}
	sc.stalled.Store(true)

	before := heapInuse()
	produce(t, s, healthy, total, padding)

	waitFor(t, func() bool { return gauge(t, s.eg.Metrics().Drops.WithLabelValues(egress.ReasonBufferFull)) == 1 }, "buffer_full drop")
	// The writer returned, one way or the other.
	waitFor(t, func() bool { return gauge(t, s.eg.Metrics().Streams) == 1 }, "stalled stream's writer to return")
	escalated := strings.Contains(s.log.String(), "closing the connection")
	if escalated {
		waitFor(t, func() bool { return s.srv.SessionCount() == 1 }, "stalled Session torn down")
		waitFor(t, func() bool { return gauge(t, s.hub.Metrics().Subscribers) == 1 }, "stalled Session's subscription freed")
	} else if got := s.srv.SessionCount(); got != 2 {
		t.Errorf("sessions = %d after a reset that reached the client", got)
	}
	t.Logf("stalled socket: connection closed = %v", escalated)
	if grew, bound := heapInuse()-before, int64(32<<20); grew > bound {
		t.Errorf("heap grew %d MiB with a stalled socket; bound %d MiB", grew>>20, bound>>20)
	}
	// The healthy stream never noticed.
	id, _ := s.emit(16)
	if !healthy.Receive() || healthy.Msg().GetEventId() != id {
		t.Fatalf("healthy stream disturbed: %v / %v", healthy.Msg(), healthy.Err())
	}
	// The client, reading again, finds its stream ended.
	close(sc.release)
	for stalled.Receive() {
	}
	if stalled.Err() == nil {
		t.Fatal("stalled stream ended without an error")
	}
}

// droppingEgress is a Subscribe seam that closes its own connection: what
// the egress escalates to when a reset cannot reach the client.
type droppingEgress struct{}

func (droppingEgress) Subscribe(ctx context.Context, _ *gateway.Session, _ *gamev1.SubscribeRequest, _ *connect.ServerStream[gamev1.EventEnvelope]) error {
	if !gateway.DropConnection(ctx) {
		return errors.New("no connection to drop")
	}
	<-ctx.Done()
	return ctx.Err()
}

// gateway.DropConnection closes the request's connection: the stream
// fails and every Session on the connection is torn down.
func TestDropConnection_TearsDownSessions(t *testing.T) {
	pki := testpki.New(t)
	srv, err := gateway.New(gateway.Options{
		Verifier: verifier{}, Listen: "127.0.0.1:0", TLSCertFile: pki.CertFile, TLSKeyFile: pki.KeyFile,
		MaxRecvBytes: 1 << 20, MaxRequestTimeout: 30 * time.Second, DrainTimeout: 5 * time.Second,
		ProtocolMin: 1, ProtocolMax: 1, Environment: "test", Egress: droppingEgress{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	tr := &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}
	c := gamev1connect.NewGameClient(&http.Client{Transport: tr}, "https://"+srv.Addr().String())
	resp, err := c.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{ProtocolVersion: 1, AuthToken: "p", ClientName: "t"}))
	if err != nil {
		t.Fatal(err)
	}
	stream, err := c.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: resp.Msg.SessionId}))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	if stream.Err() == nil {
		t.Fatal("stream survived its connection being dropped")
	}
	waitFor(t, func() bool { return srv.SessionCount() == 0 }, "session torn down")
}

// AC-8 through the gateway: a stream open when the server drains ends
// with UNAVAILABLE and a reason, and is counted as draining.
func TestDrain_EndsStreamTyped(t *testing.T) {
	s := newStack(t, 32, 64, 64, nil)
	c := s.client(nil)
	id := s.open(c, "p", "alice", "town", "plaza")
	stream, err := c.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: id}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gauge(t, s.eg.Metrics().Streams) == 1 }, "stream open")
	if err := s.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	var ce *connect.Error
	if !errors.As(stream.Err(), &ce) || ce.Code() != connect.CodeUnavailable || !strings.Contains(ce.Message(), "draining") {
		t.Fatalf("stream ended with %v", stream.Err())
	}
	if got := gauge(t, s.eg.Metrics().Drops.WithLabelValues(egress.ReasonDraining)); got != 1 {
		t.Errorf("drops{draining} = %v", got)
	}
	if got := gauge(t, s.eg.Metrics().Streams); got != 0 {
		t.Errorf("streams after drain = %v", got)
	}
}

// AC-3: 500 Sessions on one Room over real streams. Publish, which is
// what the tick calls, stays a bounded enqueue whatever the count, and
// every stream receives every Event in order.
func TestFanout_500Streams(t *testing.T) {
	const n = 500
	s := newStack(t, 64, 128, 64, nil)
	// Ten connections, fifty Sessions each: HTTP/2 multiplexes.
	clients := make([]gamev1connect.GameClient, 10)
	for i := range clients {
		clients[i] = s.client(nil)
	}
	streams := make([]*connect.ServerStreamForClient[gamev1.EventEnvelope], n)
	for i := range streams {
		c := clients[i%len(clients)]
		id := s.open(c, fmt.Sprintf("p%d", i), fmt.Sprintf("e%d", i), "town", "plaza")
		st, err := c.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: id}))
		if err != nil {
			t.Fatal(err)
		}
		streams[i] = st
	}
	waitFor(t, func() bool { return gauge(t, s.hub.Metrics().Subscribers) == n }, "500 subscribed")

	const events = 20
	var worst time.Duration
	for i := 0; i < events; i++ {
		if _, took := s.emit(64); took > worst {
			worst = took
		}
	}
	// Publish is one enqueue whatever the subscriber count: the fan-out
	// and the 500 writes happen after it returns. The bound is loose for
	// a loaded CI box under the race detector; the mechanism is asserted
	// by the Hub's own TestFanoutOutsideTick.
	if worst > 25*time.Millisecond {
		t.Errorf("worst Publish with 500 streams = %s", worst)
	}
	var wg sync.WaitGroup
	var bad atomic.Int32
	for _, st := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last uint64
			for i := 0; i < events; i++ {
				if !st.Receive() || st.Msg().GetEventId() <= last {
					bad.Add(1)
					return
				}
				last = st.Msg().GetEventId()
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d streams missed or misordered events", bad.Load())
	}
	if got := gauge(t, s.eg.Metrics().Sent.WithLabelValues(string(sim.EvRoomDescribed))); got != n*events {
		t.Errorf("sent = %v, want %d", got, n*events)
	}
}

// AC-9 through the gateway: a Game Master asks for World visibility and
// receives Events from Rooms it is not in; a player asking is refused.
func TestWorld_Gateway(t *testing.T) {
	s := newStack(t, 32, 64, 64, nil)
	c := s.client(nil)
	pid := s.open(c, "p", "", "", "")
	denied, err := c.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: pid, World: true}))
	if err != nil {
		t.Fatal(err)
	}
	for denied.Receive() {
	}
	if connect.CodeOf(denied.Err()) != connect.CodePermissionDenied {
		t.Fatalf("player world stream: %v", denied.Err())
	}
	gid := s.open(c, "gm", "", "", "")
	world, err := c.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: gid, World: true}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gauge(t, s.hub.Metrics().Subscribers) == 1 }, "gm subscribed")
	id, _ := s.emit(16)
	if !world.Receive() || world.Msg().GetEventId() != id {
		t.Fatalf("gm: %v / %v", world.Msg(), world.Err())
	}
	if !strings.Contains(s.log.String(), `"msg":"world-scope subscription: privileged read"`) {
		t.Error("no privileged-read log line")
	}
}

// AC-10: the tick loop's Kafka publisher failing every tick; Events still
// reach a Session, because the stream reads the in-process fan-out.
func TestEventsFlowWhenPublisherFails(t *testing.T) {
	s := newStack(t, 32, 64, 64, nil)
	e, err := simtest.NewVerbEngine(7)
	if err != nil {
		t.Fatal(err)
	}
	simtest.Place(e, "alice", "town", "plaza")
	source := tickloop.NewMemorySource()
	pub := &tickloop.MemoryPublisher{Fail: errors.New("broker unreachable")}
	e.Subscribe(s.hub)
	loop, err := tickloop.New(tickloop.Options{
		Engine: e, Source: source, Publisher: pub,
		TickRate: 50, TickBudget: 20 * time.Millisecond, MaxPerTick: 8, DrainTimeout: 2 * time.Second, CheckpointEvery: 1000,
		Registry: prometheus.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetObserver(loop)
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan error, 1)
	go func() { loopDone <- loop.Run(ctx) }()
	defer func() { cancel(); <-loopDone }()

	c := s.client(nil)
	id := s.open(c, "p", "alice", "town", "plaza")
	stream, err := c.Subscribe(context.Background(), connect.NewRequest(&gamev1.SubscribeRequest{SessionId: id}))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gauge(t, s.hub.Metrics().Subscribers) == 1 }, "subscribed")
	source.Push(simtest.Look("town", "alice"))
	if !stream.Receive() {
		t.Fatalf("no event: %v", stream.Err())
	}
	if stream.Msg().GetRoomDescribed() == nil {
		t.Fatalf("got %v, want RoomDescribed", stream.Msg())
	}
	waitFor(t, func() bool { return gauge(t, loop.Metrics().PublishFailures.WithLabelValues("events")) >= 1 }, "publish failures counted")
	_ = logv1.LoggedCommand{}
}
