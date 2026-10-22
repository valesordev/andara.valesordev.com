// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"

	"github.com/valesordev/andara/server/config"
)

const secret = "s3cret-not-to-be-seen"

func passwordFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// AC-5, AC-6: with a username, a password file that is missing, unreadable or
// empty fails the boot check naming the path, and no error carries the
// password.
func TestValidate_ThePasswordFileMustHoldAPassword(t *testing.T) {
	t.Parallel()
	if err := (Config{}).Validate(); err != nil {
		t.Fatalf("no username: %v", err)
	}
	if err := (Config{SASLUsername: "u", SASLPasswordFile: passwordFile(t, secret+"\n")}).Validate(); err != nil {
		t.Fatalf("a readable password: %v", err)
	}
	unreadable := passwordFile(t, secret)
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing": filepath.Join(t.TempDir(), "absent"),
		"empty":   passwordFile(t, ""),
		"newline": passwordFile(t, "\n"),
	}
	if os.Geteuid() != 0 { // root reads a mode-0 file
		cases["unreadable"] = unreadable
	}
	for name, path := range cases {
		err := (Config{SASLUsername: "u", SASLPasswordFile: path}).Validate()
		if err == nil || !strings.Contains(err.Error(), path) || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: err = %v, want one naming %s and not the password", name, err, path)
		}
	}
	if err := (Config{SASLUsername: "u"}).Validate(); err == nil || !strings.Contains(err.Error(), "kafka.sasl_password_file") {
		t.Errorf("no password file configured: err = %v", err)
	}
}

func TestNew_RefusesAConfigWithoutBrokersClientIDOrPasswordFile(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]Config{
		"no brokers":       {ClientID: "andara-server"},
		"no client id":     {Brokers: []string{dark}},
		"no password file": {Brokers: []string{dark}, ClientID: "andara-server", SASLUsername: "u"},
	} {
		if cl, err := New(c); err == nil {
			cl.Close()
			t.Errorf("%s: New built a client", name)
		}
	}
}

// AC-4: no username, no SASL; a username, SCRAM-SHA-512 and nothing else.
func TestNew_SASLOnlyWithAUsername(t *testing.T) {
	t.Parallel()
	plain, err := New(Config{Brokers: []string{dark}, ClientID: "andara-server"}, kgo.Dialer(refuse))
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if ms, _ := plain.OptValue(kgo.SASL).([]sasl.Mechanism); len(ms) != 0 {
		t.Fatalf("no username, SASL mechanisms %v", ms)
	}
	authed, err := New(Config{Brokers: []string{dark}, ClientID: "andara-server", SASLUsername: "u", SASLPasswordFile: "/f"}, kgo.Dialer(refuse))
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Close()
	ms, _ := authed.OptValue(kgo.SASL).([]sasl.Mechanism)
	if len(ms) != 1 || ms[0].Name() != "SCRAM-SHA-512" {
		t.Fatalf("with a username, SASL mechanisms %v, want SCRAM-SHA-512 alone", ms)
	}
}

// AC-11: every connection goes through the caller's Dialer, and a tapped
// client refuses DialTLSConfig, naming both.
func TestNew_TheDialerCarriesEveryConnectionAndRefusesTLSBesideIt(t *testing.T) {
	t.Parallel()
	var dials atomic.Int32
	dialer := func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("tapped")
	}
	cl, err := New(Config{Brokers: []string{dark}, ClientID: "andara-server-ingress", Dialer: dialer})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = cl.Ping(ctx)
	cl.Close()
	if dials.Load() == 0 {
		t.Fatal("the client dialed around the Dialer")
	}

	_, err = New(Config{Brokers: []string{dark}, ClientID: "andara-server-ingress", Dialer: dialer}, kgo.DialTLSConfig(&tls.Config{}))
	if err == nil || !strings.Contains(err.Error(), "Dialer") || !strings.Contains(err.Error(), "DialTLSConfig") {
		t.Fatalf("a tapped client with DialTLSConfig: err = %v, want one naming both", err)
	}
}

