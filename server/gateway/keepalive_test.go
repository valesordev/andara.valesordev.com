// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// blackhole is a TCP proxy that can stop forwarding without closing either
// side: a network partition, which no transport close ever reports.
type blackhole struct {
	ln     net.Listener
	frozen atomic.Bool
	wg     sync.WaitGroup
}

func newBlackhole(t *testing.T, target string) *blackhole {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &blackhole{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			b.wg.Add(2)
			go b.pipe(up, c)
			go b.pipe(c, up)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return b
}

// pipe copies src to dst until src closes, dropping everything while frozen.
func (b *blackhole) pipe(dst, src net.Conn) {
	defer b.wg.Done()
	defer func() { _ = dst.Close() }()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !b.frozen.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (h *harness) gameVia(addr string) gamev1connect.GameClient {
	tr := &http.Transport{TLSClientConfig: h.pki.ClientTLS(), ForceAttemptHTTP2: true}
	return gamev1connect.NewGameClient(&http.Client{Transport: tr}, "https://"+addr)
}

// AW-SRV-015 AC-1: a keepalive miss with no transport close — a partition —
// tears the Session down as linkdead within session.linkdead_detect. A
// client that is idle but answering is not touched.
func TestKeepalive_APartitionedClientIsLinkdead(t *testing.T) {
	const detect = 400 * time.Millisecond
	rr := &recordingRoster{}
	h := start(t, func(o *Options) { o.Roster = rr; o.KeepaliveTimeout = detect })

	healthy := newBlackhole(t, h.srv.Addr().String())
	alive := h.open(t, h.gameVia(healthy.ln.Addr().String()))

	partitioned := newBlackhole(t, h.srv.Addr().String())
	lost := h.open(t, h.gameVia(partitioned.ln.Addr().String()))
	partitioned.frozen.Store(true)
	froze := time.Now()

	waitFor(t, 5*detect, func() bool {
		for _, r := range rr.releases() {
			if r.id == lost.SessionId {
				return true
			}
		}
		return false
	}, "the partitioned Session to be torn down")
	if took := time.Since(froze); took > 2*detect {
		t.Errorf("detected after %s; session.linkdead_detect is %s", took, detect)
	}
	for _, r := range rr.releases() {
		if r.id == lost.SessionId && r.end != EndLinkdead {
			t.Fatalf("the partitioned Session ended %v, want linkdead", r.end)
		}
	}

	// Several detect windows later, the idle client that answers its pings
	// still has its Session.
	time.Sleep(3 * detect)
	for _, r := range rr.releases() {
		if r.id == alive.SessionId {
			t.Fatal("an idle client answering its pings was torn down")
		}
	}
	if _, ok := h.srv.sessions.get(alive.SessionId); !ok {
		t.Fatal("the healthy Session is gone")
	}
}
