// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package kafkaclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

// Mechanism is the one SASL mechanism a client speaks (ADR-0011 §6). There is
// no configuration to pick another.
const Mechanism = "SCRAM-SHA-512"

// DefaultPasswordFile is kafka.sasl_password_file's default: the workload's
// Secret mount (ADR-0011 decision 6) and the key Strimzi's User Operator
// writes the password under.
const DefaultPasswordFile = "/etc/andara/secrets/kafka/password"

// Config is what every Kafka client in the server, the projector and their
// tests is built from (AW-SRV-044, ADR-0011 §7). A process builds one with its
// brokers, credentials, metrics and log, and each site names its client with
// For.
type Config struct {
	Brokers []string // kafka.brokers
	// ClientID is the client's client.id, set per site with For
	// (client-contract.md's rule: a principal and purpose suffixes).
	ClientID string
	// SASLUsername is kafka.sasl_username, the principal. Empty means no SASL.
	SASLUsername string
	// SASLPasswordFile is kafka.sasl_password_file, read on each new
	// connection so a rotated password needs no restart.
	SASLPasswordFile string
	// Dialer replaces the client's dialer: every connection, the SASL
	// handshake included, goes through it. The ingress tap composes here, and
	// TLS, when it comes, lives inside it (client-contract.md).
	Dialer func(ctx context.Context, network, host string) (net.Conn, error)
	// Metrics counts authentication failures; nil counts nothing (the admin
	// CLI, unit tests).
	Metrics *Metrics
	// Log receives the client config line and the authentication error
	// lines; nil discards them.
	Log *slog.Logger
}

// For is c naming its client clientID.
func (c Config) For(clientID string) Config {
	c.ClientID = clientID
	return c
}

// SASL reports whether the client authenticates.
func (c Config) SASL() bool { return c.SASLUsername != "" }

// principal is the name a log line or error gives the client's principal:
// the username, never the password.
func (c Config) principal() string {
	if c.SASLUsername == "" {
		return "none"
	}
	return c.SASLUsername
}

// Validate is the boot check of a process's Kafka credentials (AC-5, AC-6):
// with a username, the password file must exist, be readable and hold a
// password. The error names the file, never its contents. A process calls it
// before it builds any client and exits 1 on an error; after boot, a password
// file that goes missing fails only the connection that needed it (AC-6a).
func (c Config) Validate() error {
	if !c.SASL() {
		return nil
	}
	if c.SASLPasswordFile == "" {
		return errors.New("kafka.sasl_username is set but kafka.sasl_password_file is empty (ANDARA_KAFKA_SASL_PASSWORD_FILE)")
	}
	_, err := readPassword(c.SASLPasswordFile)
	return err
}

// readPassword reads the password file. A trailing newline is not part of the
// password; an empty file is an error. The error names the path only.
func readPassword(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			// The path error's own text is "open <path>: <cause>": the path
			// and why, nothing read.
			return "", fmt.Errorf("kafka.sasl_password_file %s: %w", path, pe.Err)
		}
		return "", fmt.Errorf("kafka.sasl_password_file %s: unreadable", path)
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if pw == "" {
		return "", fmt.Errorf("kafka.sasl_password_file %s is empty", path)
	}
	return pw, nil
}

// New builds c's client with the shared options and then opts. It checks the
// configuration's shape and dials nothing; it does not read the password
// file, which each new connection reads (AC-3, AC-6a).
func New(c Config, opts ...kgo.Opt) (*kgo.Client, error) {
	switch {
	case len(c.Brokers) == 0:
		return nil, errors.New("kafkaclient: no brokers configured (kafka.brokers)")
	case c.ClientID == "":
		return nil, errors.New("kafkaclient: a client needs a ClientID (client-contract.md)")
	case c.SASL() && c.SASLPasswordFile == "":
		return nil, fmt.Errorf("kafkaclient: client %s: kafka.sasl_username is set but kafka.sasl_password_file is empty", c.ClientID)
	}
	c.Metrics.zero(c.ClientID)
	c.logConfig()
	cl, err := kgo.NewClient(Options(c, opts...)...)
	if err != nil {
		if c.Dialer != nil && strings.Contains(err.Error(), "DialTLSConfig") {
			return nil, fmt.Errorf("kafkaclient: client %s has a Dialer (a tapped client) and kgo.DialTLSConfig; TLS over a tapped connection blinds the tap, so it belongs inside the Dialer: %w", c.ClientID, err)
		}
		return nil, fmt.Errorf("kafkaclient: client %s: %w", c.ClientID, err)
	}
	return cl, nil
}

// Options is the option list New builds c's client with: the seed brokers, the
// client ID, the Dialer, SASL and the authentication hook, then opts.
// config-assert reads a site's settings back from it.
func Options(c Config, opts ...kgo.Opt) []kgo.Opt {
	out := []kgo.Opt{kgo.SeedBrokers(c.Brokers...), kgo.ClientID(c.ClientID), kgo.WithHooks(connectHook{c})}
	if c.Dialer != nil {
		out = append(out, kgo.Dialer(c.Dialer))
	}
	if c.SASL() {
		out = append(out, kgo.SASL(mechanism{c}))
	}
	return append(out, opts...)
}

