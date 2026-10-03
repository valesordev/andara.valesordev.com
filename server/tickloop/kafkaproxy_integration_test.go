// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build integration

package tickloop

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// leaderProxy stands between a client and a single-broker cluster and can
// answer, for one Partition, the way a Kafka broker that has just restarted
// and not yet loaded its partitions does: UNKNOWN_TOPIC_OR_PARTITION on every
// Produce (#128). Metadata responses are rewritten to name the proxy, so the
// client never learns the broker's own address and every request passes
// through here.
type leaderProxy struct {
	t        *testing.T
	ln       net.Listener
	upstream string
	// Unknown, while set, fails every Produce to Partition with
	// UNKNOWN_TOPIC_OR_PARTITION. The broker has already written the batch;
	// only the answer changes, which is what an idempotent producer retrying
	// it sees as a duplicate once the error stops.
	Unknown   atomic.Bool
	Partition int32
	// Injected counts the Produce responses rewritten.
	Injected atomic.Int64

	// Refuse, while set, answers every Produce itself with the retriable
	// NOT_ENOUGH_REPLICAS and forwards nothing: a broker that takes no
	// writes, on every Partition (AW-SRV-026). Every batch gets an answer,
	// and nothing is written, so the topic ends exactly at the last boundary
	// acknowledged.
	Refuse atomic.Bool
	// Refused counts the Produce requests answered that way.
	Refused atomic.Int64
}

// refuse is the NOT_ENOUGH_REPLICAS answer to a Produce request frame, or nil
// if the frame is not one it can read.
func (p *leaderProxy) refuse(frame []byte) []byte {
	if len(frame) < 10 || int16(binary.BigEndian.Uint16(frame)) != 0 {
		return nil
	}
	req := kmsg.NewPtrProduceRequest()
	req.SetVersion(int16(binary.BigEndian.Uint16(frame[2:])))
	off := 10 // key, version, correlation ID, client ID length
	if n := int16(binary.BigEndian.Uint16(frame[8:])); n > 0 {
		off += int(n)
	}
	if req.IsFlexible() {
		if len(frame) <= off || frame[off] != 0 {
			return nil // request header tags: not ours to read
		}
		off++
	}
	if err := req.ReadFrom(frame[off:]); err != nil {
		p.t.Logf("proxy: cannot read produce v%d: %v", req.GetVersion(), err)
		return nil
	}
	resp := req.ResponseKind().(*kmsg.ProduceResponse)
	for _, t := range req.Topics {
		rt := kmsg.NewProduceResponseTopic()
		rt.Topic, rt.TopicID = t.Topic, t.TopicID
		for _, part := range t.Partitions {
			rp := kmsg.NewProduceResponseTopicPartition()
			rp.Partition, rp.ErrorCode, rp.BaseOffset = part.Partition, kerr.NotEnoughReplicas.Code, -1
			rt.Partitions = append(rt.Partitions, rp)
		}
		resp.Topics = append(resp.Topics, rt)
	}
	out := append([]byte(nil), frame[4:8]...) // the correlation ID
	if resp.IsFlexible() {
		out = append(out, 0)
	}
	p.Refused.Add(1)
	return resp.AppendTo(out)
}

func newLeaderProxy(t *testing.T, upstream string, partition int32) *leaderProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &leaderProxy{t: t, ln: ln, upstream: upstream, Partition: partition}
	go p.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *leaderProxy) Addr() string { return p.ln.Addr().String() }

func (p *leaderProxy) accept() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp", p.upstream)
		if err != nil {
			_ = c.Close()
			continue
		}
		// Both directions write to the client once Refuse answers for
		// the broker.
		var toClient sync.Mutex
		writeClient := func(frame []byte) error {
			toClient.Lock()
			defer toClient.Unlock()
			return writeFrame(c, frame)
		}
		var inflight sync.Map // correlation ID -> [2]int16{key, version}
		go func() {
			defer u.Close()
			for {
				frame, err := readFrame(c)
				if err != nil {
					return
				}
				if p.Refuse.Load() {
					if resp := p.refuse(frame); resp != nil {
						if writeClient(resp) != nil {
							return
						}
						continue
					}
				}
				if len(frame) >= 8 {
					key := int16(binary.BigEndian.Uint16(frame[0:]))
					ver := int16(binary.BigEndian.Uint16(frame[2:]))
					inflight.Store(int32(binary.BigEndian.Uint32(frame[4:])), [2]int16{key, ver})
				}
				if writeFrame(u, frame) != nil {
					return
				}
			}
		}()
		go func() {
			defer c.Close()
			for {
				frame, err := readFrame(u)
				if err != nil {
					return
				}
				if len(frame) >= 4 {
					if kv, ok := inflight.LoadAndDelete(int32(binary.BigEndian.Uint32(frame))); ok {
						frame = p.rewrite(kv.([2]int16), frame)
					}
				}
				if writeClient(frame) != nil {
					return
				}
			}
		}()
	}
}

// rewrite edits a Produce or Metadata response and returns every other
// frame as it came.
func (p *leaderProxy) rewrite(kv [2]int16, frame []byte) []byte {
	key, ver := kv[0], kv[1]
	if key != 0 && key != 3 {
		return frame
	}
	resp := kmsg.ResponseForKey(key)
	resp.SetVersion(ver)
	hdr := 4
	if resp.IsFlexible() {
		if len(frame) < 5 || frame[4] != 0 {
			return frame // a response header carrying tags: not ours to edit
		}
		hdr = 5
	}
	if err := resp.ReadFrom(frame[hdr:]); err != nil {
		p.t.Logf("proxy: cannot read key %d v%d: %v", key, ver, err)
		return frame
	}
	switch r := resp.(type) {
	case *kmsg.ProduceResponse:
		if !p.Unknown.Load() {
			return frame
		}
		for i := range r.Topics {
			for j := range r.Topics[i].Partitions {
				if r.Topics[i].Partitions[j].Partition == p.Partition {
					r.Topics[i].Partitions[j].ErrorCode = kerr.UnknownTopicOrPartition.Code
					r.Topics[i].Partitions[j].BaseOffset = -1
					p.Injected.Add(1)
				}
			}
		}
	case *kmsg.MetadataResponse:
		host, port, _ := net.SplitHostPort(p.Addr())
		n, _ := strconv.Atoi(port)
		for i := range r.Brokers {
			r.Brokers[i].Host, r.Brokers[i].Port = host, int32(n)
		}
	}
	return resp.AppendTo(append([]byte(nil), frame[:hdr]...))
}

func readFrame(r io.Reader) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	frame := make([]byte, binary.BigEndian.Uint32(size[:]))
	_, err := io.ReadFull(r, frame)
	return frame, err
}

func writeFrame(w io.Writer, frame []byte) error {
	out := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(frame)), uint32(len(frame)))
	_, err := w.Write(append(out, frame...))
	return err
}
