// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valesordev/andara/internal/testpki"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/recordlog"
)

// liveServer is an in-process andara-server gateway over a memory account
// store — the CLI's connected commands tested end to end over TLS, the way
// AW-CLI-001's skeleton tests could not (there was nothing to connect to).
type liveServer struct {
	addr  string
	ca    string
	store *auth.Store
}

func startServer(t *testing.T) *liveServer {
	t.Helper()
	pki := testpki.New(t)
	kr, err := auth.ParseKeyring(strings.NewReader("k1: " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := auth.Open(context.Background(), auth.Options{
		Accounts:   recordlog.NewMemory(),
		Audit:      recordlog.NewMemory(),
		Keys:       kr,
		Argon2:     auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1},
		SessionTTL: time.Hour,
		RefreshTTL: time.Hour,
		InviteTTL:  time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bootstrap(context.Background(), "oper", "operator-password"); err != nil {
		t.Fatal(err)
	}
	srv, err := gateway.New(gateway.Options{
		Listen: "127.0.0.1:0", TLSCertFile: pki.CertFile, TLSKeyFile: pki.KeyFile,
		MaxRecvBytes: 65536, MaxRequestTimeout: 10 * time.Second, DrainTimeout: 2 * time.Second,
		ProtocolMin: 1, ProtocolMax: 1,
		Verifier: store, Auth: auth.NewService(store), Accounts: auth.NewAdmin(store),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return &liveServer{addr: srv.Addr().String(), ca: pki.CAFile, store: store}
}

func (s *liveServer) env(t *testing.T) map[string]string {
	return isolatedEnv(t, map[string]string{
		"ANDARA_SERVER_ADDRESS": s.addr,
		"ANDARA_TLS_CA_FILE":    s.ca,
	})
}

// runWithStdin is runCLI with a stdin, for --password-stdin.
func runWithStdin(t *testing.T, args []string, env map[string]string, stdin string) runResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args:      args,
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookupFrom(env),
		stdin:     strings.NewReader(stdin),
	})
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

func TestAuthLogin_StoresCredentialAndAdminUsesIt(t *testing.T) {
	s := startServer(t)
	env := s.env(t)
	credPath := filepath.Join(env["XDG_CONFIG_HOME"], "andara", "credentials.yaml")

	// Not logged in: usage error, nothing attempted.
	res := runCLI(t, []string{"account", "create", "--username", "brian", "--password-stdin"}, env)
	if res.exit != ExitUsage || !strings.Contains(res.stderr, "auth login") {
		t.Fatalf("before login: exit=%d stderr=%q", res.exit, res.stderr)
	}

	// Wrong password: the server was reached and refused — exit 3.
	res = runWithStdin(t, []string{"auth", "login", "--username", "oper", "--password-stdin"}, env, "wrong\n")
	if res.exit != ExitConnect {
		t.Fatalf("wrong password: exit=%d stderr=%q", res.exit, res.stderr)
	}
	if _, err := os.Stat(credPath); err == nil {
		t.Fatal("a failed login wrote a credential file")
	}

	res = runWithStdin(t, []string{"auth", "login", "--username", "oper", "--password-stdin", "-o", "json"}, env, "operator-password\n")
	if res.exit != 0 {
		t.Fatalf("login: exit=%d stderr=%q", res.exit, res.stderr)
	}
	out := decodeJSON(t, res.stdout)
	if out["username"] != "oper" || out["credentials"] != credPath {
		t.Fatalf("login output %v", out)
	}
	for _, k := range []string{"session_token", "refresh_token", "token"} {
		if strings.Contains(res.stdout, k) {
			t.Fatalf("login output mentions %s", k)
		}
	}
	fi, err := os.Stat(credPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode %04o", fi.Mode().Perm())
	}
	entries, err := readCredentialFile(credPath)
	if err != nil {
		t.Fatal(err)
	}
	cred := entries[s.addr]
	if cred.SessionToken == "" || cred.RefreshToken == "" || cred.Username != "oper" {
		t.Fatalf("stored credential %+v", cred)
	}

	// config show names the file and says a credential is present, never its value.
	res = runCLI(t, []string{"config", "show", "-o", "json"}, env)
	if res.exit != 0 || !strings.Contains(res.stdout, `"present":true`) || strings.Contains(res.stdout, cred.SessionToken) {
		t.Fatalf("config show: exit=%d stdout=%q", res.exit, res.stdout)
	}

	// whoami
	res = runCLI(t, []string{"auth", "whoami"}, env)
	if res.exit != 0 || !strings.Contains(res.stdout, "as oper") || strings.Contains(res.stdout, cred.SessionToken) {
		t.Fatalf("whoami: exit=%d stdout=%q", res.exit, res.stdout)
	}

	// The stored token authenticates Admin calls.
	res = runWithStdin(t, []string{"account", "create", "--username", "brian", "--role", "player,builder", "--password-stdin", "-o", "json"}, env, "correct horse battery\n")
	if res.exit != 0 {
		t.Fatalf("account create: exit=%d stderr=%q", res.exit, res.stderr)
	}
	id, _ := decodeJSON(t, res.stdout)["account_id"].(string)
	if id == "" {
		t.Fatalf("no account_id in %q", res.stdout)
	}
	res = runCLI(t, []string{"account", "set-roles", id, "--role", "player"}, env)
	if res.exit != 0 {
		t.Fatalf("set-roles: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"account", "set-roles", id, "--role", "wizard"}, env)
	if res.exit != ExitUsage {
		t.Fatalf("bad role: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"registration", "set", "invite"}, env)
	if res.exit != 0 || !strings.Contains(res.stdout, "was closed") {
		t.Fatalf("registration set: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	res = runCLI(t, []string{"invite", "issue", "--count", "2"}, env)
	if res.exit != 0 {
		t.Fatalf("invite issue: exit=%d stderr=%q", res.exit, res.stderr)
	}
	codes := strings.Fields(res.stdout)
	if len(codes) != 2 {
		t.Fatalf("invite issue stdout %q", res.stdout)
	}
	res = runCLI(t, []string{"invite", "revoke", codes[0]}, env)
	if res.exit != 0 {
		t.Fatalf("invite revoke: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"account", "set-status", id, "disabled", "-o", "json"}, env)
	if res.exit != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", res.exit, res.stderr)
	}
	res = runCLI(t, []string{"account", "create-agent", "--username", "town-agent", "--pack", "pack.town", "-o", "json"}, env)
	if res.exit != 0 || !strings.Contains(res.stdout, `"api_key":"ak_`) {
		t.Fatalf("create-agent: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	// A refused operation is exit 1 with the server's code named.
	res = runCLI(t, []string{"account", "set-status", "nobody", "active", "-o", "json"}, env)
	if res.exit != ExitFail || !strings.Contains(res.stdout, "not_found") {
		t.Fatalf("not found: exit=%d stdout=%q", res.exit, res.stdout)
	}

	// refresh rotates; logout revokes and forgets.
	res = runCLI(t, []string{"auth", "refresh"}, env)
	if res.exit != 0 {
		t.Fatalf("refresh: exit=%d stderr=%q", res.exit, res.stderr)
	}
	entries, _ = readCredentialFile(credPath)
	if entries[s.addr].RefreshToken == cred.RefreshToken {
		t.Fatal("refresh did not rotate the stored token")
	}
	res = runCLI(t, []string{"auth", "logout", "-o", "json"}, env)
	if res.exit != 0 || !strings.Contains(res.stdout, `"revoked":1`) {
		t.Fatalf("logout: exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
	entries, _ = readCredentialFile(credPath)
	if _, still := entries[s.addr]; still {
		t.Fatal("logout left the credential")
	}
	res = runCLI(t, []string{"auth", "whoami"}, env)
	if res.exit != ExitUsage {
		t.Fatalf("whoami after logout: exit=%d", res.exit)
	}
}

func TestAuthLogin_UnreachableServerIsExit3(t *testing.T) {
	env := isolatedEnv(t, map[string]string{"ANDARA_SERVER_ADDRESS": "127.0.0.1:1", "ANDARA_TIMEOUT": "2s"})
	res := runWithStdin(t, []string{"auth", "login", "--username", "oper", "--password-stdin", "-o", "json"}, env, "pw\n")
	if res.exit != ExitConnect || !strings.Contains(res.stdout, CodeConnect) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", res.exit, res.stdout, res.stderr)
	}
}

func TestAuthLogin_NoTerminalNeedsPasswordStdin(t *testing.T) {
	env := isolatedEnv(t, nil)
	res := runWithStdin(t, []string{"auth", "login", "--username", "oper"}, env, "pw\n")
	if res.exit != ExitUsage || !strings.Contains(res.stderr, "--password-stdin") {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
}