// The counter's three series exist at 0 once a client is built, Check counts
// a topic refusal once, and nil Metrics counts nothing.
func TestCheck_CountsATopicRefusalOnceAsAnAuthError(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	c := Config{Brokers: []string{dark}, ClientID: "andara-server-tick", SASLUsername: "andara-server-dev", SASLPasswordFile: "/f", Metrics: m}
	cl, err := New(c, kgo.Dialer(refuse))
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()
	if n := testutil.CollectAndCount(m.Failures); n != 3 {
		t.Fatalf("%d series after New, want 3 at 0", n)
	}

	refused := fmt.Errorf("produce: %w", kerr.TopicAuthorizationFailed)
	err = c.Check("dev.andara.commands.v1", refused)
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Reason != ReasonTopicUnauthorized || ae.Topic != "dev.andara.commands.v1" ||
		ae.Principal != "andara-server-dev" || ae.ClientID != "andara-server-tick" || !errors.Is(err, kerr.TopicAuthorizationFailed) {
		t.Fatalf("Check = %#v", err)
	}
	if again := c.Check("dev.andara.commands.v1", err); again != err {
		t.Fatalf("Check of an AuthError = %v, want it unchanged", again)
	}
	if got := testutil.ToFloat64(m.Failures.WithLabelValues("andara-server-tick", ReasonTopicUnauthorized)); got != 1 {
		t.Fatalf("topic_unauthorized = %v, want 1", got)
	}
	other := errors.New("broker gone")
	if got := c.Check("t", other); got != other {
		t.Fatalf("Check of another error = %v, want it unchanged", got)
	}
	if got := c.Check("t", nil); got != nil {
		t.Fatalf("Check(nil) = %v", got)
	}
	if ErrorName(err) != ReasonTopicUnauthorized || ErrorName(other) != "" {
		t.Fatalf("ErrorName: %q, %q", ErrorName(err), ErrorName(other))
	}
	nm := Config{ClientID: "andara-server"}
	if err := nm.Check("t", kerr.TopicAuthorizationFailed); ErrorName(err) != ReasonTopicUnauthorized {
		t.Fatalf("nil Metrics: Check = %v", err)
	}
}

