// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/internal/testpki"
)

// AW-CLI-011: a certificate that didn't verify says which CA the CLI trusted
// and where that setting came from. The walk-through case: a CA pinned by
// ANDARA_CONFIG against a server another CA signed.

type tlsRig struct {
	s       *liveServer
	otherCA string // a CA that did not sign s's certificate
	env     map[string]string
	cfgDir  string
}

func newTLSRig(t *testing.T) *tlsRig {
	t.Helper()
	s := startServer(t)
	env := isolatedEnv(t, map[string]string{"ANDARA_SERVER_ADDRESS": s.addr})
	return &tlsRig{s: s, otherCA: testpki.New(t).CAFile, env: env, cfgDir: filepath.Join(env["XDG_CONFIG_HOME"], "andara")}
}

// writeConfig writes a config file pinning ca at path, creating its directory.
func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// connect runs a command that connects and returns its result.
func (r *tlsRig) connect(t *testing.T, env map[string]string, extra ...string) runResult {
	t.Helper()
	return runWithStdin(t, append([]string{"auth", "login", "--username", "oper", "--password-stdin"}, extra...), env, "operator-password\n")
}

func (r *tlsRig) envWith(kv ...string) map[string]string {
	env := map[string]string{}
	for k, v := range r.env {
		env[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		env[kv[i]] = kv[i+1]
	}
	return env
}

func wantFailure(t *testing.T, res runResult, suffix string) {
	t.Helper()
	if res.exit != ExitConnect {
		t.Fatalf("exit=%d, want %d; stderr=%q", res.exit, ExitConnect, res.stderr)
	}
	if line := strings.TrimSpace(res.stderr); !strings.Contains(line, "x509") || !strings.HasSuffix(line, suffix) {
		t.Errorf("stderr %q\nwant it to mention x509 and end with %q", line, suffix)
	}
}

// AC-1, AC-3 (the default path, no suffix).
func TestTLSFailure_NamesACAFromTheConfigFile(t *testing.T) {
	r := newTLSRig(t)
	cfg := filepath.Join(r.cfgDir, "cli.yaml")
	writeConfig(t, cfg, "server:\n  tls_ca: "+r.otherCA+"\n")
	wantFailure(t, r.connect(t, r.env),
		"; trusted only the CA in "+r.otherCA+" (server.tls_ca, from config file "+cfg+")")
}

// AC-2.
func TestTLSFailure_NamesTheEnvAndFlagSources(t *testing.T) {
	r := newTLSRig(t)
	wantFailure(t, r.connect(t, r.envWith("ANDARA_TLS_CA_FILE", r.otherCA)),
		"; trusted only the CA in "+r.otherCA+" (server.tls_ca, from ANDARA_TLS_CA_FILE)")
	wantFailure(t, r.connect(t, r.env, "--tls-ca", r.otherCA),
		"; trusted only the CA in "+r.otherCA+" (server.tls_ca, from --tls-ca)")
	// The flag wins over the environment, and the message says which won.
	wantFailure(t, r.connect(t, r.envWith("ANDARA_TLS_CA_FILE", r.s.ca), "--tls-ca", r.otherCA),
		"(server.tls_ca, from --tls-ca)")
}

// AC-3: the file was chosen by ANDARA_CONFIG or --config.
func TestTLSFailure_NamesWhatChoseTheConfigFile(t *testing.T) {
	r := newTLSRig(t)
	cfg := filepath.Join(t.TempDir(), "local.yaml")
	writeConfig(t, cfg, "server:\n  tls_ca: "+r.otherCA+"\n")
	wantFailure(t, r.connect(t, r.envWith("ANDARA_CONFIG", cfg)),
		"(server.tls_ca, from config file "+cfg+" (ANDARA_CONFIG))")
	wantFailure(t, r.connect(t, r.env, "--config", cfg),
		"(server.tls_ca, from config file "+cfg+" (--config))")
}

// AC-4: no CA set, and the system trust store doesn't hold the server's.
func TestTLSFailure_NamesTheSystemTrustStore(t *testing.T) {
	r := newTLSRig(t)
	wantFailure(t, r.connect(t, r.env), "; trusted the system trust store")
}

// AC-5: a failure that isn't certificate verification keeps its message.
func TestTLSFailure_LeavesOtherConnectFailuresAlone(t *testing.T) {
	r := newTLSRig(t)
	// Nothing listens at port 1: refused, which is no certificate's fault.
	env := r.envWith("ANDARA_SERVER_ADDRESS", "127.0.0.1:1", "ANDARA_TLS_CA_FILE", r.otherCA)
	res := r.connect(t, env)
	if res.exit != ExitConnect {
		t.Fatalf("exit=%d, want %d; stderr=%q", res.exit, ExitConnect, res.stderr)
	}
	if strings.Contains(res.stderr, "trusted") || strings.Contains(res.stderr, "expected the name") {
		t.Errorf("a refused connection was blamed on the CA: %q", res.stderr)
	}
	if !strings.Contains(res.stderr, "cannot reach the server") && !strings.Contains(res.stderr, "unavailable") && !strings.Contains(res.stderr, "refused") {
		t.Errorf("the refused connection's message changed: %q", res.stderr)
	}
}

// AC-6: the JSON envelope carries each fact the message states.
func TestTLSFailure_JSONDetailCarriesTheSources(t *testing.T) {
	r := newTLSRig(t)
	cfg := filepath.Join(t.TempDir(), "local.yaml")
	writeConfig(t, cfg, "server:\n  tls_ca: "+r.otherCA+"\n")

	detail := func(res runResult) map[string]any {
		t.Helper()
		if res.exit != ExitConnect {
			t.Fatalf("exit=%d; stderr=%q stdout=%q", res.exit, res.stderr, res.stdout)
		}
		env := decodeJSON(t, res.stdout+res.stderr)
		e, _ := env["error"].(map[string]any)
		if e["code"] != CodeConnect {
			t.Fatalf("error.code = %v, want %s", e["code"], CodeConnect)
		}
		d, _ := e["detail"].(map[string]any)
		return d
	}
	want := func(d map[string]any, kv ...string) {
		t.Helper()
		for i := 0; i < len(kv); i += 2 {
			if d[kv[i]] != kv[i+1] {
				t.Errorf("detail[%s] = %v, want %q (detail %v)", kv[i], d[kv[i]], kv[i+1], d)
			}
		}
	}
	want(detail(r.connect(t, r.envWith("ANDARA_CONFIG", cfg), "-o", "json")),
		"tls_ca", r.otherCA, "tls_ca_source", "file", "config_path", cfg, "config_path_source", "env")
	want(detail(r.connect(t, r.env, "-o", "json", "--tls-ca", r.otherCA, "--config", cfg)),
		"tls_ca", r.otherCA, "tls_ca_source", "flag", "config_path_source", "flag")
	want(detail(r.connect(t, r.envWith("ANDARA_TLS_CA_FILE", r.otherCA), "-o", "json")),
		"tls_ca", r.otherCA, "tls_ca_source", "env", "config_path_source", "default")
	want(detail(r.connect(t, r.env, "-o", "json")),
		"tls_ca", "", "tls_ca_source", "default")
}

// AC-7: the certificate is valid, but not for the name verified.
func TestTLSFailure_NamesTheExpectedServerName(t *testing.T) {
	r := newTLSRig(t)
	env := r.envWith("ANDARA_TLS_CA_FILE", r.s.ca)
	wantFailure(t, r.connect(t, env, "--tls-server-name", "andara-0.wrong.svc"),
		"; expected the name andara-0.wrong.svc (server.tls_server_name, from --tls-server-name)")
	wantFailure(t, r.connect(t, r.envWith("ANDARA_TLS_CA_FILE", r.s.ca, "ANDARA_TLS_SERVER_NAME", "andara-0.wrong.svc")),
		"(server.tls_server_name, from ANDARA_TLS_SERVER_NAME)")
	cfg := filepath.Join(r.cfgDir, "cli.yaml")
	writeConfig(t, cfg, "server:\n  tls_server_name: andara-0.wrong.svc\n")
	wantFailure(t, r.connect(t, env), "(server.tls_server_name, from config file "+cfg+")")
}

// AC-7's JSON half: the name and its source ride in detail.
func TestTLSFailure_JSONDetailCarriesTheServerName(t *testing.T) {
	r := newTLSRig(t)
	for _, c := range []struct {
		env  map[string]string
		args []string
		src  string
	}{
		{r.envWith("ANDARA_TLS_CA_FILE", r.s.ca), []string{"--tls-server-name", "x.wrong.svc"}, "flag"},
		{r.envWith("ANDARA_TLS_CA_FILE", r.s.ca, "ANDARA_TLS_SERVER_NAME", "x.wrong.svc"), nil, "env"},
	} {
		res := r.connect(t, c.env, append([]string{"-o", "json"}, c.args...)...)
		e, _ := decodeJSON(t, res.stdout+res.stderr)["error"].(map[string]any)
		d, _ := e["detail"].(map[string]any)
		if d["tls_server_name"] != "x.wrong.svc" || d["tls_server_name_source"] != c.src {
			t.Errorf("detail = %v, want the name and source %q", d, c.src)
		}
	}
}

func TestVerificationFailure_ClassifiesOnlyCertificateErrors(t *testing.T) {
	for name, c := range map[string]struct {
		err              error
		hostname, wanted bool
	}{
		"unknown authority": {x509.UnknownAuthorityError{}, false, true},
		"expired":           {x509.CertificateInvalidError{Reason: x509.Expired}, false, true},
		"hostname":          {x509.HostnameError{}, true, true},
		"no system roots":   {x509.SystemRootsError{Err: errors.New("no roots")}, false, true},
		"no roots, wrapped": {&tls.CertificateVerificationError{Err: x509.SystemRootsError{Err: errors.New("no roots")}}, false, true},
		"wrapped":           {errors.Join(errors.New("tls"), x509.UnknownAuthorityError{}), false, true},
		"refused":           {&net.OpError{Op: "dial", Err: errors.New("connection refused")}, false, false},
		"deadline":          {context.DeadlineExceeded, false, false},
	} {
		hn, ok := verificationFailure(c.err)
		if hn != c.hostname || ok != c.wanted {
			t.Errorf("%s: hostname=%v ok=%v, want %v %v", name, hn, ok, c.hostname, c.wanted)
		}
	}
}

// A name mismatch with no server name set adds no CA suffix: the CA verified.
func TestTLSFailure_AHostMismatchWithNoNameBlamesNoCA(t *testing.T) {
	f := &tlsFailure{hostname: true, ca: "/x/ca.pem", caSrc: "file"}
	if got := f.suffix(); got != "" {
		t.Errorf("suffix = %q, want none", got)
	}
}

// A hostname mismatch's JSON blames no CA either (review of #487).
func TestTLSFailure_AHostMismatchJSONCarriesNoCADetail(t *testing.T) {
	named := &tlsFailure{hostname: true, ca: "/x/ca.pem", caSrc: "file", configPath: "/c.yaml", configSrc: "env",
		serverName: "n.svc", serverNmSrc: "file"}
	d := map[string]any{}
	named.detail(d)
	if _, ok := d["tls_ca"]; ok {
		t.Errorf("a name mismatch carries the CA: %v", d)
	}
	if d["tls_server_name"] != "n.svc" || d["tls_server_name_source"] != "file" || d["config_path"] != "/c.yaml" || d["config_path_source"] != "env" {
		t.Errorf("a file-sourced name needs the name, source and config path: %v", d)
	}
	flagged := &tlsFailure{hostname: true, ca: "/x/ca.pem", caSrc: "file", serverName: "n.svc", serverNmSrc: "flag"}
	d = map[string]any{}
	flagged.detail(d)
	if _, ok := d["config_path"]; ok || d["config_path_source"] != nil || d["tls_server_name_source"] != "flag" {
		t.Errorf("a flag-sourced name needs no config path: %v", d)
	}
	bare := &tlsFailure{hostname: true, ca: "/x/ca.pem", caSrc: "file", configPath: "/c.yaml"}
	d = map[string]any{}
	bare.detail(d)
	if len(d) != 0 {
		t.Errorf("a mismatch with no name set carries nothing: %v", d)
	}
}
