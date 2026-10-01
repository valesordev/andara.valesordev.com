// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/internal/testpki"
)

// AW-SRV-042 AC-5: through a port-forward, the CLI dials localhost while the
// pod's certificate names its Service. server.tls_server_name verifies against
// that name; without it the handshake fails, and the CLI exits 3. Flag, env
// and config resolve with AW-CLI-001's precedence, and config show lists the
// winner. The dial target and the credential key stay server.address.
func TestTLSServerName_VerifiesAgainstTheNamedHost(t *testing.T) {
	const pod = "andara-0.andara.test.svc"
	s := startServerPKI(t, testpki.NewFor(t, pod), nil)
	if !strings.HasPrefix(s.addr, "127.0.0.1:") {
		t.Fatalf("the server listens at %s; the test dials the loopback address", s.addr)
	}
	login := func(env map[string]string, extra ...string) runResult {
		t.Helper()
		return runWithStdin(t, append([]string{"auth", "login", "--username", "oper", "--password-stdin"}, extra...), env, "operator-password\n")
	}

	// Without the name: the certificate doesn't name 127.0.0.1.
	env := s.env(t)
	if res := login(env); res.exit != ExitConnect {
		t.Fatalf("without --tls-server-name: exit=%d, want %d; stderr=%q", res.exit, ExitConnect, res.stderr)
	}
	// With the flag: verified against the pod's name, and the credential is
	// still keyed by the dialed address.
	if res := login(env, "--tls-server-name", pod); res.exit != ExitOK {
		t.Fatalf("with --tls-server-name: exit=%d stderr=%q", res.exit, res.stderr)
	}
	creds, err := readCredentialFile(filepath.Join(env["XDG_CONFIG_HOME"], "andara", "credentials.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := creds[s.addr]; !ok || len(creds) != 1 {
		t.Errorf("credentials are keyed %v, want the dialed address %s", keys(creds), s.addr)
	}

	// The env var, and the config key, each do the same.
	envd := s.env(t)
	envd["ANDARA_TLS_SERVER_NAME"] = pod
	if res := login(envd); res.exit != ExitOK {
		t.Errorf("ANDARA_TLS_SERVER_NAME: exit=%d stderr=%q", res.exit, res.stderr)
	}
	filed := s.env(t)
	cfg := filepath.Join(filed["XDG_CONFIG_HOME"], "andara", "cli.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("server:\n  tls_server_name: "+pod+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res := login(filed); res.exit != ExitOK {
		t.Errorf("server.tls_server_name in config: exit=%d stderr=%q", res.exit, res.stderr)
	}

	// Precedence: the flag over the env over the file, as config show says.
	filed["ANDARA_TLS_SERVER_NAME"] = "from-env"
	show := func(extra ...string) (string, string) {
		t.Helper()
		res := runCLI(t, append([]string{"config", "show", "-o", "json"}, extra...), filed)
		var out struct {
			Settings []Setting `json:"settings"`
		}
		if err := json.Unmarshal([]byte(res.stdout), &out); err != nil {
			t.Fatalf("config show: %v\n%s", err, res.stdout)
		}
		for _, st := range out.Settings {
			if st.Key == "server.tls_server_name" {
				return st.Value, string(st.Source)
			}
		}
		t.Fatalf("config show lists no server.tls_server_name: %s", res.stdout)
		return "", ""
	}
	if v, src := show(); v != "from-env" || src != "env" {
		t.Errorf("env over file: %s from %s", v, src)
	}
	if v, src := show("--tls-server-name", "from-flag"); v != "from-flag" || src != "flag" {
		t.Errorf("flag over env: %s from %s", v, src)
	}
	delete(filed, "ANDARA_TLS_SERVER_NAME")
	if v, src := show(); v != pod || src != "file" {
		t.Errorf("file: %s from %s", v, src)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
