// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build soak

// The stream soak (AW-INF-006 AC-1): a Subscribe opened from outside the cluster,
// through the edge, held for ANDARA_SOAK_DURATION with no ingress-initiated close.
//
// Its own build tag rather than `smoke`, because `make stack-smoke` runs the smoke
// package against the compose stack in seconds and this runs against a cluster for
// an hour. Run it with `make stream-soak ENV=<env> DURATION=<d>`.
package smoke

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"golang.org/x/net/http2"
)

func soakEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// soakClient dials the edge the way a player does: gRPC over HTTP/2 and TLS, trusting
// the CA in ANDARA_TLS_CA_FILE or, when that is empty, the system store (a public
// issuer). No http.Client Timeout — a timeout there would close the stream from the
// client side and look exactly like the ingress close this test exists to detect.
func soakClient(t *testing.T) gamev1connect.GameClient {
	t.Helper()
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile := os.Getenv("ANDARA_TLS_CA_FILE"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatalf("read CA %s: %v", caFile, err)
		}
		tc.RootCAs = x509.NewCertPool()
		if !tc.RootCAs.AppendCertsFromPEM(pem) {
			t.Fatalf("%s is not a PEM certificate", caFile)
		}
	}
	tr := &http2.Transport{TLSClientConfig: tc}
	// ANDARA_SOAK_RESOLVE=<ip> is curl's --resolve: dial that address, keep the host
	// for SNI and :authority, so the routers match without an /etc/hosts line (CI).
	if ip := os.Getenv("ANDARA_SOAK_RESOLVE"); ip != "" {
		tr.DialTLSContext = func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			c := cfg.Clone()
			c.ServerName = host
			d := &tls.Dialer{Config: c}
			return d.DialContext(ctx, network, net.JoinHostPort(ip, port))
		}
	}
	hc := &http.Client{Transport: tr}
	return gamev1connect.NewGameClient(hc, "https://"+soakEnv("ANDARA_SOAK_ADDR", "andara.local:443"),
		connect.WithGRPC())
}

// TestSoak_SubscribeStaysOpen opens a Session and a Subscribe through the edge and
// holds the stream for the duration. The stream carries nothing today (HoldingEgress,
// AW-SRV-005) — which is the harder case for a proxy: an idle HTTP/2 stream with no
// frames either way is what a read or idle timeout would reap. The stream ending for
// any reason before the duration is the failure; the client canceling it afterwards
// is the only acceptable end.
func TestSoak_SubscribeStaysOpen(t *testing.T) {
	dur, err := time.ParseDuration(soakEnv("ANDARA_SOAK_DURATION", "5m"))
	if err != nil {
		t.Fatalf("ANDARA_SOAK_DURATION: %v", err)
	}
	c := soakClient(t)
	open, err := c.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "soak-token", ClientName: "stream-soak/0.1",
	}))
	if err != nil {
		t.Fatalf("OpenSession through the edge: %v", err)
	}
	sid := open.Msg.GetSessionId()
	t.Logf("session %s open; holding Subscribe for %s", sid, dur)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := c.Subscribe(ctx, connect.NewRequest(&gamev1.SubscribeRequest{SessionId: sid}))
	if err != nil {
		t.Fatalf("Subscribe through the edge: %v", err)
	}
	started := time.Now()
	ended := make(chan error, 1)
	go func() {
		for stream.Receive() {
		}
		ended <- stream.Err()
	}()

	// Progress lines so a nightly log shows where it died, not only that it did.
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	deadline := time.After(dur)
	for {
		select {
		case err := <-ended:
			t.Fatalf("stream ended after %s, before the %s soak elapsed: %v", time.Since(started).Round(time.Second), dur, err)
		case <-tick.C:
			t.Logf("stream open for %s", time.Since(started).Round(time.Second))
		case <-deadline:
			cancel()
			err := <-ended
			if !errors.Is(err, context.Canceled) && connect.CodeOf(err) != connect.CodeCanceled {
				t.Fatalf("stream ended with %v after the client canceled; want Canceled", err)
			}
			t.Logf("stream held %s through the edge; closed by the client", time.Since(started).Round(time.Second))
			if _, err := c.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: sid})); err != nil {
				t.Logf("CloseSession after the soak: %v (the Session is torn down with the connection anyway)", err)
			}
			return
		}
	}
}
