// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// withTLS layers the required-to-serve keys over env — TLS material, the
// token key file, and a broker — so a test about something else does not
// fail the "no plaintext mode" and "no accounts without a store" checks.
func withTLS(env EnvLookup) EnvLookup {
	return func(k string) (string, bool) {
		switch k {
		case "ANDARA_TLS_CERT_FILE":
			return "/tls/server.pem", true
		case "ANDARA_TLS_KEY_FILE":
			return "/tls/server-key.pem", true
		case "ANDARA_AUTH_TOKEN_KEY_FILE":
			return "/auth/token-keys", true
		case "ANDARA_KAFKA_BROKERS":
			return "redpanda:29092", true
		}
		if env == nil {
			return "", false
		}
		return env(k)
	}
}

func TestParse_Defaults(t *testing.T) {
	c, err := Parse(nil, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.GRPCListen != ":8443" {
		t.Errorf("grpc listen = %q", c.GRPCListen)
	}
	if c.GRPCMaxRecvBytes != 65536 {
		t.Errorf("max recv = %d", c.GRPCMaxRecvBytes)
	}
	if c.GRPCMaxRequestTimeout != 30*time.Second {
		t.Errorf("max request timeout = %s", c.GRPCMaxRequestTimeout)
	}
	if c.GRPCDrainTimeout != 15*time.Second {
		t.Errorf("drain timeout = %s", c.GRPCDrainTimeout)
	}
	if c.ProtocolMinVersion != 1 || c.ProtocolMaxVersion != 1 {
		t.Errorf("protocol range = %d..%d", c.ProtocolMinVersion, c.ProtocolMaxVersion)
	}
	if c.ContentSource != DefaultContentSource {
		t.Errorf("source = %q", c.ContentSource)
	}
	if c.ContentPath != DefaultContentPath {
		t.Errorf("path = %q", c.ContentPath)
	}
	if c.StrictOrphans {
		t.Error("strict orphans default is false")
	}
	if c.HTTPListen() != ":8080" {
		t.Errorf("listen = %q", c.HTTPListen())
	}
}

func TestParse_FlagBeatsEnv(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "ANDARA_CONTENT_SOURCE" {
			return "kafka", true
		}
		if k == "ANDARA_CONTENT_PATH" {
			return "/from-env", true
		}
		return "", false
	}
	c, err := Parse([]string{"--content-source=dir", "--content-path=/from-flag", "--validate-only"}, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != "dir" {
		t.Errorf("source = %q, want dir", c.ContentSource)
	}
	if c.ContentPath != "/from-flag" {
		t.Errorf("path = %q", c.ContentPath)
	}
	if !c.ValidateOnly {
		t.Error("validate-only not set")
	}
}

func TestParse_EnvBeatsDefault(t *testing.T) {
	env := func(k string) (string, bool) {
		switch k {
		case "ANDARA_CONTENT_SOURCE":
			return "dir", true
		case "ANDARA_STRICT_ORPHANS":
			return "true", true
		case "ANDARA_HTTP_PORT":
			return "9099", true
		default:
			return "", false
		}
	}
	c, err := Parse(nil, withTLS(env), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != "dir" || !c.StrictOrphans || c.HTTPPort != "9099" {
		t.Errorf("%+v", c)
	}
}

func TestParse_UnknownSource(t *testing.T) {
	var buf bytes.Buffer
	_, err := Parse([]string{"--content-source=s3"}, withTLS(nil), &buf)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "kafka or dir") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/server.yaml"
	body := []byte("content:\n  source: dir\n  path: /from-file\n  strict_orphans: true\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]string{"--config", path}, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != "dir" || c.ContentPath != "/from-file" || !c.StrictOrphans {
		t.Errorf("%+v", c)
	}
}

// AW-SRV-005: starting without TLS material is a fatal configuration error,
// and there is no flag that makes it not one.
func TestParse_TLSRequired(t *testing.T) {
	_, err := Parse(nil, nil, nil)
	if err == nil {
		t.Fatal("expected error without TLS material")
	}
	for _, want := range []string{"grpc.tls_cert_file", "grpc.tls_key_file", "ANDARA_TLS_CERT_FILE", "no plaintext mode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	// One half is as fatal as none.
	_, err = Parse([]string{"--tls-cert-file=/tls/server.pem"}, nil, nil)
	if err == nil {
		t.Fatal("expected error with only a certificate")
	}
	// --validate-only opens no listener and is exempt.
	if _, err := Parse([]string{"--validate-only"}, nil, nil); err != nil {
		t.Fatalf("validate-only should not need TLS: %v", err)
	}
}

func TestParse_GatewayFileKeys(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/server.yaml"
	body := []byte(`grpc:
  listen: "127.0.0.1:9443"
  tls_cert_file: /file/server.pem
  tls_key_file: /file/server-key.pem
  max_recv_bytes: 1024
  max_request_timeout: 5s
  drain_timeout: 2s
protocol:
  min_version: 2
  max_version: 3
kafka:
  brokers: [file:9092]
auth:
  token_key_file: /file/keys
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]string{"--config", path}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.GRPCListen != "127.0.0.1:9443" || c.TLSCertFile != "/file/server.pem" || c.TLSKeyFile != "/file/server-key.pem" {
		t.Errorf("%+v", c)
	}
	if c.GRPCMaxRecvBytes != 1024 || c.GRPCMaxRequestTimeout != 5*time.Second || c.GRPCDrainTimeout != 2*time.Second {
		t.Errorf("%+v", c)
	}
	if c.ProtocolMinVersion != 2 || c.ProtocolMaxVersion != 3 {
		t.Errorf("protocol range = %d..%d", c.ProtocolMinVersion, c.ProtocolMaxVersion)
	}
}

func TestParse_GatewayEnvAndFlags(t *testing.T) {
	env := func(k string) (string, bool) {
		switch k {
		case "ANDARA_GRPC_LISTEN":
			return ":19443", true
		case "ANDARA_TLS_CERT_FILE":
			return "/env/server.pem", true
		case "ANDARA_TLS_KEY_FILE":
			return "/env/server-key.pem", true
		case "ANDARA_GRPC_MAX_RECV_BYTES":
			return "4096", true
		case "ANDARA_GRPC_MAX_REQUEST_TIMEOUT":
			return "10s", true
		case "ANDARA_GRPC_DRAIN_TIMEOUT":
			return "3s", true
		case "ANDARA_PROTOCOL_MIN":
			return "1", true
		case "ANDARA_PROTOCOL_MAX":
			return "4", true
		case "ANDARA_AUTH_TOKEN_KEY_FILE":
			return "/env/keys", true
		case "ANDARA_KAFKA_BROKERS":
			return "env:9092, env2:9092", true
		}
		return "", false
	}
	c, err := Parse(nil, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.GRPCListen != ":19443" || c.TLSCertFile != "/env/server.pem" || c.TLSKeyFile != "/env/server-key.pem" {
		t.Errorf("%+v", c)
	}
	if c.GRPCMaxRecvBytes != 4096 || c.GRPCMaxRequestTimeout != 10*time.Second || c.GRPCDrainTimeout != 3*time.Second {
		t.Errorf("%+v", c)
	}
	if c.ProtocolMinVersion != 1 || c.ProtocolMaxVersion != 4 {
		t.Errorf("protocol range = %d..%d", c.ProtocolMinVersion, c.ProtocolMaxVersion)
	}

	// Flags beat env for every gateway key.
	c, err = Parse([]string{
		"--grpc-listen=:29443", "--tls-cert-file=/flag/server.pem", "--tls-key-file=/flag/server-key.pem",
		"--grpc-max-recv-bytes=8192", "--grpc-max-request-timeout=1s", "--grpc-drain-timeout=1s",
		"--protocol-min-version=2", "--protocol-max-version=2",
	}, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.GRPCListen != ":29443" || c.TLSCertFile != "/flag/server.pem" || c.TLSKeyFile != "/flag/server-key.pem" {
		t.Errorf("%+v", c)
	}
	if c.GRPCMaxRecvBytes != 8192 || c.GRPCMaxRequestTimeout != time.Second || c.GRPCDrainTimeout != time.Second {
		t.Errorf("%+v", c)
	}
	if c.ProtocolMinVersion != 2 || c.ProtocolMaxVersion != 2 {
		t.Errorf("protocol range = %d..%d", c.ProtocolMinVersion, c.ProtocolMaxVersion)
	}
}

func TestParse_GatewayValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"min above max", []string{"--protocol-min-version=3", "--protocol-max-version=2"}, "exceeds"},
		{"min zero", []string{"--protocol-min-version=0"}, "at least 1"},
		{"version not a number", []string{"--protocol-max-version=one"}, "unsigned integer"},
		{"recv bytes zero", []string{"--grpc-max-recv-bytes=0"}, "must be positive"},
		{"timeout zero", []string{"--grpc-max-request-timeout=0s"}, "must be positive"},
		{"drain zero", []string{"--grpc-drain-timeout=0s"}, "must be positive"},
		{"empty listen", []string{"--grpc-listen="}, "must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			_, err := Parse(tc.args, withTLS(nil), &buf)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	env := func(k string) (string, bool) {
		if k == "ANDARA_GRPC_MAX_RECV_BYTES" {
			return "lots", true
		}
		return "", false
	}
	if _, err := Parse(nil, withTLS(env), nil); err == nil || !strings.Contains(err.Error(), "ANDARA_GRPC_MAX_RECV_BYTES") {
		t.Errorf("err = %v", err)
	}
}

func TestContentSourceName(t *testing.T) {
	c := Config{ContentSource: "dir", ContentPath: "/c"}
	if c.ContentSourceName() != "dir:/c" {
		t.Errorf("got %q", c.ContentSourceName())
	}
	c.ContentSource = "kafka"
	if c.ContentSourceName() != "kafka" {
		t.Errorf("got %q", c.ContentSourceName())
	}
}

func TestParse_AuthKeys(t *testing.T) {
	// Required to serve, exempt under --validate-only, like TLS.
	tlsOnly := func(k string) (string, bool) {
		switch k {
		case "ANDARA_TLS_CERT_FILE":
			return "/tls/server.pem", true
		case "ANDARA_TLS_KEY_FILE":
			return "/tls/server-key.pem", true
		}
		return "", false
	}
	if _, err := Parse(nil, tlsOnly, nil); err == nil || !strings.Contains(err.Error(), "kafka.brokers") {
		t.Fatalf("kafka store without brokers: %v", err)
	}
	if _, err := Parse([]string{"--auth-store=memory"}, tlsOnly, nil); err == nil || !strings.Contains(err.Error(), "auth.token_key_file") {
		t.Fatalf("no key file: %v", err)
	}
	if _, err := Parse([]string{"--validate-only"}, tlsOnly, nil); err != nil {
		t.Fatalf("validate-only should need neither: %v", err)
	}

	c, err := Parse(nil, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthStore != "kafka" || c.AuthSessionTTL != time.Hour || c.AuthRefreshTTL != 720*time.Hour ||
		c.AuthArgon2MemoryKiB != 65536 || c.AuthArgon2Time != 3 || c.AuthArgon2Threads != 4 ||
		c.AuthRateLimit != "10/m" || c.AuthInviteTTL != 168*time.Hour || c.AuthRecheckInterval != 30*time.Second ||
		c.SessionLinkdeadMax != 300*time.Second || len(c.KafkaBrokers) != 1 {
		t.Errorf("defaults: %+v", c)
	}

	// File, env, flag precedence and every key.
	dir := t.TempDir()
	path := dir + "/server.yaml"
	body := []byte(`kafka:
  brokers: [a:1, b:2]
auth:
  store: memory
  session_ttl: 2h
  refresh_ttl: 48h
  token_key_file: /f/keys
  argon2: {memory_kib: 1024, time: 2, threads: 1}
  rate_limit: 5/m
  invite_ttl: 24h
  recheck_interval: 10s
  k8s_issuer: https://kubernetes.default.svc
  k8s_jwks_url: https://kubernetes.default.svc/openid/v1/jwks
session:
  linkdead_grace: 75s
  linkdead_max: 90s
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Parse([]string{"--config", path, "--auth-argon2-time=5"}, func(k string) (string, bool) {
		if k == "ANDARA_AUTH_SESSION_TTL" {
			return "3h", true
		}
		return tlsOnly(k)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthStore != "memory" || c.AuthSessionTTL != 3*time.Hour || c.AuthRefreshTTL != 48*time.Hour ||
		c.AuthTokenKeyFile != "/f/keys" || c.AuthArgon2MemoryKiB != 1024 || c.AuthArgon2Time != 5 || c.AuthArgon2Threads != 1 ||
		c.AuthRateLimit != "5/m" || c.AuthInviteTTL != 24*time.Hour || c.AuthRecheckInterval != 10*time.Second ||
		c.AuthK8sIssuer != "https://kubernetes.default.svc" || c.SessionLinkdeadMax != 90*time.Second ||
		strings.Join(c.KafkaBrokers, ",") != "a:1,b:2" {
		t.Errorf("file+env+flag: %+v", c)
	}

	// ADR-0006's invariant: a token must outlive the linkdead ceiling.
	if _, err := Parse([]string{"--auth-session-ttl=4m"}, withTLS(nil), nil); err == nil || !strings.Contains(err.Error(), "linkdead_max") {
		t.Fatalf("session_ttl <= linkdead_max accepted: %v", err)
	}
	for _, bad := range [][]string{
		{"--auth-store=redis"},
		{"--auth-argon2-memory-kib=4"},
		{"--auth-argon2-threads=0"},
		{"--auth-k8s-issuer=x"},
		{"--auth-recheck-interval=0s"},
	} {
		if _, err := Parse(bad, withTLS(nil), nil); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestParse_SimKeys(t *testing.T) {
	c, err := Parse(nil, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimSource != "kafka" || c.SimTickRate != 10 || c.SimTickBudget != 50*time.Millisecond || c.SimMaxPerTick != 1024 ||
		c.SimDrainTimeout != 5*time.Second || c.SimSeed != 0 || len(c.SimPartitions) != 64 || c.SimCheckpointEveryTicks != 100 {
		t.Errorf("defaults: %+v", c)
	}
	env := withTLS(func(k string) (string, bool) {
		switch k {
		case "ANDARA_TICK_RATE":
			return "20", true
		case "ANDARA_TICK_BUDGET_MS":
			return "25", true
		case "ANDARA_SIM_PARTITIONS":
			return "0,2,4", true
		case "ANDARA_SIM_SEED":
			return "99", true
		case "ANDARA_DRAIN_TIMEOUT_MS":
			return "0", true
		}
		return "", false
	})
	c, err = Parse([]string{"--sim-max-per-tick=7", "--sim-checkpoint-every-ticks=3"}, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimTickRate != 20 || c.SimTickBudget != 25*time.Millisecond || fmt.Sprint(c.SimPartitions) != "[0 2 4]" || c.SimSeed != 99 ||
		c.SimDrainTimeout != 0 || c.SimMaxPerTick != 7 || c.SimCheckpointEveryTicks != 3 {
		t.Errorf("env+flags: %+v", c)
	}
	// File keys.
	dir := t.TempDir()
	path := dir + "/server.yaml"
	if err := os.WriteFile(path, []byte("sim:\n  source: memory\n  tick_rate: 5\n  tick_budget_ms: 100\n  partitions: 8-11\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Parse([]string{"--config", path}, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimSource != "memory" || c.SimTickRate != 5 || c.SimTickBudget != 100*time.Millisecond || fmt.Sprint(c.SimPartitions) != "[8 9 10 11]" {
		t.Errorf("file: %+v", c)
	}
	// Refusals.
	for _, bad := range [][]string{
		{"--sim-source=redis"},
		{"--sim-tick-rate=0"},
		{"--sim-tick-budget-ms=150"}, // past the 100 ms interval
		{"--sim-partitions=64"},
		{"--sim-partitions=5-2"},
		{"--sim-max-per-tick=0"},
	} {
		if _, err := Parse(bad, withTLS(nil), nil); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if ps, err := ParsePartitions(" 3, 1 ,3,0-1"); err != nil || fmt.Sprint(ps) != "[0 1 3]" {
		t.Errorf("ParsePartitions = %v, %v", ps, err)
	}
}

// The Helm chart emits ANDARA_CONTENT_MAX_BLOB_BYTES, so the server has to
// read it. It did not: the variable was generated into _env.tpl from
// keys.yaml but never parsed, so every Helm-configured value was silently
// ignored and the server kept the 8 MiB default — raising the limit would
// still reject a large blob, and lowering it would still admit one.
func TestContentMaxBlobBytesIsReadFromTheEnvironment(t *testing.T) {
	c, err := Parse(nil, withTLS(func(k string) (string, bool) {
		if k == "ANDARA_CONTENT_MAX_BLOB_BYTES" {
			return "16777216", true
		}
		return "", false
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentMaxBlobBytes != 16<<20 {
		t.Fatalf("content.max_blob_bytes = %d, want the configured 16 MiB", c.ContentMaxBlobBytes)
	}
}

func TestContentMaxBlobBytesRejectsNonsense(t *testing.T) {
	_, err := Parse(nil, withTLS(func(k string) (string, bool) {
		if k == "ANDARA_CONTENT_MAX_BLOB_BYTES" {
			return "eight megabytes", true
		}
		return "", false
	}), nil)
	if err == nil {
		t.Fatal("want an error naming the variable")
	}
	if !strings.Contains(err.Error(), "ANDARA_CONTENT_MAX_BLOB_BYTES") {
		t.Errorf("error %q should name the variable", err)
	}
}

// AW-SRV-015 AC-7 and AC-8: the startup invariants, asserted in the story's
// order, each failure naming its relation and every value in it. A
// configuration violating several fails on the first.
func TestParse_LinkdeadInvariants(t *testing.T) {
	for _, c := range []struct {
		name     string
		args     []string
		relation string
		values   []string
	}{
		{"max below grace", []string{"--session-linkdead-max=2m"},
			"session.linkdead_max >= session.linkdead_grace", []string{"session.linkdead_max=2m0s", "session.linkdead_grace=3m0s"}},
		{"grace not past the RTO", []string{"--session-linkdead-grace=60s"},
			"session.linkdead_grace > recovery.rto_target", []string{"session.linkdead_grace=1m0s", "recovery.rto_target=1m0s"}},
		{"resume window too small", []string{"--egress-buffer=64", "--egress-resume-window=1000"},
			"egress.resume_window >= session.linkdead_max × egress.assumed_event_rate", []string{"egress.resume_window=1000", "session.linkdead_max=5m0s", "egress.assumed_event_rate=5"}},
		{"rate raised past the window", []string{"--egress-assumed-event-rate=7"},
			"egress.resume_window >= session.linkdead_max × egress.assumed_event_rate", []string{"egress.resume_window=2048", "egress.assumed_event_rate=7"}},
		{"token inside the ceiling", []string{"--auth-session-ttl=5m"},
			"auth.session_ttl > session.linkdead_max", []string{"auth.session_ttl=5m0s", "session.linkdead_max=5m0s"}},
		{"several at once: the first wins", []string{"--session-linkdead-max=2m", "--session-linkdead-grace=3m", "--recovery-rto-target=5m"},
			"session.linkdead_max >= session.linkdead_grace", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(append(c.args, "--validate-only"), nil, io.Discard)
			var inv *ErrInvariant
			if !errors.As(err, &inv) {
				t.Fatalf("got %v, want an ErrInvariant", err)
			}
			if inv.Relation != c.relation {
				t.Fatalf("relation %q, want %q", inv.Relation, c.relation)
			}
			for _, v := range c.values {
				if !strings.Contains(err.Error(), v) {
					t.Errorf("%q does not name %s", err.Error(), v)
				}
			}
		})
	}
	// The defaults hold every relation: 300 >= 180 > 60, 2048 >= 300 × 5,
	// and the token outlives the ceiling.
	cfg, err := Parse([]string{"--validate-only"}, nil, io.Discard)
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if g, e, m := cfg.LinkdeadTicks(); g != 1800 || e != 600 || m != 3000 {
		t.Fatalf("ticks at 10 Hz: grace %d, extension %d, max %d", g, e, m)
	}
}

// AW-SRV-028: the cross-Zone handoff retry's three keys, in ticks.
func TestParse_HandoffRetryKeys(t *testing.T) {
	c, err := Parse(nil, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimHandoffRetryTicks != 10 || c.SimHandoffRetryMaxTicks != 100 || c.SimHandoffRetryBatch != 50 {
		t.Errorf("defaults: %d %d %d, want 10 100 50", c.SimHandoffRetryTicks, c.SimHandoffRetryMaxTicks, c.SimHandoffRetryBatch)
	}
	env := withTLS(func(k string) (string, bool) {
		switch k {
		case "ANDARA_HANDOFF_RETRY_TICKS":
			return "4", true
		case "ANDARA_HANDOFF_RETRY_MAX_TICKS":
			return "40", true
		case "ANDARA_HANDOFF_RETRY_BATCH":
			return "9", true
		}
		return "", false
	})
	c, err = Parse(nil, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimHandoffRetryTicks != 4 || c.SimHandoffRetryMaxTicks != 40 || c.SimHandoffRetryBatch != 9 {
		t.Errorf("env: %d %d %d", c.SimHandoffRetryTicks, c.SimHandoffRetryMaxTicks, c.SimHandoffRetryBatch)
	}
	c, err = Parse([]string{"--sim-handoff-retry-ticks=6", "--sim-handoff-retry-max-ticks=6", "--sim-handoff-retry-batch=1"}, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimHandoffRetryTicks != 6 || c.SimHandoffRetryMaxTicks != 6 || c.SimHandoffRetryBatch != 1 {
		t.Errorf("flags beat env: %d %d %d", c.SimHandoffRetryTicks, c.SimHandoffRetryMaxTicks, c.SimHandoffRetryBatch)
	}
	path := t.TempDir() + "/server.yaml"
	if err := os.WriteFile(path, []byte("sim:\n  handoff_retry_ticks: 3\n  handoff_retry_max_ticks: 30\n  handoff_retry_batch: 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Parse([]string{"--config", path}, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.SimHandoffRetryTicks != 3 || c.SimHandoffRetryMaxTicks != 30 || c.SimHandoffRetryBatch != 12 {
		t.Errorf("file: %d %d %d", c.SimHandoffRetryTicks, c.SimHandoffRetryMaxTicks, c.SimHandoffRetryBatch)
	}
	for name, bad := range map[string][]string{
		"zero first interval":          {"--sim-handoff-retry-ticks=0"},
		"negative first interval":      {"--sim-handoff-retry-ticks=-1"},
		"max below the first interval": {"--sim-handoff-retry-ticks=20", "--sim-handoff-retry-max-ticks=19"},
		"zero batch":                   {"--sim-handoff-retry-batch=0"},
	} {
		if _, err := Parse(bad, withTLS(nil), nil); err == nil {
			t.Errorf("%s: %v accepted", name, bad)
		}
	}
	// The maximum may equal the first interval: no backoff, a fixed retry.
	if _, err := Parse([]string{"--sim-handoff-retry-ticks=7", "--sim-handoff-retry-max-ticks=7"}, withTLS(nil), nil); err != nil {
		t.Errorf("a max equal to the first interval was refused: %v", err)
	}
}

// AW-SRV-007's keys: defaults, env, flag, and the refusals.
func TestParse_RecoveryKeys(t *testing.T) {
	c, err := Parse(nil, withTLS(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.RecoveryRequireSnapshot || c.RecoveryReplayBatch != 4096 || c.RecoveryVerifyTimeout != 600*time.Second || c.RecoveryPinRound != 0 || c.RecoveryMismatchLinger != 0 {
		t.Fatalf("defaults %+v", c)
	}
	env := withTLS(func(k string) (string, bool) {
		return map[string]string{
				"ANDARA_RECOVERY_REQUIRE_SNAPSHOT": "true", "ANDARA_RECOVERY_REPLAY_BATCH": "7",
				"ANDARA_RECOVERY_VERIFY_TIMEOUT": "9s", "ANDARA_RECOVERY_PIN_ROUND": "4200", "ANDARA_RECOVERY_MISMATCH_LINGER": "60s",
			}[k], map[string]bool{"ANDARA_RECOVERY_REQUIRE_SNAPSHOT": true, "ANDARA_RECOVERY_REPLAY_BATCH": true,
				"ANDARA_RECOVERY_VERIFY_TIMEOUT": true, "ANDARA_RECOVERY_PIN_ROUND": true, "ANDARA_RECOVERY_MISMATCH_LINGER": true}[k]
	})
	c, err = Parse(nil, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.RecoveryRequireSnapshot || c.RecoveryReplayBatch != 7 || c.RecoveryVerifyTimeout != 9*time.Second || c.RecoveryPinRound != 4200 || c.RecoveryMismatchLinger != time.Minute {
		t.Fatalf("env %+v", c)
	}
	c, err = Parse([]string{"--recovery-pin-round=12", "--recovery-mismatch-linger=5s"}, withTLS(nil), nil)
	if err != nil || c.RecoveryPinRound != 12 || c.RecoveryMismatchLinger != 5*time.Second {
		t.Fatalf("flags: %v %+v", err, c)
	}
	for _, bad := range [][]string{{"--recovery-replay-batch=0"}, {"--recovery-verify-timeout=0s"}, {"--recovery-mismatch-linger=-1s"}} {
		if _, err := Parse(bad, withTLS(nil), io.Discard); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
	if _, err := Parse(nil, withTLS(func(k string) (string, bool) { return "x", k == "ANDARA_RECOVERY_PIN_ROUND" }), nil); err == nil {
		t.Error("a non-numeric pin round was accepted")
	}
}
