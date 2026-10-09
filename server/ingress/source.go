// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package ingress

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// logSource is the probe's view of the Command log: what the brokers say of
// the topic, and what they say its min.insync.replicas is.
type logSource interface {
	// Topic is the topic's leaders and ISR sizes, or an error when no
	// broker answers.
	Topic(ctx context.Context) (*topicView, error)
	// MinISR is the topic's min.insync.replicas.
	MinISR(ctx context.Context) (int, error)
}

// kafkaSource asks the brokers with a metadata request and a
// DescribeConfigs; neither pings a broker. Each goes to every broker the
// client has discovered at once and the first answer wins, as
// recordlog.Ping does and for the same reason (#129): kgo sends a request
// to one broker, and a broker whose pod is gone but whose address is still
// in metadata spends the whole probe interval on a dial that never answers,
// which two probes in a row would read as the log being unreachable while
// two of three brokers serve.
type kafkaSource struct {
	client *kgo.Client
	topic  string
}

// ask sends the request every discovered broker is asked at once and returns
// the first answer. A client that has discovered nothing yet knows only its
// seeds, and so does the last resort when no discovered broker answers: kgo's
// own Request with what is left of ctx. A seed cannot be asked directly; kgo
// refuses a request to its internal ID.
func (s *kafkaSource) ask(ctx context.Context, build func() kmsg.Request) (kmsg.Response, error) {
	brokers := s.client.DiscoveredBrokers()
	if len(brokers) == 0 {
		return s.client.Request(ctx, build())
	}
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		resp kmsg.Response
		err  error
	}
	answers := make(chan answer, len(brokers))
	for _, b := range brokers {
		go func() {
			resp, err := b.Request(ctx2, build())
			answers <- answer{resp, err}
		}()
	}
	var err error
	for range brokers {
		a := <-answers
		if a.err == nil {
			return a.resp, nil
		}
		err = a.err
	}
	if ctx.Err() != nil {
		return nil, err
	}
	return s.client.Request(ctx, build())
}

func (s *kafkaSource) Topic(ctx context.Context) (*topicView, error) {
	r, err := s.ask(ctx, func() kmsg.Request {
		req := kmsg.NewPtrMetadataRequest()
		req.Topics = []kmsg.MetadataRequestTopic{{Topic: kmsg.StringPtr(s.topic)}}
		return req
	})
	if err != nil {
		return nil, err
	}
	resp := r.(*kmsg.MetadataResponse)
	if len(resp.Topics) != 1 {
		return nil, fmt.Errorf("metadata answered %d topics for one", len(resp.Topics))
	}
	t := resp.Topics[0]
	switch err := kerr.ErrorForCode(t.ErrorCode); {
	case errors.Is(err, kerr.UnknownTopicOrPartition):
		return &topicView{Missing: true}, nil
	case err != nil && !errors.Is(err, kerr.LeaderNotAvailable):
		return nil, err
	}
	v := &topicView{Partitions: make(map[int32]partitionView, len(t.Partitions))}
	for _, p := range t.Partitions {
		leader := p.Leader
		// A partition error is a Partition without a leader, except
		// REPLICA_NOT_AVAILABLE, which names a replica that is not a
		// fault of the Partition.
		if err := kerr.ErrorForCode(p.ErrorCode); err != nil && !errors.Is(err, kerr.ReplicaNotAvailable) {
			leader = -1
		}
		v.Partitions[p.Partition] = partitionView{Leader: leader, ISR: len(p.ISR)}
	}
	return v, nil
}

func (s *kafkaSource) MinISR(ctx context.Context) (int, error) {
	r, err := s.ask(ctx, func() kmsg.Request {
		req := kmsg.NewPtrDescribeConfigsRequest()
		req.Resources = []kmsg.DescribeConfigsRequestResource{{
			ResourceType: kmsg.ConfigResourceTypeTopic,
			ResourceName: s.topic,
			ConfigNames:  []string{"min.insync.replicas"},
		}}
		return req
	})
	if err != nil {
		return 0, err
	}
	resp := r.(*kmsg.DescribeConfigsResponse)
	for _, r := range resp.Resources {
		if err := kerr.ErrorForCode(r.ErrorCode); err != nil {
			return 0, err
		}
		for _, c := range r.Configs {
			if c.Name != "min.insync.replicas" || c.Value == nil {
				continue
			}
			n, err := strconv.Atoi(*c.Value)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("min.insync.replicas %q is not a count", *c.Value)
			}
			return n, nil
		}
	}
	// A broker that does not report the property (Redpanda, which has no
	// such setting, answers an empty list) is held to the Kafka default.
	return defaultMinISR, nil
}

// defaultMinISR is Kafka's min.insync.replicas default, and what a broker
// that does not report one is taken to have.
const defaultMinISR = 1

// brokerErrorName is the protocol name of a produce error code. Only the
// five that mark a Partition are named; any other is its number.
func brokerErrorName(code int16) string {
	switch code {
	case kerr.LeaderNotAvailable.Code:
		return "LEADER_NOT_AVAILABLE"
	case kerr.NotLeaderForPartition.Code:
		return "NOT_LEADER_OR_FOLLOWER"
	case kerr.RequestTimedOut.Code:
		return "REQUEST_TIMED_OUT"
	case kerr.NotEnoughReplicas.Code:
		return "NOT_ENOUGH_REPLICAS"
	case kerr.NotEnoughReplicasAfterAppend.Code:
		return "NOT_ENOUGH_REPLICAS_AFTER_APPEND"
	}
	return "ERROR_" + strconv.Itoa(int(code))
}
