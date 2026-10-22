// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package projector

import (
	"context"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/kafkaclient"
)

// AW-SRV-058 AC-5a: every constructor that takes a topic refuses an empty
// one, naming the option, before it reaches for a broker.
func TestConstructors_RequireTheirTopic(t *testing.T) {
	ctx := context.Background()
	dark := kafkaclient.Config{Brokers: []string{"127.0.0.1:1"}}
	full := RunOptions{Kafka: dark, Group: "it-g", CommandsTopic: "it7.c", EventsTopic: "it7.e", StateTopic: "it7.s"}
	for _, c := range []struct {
		option string
		call   func() error
	}{
		{"NewProducer topic", func() error { _, err := NewProducer(ctx, dark, ""); return err }},
		{"NewCommitter commandsTopic", func() error { _, err := NewCommitter(dark, "it-g", ""); return err }},
		{"TopicKeys topic", func() error { _, err := TopicKeys(ctx, dark, ""); return err }},
		{"NewBoundaryReader eventsTopic", func() error { _, err := NewBoundaryReader(ctx, dark, ""); return err }},
		{"NewCommandSource topic", func() error { _, err := NewCommandSource(ctx, dark, "", nil); return err }},
		{"RunOptions.CommandsTopic", func() error { o := full; o.CommandsTopic = ""; return Run(ctx, o) }},
		{"RunOptions.EventsTopic", func() error { o := full; o.EventsTopic = ""; return Run(ctx, o) }},
		{"RunOptions.StateTopic", func() error { o := full; o.StateTopic = ""; return Run(ctx, o) }},
	} {
		if err := c.call(); err == nil || !strings.Contains(err.Error(), c.option) {
			t.Errorf("%s: err = %v, want one naming %s", c.option, err, c.option)
		}
	}
}
