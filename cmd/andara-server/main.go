// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"context"
	"errors"
	"io"
	"net"
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

func run(args []string, env config.EnvLookup, stdout, stderr io.Writer) (exit int) {
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
	// An empty store's no_zones_found, held by LoadContent, is reported if
	// the process exits 1 before the boot decides, by any path, and dropped
	// otherwise (AW-SRV-042, #299).
	defer func() { rt.SettleHeld(exit) }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	rt.BuildVersion = version
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

	// The content publish path (AW-SRV-013), on a content.source=kafka
	// server: the store's write side, beside the Loader that reads it.
	var contentAdmin gateway.ContentAdmin
	publish, registry, err := rt.OpenContentAdmin(ctx)
	if err != nil {
		tel.Log.Error("content publish path", "detail", err.Error())
		return boot.ExitFail
	}
	if publish != nil {
		contentAdmin = publish
		defer func() { _ = registry.Close() }()
	}

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
		code := boot.StartExit(err)
		if boot.Lingers(err) && cfg.RecoveryMismatchLinger > 0 {
			// The mismatch is scraped before the process exits, or the
			// 0 never leaves it (AC-14). grpc.listen is never bound.
			if ln, lerr := net.Listen("tcp", cfg.HTTPListen()); lerr != nil {
				tel.Log.Error("http listen for the mismatch linger", "detail", lerr.Error())
			} else {
				rt.HoldMismatch(ctx, ln, cfg.RecoveryMismatchLinger, time.After)
			}
		}
		return code
	}
	loopCtx, stopLoop := context.WithCancel(context.Background())
	defer stopLoop()
	loopErr := make(chan error, 1)
	loopDone := make(chan struct{})
	go func() { loopErr <- loop.Run(loopCtx); close(loopDone) }()
	// halt stops the loop and waits for it, for every return below that is
	// not the drain. It returns what the loop returned, unless a select
	// below already took it.
	halt := func() error {
		stopLoop()
		<-loopDone
		select {
		case err := <-loopErr:
			return err
		default:
			return nil
		}
	}

	// The operator surface, so /readyz answers 503 while content comes into
	// effect and until the Gateway serves.
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
	// A signal during reconcile: the drain's exit, 0, or 5 for a boundary
	// lost before it (AW-SRV-026).
	signaled := func() int {
		err := halt()
		_ = srv.Shutdown(context.Background())
		if err != nil {
			tel.Log.Error("tick loop", "detail", err.Error())
			return boot.LoopExit(err)
		}
		return boot.ExitOK
	}
	select {
	case code := <-reconciled:
		stopReconcile()
		if code != boot.ExitOK && ctx.Err() != nil {
			// Reconcile failed because the signal canceled it, and its
			// result won the select over ctx.Done: still a signal, not an
			// exit 1, so the held no_zones_found is dropped (Codex on #369).
			return signaled()
		}
		if code != boot.ExitOK {
			_ = halt()
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
		return boot.LoopExit(err)
	case <-ctx.Done():
		stopReconcile()
		return signaled()
	}

	// A spawn Room the content in effect lacks fails the boot. A World
	// waiting for its first content has no Rooms yet; the Loader holds the
	// first version to the spawn Room instead (AW-SRV-042 AC-8).
	if err := rt.CheckSpawnInEffect(); err != nil && !rt.Waiting() {
		tel.Log.Error("roster", "detail", err.Error())
		_ = halt()
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
		ContentAdmin:            contentAdmin,
		Rechecker:               accounts,
		RecheckInterval:         cfg.AuthRecheckInterval,
		KeepaliveTimeout:        cfg.SessionLinkdeadDetect,
		Ingress:                 rt.Ingress,
		Egress:                  rt.Egress,
		Roster:                  rt.Roster,
		Content:                 rt.Content.InEffect,
		ContentWaiting:          rt.Waiting,
		TrustInboundTraceparent: cfg.TrustInboundTraceparent,
		OnDrain:                 func() { rt.Egress.Drain(); rt.Drain() },
		Log:                     tel.Log,
		Tracer:                  tel.Tracer,
		Registry:                tel.Reg,
	})
	if err != nil {
		tel.Log.Error("gateway", "detail", err.Error())
		_ = halt()
		_ = srv.Shutdown(context.Background())
		return boot.ExitFail
	}
	if err := gw.Start(); err != nil {
		tel.Log.Error("gateway", "detail", err.Error())
		_ = halt()
		_ = srv.Shutdown(context.Background())
		return boot.ExitFail
	}
	// Started once the Gateway serves: ready with content in effect
	// (AW-SRV-012), or, waiting for its first content, ready when it arrives
	// (AW-SRV-042).
	rt.MarkStarted()

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
			// Past sim.drain_timeout_ms, 1; a boundary lost as the drain
			// began, 5 (AW-SRV-026).
			tel.Log.Error("tick loop drain", "detail", loopDrain.Error())
			return boot.LoopExit(loopDrain)
		}
		return boot.ExitOK
	case err := <-loopErr:
		// The loop stopped on its own: an offset gap, or a source the
		// process cannot continue past, exits 1 rather than serve a World
		// that has stopped moving. A lost boundary exits 5, into exact
		// recovery (AW-SRV-026).
		if err == nil {
			err = errors.New("tick loop exited")
		}
		tel.Log.Error("tick loop", "detail", err.Error())
		_ = gw.Shutdown(context.Background())
		return boot.LoopExit(err)
	case err := <-gwErr:
		_ = halt()
		if err != nil {
			tel.Log.Error("gateway", "detail", err.Error())
			return boot.ExitFail
		}
		return boot.ExitOK
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			tel.Log.Error("http server", "detail", err.Error())
			_ = gw.Shutdown(context.Background())
			_ = halt()
			return boot.ExitFail
		}
		_ = halt()
		return boot.ExitOK
	}
}
