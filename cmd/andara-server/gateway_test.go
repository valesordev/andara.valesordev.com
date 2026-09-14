// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
	"github.com/valesordev/andara/gen/go/andara/auth/v1/authv1connect"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
	"github.com/valesordev/andara/internal/testpki"
)

// AW-SRV-005: starting without TLS material is a fatal configuration error.
// Exit 1, the error on stderr, and nothing listened.
func TestRun_NoTLSExitsOne(t *testing.T) {
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "valid"))
	var stdout, stderr bytes.Buffer
	code := run([]string{"--content-source=dir", "--content-path=" + path, "--grpc-listen=127.0.0.1:0", "--http-port=0"},
		emptyEnv, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "grpc.tls_cert_file") {
		t.Errorf("stderr does not name the missing key:\n%s", stderr.String())
	}
}

// A certificate that does not load is as fatal as one that is missing.
func TestRun_BadTLSExitsOne(t *testing.T) {
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "valid"))
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--content-source=dir", "--content-path=" + path, "--grpc-listen=127.0.0.1:0", "--http-port=0",
		"--tls-cert-file=" + junk, "--tls-key-file=" + junk, "--auth-store=memory", "--auth-token-key-file=" + keyFile(t)},
		emptyEnv, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "load TLS key pair") {
		t.Errorf("stderr does not name the TLS failure:\n%s", stderr.String())
	}
}

// AC-8 at the process boundary: a full boot serves the Protocol over TLS,
// a Session can be opened through it, and SIGTERM drains and exits 0.
func TestRun_ServesAndDrainsOnSIGTERM(t *testing.T) {
	pki := testpki.New(t)
	path := abs(t, filepath.Join("..", "..", "testdata", "content", "valid"))

	// Pick free ports up front: run() logs its bound address but the test
	// needs to know it before the process does.
	grpcAddr := freeAddr(t)
	httpAddr := freeAddr(t)

	var stdout bytes.Buffer
	stderr := &lockedBuffer{}
	done := make(chan int, 1)
	const bootstrapPassword = "first-operator-password"
	go func() {
		done <- run([]string{
			"--content-source=dir", "--content-path=" + path,
			"--grpc-listen=" + grpcAddr, "--http-port=" + httpAddr,
			"--tls-cert-file=" + pki.CertFile, "--tls-key-file=" + pki.KeyFile,
			"--grpc-drain-timeout=5s",
			// AW-SRV-008: a real store, a real key file, and the first
			// operator, so the token below is a real one. Argon2 is turned
			// down so the test does not spend seconds hashing under -race.
			"--auth-store=memory", "--auth-token-key-file=" + keyFile(t),
			"--auth-bootstrap-operator=brian:" + bootstrapPassword,
			"--auth-argon2-memory-kib=64", "--auth-argon2-time=1", "--auth-argon2-threads=1",
		}, emptyEnv, &stdout, stderr)
	}()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: pki.ClientTLS(), ForceAttemptHTTP2: true}}
	authc := authv1connect.NewAuthClient(client, "https://"+grpcAddr)
	var tokens *connect.Response[authv1.AuthenticateResponse]
	deadline := time.Now().Add(10 * time.Second)
	for {
		var err error
		tokens, err = authc.Authenticate(context.Background(), connect.NewRequest(&authv1.AuthenticateRequest{
			Username: "brian", Password: bootstrapPassword,
		}))
		if err == nil {
			break
		}
		select {
		case code := <-done:
			t.Fatalf("server exited %d before serving; stderr=%s", code, stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never served: %v; stderr=%s", err, stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	game := gamev1connect.NewGameClient(client, "https://"+grpcAddr)
	resp, err := game.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: tokens.Msg.GetTokens().GetSessionToken(), ClientName: "main-test/0",
	}))
	if err != nil {
		t.Fatalf("OpenSession with a real token: %v", err)
	}
	if resp.Msg.SessionId == "" || resp.Msg.NegotiatedVersion != 1 {
		t.Fatalf("OpenSession = %v", resp.Msg)
	}
	// A made-up token is refused: the accept-anything stub is gone.
	if _, err := game.OpenSession(context.Background(), connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: 1, AuthToken: "tok", ClientName: "main-test/0",
	})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("made-up token: %v", err)
	}

	ready, err := http.Get("http://" + httpAddr + "/readyz")
	if err != nil || ready.StatusCode != http.StatusOK {
		t.Fatalf("readyz = %v %v", ready, err)
	}
	ready.Body.Close()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d after SIGTERM, want 0; stderr=%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("server did not exit after SIGTERM; stderr=%s", stderr.String())
	}
	out := stderr.String()
	for _, want := range []string{"grpc listen", "session opened", "grpc drain begin", "session closed", "grpc drain complete"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{tokens.Msg.GetTokens().GetSessionToken(), tokens.Msg.GetTokens().GetRefreshToken(), bootstrapPassword} {
		if strings.Contains(out, secret) {
			t.Error("a credential appeared on stderr")
		}
	}
	for _, want := range []string{"account index loaded", "bootstrap operator created", `"outcome":"ok"`} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q", want)
		}
	}
	if c, err := net.DialTimeout("tcp", grpcAddr, time.Second); err == nil {
		c.Close()
		t.Error("gRPC listener still accepting after exit")
	}
}

// keyFile writes a one-key token keyring with the mode the server requires.
func keyFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token-keys")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	if err := os.WriteFile(path, []byte("k1: "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
