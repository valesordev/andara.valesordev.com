// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/valesordev/andara/server/sim"
)

// fakeBroker is one scripted Kafka broker for the Commands topic: it answers
// ApiVersions, Metadata, InitProducerID, DescribeConfigs and Produce over the
// wire, with each Partition's leader, ISR and produce error set by the test.
// Real brokers cannot be made to return NOT_ENOUGH_REPLICAS or
// REQUEST_TIMED_OUT on one Partition of a healthy topic; this one does.
type fakeBroker struct {
	t     *testing.T
	ln    net.Listener
	topic string

	mu          sync.Mutex
	conns       []net.Conn
	partitions  map[int32]partitionView
	produceErr  map[int32]int16 // error code answered to a produce; 0 is success
	errBudget   map[int32]int   // produces left to refuse before succeeding; <0 forever
	minISR      string
	delay       map[int32]time.Duration // produce responses held, by Partition
	noMetadata  bool
	extra       []string // further broker addresses Metadata names, which nothing serves
	noMinISR    bool     // DescribeConfigs answers an empty list, as Redpanda does
	nextOffset  map[int32]int64
	landed      map[int32][]int64 // offsets appended, by Partition
	produceReqs map[int32]int     // produce requests received, by Partition

	produceCount atomic.Int64
}

func newFakeBroker(t *testing.T, topic string) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{t: t, ln: ln, topic: topic, minISR: "2",
		partitions: map[int32]partitionView{}, delay: map[int32]time.Duration{}, produceErr: map[int32]int16{}, errBudget: map[int32]int{},
		nextOffset: map[int32]int64{}, landed: map[int32][]int64{}, produceReqs: map[int32]int{}}
	for p := range sim.PartitionCount {
		b.partitions[p] = partitionView{Leader: 0, ISR: 3}
	}
	go b.serve()
	t.Cleanup(b.close)
	return b
}

func (b *fakeBroker) addr() string { return b.ln.Addr().String() }

func (b *fakeBroker) close() {
	_ = b.ln.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.Close()
	}
}

// failProduce makes every produce to the Partition answer code, for n
// requests (n < 0: until cleared).
func (b *fakeBroker) failProduce(p int32, code int16, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.produceErr[p], b.errBudget[p] = code, n
}

func (b *fakeBroker) setPartition(p int32, v partitionView) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.partitions[p] = v
}

func (b *fakeBroker) setMinISR(v string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minISR = v
}

func (b *fakeBroker) requests(p int32) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.produceReqs[p]
}

func (b *fakeBroker) appended(p int32) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.landed[p])
}

func (b *fakeBroker) serve() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		b.conns = append(b.conns, conn)
		b.mu.Unlock()
		go b.handle(conn)
	}
}

