// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/telemetry"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

func run(args []string, env config.EnvLookup, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(args, env, stderr)
	if err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return boot.ExitFail
	}
	_ = stdout

	tel := telemetry.Setup(cfg, stderr)
	defer tel.Shutdown(context.Background())
	if tel.SetupErr != nil {
		// A misconfigured endpoint is a configuration error, fatal like a
		// bad certificate (AW-SRV-024).
		tel.Log.Error("telemetry", "detail", tel.SetupErr.Error())
		return boot.ExitFail
	}

	rt := boot.New(cfg, tel)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := rt.LoadContent(ctx)
	if rt.ContentMetrics != nil {
		rt.ContentMetrics.SetBuild(version, commit, cfg.Environment)
	}
	if cfg.ValidateOnly || code != boot.ExitOK {
		return code
	}

	// The verb table (AW-SRV-003): what the Gateway will accept. A file
	// that does not parse fails the boot.
	if err := rt.LoadVerbs(ctx); err != nil {
		tel.Log.Error("verb table", "detail", err.Error())
		return boot.ExitFail
	}

	// The account store (AW-SRV-008): keyring, broker, index replay,
	// bootstrap operator. A server that cannot authenticate anyone has
	// nothing to serve, so this is a boot failure like a bad certificate.
	accounts, err := rt.OpenAccounts(ctx)
	if err != nil {
		tel.Log.Error("accounts", "detail", err.Error())
		return boot.ExitFail
	}
	defer accounts.Close()

	// The Event fan-out (AW-SRV-004) and the two halves of the Protocol
	// the gateway takes as seams: Submit (AW-SRV-010) — parse, authorize,
	// produce — and Subscribe (AW-SRV-011), which streams from the fan-out.
	// The producer is closed after the gateway has drained.
	rt.StartEvents()
	if err := rt.StartIngress(ctx); err != nil {
		tel.Log.Error("ingress", "detail", err.Error())
		return boot.ExitFail
	}
	defer func() { _ = rt.CloseIngress() }()
	rt.StartEgress(ctx)
	// The roster (AW-SRV-014): Characters, and the binding of one to a
	// Session. Built before the loop runs, which reads it every tick; its
	// spawn Room is checked again below, against the content in effect.
	if err := rt.StartRoster(ctx); err != nil {
		tel.Log.Error("roster", "detail", err.Error())
		return boot.ExitFail
	}

	// The tick loop (AW-SRV-002), recovered from the log and running before
	// anything is served: the content in effect is whatever the log recorded
	// (AW-SRV-012), and ReconcileContent below brings the World to what the
	// content source names through the loop. A bad broker fails the boot
	// here, before anything is served.
	loop, err := rt.StartTickLoop(ctx)
	if err != nil {
		tel.Log.Error("tick loop", "detail", err.Error())
		return boot.ExitFail
	}
	loopCtx, stopLoop := context.WithCancel(context.Background())
	defer stopLoop()
	loopErr := make(chan error, 1)
	loopDone := make(chan struct{})
	go func() { loopErr <- loop.Run(loopCtx); close(loopDone) }()
	// halt stops the loop and waits for it, for every return below that is
	// not the drain.
	halt := func() { stopLoop(); <-loopDone }

	// The operator surface, so /readyz answers 503 while content comes into
	// effect.
	srv := &http.Server{
		Addr:              cfg.HTTPListen(),
		Handler:           rt.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		tel.Log.Info("http listen", "addr", srv.Addr)
		errCh <- srv.ListenAndServe()
	}()

	// Genesis on an empty log, or whatever moved while the process was down.
	// It waits for its swaps to apply, so a loop that stops meanwhile ends it.
	rctx, stopReconcile := context.WithCancel(ctx)
	reconciled := make(chan int, 1)
	go func() { reconciled <- rt.ReconcileContent(rctx) }()
	select {
	case code := <-reconciled:
		stopReconcile()
		if code != boot.ExitOK {
			halt()
			_ = srv.Shutdown(context.Background())
			return code
		}
	case err := <-loopErr:
		stopReconcile()
		if err == nil {
			err = errors.New("tick loop exited")
		}
		tel.Log.Error("tick loop", "detail", err.Error())
		_ = srv.Shutdown(context.Background())
		return boot.ExitFail
	case <-ctx.Done():
		stopReconcile()
		halt()
		_ = srv.Shutdown(context.Background())
		return boot.ExitOK
	}

	// A spawn Room the content in effect lacks fails the boot.
	if err := rt.CheckSpawnInEffect(); err != nil {
		tel.Log.Error("roster", "detail", err.Error())
		halt()
		_ = srv.Shutdown(context.Background())
		return boot.ExitFail
	}

	// The Protocol endpoint (AW-SRV-005). Built before the health server
	// listens so that a bad certificate fails the boot rather than a boot
	// that reports live and never serves.
	gw, err := gateway.New(gateway.Options{
		Listen:                  cfg.GRPCListen,
		TLSCertFile:             cfg.TLSCertFile,
		TLSKeyFile:              cfg.TLSKeyFile,
		MaxRecvBytes:            cfg.GRPCMaxRecvBytes,
		MaxRequestTimeout:       cfg.GRPCMaxRequestTimeout,
		DrainTimeout:            cfg.GRPCDrainTimeout,
		ProtocolMin:             cfg.ProtocolMinVersion,
		ProtocolMax:             cfg.ProtocolMaxVersion,
		Build:                   gateway.BuildInfo{Version: version, Commit: commit},
		Environment:             cfg.Environment,
		Verifier:                accounts,
		Auth:                    auth.NewService(accounts),
		Accounts:                auth.NewAdmin(accounts),
		Rechecker:               accounts,
		RecheckInterval:         cfg.AuthRecheckInterval,
		Ingress:                 rt.Ingress,
		Egress:                  rt.Egress,
		Roster:                  rt.Roster,
		TrustInboundTraceparent: cfg.TrustInboundTraceparent,
		OnDrain:                 func() { rt.Egress.Drain(); rt.Drain() },
		Log:                     tel.Log,
		Tracer:                  tel.Tracer,
		Registry:                tel.Reg,
	})
	if err != nil {
		tel.Log.Error("gateway", "detail", err.Error())
		halt()
		_ = srv.Shutdown(context.Background())
		return boot.ExitFail
	}
	if err := gw.Start(); err != nil {
		tel.Log.Error("gateway", "detail", err.Error())
		halt()
		_ = srv.Shutdown(context.Background())
		return boot.ExitFail
	}

	// Active Pointer moves, applied through the log for as long as the
	// process runs (AW-SRV-012). A no-op for the dir source.
	followCtx, stopFollow := context.WithCancel(ctx)
	defer stopFollow()
	go func() {
		if err := rt.FollowContent(followCtx); err != nil && followCtx.Err() == nil {
			tel.Log.Error("content follow stopped; pointer moves are no longer applied", "detail", err.Error())
		}
	}()

	gwErr := make(chan error, 1)
	go func() { gwErr <- gw.Wait() }()

	select {
	case <-ctx.Done():
		// Drain the Protocol first, then the operator surface, so /readyz
		// answers 503 for the whole drain. A drain that runs past its
		// timeout is logged and still exits 0: the process was asked to
		// stop and it stopped (AC-8).
		shctx, shcancel := context.WithTimeout(context.Background(), cfg.GRPCDrainTimeout+5*time.Second)
		defer shcancel()
		if err := gw.Shutdown(shctx); err != nil {
			tel.Log.Warn("gateway shutdown", "detail", err.Error())
		}
		_ = srv.Shutdown(shctx)
		// Then the loop: it completes the in-flight tick, checkpoints, and
		// emits SimulationStopped. Past sim.drain_timeout_ms it exits 1
		// naming the tick that would not complete (AW-SRV-002 AC-15).
		stopLoop()
		loopDrain := <-loopErr
		// SimulationStopped has reached every subscriber; end them.
		rt.Events.Close()
		if loopDrain != nil {
			tel.Log.Error("tick loop drain", "detail", loopDrain.Error())
			return boot.ExitFail
		}
		return boot.ExitOK
	case err := <-loopErr:
		// The loop stopped on its own: an offset gap, or a source the
		// process cannot continue past. Exit 1 rather than serve a World
		// that has stopped moving.
		if err == nil {
			err = errors.New("tick loop exited")
		}
		tel.Log.Error("tick loop", "detail", err.Error())
		_ = gw.Shutdown(context.Background())
		return boot.ExitFail
	case err := <-gwErr:
		halt()
		if err != nil {
			tel.Log.Error("gateway", "detail", err.Error())
			return boot.ExitFail
		}
		return boot.ExitOK
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			tel.Log.Error("http server", "detail", err.Error())
			_ = gw.Shutdown(context.Background())
			halt()
			return boot.ExitFail
		}
		halt()
		return boot.ExitOK
	}
}