// Build is New for a site's options helper, which returns its named Config
// and its options together: Build(helper(...)).
func Build(c Config, opts []kgo.Opt) (*kgo.Client, error) { return New(c, opts...) }

// SiteOpts is Options for a site's options helper: the list config-assert
// reads back, the one Build builds with.
func SiteOpts(c Config, opts []kgo.Opt) []kgo.Opt { return Options(c, opts...) }

// logConfig writes `kafka client config` once per client ID per process.
func (c Config) logConfig() {
	if c.Log == nil || !c.Metrics.firstConfig(c.ClientID) {
		return
	}
	mech := "none"
	if c.SASL() {
		mech = Mechanism
	}
	c.Log.LogAttrs(context.Background(), slog.LevelInfo, "kafka client config",
		slog.String("client_id", c.ClientID),
		slog.Bool("sasl", c.SASL()),
		slog.String("mechanism", mech),
		slog.String("principal", c.principal()))
}

// mechanism is SCRAM-SHA-512 with the password read from the file each time a
// connection authenticates. A file that can't be read fails that connection
// only, counted as password_file; the client retries on its own backoff.
type mechanism struct{ c Config }

func (m mechanism) Name() string { return Mechanism }

func (m mechanism) Authenticate(ctx context.Context, host string) (sasl.Session, []byte, error) {
	pw, err := readPassword(m.c.SASLPasswordFile)
	if err != nil {
		ae := &AuthError{Reason: ReasonPasswordFile, ClientID: m.c.ClientID, Principal: m.c.principal(), Err: err}
		m.c.failure(ae, host)
		return nil, nil, ae
	}
	return scram.Auth{User: m.c.SASLUsername, Pass: pw}.AsSha512Mechanism().Authenticate(ctx, host)
}

// connectHook sees every connection's outcome, the SASL handshake included,
// and counts the broker's refusal of it.
type connectHook struct{ c Config }

var _ kgo.HookBrokerConnect = connectHook{}

func (h connectHook) OnBrokerConnect(meta kgo.BrokerMetadata, _ time.Duration, _ net.Conn, err error) {
	if err == nil {
		return
	}
	var ae *AuthError
	if errors.As(err, &ae) {
		// password_file, counted where the file was read.
		return
	}
	if !errors.Is(err, kerr.SaslAuthenticationFailed) && !errors.Is(err, kerr.UnsupportedSaslMechanism) && !errors.Is(err, kerr.IllegalSaslState) {
		return
	}
	h.c.failure(&AuthError{Reason: ReasonSASLFailed, ClientID: h.c.ClientID, Principal: h.c.principal(), Err: err},
		net.JoinHostPort(meta.Host, strconv.Itoa(int(meta.Port))))
}

// Check is the topic_unauthorized side of AC-8: err as an *AuthError, counted
// once, when the broker refused the principal the topic; otherwise err
// unchanged. Every caller passes the errors of its produce, fetch, metadata
// and admin requests through it, since a hook can't rewrite them. An error
// that is already an *AuthError is left alone, so nothing is counted twice.
func (c Config) Check(topic string, err error) error {
	if err == nil {
		return nil
	}
	var ae *AuthError
	if errors.As(err, &ae) {
		return err
	}
	if !errors.Is(err, kerr.TopicAuthorizationFailed) {
		return err
	}
	ae = &AuthError{Reason: ReasonTopicUnauthorized, ClientID: c.ClientID, Principal: c.principal(), Topic: topic, Err: err}
	c.failure(ae, "")
	return ae
}

// LineAllowed is the per-minute limit of the package's error line, for a
// caller's own line about the same failure (a request path's line, which
// carries its trace): whether the line for kind on c's client may be written
// now, and how many were held back since the last. kind keys it apart from
// the package's own line.
func (c Config) LineAllowed(kind string) (suppressed int, ok bool) {
	return c.Metrics.allowLine(c.ClientID, kind)
}

// failure counts an authentication or authorization failure and writes its
// error line, at most one per client ID and reason per minute.
func (c Config) failure(ae *AuthError, broker string) {
	c.Metrics.count(ae.ClientID, ae.Reason)
	if c.Log == nil {
		return
	}
	suppressed, ok := c.Metrics.allowLine(ae.ClientID, ae.Reason)
	if !ok {
		return
	}
	attrs := []slog.Attr{
		slog.String("client_id", ae.ClientID),
		slog.String("reason", ae.Reason),
		slog.String("principal", ae.Principal),
		slog.String("broker", broker),
		slog.String("error", ae.Err.Error()),
	}
	if ae.Topic != "" {
		attrs = append(attrs, slog.String("topic", ae.Topic))
	}
	if suppressed > 0 {
		attrs = append(attrs, slog.Int("suppressed", suppressed))
	}
	c.Log.LogAttrs(context.Background(), slog.LevelError, "kafka client authentication failed", attrs...)
}
