// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/sim"
)

// AC-6a and AC-8 at the source's decision: a lost password file is a
// connection's failure, recorded and reported as starvation while the fetch
// goes on; only the broker's refusal of the topic stops it, and Poll then
// returns the AuthError itself.
func TestKafkaSource_OnlyATopicRefusalStopsTheFetch(t *testing.T) {
	lost := fmt.Errorf("fetch: %w", &kafkaclient.AuthError{Reason: kafkaclient.ReasonPasswordFile, ClientID: "andara-server-sim",
		Principal: "andara-server-dev", Err: errors.New("kafka.sasl_password_file /f: no such file or directory")})
	refused := fmt.Errorf("fetch: %w", &kafkaclient.AuthError{Reason: kafkaclient.ReasonTopicUnauthorized, ClientID: "andara-server-sim",
		Principal: "andara-server-dev", Topic: "dev.andara.commands.v1", Err: errors.New("TOPIC_AUTHORIZATION_FAILED")})

	s := &KafkaSource{buffers: map[int32][]sim.Record{}}
	if s.refuse(lost) {
		t.Fatal("a lost password file stopped the fetch")
	}
	s.fetchErr = lost // what fetch records for an error it doesn't stop on
	_, err := s.Poll(nil, 10)
	if !errors.Is(err, ErrStarved) || kafkaclient.ErrorName(err) != kafkaclient.ReasonPasswordFile {
		t.Fatalf("Poll after a lost password file = %v, want starvation carrying it", err)
	}

	if !s.refuse(refused) {
		t.Fatal("a topic refusal did not stop the fetch")
	}
	_, err = s.Poll(nil, 10)
	if errors.Is(err, ErrStarved) || !kafkaclient.Refused(err) {
		t.Fatalf("Poll after a refusal = %v, want the refusal itself", err)
	}
}

// AC-8 through a consumer: when the broker refuses the principal the
// commands topic on fetch, the tick loop's source reports a
// *kafkaclient.AuthError (not starvation) naming the topic and the
// principal, counts it once, and stops fetching instead of retrying.
func TestKafkaSource_ATopicRefusalStopsTheFetchAsAnAuthError(t *testing.T) {
	const topic = "it7.andara.commands.v1"
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.EnableSASL(),
		kfake.Superuser(kafkaclient.Mechanism, "andara-server-dev", "pw"), kfake.SeedTopics(1, topic))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pw := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pw, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	kc := kafkaclient.Config{Brokers: c.ListenAddrs(), SASLUsername: "andara-server-dev", SASLPasswordFile: pw,
		Metrics: kafkaclient.NewMetrics(prometheus.NewRegistry())}

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

	src, err := NewKafkaSource(context.Background(), KafkaSourceOptions{
		Kafka: kc, ClientID: "andara-server", Group: "it-auth", Topic: topic, Start: map[int32]int64{0: 0}, LagEvery: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	var pollErr error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, pollErr = src.Poll(nil, 10); kafkaclient.ErrorName(pollErr) != "" {
			break
		}
	}
	var ae *kafkaclient.AuthError
	if !errors.As(pollErr, &ae) || ae.Reason != kafkaclient.ReasonTopicUnauthorized || ae.Topic != topic || ae.Principal != "andara-server-dev" ||
		errors.Is(pollErr, ErrStarved) {
		t.Fatalf("Poll = %v, want a topic_unauthorized AuthError on %s as andara-server-dev, not starvation", pollErr, topic)
	}
	// Stopped, not retrying: no further fetch reaches the broker, and the
	// refusal is counted once.
	seen := fetches.Load()
	time.Sleep(500 * time.Millisecond)
	if more := fetches.Load() - seen; more > 0 {
		t.Fatalf("%d more fetches after the refusal: the source keeps retrying", more)
	}
	if got := testutil.ToFloat64(kc.Metrics.Failures.WithLabelValues("andara-server-sim", kafkaclient.ReasonTopicUnauthorized)); got != 1 {
		t.Fatalf("topic_unauthorized = %v, want 1", got)
	}
}
