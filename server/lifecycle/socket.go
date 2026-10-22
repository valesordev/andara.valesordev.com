// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/gen/go/andara/admin/v1/adminv1connect"
)

// SocketMode is the control socket's mode: the server's uid only (ruling 1).
const SocketMode os.FileMode = 0o600

// Server serves the Lifecycle service on a Unix socket.
type Server struct {
	path string
	ln   *net.UnixListener
	srv  *http.Server
}

// Listen binds the Lifecycle service at path, mode 0600, replacing a socket
// a previous process left behind. The socket is bound under a temporary name,
// given its mode, and renamed into place, so it is never reachable at path
// with a looser mode.
func Listen(path string, h adminv1connect.LifecycleHandler) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("lifecycle: control socket directory: %w", err)
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	_ = os.Remove(tmp)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("lifecycle: listen on %s: %w", path, err)
	}
	// The listener is closed by name below, not by the name it was bound to.
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(tmp, SocketMode); err != nil {
		_ = ln.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("lifecycle: chmod %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = ln.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("lifecycle: bind %s: %w", path, err)
	}
	mux := http.NewServeMux()
	mux.Handle(adminv1connect.NewLifecycleHandler(h))
	s := &Server{path: path, ln: ln, srv: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Close stops serving and removes the socket.
func (s *Server) Close() error {
	err := s.srv.Close()
	_ = os.Remove(s.path)
	return err
}

// The `andara-server prestop` exit codes (AC-5).
const (
	ExitComplete   = 0
	ExitTimeout    = 1
	ExitStoreError = 2
	ExitNoSocket   = 3
)

// ErrNoSocket: nothing answers on the control socket.
var ErrNoSocket = errors.New("no Lifecycle service on the control socket")

// Call is `andara-server prestop`'s one request: PrepareStop on the socket at
// path, bounded by ctx. A socket that doesn't exist or refuses the connection
// is ErrNoSocket.
func Call(ctx context.Context, path string) (*adminv1.PrepareStopResponse, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNoSocket, path, err)
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	lc := adminv1connect.NewLifecycleClient(client, "http://lifecycle")
	resp, err := lc.PrepareStop(ctx, connect.NewRequest(&adminv1.PrepareStopRequest{}))
	if err != nil {
		// Only a socket nothing listens on, or one removed since the Stat, is
		// a missing socket; a call that ran out of time is a timeout.
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w: %s: %w", ErrNoSocket, path, err)
		}
		return nil, err
	}
	return resp.Msg, nil
}

// Exit is the exit code for a Call's result (AC-5): 0 complete, 1 timeout
// (or no answer in time), 2 store error, 3 no socket.
func Exit(resp *adminv1.PrepareStopResponse, err error) int {
	switch {
	case errors.Is(err, ErrNoSocket):
		return ExitNoSocket
	case err != nil:
		return ExitTimeout
	}
	switch resp.GetOutcome() {
	case adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_COMPLETE:
		return ExitComplete
	case adminv1.PrepareStopOutcome_PREPARE_STOP_OUTCOME_STORE_ERROR:
		return ExitStoreError
	}
	return ExitTimeout
}

// Format is the response as `prestop` prints it: one key=value per line.
func Format(resp *adminv1.PrepareStopResponse) string {
	return fmt.Sprintf("outcome=%s\ntick=%d\nstate_hash=%x\ntag=%s\nduration_ms=%d\nrefused_ms=%d\ndeploy_id=%s\n",
		outcomeName(resp.GetOutcome()), resp.GetTick(), resp.GetStateHash(), resp.GetTag(),
		resp.GetDurationMs(), resp.GetRefusedMs(), resp.GetDeployId())
}
