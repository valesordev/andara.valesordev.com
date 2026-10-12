// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/kafkaclient"
	"github.com/valesordev/andara/server/telemetry"
	"github.com/valesordev/andara/server/tickloop"
)

// topicLines resolves cfg's topics and returns the lines ResolveTopics logged.
func topicLines(t *testing.T, cfg config.Config) (kafkaclient.Topics, []map[string]any, error) {
	t.Helper()
	var buf bytes.Buffer
	tp, err := ResolveTopics(cfg, slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	var lines []map[string]any
	sc := bufio.NewScanner(&buf)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	return tp, lines, err
}

func atLevel(lines []map[string]any, level string) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["level"] == level {
			out = append(out, l)
		}
	}
	return out
}

// AC-3 and AC-9: prod with no prefix keeps today's names, logs the info line
// and no warn.
func TestResolveTopics_ProdUnprefixedLogsNoWarn(t *testing.T) {
	tp, lines, err := topicLines(t, config.Config{Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if tp.Commands() != "andara.commands.v1" || tp.State() != "andara.state.v1" {
		t.Errorf("commands %q state %q", tp.Commands(), tp.State())
	}
	if w := atLevel(lines, "WARN"); len(w) != 0 {
		t.Errorf("warn lines %v, want none", w)
	}
	info := atLevel(lines, "INFO")
	if len(info) != 1 {
		t.Fatalf("info lines %v, want one", info)
	}
	if info[0]["kafka.topic_prefix"] != "" || info[0]["environment"] != "prod" || info[0]["commands_topic"] != "andara.commands.v1" {
		t.Errorf("info line %v", info[0])
	}
}

// AC-4: an empty prefix outside prod is exactly one warn naming the key and
// the environment, and startup goes on with the unprefixed names.
func TestResolveTopics_EmptyPrefixOutsideProdWarnsOnce(t *testing.T) {
	for _, env := range []string{"dev", "local", config.DefaultEnvironment} {
		tp, lines, err := topicLines(t, config.Config{Environment: env})
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		if tp.Commands() != "andara.commands.v1" {
			t.Errorf("%s: commands %q", env, tp.Commands())
		}
		w := atLevel(lines, "WARN")
		if len(w) != 1 {
			t.Fatalf("%s: warn lines %v, want one", env, w)
		}
		if _, ok := w[0]["kafka.topic_prefix"]; !ok || w[0]["environment"] != env {
			t.Errorf("%s: warn line %v does not name kafka.topic_prefix and the environment", env, w[0])
		}
	}
}

// AC-9: a prefixed start names the prefix, the environment and the resolved
// commands topic, and does not warn.
func TestResolveTopics_PrefixedLogsTheResolvedCommandsTopic(t *testing.T) {
	_, lines, err := topicLines(t, config.Config{Environment: "dev", KafkaTopicPrefix: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	info := atLevel(lines, "INFO")
	if len(info) != 1 || info[0]["kafka.topic_prefix"] != "dev" || info[0]["environment"] != "dev" || info[0]["commands_topic"] != "dev.andara.commands.v1" {
		t.Errorf("info lines %v", info)
	}
	if w := atLevel(lines, "WARN"); len(w) != 0 {
		t.Errorf("warn lines %v, want none", w)
	}
}

// AC-8: a malformed prefix is an error line naming the key, and an error.
func TestResolveTopics_MalformedPrefixIsAnErrorLine(t *testing.T) {
	_, lines, err := topicLines(t, config.Config{Environment: "dev", KafkaTopicPrefix: "dev."})
	if err == nil {
		t.Fatal("resolved")
	}
	e := atLevel(lines, "ERROR")
	if len(e) != 1 || !strings.Contains(e[0]["detail"].(string), "kafka.topic_prefix") {
		t.Errorf("error lines %v, want one naming kafka.topic_prefix", e)
	}
}

// The boot path passes the configured prefix to the builder: every topic the
// Runtime hands down, content and audit included, carries it (Amendment 17's
// unit test; no process is booted against a broker).
func TestNew_FillsEveryTopicFromThePrefix(t *testing.T) {
	cfg := config.Config{ServiceName: "andara-server", Environment: "dev", LogLevel: "info", KafkaTopicPrefix: "it7"}
	tel := telemetry.Setup(cfg, &bytes.Buffer{})
	t.Cleanup(func() { tel.Shutdown(context.Background()) })
	rt := mustNew(t, cfg, tel)
	if rt.Topics.Commands() != "it7.andara.commands.v1" || rt.Topics.Events() != "it7.andara.events.v1" {
		t.Errorf("topics %q %q", rt.Topics.Commands(), rt.Topics.Events())
	}
	want := content.Topics{Blobs: "it7.andara.content.blobs.v1", Versions: "it7.andara.content.versions.v1", Active: "it7.andara.content.active.v1"}
	if rt.ContentTopics != want {
		t.Errorf("content topics %+v", rt.ContentTopics)
	}
	if rt.AuditTopicName != "it7.andara.audit.v1" {
		t.Errorf("audit %q", rt.AuditTopicName)
	}
	if got := rt.recoveryLog().(kafkaLog).topics; got != rt.Topics {
		t.Errorf("recovery reads %+v", got)
	}
}

func TestNew_RefusesAMalformedPrefix(t *testing.T) {
	cfg := config.Config{ServiceName: "andara-server", Environment: "dev", LogLevel: "info", KafkaTopicPrefix: "Dev"}
	tel := telemetry.Setup(cfg, &bytes.Buffer{})
	t.Cleanup(func() { tel.Shutdown(context.Background()) })
	if _, err := New(cfg, tel); err == nil || !strings.Contains(err.Error(), "kafka.topic_prefix") {
		t.Errorf("err = %v", err)
	}
}

// AC-5b: under a prefix, a failed produce to the commands topic counts as
// kind=commands.
func TestPublishFailureKind_FollowsTheResolvedCommandsTopic(t *testing.T) {
	tp, err := kafkaclient.TopicsFor(config.Config{KafkaTopicPrefix: "it7"})
	if err != nil {
		t.Fatal(err)
	}
	kp := &tickloop.KafkaPublisher{Commands: tp.Commands(), Events: tp.Events()}
	if got := publishFailureKind(kp, "it7.andara.commands.v1"); got != "commands" {
		t.Errorf("commands topic: kind %q", got)
	}
	if got := publishFailureKind(kp, "it7.andara.events.v1"); got != "events" {
		t.Errorf("events topic: kind %q", got)
	}
	if got := publishFailureKind(kp, "andara.commands.v1"); got != "events" {
		t.Errorf("the unprefixed name is not this publisher's commands topic: kind %q", got)
	}
}