func (b *fakeBroker) handle(conn net.Conn) {
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
		// Request header: client ID, then tagged fields when flexible.
		off := 8
		off += 2 + max(int(int16(binary.BigEndian.Uint16(body[off:]))), 0)
		if req.IsFlexible() {
			off++
		}
		if key != 18 {
			if err := req.ReadFrom(body[off:]); err != nil {
				b.t.Errorf("fake broker: key %d v%d: %v", key, version, err)
				return
			}
		}
		if pr, ok := req.(*kmsg.ProduceRequest); ok {
			b.hold(pr)
		}
		resp := b.answer(key, version, req)
		if resp == nil {
			return
		}
		resp.SetVersion(version)
		out := append([]byte{0, 0, 0, 0}, corr...)
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

// hold sleeps for the longest delay set on a Partition the request names.
func (b *fakeBroker) hold(pr *kmsg.ProduceRequest) {
	b.mu.Lock()
	var d time.Duration
	for _, t := range pr.Topics {
		for _, p := range t.Partitions {
			d = max(d, b.delay[p.Partition])
		}
	}
	b.mu.Unlock()
	time.Sleep(d)
}

// holdProduce delays every produce response for the Partition.
func (b *fakeBroker) holdProduce(p int32, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.delay[p] = d
}

func (b *fakeBroker) answer(key, version int16, req kmsg.Request) kmsg.Response {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch key {
	case 18:
		r := kmsg.NewPtrApiVersionsResponse()
		r.ApiKeys = []kmsg.ApiVersionsResponseApiKey{
			{ApiKey: 18, MaxVersion: 3}, {ApiKey: 3, MaxVersion: 12}, {ApiKey: 0, MinVersion: 3, MaxVersion: 9},
			{ApiKey: 22, MaxVersion: 4}, {ApiKey: 32, MaxVersion: 4},
		}
		return r
	case 3:
		if b.noMetadata {
			return nil
		}
		host, port, _ := net.SplitHostPort(b.addr())
		pn, _ := strconv.Atoi(port)
		r := kmsg.NewPtrMetadataResponse()
		r.Brokers = []kmsg.MetadataResponseBroker{{NodeID: 0, Host: host, Port: int32(pn)}}
		for i, a := range b.extra {
			h, ps, _ := net.SplitHostPort(a)
			pe, _ := strconv.Atoi(ps)
			r.Brokers = append(r.Brokers, kmsg.MetadataResponseBroker{NodeID: int32(i + 1), Host: h, Port: int32(pe)})
		}
		t := kmsg.NewMetadataResponseTopic()
		t.Topic = kmsg.StringPtr(b.topic)
		for p := range sim.PartitionCount {
			v := b.partitions[p]
			mp := kmsg.NewMetadataResponseTopicPartition()
			mp.Partition, mp.Leader, mp.Replicas = p, v.Leader, []int32{0, 1, 2}
			for i := range v.ISR {
				mp.ISR = append(mp.ISR, int32(i))
			}
			if v.Leader < 0 {
				mp.ErrorCode = 5 // LEADER_NOT_AVAILABLE
			}
			t.Partitions = append(t.Partitions, mp)
		}
		r.Topics = []kmsg.MetadataResponseTopic{t}
		return r
	case 22:
		r := kmsg.NewPtrInitProducerIDResponse()
		r.ProducerID, r.ProducerEpoch = 7, 0
		return r
	case 32:
		r := kmsg.NewPtrDescribeConfigsResponse()
		res := kmsg.NewDescribeConfigsResponseResource()
		res.ResourceName = b.topic
		res.ResourceType = kmsg.ConfigResourceTypeTopic
		c := kmsg.NewDescribeConfigsResponseResourceConfig()
		c.Name, c.Value = "min.insync.replicas", kmsg.StringPtr(b.minISR)
		if !b.noMinISR {
			res.Configs = []kmsg.DescribeConfigsResponseResourceConfig{c}
		}
		r.Resources = []kmsg.DescribeConfigsResponseResource{res}
		return r
	case 0:
		pr := req.(*kmsg.ProduceRequest)
		r := kmsg.NewPtrProduceResponse()
		for _, t := range pr.Topics {
			rt := kmsg.NewProduceResponseTopic()
			rt.Topic = t.Topic
			for _, p := range t.Partitions {
				b.produceReqs[p.Partition]++
				rp := kmsg.NewProduceResponseTopicPartition()
				rp.Partition = p.Partition
				if code := b.produceErr[p.Partition]; code != 0 && b.errBudget[p.Partition] != 0 {
					rp.ErrorCode = code
					if b.errBudget[p.Partition] > 0 {
						b.errBudget[p.Partition]--
					}
					if code == 20 { // NOT_ENOUGH_REPLICAS_AFTER_APPEND: appended, then refused
						b.landed[p.Partition] = append(b.landed[p.Partition], b.nextOffset[p.Partition])
						b.nextOffset[p.Partition]++
					}
				} else {
					rp.BaseOffset = b.nextOffset[p.Partition]
					b.landed[p.Partition] = append(b.landed[p.Partition], rp.BaseOffset)
					b.nextOffset[p.Partition]++
				}
				rt.Partitions = append(rt.Partitions, rp)
			}
			r.Topics = append(r.Topics, rt)
		}
		b.produceCount.Add(1)
		return r
	}
	return nil
}
