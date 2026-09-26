// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/valesordev/andara/server/projector"
)

func noEnv(string) (string, bool) { return "", false }

// A projection is named; anything else is a usage error, exit 1, before any
// broker is touched.
func TestRun_ProjectionIsRequiredAndKnown(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "usage: andara-projector"},
		{[]string{"redis"}, "unknown projection redis"},
		{[]string{"state"}, "kafka.brokers"},
	} {
		var stderr bytes.Buffer
		if code := run(tc.args, noEnv, &stderr); code != 1 {
			t.Errorf("%v: exit %d, want 1", tc.args, code)
		}
		if !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("%v: stderr %q does not name %q", tc.args, stderr.String(), tc.want)
		}
	}
}

// andara_state_topic_bytes: a failing query is logged once when it starts
// failing and once when it recovers, and the gauge keeps its last good value
// rather than dropping to a silent 0 (AW-SRV-019 feedback §1).
func TestTopicBytesReportLogsAChangeOfState(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	m := projector.NewMetrics(nil)
	var r topicBytesReport
	boom := errors.New("broker gone")

	r.observe(700, nil, m, log)
	r.observe(0, boom, m, log)
	r.observe(0, boom, m, log)
	if got := testutil.ToFloat64(m.TopicBytes); got != 700 {
		t.Fatalf("gauge %v while failing, want the last good 700", got)
	}
	r.observe(900, nil, m, log)
	if got := testutil.ToFloat64(m.TopicBytes); got != 900 {
		t.Fatalf("gauge %v after recovery, want 900", got)
	}
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 1 || !strings.Contains(out, "broker gone") {
		t.Errorf("want one warn naming the failure, got:\n%s", out)
	}
	if n := strings.Count(out, "refreshed again"); n != 1 {
		t.Errorf("want one recovery line, got:\n%s", out)
	}
}
