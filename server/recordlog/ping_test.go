// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package recordlog

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// #129: one broker of three goes dark — its pod deleted, its address no
// longer answering — and stays in metadata until the controller drops it.
// A ping must still find the two that answer, within its budget.
// kgo.Client.Ping asks the dark one first and spends the budget there;
// the test shows that happens (live-assertions.md rule 4), then that Ping
// does not.
func TestPing_OneDarkBrokerOfThree(t *testing.T) {
	c := newFakeCluster(t, 3)
	dark := c.addrs[0]
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(c.addrs[1]),
		kgo.Dialer(func(ctx context.Context, network, host string) (net.Conn, error) {
			if host == dark {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-stop:
					return nil, net.ErrClosed
				}
			}
			var d net.Dialer
			return d.DialContext(ctx, network, host)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	// Discover the cluster through the seed: all three are now known.
	if err := cl.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(cl.DiscoveredBrokers()); n != 3 {
		t.Fatalf("discovered %d brokers", n)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	err = cl.Ping(ctx)
	cancel()
	if err == nil {
		t.Fatal("kgo's Ping answered past the dark broker; the window this test holds open is closed")
	}

	start := time.Now()
	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := Ping(ctx, cl); err != nil {
		t.Fatalf("Ping with two of three brokers answering: %v", err)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Fatalf("Ping took %s: it waited on the dark broker", d)
	}
}

// With every broker dark, Ping fails at its deadline.
func TestPing_AllDark(t *testing.T) {
	c := newFakeCluster(t, 1)
	cl, err := kgo.NewClient(kgo.SeedBrokers(c.addrs[0]))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cl.Close)
	if err := cl.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Ping(ctx, cl); err == nil {
		t.Fatal("Ping answered with no broker up")
	}
}

// fakeCluster is n brokers that answer ApiVersions and Metadata, each
// Metadata naming all n, broker 0 first.
type fakeCluster struct {
	t     *testing.T
	lns   []net.Listener
	addrs []string
	mu    sync.Mutex
	conns []net.Conn
}

func newFakeCluster(t *testing.T, n int) *fakeCluster {
	c := &fakeCluster{t: t}
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		c.lns = append(c.lns, ln)
		c.addrs = append(c.addrs, ln.Addr().String())
	}
	for _, ln := range c.lns {
		go c.serve(ln)
	}
	t.Cleanup(c.close)
	return c
}

// close takes every broker down, connections already open included.
func (c *fakeCluster) close() {
	for _, ln := range c.lns {
		_ = ln.Close()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.conns {
		_ = conn.Close()
	}
}

func (c *fakeCluster) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		c.mu.Lock()
		c.conns = append(c.conns, conn)
		c.mu.Unlock()
		go c.handle(conn)
	}
}

func (c *fakeCluster) handle(conn net.Conn) {
	defer conn.Close()
	for {
		var size [4]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(size[:]))
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		key, version := int16(binary.BigEndian.Uint16(body)), int16(binary.BigEndian.Uint16(body[2:]))
		corr := body[4:8]
		req := kmsg.RequestForKey(key)
		if req == nil {
			return
		}
		req.SetVersion(version)
		var resp kmsg.Response
		switch key {
		case 18:
			r := kmsg.NewPtrApiVersionsResponse()
			r.ApiKeys = []kmsg.ApiVersionsResponseApiKey{{ApiKey: 18, MaxVersion: 3}, {ApiKey: 3, MaxVersion: 12}}
			resp = r
		case 3:
			r := kmsg.NewPtrMetadataResponse()
			for id, a := range c.addrs {
				host, port, _ := net.SplitHostPort(a)
				p, _ := strconv.Atoi(port)
				r.Brokers = append(r.Brokers, kmsg.MetadataResponseBroker{NodeID: int32(id), Host: host, Port: int32(p)})
			}
			r.ControllerID = 1
			resp = r
		default:
			return
		}
		resp.SetVersion(version)
		out := append([]byte{0, 0, 0, 0}, corr...)
		// ApiVersions answers with a v0 header whatever its version; every
		// other flexible response carries empty tagged fields.
		if req.IsFlexible() && key != 18 {
			out = append(out, 0)
		}
		out = resp.AppendTo(out)
		binary.BigEndian.PutUint32(out, uint32(len(out)-4))
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}
