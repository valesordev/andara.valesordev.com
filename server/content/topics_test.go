// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package content

import (
	"strings"
	"testing"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/kafkaclient"
)

// AW-SRV-058 AC-5a: the resolver refuses a missing topic, naming the option;
// there is no default name to fall back to.
func TestNewKafkaResolver_RequiresEveryTopic(t *testing.T) {
	full := Topics{Blobs: "it7.b", Versions: "it7.v", Active: "it7.a"}
	for _, c := range []struct {
		option string
		topics Topics
	}{
		{"KafkaOptions.Topics.Blobs", Topics{Versions: full.Versions, Active: full.Active}},
		{"KafkaOptions.Topics.Versions", Topics{Blobs: full.Blobs, Active: full.Active}},
		{"KafkaOptions.Topics.Active", Topics{Blobs: full.Blobs, Versions: full.Versions}},
	} {
		_, err := NewKafkaResolver(KafkaOptions{Kafka: kafkaclient.Config{Brokers: []string{"127.0.0.1:1"}}, Topics: c.topics})
		if err == nil || !strings.Contains(err.Error(), c.option) {
			t.Errorf("%s: err = %v, want one naming it", c.option, err)
		}
	}
	if _, err := NewKafkaResolver(KafkaOptions{Kafka: kafkaclient.Config{Brokers: []string{"127.0.0.1:1"}}, Topics: full}); err != nil {
		t.Errorf("all three topics: %v", err)
	}
}

func TestTopicsFrom_TakesTheBuildersNames(t *testing.T) {
	kt, err := kafkaclient.TopicsFor(config.Config{KafkaTopicPrefix: "it7"})
	if err != nil {
		t.Fatal(err)
	}
	got := TopicsFrom(kt)
	want := Topics{Blobs: "it7.andara.content.blobs.v1", Versions: "it7.andara.content.versions.v1", Active: "it7.andara.content.active.v1"}
	if got != want {
		t.Errorf("TopicsFrom = %+v, want %+v", got, want)
	}
}

// A missing blob names the blob topic the resolver reads, prefix included.
func TestErrBlobMissing_NamesTheResolvedTopic(t *testing.T) {
	err := &ErrBlobMissing{Hash: []byte{0xab}, Path: "zones/town.json", Topic: "it7.andara.content.blobs.v1"}
	if !strings.Contains(err.Error(), "it7.andara.content.blobs.v1") {
		t.Errorf("%q does not name the topic", err.Error())
	}
}
