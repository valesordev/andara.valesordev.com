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
	_ = version
	_ = commit

	tel := telemetry.Setup(cfg, stderr)
	defer tel.Shutdown(context.Background())

	rt := boot.New(cfg, tel)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := rt.LoadContent(ctx)
	if cfg.ValidateOnly || code != boot.ExitOK {
		return code
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
	select {
	case <-ctx.Done():
		shctx, shcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shcancel()
		_ = srv.Shutdown(shctx)
		return boot.ExitOK
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			tel.Log.Error("http server", "detail", err.Error())
			return boot.ExitFail
		}
		return boot.ExitOK
	}
}