// One error line per client ID and reason per minute, carrying how many it
// held back; every failure is counted.
func TestFailure_LogsAtMostOneLinePerMinuteWithTheSuppressedCount(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	m := NewMetrics(nil)
	now := time.Unix(1_800_000_000, 0)
	m.now = func() time.Time { return now }
	c := Config{ClientID: "andara-server-tick", SASLUsername: "u", Metrics: m, Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	fail := func() { c.Check("t", kerr.TopicAuthorizationFailed) }

	fail()
	fail()
	fail()
	now = now.Add(59 * time.Second)
	fail()
	if n := strings.Count(logs.String(), `"msg":"kafka client authentication failed"`); n != 1 {
		t.Fatalf("%d lines within the minute, want 1:\n%s", n, logs.String())
	}
	now = now.Add(2 * time.Second)
	fail()
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"suppressed":3`) {
		t.Fatalf("after the minute, want a second line with suppressed=3:\n%s", logs.String())
	}
	for _, f := range []string{`"level":"ERROR"`, `"client_id":"andara-server-tick"`, `"reason":"topic_unauthorized"`, `"principal":"u"`, `"topic":"t"`, `"broker":`, `"error":`} {
		if !strings.Contains(lines[0], f) {
			t.Errorf("error line lacks %s: %s", f, lines[0])
		}
	}
	if got := testutil.ToFloat64(m.Failures.WithLabelValues("andara-server-tick", ReasonTopicUnauthorized)); got != 5 {
		t.Fatalf("counted %v, want every failure (5)", got)
	}
}

// `kafka client config` once per client ID per process, with no password.
func TestNew_LogsTheClientConfigOncePerClientID(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	c := Config{Brokers: []string{dark}, SASLUsername: "andara-server-dev", SASLPasswordFile: passwordFile(t, secret), Metrics: NewMetrics(nil), Log: slog.New(slog.NewJSONHandler(&logs, nil))}
	for _, id := range []string{"andara-server-tick", "andara-server-tick", "andara-server-sim"} {
		cl, err := New(c.For(id), kgo.Dialer(refuse))
		if err != nil {
			t.Fatal(err)
		}
		cl.Close()
	}
	out := logs.String()
	if n := strings.Count(out, `"msg":"kafka client config"`); n != 2 {
		t.Fatalf("%d config lines for two client IDs, want 2:\n%s", n, out)
	}
	for _, f := range []string{`"client_id":"andara-server-tick"`, `"sasl":true`, `"mechanism":"SCRAM-SHA-512"`, `"principal":"andara-server-dev"`} {
		if !strings.Contains(out, f) {
			t.Errorf("config line lacks %s:\n%s", f, out)
		}
	}
	if strings.Contains(out, secret) {
		t.Fatalf("the password is in the log:\n%s", out)
	}
	logs.Reset()
	cl, err := New(Config{Brokers: []string{dark}, ClientID: "andara-server", Log: slog.New(slog.NewJSONHandler(&logs, nil))}, kgo.Dialer(refuse))
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()
	if !strings.Contains(logs.String(), `"sasl":false`) || !strings.Contains(logs.String(), `"mechanism":"none"`) || !strings.Contains(logs.String(), `"principal":"none"`) {
		t.Fatalf("no username: %s", logs.String())
	}
}

// AC-6a at the mechanism: a new connection that finds the file gone fails
// with password_file naming the path, counted once, and the connect hook does
// not count it again.
func TestMechanism_APasswordFileGoneFailsTheConnectionCountedOnce(t *testing.T) {
	t.Parallel()
	m := NewMetrics(nil)
	path := filepath.Join(t.TempDir(), "password")
	c := Config{ClientID: "andara-server-tick", SASLUsername: "u", SASLPasswordFile: path, Metrics: m}
	_, _, err := mechanism{c}.Authenticate(context.Background(), "broker:9092")
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Reason != ReasonPasswordFile || !strings.Contains(err.Error(), path) {
		t.Fatalf("Authenticate = %v, want a password_file AuthError naming %s", err, path)
	}
	connectHook{c}.OnBrokerConnect(kgo.BrokerMetadata{Host: "broker", Port: 9092}, 0, nil, err)
	if got := testutil.ToFloat64(m.Failures.WithLabelValues("andara-server-tick", ReasonPasswordFile)); got != 1 {
		t.Fatalf("password_file = %v, want 1", got)
	}
	connectHook{c}.OnBrokerConnect(kgo.BrokerMetadata{Host: "broker", Port: 9092}, 0, nil, fmt.Errorf("x: %w", kerr.SaslAuthenticationFailed))
	if got := testutil.ToFloat64(m.Failures.WithLabelValues("andara-server-tick", ReasonSASLFailed)); got != 1 {
		t.Fatalf("sasl_failed = %v, want 1", got)
	}
	connectHook{c}.OnBrokerConnect(kgo.BrokerMetadata{}, 0, nil, errors.New("connection refused"))
	if got := testutil.ToFloat64(m.Failures.WithLabelValues("andara-server-tick", ReasonSASLFailed)); got != 1 {
		t.Fatalf("a refused dial counted as sasl_failed: %v", got)
	}
}

// The config package can't import this one, so it carries the default path
// as a literal of its own; the two must agree.
func TestDefaultPasswordFile_MatchesTheConfigDefault(t *testing.T) {
	if DefaultPasswordFile != config.DefaultKafkaSASLPasswordFile {
		t.Fatalf("kafkaclient %q, config %q", DefaultPasswordFile, config.DefaultKafkaSASLPasswordFile)
	}
}

func refuse(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("kafkaclient test: no dial")
}
