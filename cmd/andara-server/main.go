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

	// The Submit path (AW-SRV-010): parse, authorize, produce. Built
	// before the gateway, which takes it as a seam; its producer is
	// closed after the gateway has drained.
	if err := rt.StartIngress(ctx); err != nil {
		tel.Log.Error("ingress", "detail", err.Error())
		return boot.ExitFail
	}
	defer func() { _ = rt.CloseIngress() }()

	// The Protocol endpoint (AW-SRV-005). Built before the health server
	// listens so that a bad certificate fails the boot rather than a boot
	// that reports live and never serves.
	gw, err := gateway.New(gateway.Options{
		Listen:            cfg.GRPCListen,
		TLSCertFile:       cfg.TLSCertFile,
		TLSKeyFile:        cfg.TLSKeyFile,
		MaxRecvBytes:      cfg.GRPCMaxRecvBytes,
		MaxRequestTimeout: cfg.GRPCMaxRequestTimeout,
		DrainTimeout:      cfg.GRPCDrainTimeout,
		ProtocolMin:       cfg.ProtocolMinVersion,
		ProtocolMax:       cfg.ProtocolMaxVersion,
		Build:             gateway.BuildInfo{Version: version, Commit: commit},
		Environment:       cfg.Environment,
		Verifier:          accounts,
		Auth:              auth.NewService(accounts),
		Accounts:          auth.NewAdmin(accounts),
		Rechecker:         accounts,
		RecheckInterval:   cfg.AuthRecheckInterval,
		Ingress:           rt.Ingress,
		OnDrain:           rt.Drain,
		Log:               tel.Log,
		Tracer:            tel.Tracer,
		Registry:          tel.Reg,
	})
	if err != nil {
		tel.Log.Error("gateway", "detail", err.Error())
		return boot.ExitFail
	}
	if err := gw.Start(); err != nil {
		tel.Log.Error("gateway", "detail", err.Error())
		return boot.ExitFail
	}

	// The tick loop (AW-SRV-002): built after the gateway so a bad broker
	// fails the boot before anything is served, run until the drain.
	loop, err := rt.StartTickLoop(ctx)
	if err != nil {
		tel.Log.Error("tick loop", "detail", err.Error())
		_ = gw.Shutdown(context.Background())
		return boot.ExitFail
	}
	loopCtx, stopLoop := context.WithCancel(context.Background())
	defer stopLoop()
	loopErr := make(chan error, 1)
	go func() { loopErr <- loop.Run(loopCtx) }()

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
		if err != nil {
			tel.Log.Error("gateway", "detail", err.Error())
			return boot.ExitFail
		}
		return boot.ExitOK
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			tel.Log.Error("http server", "detail", err.Error())
			_ = gw.Shutdown(context.Background())
			return boot.ExitFail
		}
		return boot.ExitOK
	}
}
