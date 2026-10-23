// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// saslCluster is an in-process broker that speaks SCRAM-SHA-512 and knows one
// user, u, with password p1.
func saslCluster(t *testing.T) *kfake.Cluster {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.EnableSASL(), kfake.Superuser(Mechanism, "u", "p1"), kfake.SeedTopics(1, "t"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func saslConfig(t *testing.T, c *kfake.Cluster, password string) (Config, string, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	path := passwordFile(t, password)
	cfg := Config{
		Brokers: c.ListenAddrs(), ClientID: "andara-it", SASLUsername: "u", SASLPasswordFile: path,
		Metrics: NewMetrics(nil), Log: slog.New(slog.NewJSONHandler(&logs, nil)),
	}
	return cfg, path, &logs
}

func ping(cl *kgo.Client) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return cl.Ping(ctx)
}

// pingUntil pings until done accepts the result, for at most 5 s: the
// request that meets a dropped connection fails with the drop itself, and a
// later one opens a new connection.
func pingUntil(cl *kgo.Client, done func(error) bool) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := ping(cl)
		if done(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// dropNextMetadata has the broker close the connection that sends the next
// Metadata request: the client must open, and authenticate, a new one.
func dropNextMetadata(c *kfake.Cluster) {
	c.ControlKey(int16(kmsg.Metadata), func(kmsg.Request) (kmsg.Response, error, bool) {
		return nil, errors.New("kfake: drop the connection"), true
	})
}

// refuseNextAuthentication answers the next SASLAuthenticate as Kafka answers
// a wrong password: SASL_AUTHENTICATION_FAILED. (kfake itself closes the
// connection instead, which a client can't tell from any other drop.)
func refuseNextAuthentication(c *kfake.Cluster) {
	c.ControlKey(int16(kmsg.SASLAuthenticate), func(req kmsg.Request) (kmsg.Response, error, bool) {
		resp := req.(*kmsg.SASLAuthenticateRequest).ResponseKind().(*kmsg.SASLAuthenticateResponse)
		resp.ErrorCode = kerr.SaslAuthenticationFailed.Code
		msg := "Authentication failed during authentication due to invalid credentials with SASL mechanism SCRAM-SHA-512"
		resp.ErrorMessage = &msg
		return resp, nil, true
	})
}

func rotate(t *testing.T, cl *kgo.Client, password string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := kadm.NewClient(cl).AlterUserSCRAMs(ctx, nil, []kadm.UpsertSCRAM{{User: "u", Mechanism: kadm.ScramSha512, Iterations: 4096, Password: password}})
	if err == nil {
		err = res["u"].Err
	}
	if err != nil {
		t.Fatalf("rotate u's password: %v", err)
	}
}

// AC-2: with a username and a password file, the client authenticates with
// SCRAM-SHA-512 as that user, and the password never reaches a log line. The
// broker's refusal of the handshake is counted as sasl_failed.
func TestSASL_AuthenticatesAsTheUserWithThePasswordFromTheFile(t *testing.T) {
	t.Parallel()
	c := saslCluster(t)
	cfg, _, logs := saslConfig(t, c, "p1\n")
	cl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := ping(cl); err != nil {
		t.Fatalf("Ping as u: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := cl.ProduceSync(ctx, &kgo.Record{Topic: "t", Value: []byte("v")}).FirstErr(); err != nil {
		t.Fatalf("produce as u: %v", err)
	}

	bad, _, badLogs := saslConfig(t, c, "wrong")
	bcl, err := New(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer bcl.Close()
	refuseNextAuthentication(c)
	if err := ping(bcl); !errors.Is(err, kerr.SaslAuthenticationFailed) {
		t.Fatalf("Ping refused at the handshake: %v, want SASL_AUTHENTICATION_FAILED", err)
	}
	if got := testutil.ToFloat64(bad.Metrics.Failures.WithLabelValues("andara-it", ReasonSASLFailed)); got != 1 {
		t.Fatalf("sasl_failed = %v after one refused handshake, want 1", got)
	}
	line := badLogs.String()
	for _, f := range []string{`"reason":"sasl_failed"`, `"principal":"u"`, `"broker":"127.0.0.1:`} {
		if !strings.Contains(line, f) {
			t.Errorf("error line lacks %s: %s", f, line)
		}
	}
	if strings.Contains(logs.String()+line, "p1") || strings.Contains(line, "wrong") {
		t.Fatalf("a password is in the log:\n%s%s", logs.String(), line)
	}
}

// AC-3: the password is read on each new connection. Rotated at the broker
// and rewritten in the file, the next connection authenticates with the new
// one, with no restart.
func TestSASL_TheNextConnectionUsesTheRewrittenPassword(t *testing.T) {
	t.Parallel()
	c := saslCluster(t)
	cfg, path, logs := saslConfig(t, c, "p1")
	// SCRAM authenticates a connection once, so only a new connection reads
	// the file. The client holds one per broker and purpose; closing them all
	// after a second idle stands in for the broker dropping each.
	cl, err := New(cfg, kgo.ConnIdleTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := ping(cl); err != nil {
		t.Fatal(err)
	}
	rotate(t, cl, "p2")

	// Once the connections are gone (no traffic for over a second, and the
	// reaper's next pass), the file still holds p1, which the broker no
	// longer accepts: the reconnect fails. Pinging meanwhile would keep them.
	time.Sleep(2500 * time.Millisecond)
	if err := ping(cl); err == nil {
		t.Fatal("a reconnect with the old password in the file succeeded")
	}

	if err := os.WriteFile(path, []byte("p2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pingUntil(cl, func(err error) bool { return err == nil }); err != nil {
		t.Fatalf("Ping after the file was rewritten: %v", err)
	}
	if strings.Contains(logs.String(), "p1") || strings.Contains(logs.String(), "p2") {
		t.Fatalf("a password is in the log:\n%s", logs.String())
	}
}

// AC-6a: a running client whose password file goes missing fails the new
// connection with an error naming the path, counted as password_file, and
// does not exit; once the file is back, the next connection uses it.
func TestSASL_APasswordFileLostAtRuntimeFailsOnlyTheConnection(t *testing.T) {
	t.Parallel()
	c := saslCluster(t)
	cfg, path, logs := saslConfig(t, c, "p1")
	cl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := ping(cl); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	dropNextMetadata(c)
	err = pingUntil(cl, func(err error) bool { return ErrorName(err) != "" })
	var ae *AuthError
	if !errors.As(err, &ae) || ae.Reason != ReasonPasswordFile || !strings.Contains(err.Error(), path) {
		t.Fatalf("Ping with the file gone: %v, want a password_file AuthError naming %s", err, path)
	}
	if got := testutil.ToFloat64(cfg.Metrics.Failures.WithLabelValues("andara-it", ReasonPasswordFile)); got < 1 {
		t.Fatalf("password_file = %v, want at least 1", got)
	}
	if !strings.Contains(logs.String(), `"reason":"password_file"`) || !strings.Contains(logs.String(), path) {
		t.Fatalf("no error line naming the file:\n%s", logs.String())
	}
	if err := os.WriteFile(path, []byte("p1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pingUntil(cl, func(err error) bool { return err == nil }); err != nil {
		t.Fatalf("Ping after the file is back: %v", err)
	}
}
