// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/valesordev/andara/server/kafkaclient"
)

// AW-SRV-044 AC-8 on the Active Pointer watch: a broker that refuses the
// principal the pointer topic is asked again once a minute, not every two
// seconds, and the watch writes no warn of its own (the kafkaclient error
// line, one a minute, is the signal). The server keeps serving what it has.
func TestWatch_ATopicRefusalBacksOffAMinuteWithoutWarning(t *testing.T) {
	topics := Topics{Blobs: "it7.blobs", Versions: "it7.versions", Active: "it7.active"}
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, topics.Blobs, topics.Versions, topics.Active))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var fetches atomic.Int32
	c.ControlKey(int16(kmsg.Fetch), func(req kmsg.Request) (kmsg.Response, error, bool) {
		fetches.Add(1)
		c.KeepControl()
		fr := req.(*kmsg.FetchRequest)
		resp := fr.ResponseKind().(*kmsg.FetchResponse)
		for _, rt := range fr.Topics {
			st := kmsg.NewFetchResponseTopic()
			st.Topic, st.TopicID = rt.Topic, rt.TopicID
			for _, rp := range rt.Partitions {
				sp := kmsg.NewFetchResponseTopicPartition()
				sp.Partition = rp.Partition
				sp.ErrorCode = kerr.TopicAuthorizationFailed.Code
				st.Partitions = append(st.Partitions, sp)
			}
			resp.Topics = append(resp.Topics, st)
		}
		return resp, nil, true
	})
	var logs bytes.Buffer
	kc := kafkaclient.Config{Brokers: c.ListenAddrs(), Metrics: kafkaclient.NewMetrics(prometheus.NewRegistry())}
	r, err := NewKafkaResolver(KafkaOptions{Kafka: kc, Topics: topics, Log: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := r.Watch(ctx); err != nil {
		t.Fatal(err)
	}
	// Longer than the ordinary backoff (2 s), well inside the minute. The
	// client fetches in the background on its own; what the watch controls
	// is how often it takes up a refusal, which Check counts.
	time.Sleep(2500 * time.Millisecond)
	if fetches.Load() == 0 {
		t.Fatal("the watch never fetched")
	}
	if strings.Contains(logs.String(), "content pointer watch: fetch failed") {
		t.Fatalf("the watch warned on a refusal the package already logs:\n%s", logs.String())
	}
	if got := testutil.ToFloat64(kc.Metrics.Failures.WithLabelValues(DefaultClientID+"-watch", kafkaclient.ReasonTopicUnauthorized)); got != 1 {
		t.Fatalf("the watch took up %v refusals in 2.5 s, want 1 (a minute's backoff)", got)
	}
}
