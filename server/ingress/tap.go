// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kbin"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// The produce tap (AW-SRV-052). franz-go retries NOT_ENOUGH_REPLICAS,
// LEADER_NOT_AVAILABLE, NOT_LEADER_OR_FOLLOWER and REQUEST_TIMED_OUT until
// the record's delivery timeout and then completes the promise with
// ErrRecordTimeout, not the broker's error; it offers no hook that sees a
// produce response, and logs a retried error only at debug level in a
// string. The error is on the wire, though. The tap wraps the produce
// connection, follows the requests written and the responses read (a
// connection answers in the order it was asked, so the Nth response belongs
// to the Nth request), and decodes the partition error codes of the produce
// responses with kmsg. It never writes, never changes a byte it passes, and
// stops following a connection the moment it cannot keep step.
//
// It reads the bytes the client reads, so it must sit above any TLS: a
// Dialer that returns an encrypted conn (a tls.Dialer's DialContext does
// not) is not tapped, and the produce-error trigger then does nothing, the
// probe being the only one. kgo's own DialTLSConfig layers TLS on the conn
// the Dialer returns, which would hide the frames from the tap; a client
// that needs TLS gives its Dialer a tls.Dialer instead.

// maxTapFrame bounds a frame the tap will follow. A producer's responses are
// small; anything larger is not what the tap thinks it is.
const maxTapFrame = 64 << 20

// tapDialer wraps dial, the net dialer when nil, in a tap that calls
// onProduce for each partition of each produce response.
func tapDialer(dial func(ctx context.Context, network, host string) (net.Conn, error), onProduce func(partition int32, code int16), onLost func()) func(ctx context.Context, network, host string) (net.Conn, error) {
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	return func(ctx context.Context, network, host string) (net.Conn, error) {
		conn, err := dial(ctx, network, host)
		if err != nil {
			return nil, err
		}
		return &tapConn{Conn: conn, onProduce: onProduce, onLost: onLost}, nil
	}
}

type tapRequest struct {
	corr    int32
	key     int16
	version int16
}

type tapConn struct {
	net.Conn
	onProduce func(partition int32, code int16)
	onLost    func() // called once, when the tap stops following the conn

	mu      sync.Mutex
	off     bool
	pending []tapRequest
	wbuf    []byte // a request frame still being written

	hdr    [8]byte // size and correlation ID of the response frame being read
	hn     int
	inside bool
	remain int
	cur    tapRequest
	body   []byte // a produce response's bytes, collected until the frame ends
}

func (c *tapConn) Write(p []byte) (int, error) {
	// Noted before the bytes leave: the answer can be read before Write
	// returns.
	c.noteWrite(p)
	return c.Conn.Write(p)
}

func (c *tapConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.noteRead(p[:n])
	}
	return n, err
}

func (c *tapConn) stopLocked() {
	c.off, c.pending, c.wbuf, c.body = true, nil, nil, nil
	if c.onLost != nil {
		// Under the lock, but only counts and logs.
		c.onLost()
	}
}

func (c *tapConn) noteWrite(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.off {
		return
	}
	c.wbuf = append(c.wbuf, p...)
	for len(c.wbuf) >= 4 {
		size := int(binary.BigEndian.Uint32(c.wbuf))
		if size < 8 || size > maxTapFrame {
			c.stopLocked()
			return
		}
		if len(c.wbuf) < 4+size {
			return
		}
		c.pending = append(c.pending, tapRequest{
			key:     int16(binary.BigEndian.Uint16(c.wbuf[4:])),
			version: int16(binary.BigEndian.Uint16(c.wbuf[6:])),
			corr:    int32(binary.BigEndian.Uint32(c.wbuf[8:])),
		})
		c.wbuf = c.wbuf[4+size:]
	}
}

func (c *tapConn) noteRead(b []byte) {
	var codes []partitionCode
	c.mu.Lock()
	for len(b) > 0 && !c.off {
		if !c.inside {
			n := copy(c.hdr[c.hn:], b)
			c.hn += n
			b = b[n:]
			if c.hn < len(c.hdr) {
				break
			}
			size := int(binary.BigEndian.Uint32(c.hdr[:4]))
			corr := int32(binary.BigEndian.Uint32(c.hdr[4:]))
			if size < 4 || size > maxTapFrame || len(c.pending) == 0 || c.pending[0].corr != corr {
				c.stopLocked()
				break
			}
			c.cur, c.pending = c.pending[0], c.pending[1:]
			c.inside, c.remain = true, size-4
			c.body = c.body[:0]
		}
		n := min(c.remain, len(b))
		if c.cur.key == produceKey {
			c.body = append(c.body, b[:n]...)
		}
		c.remain -= n
		b = b[n:]
		if c.remain > 0 {
			break
		}
		if c.cur.key == produceKey {
			codes = append(codes, decodeProduce(c.cur.version, c.body)...)
		}
		c.inside, c.hn = false, 0
	}
	c.mu.Unlock()
	for _, pc := range codes {
		c.onProduce(pc.partition, pc.code)
	}
}

type partitionCode struct {
	partition int32
	code      int16
}

// decodeProduce is each partition's error code in a produce response's
// bytes after the correlation ID; nothing when they do not decode.
func decodeProduce(version int16, body []byte) []partitionCode {
	resp := kmsg.NewPtrProduceResponse()
	resp.SetVersion(version)
	if resp.IsFlexible() {
		// Response header v1: tagged fields, ignored.
		r := kbin.Reader{Src: body}
		for n := r.Uvarint(); n > 0; n-- {
			r.Uvarint()
			r.Span(int(r.Uvarint()))
		}
		if r.Complete() != nil {
			return nil
		}
		body = r.Src
	}
	if resp.ReadFrom(body) != nil {
		return nil
	}
	var out []partitionCode
	for _, t := range resp.Topics {
		for _, p := range t.Partitions {
			out = append(out, partitionCode{p.Partition, p.ErrorCode})
		}
	}
	return out
}
