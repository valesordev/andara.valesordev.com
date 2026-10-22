// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/lifecycle"
)

// prestopArgs is the server configuration `prestop` reads: enough to parse,
// with the control socket last so a test can replace it.
func prestopArgs(socket string, extra ...string) []string {
	args := []string{"prestop", "--tls-cert-file=/tls/server.pem", "--tls-key-file=/tls/server-key.pem",
		"--auth-token-key-file=/auth/token-keys", "--kafka-brokers=127.0.0.1:9"}
	return append(append(args, extra...), "--deploy-control-socket="+socket)
}

// AW-SRV-054 AC-5, ruling 1: with no server listening on deploy.control_socket,
// `andara-server prestop` exits 3 and says why.
func TestPrestop_NoSocketExits3(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "control.sock")
	if code := run(prestopArgs(missing), emptyEnv, &stdout, &stderr); code != lifecycle.ExitNoSocket {
		t.Fatalf("exit %d, want %d; stderr=%s", code, lifecycle.ExitNoSocket, stderr.String())
	}
	if !strings.Contains(stderr.String(), "prestop:") || stdout.Len() != 0 {
		t.Fatalf("stdout %q stderr %q, want the cause on stderr only", stdout.String(), stderr.String())
	}
}

// A configuration prestop cannot parse names no socket to call: exit 3, with
// the configuration error on stderr.
func TestPrestop_BadConfigExits3(t *testing.T) {
	var stdout, stderr bytes.Buffer
	sock := filepath.Join(t.TempDir(), "control.sock")
	if code := run(prestopArgs(sock, "--deploy-image-tag=sha256:0123"), emptyEnv, &stdout, &stderr); code != lifecycle.ExitNoSocket {
		t.Fatalf("exit %d, want %d; stderr=%s", code, lifecycle.ExitNoSocket, stderr.String())
	}
	if !strings.Contains(stderr.String(), "deploy.image_tag") {
		t.Fatalf("stderr %q does not name deploy.image_tag", stderr.String())
	}
}
