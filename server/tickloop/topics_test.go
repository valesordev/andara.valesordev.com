// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package tickloop

import (
	"context"
	"strings"
	"testing"
)

// AW-SRV-058 AC-5a: every constructor that takes a topic refuses an empty
// one, naming the option, before it reaches for a broker.
func TestConstructors_RequireTheirTopic(t *testing.T) {
	ctx := context.Background()
	dark := []string{"127.0.0.1:1"}
	for _, c := range []struct {
		option string
		call   func() error
	}{
		{"KafkaSourceOptions.Topic", func() error {
			_, err := NewKafkaSource(ctx, KafkaSourceOptions{Brokers: dark, Group: "it-g", Start: map[int32]int64{0: 0}})
			return err
		}},
		{"commandsTopic", func() error {
			_, err := NewKafkaPublisher(ctx, dark, "andara-server", "", "it7.events")
			return err
		}},
		{"eventsTopic", func() error {
			_, err := NewKafkaPublisher(ctx, dark, "andara-server", "it7.commands", "")
			return err
		}},
		{"ReadBoundaries eventsTopic", func() error {
			_, err := ReadBoundaries(ctx, dark, "")
			return err
		}},
		{"KafkaRecords.Topic", func() error {
			_, err := KafkaRecords{Brokers: dark}.Fetch(0, 0, 1)
			return err
		}},
		{"EndOffset topic", func() error {
			_, err := EndOffset(ctx, dark, "", 0)
			return err
		}},
		{"NewBoundaryReader eventsTopic", func() error {
			_, err := NewBoundaryReader(ctx, dark, "", "andara-server-recovery")
			return err
		}},
		{"NewCommandSource topic", func() error {
			_, err := NewCommandSource(ctx, dark, "", "andara-server-recovery", map[int32]int64{0: 0})
			return err
		}},
	} {
		if err := c.call(); err == nil || !strings.Contains(err.Error(), c.option) {
			t.Errorf("%s: err = %v, want one naming %s", c.option, err, c.option)
		}
	}
}
