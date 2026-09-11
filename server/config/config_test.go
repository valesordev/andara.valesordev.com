package config

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// withTLS layers the two required TLS keys over env, so a test about something
// else does not fail the gateway's "no plaintext mode" check.
func withTLS(env EnvLookup) EnvLookup {
	return func(k string) (string, bool) {
		switch k {
		case "ANDARA_TLS_CERT_FILE":
			return "/tls/server.pem", true
		case "ANDARA_TLS_KEY_FILE":
			return "/tls/server-key.pem", true
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
