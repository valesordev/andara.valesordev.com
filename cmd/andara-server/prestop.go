// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/lifecycle"
)

// prestopMargin is what the call's bound allows beyond the sequence's own
// terms, for the socket round trip and the response.
const prestopMargin = 5 * time.Second

// runPrestop is `andara-server prestop` (AW-SRV-054), the pod's preStop hook:
// a thin client of Lifecycle.PrepareStop on deploy.control_socket. The serving
// process does the work and logs it; this prints the response and exits 0
// complete, 1 timeout, 2 store_error, 3 no socket (lifecycle.md, ruling 1).
// The arguments are the server's own configuration flags, so it reads the
// same keys the pod's server does.
func runPrestop(args []string, env config.EnvLookup, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(args, env, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return lifecycle.ExitComplete
		}
		// Without a configuration there is no socket to call.
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return lifecycle.ExitNoSocket
	}
	// Every term of the sequence, so a server that is working is never cut
	// off here; the kubelet's grace period is the outer bound (AW-INF-042).
	tick := time.Second / time.Duration(max(cfg.SimTickRate, 1))
	bound := cfg.DeployNoticeLead + cfg.DeployAdmitWait + cfg.SnapshotUploadTimeout + 4*tick + time.Second + prestopMargin
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	resp, err := lifecycle.Call(ctx, cfg.DeployControlSocket)
	code := lifecycle.Exit(resp, err)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "prestop: %v\n", err)
		return code
	}
	_, _ = io.WriteString(stdout, lifecycle.Format(resp))
	return code
}
