// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	rt := boot.New(cfg, tel)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := rt.LoadContent(ctx)
	if cfg.ValidateOnly || code != boot.ExitOK {
		return code
	}

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
		return boot.ExitOK
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
