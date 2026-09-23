// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

//go:build unix

package cli

import (
	"os"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/internal/eventually"
)

// A signal while a Submit is being retried ends play at once, with the
// Session closed, rather than after the retries run out.
func TestPlay_SignalWhileSubmitBlocked(t *testing.T) {
	w := newWorld()
	s, env := playServer(t, w, nil)
	// The hold between read-only retries is what the signal must cut
	// through.
	w.answer = func(w *world, req *gamev1.SubmitRequest) (*gamev1.SubmitResponse, error) {
		if req.GetRaw() == "north" {
			return nil, ingressError(connect.CodeUnavailable, "world_read_only", "read only", 5*time.Second)
		}
		return defaultAnswer(w, req)
	}
	sc := newScript()
	stdout, _, wait := playLive(t, env, sc)
	stdout.await(t, "Here: Mara")
	sc.line(t, "north")
	eventually.True(t, 5*time.Second, "the retried Submit", func() bool { return len(w.submitted()) >= 2 })
	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := wait(); code != 0 {
		t.Fatalf("exit=%d\n%s", code, stdout.String())
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("play took %s to leave after SIGTERM", took)
	}
	if got := s.counter(t, "andara_sessions_total", map[string]string{"outcome": "closed"}); got != 1 {
		t.Errorf("sessions closed = %v, want 1", got)
	}
}
