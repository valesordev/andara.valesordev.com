// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// andara-projector runs the projections that read the World's history into
// indexes. `state` (AW-SRV-019) is the only one today: a replica of the
// simulation that writes andara.state.v1.
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/projector"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
	"github.com/valesordev/andara/server/telemetry"
)

var (
	version = "dev"
	commit  = "unknown"
)

const usage = `usage: andara-projector <projection> [flags]

projections:
  state    replicate the simulation and write andara.state.v1 (AW-SRV-019)

exit codes (state): 0 clean stop · 1 config/store · 2 digest divergence · 3 log gap · 4 state_version
`

func main() {
	os.Exit(run(os.Args[1:], os.LookupEnv, os.Stderr))
}

func run(args []string, env config.EnvLookup, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = io.WriteString(stderr, usage)
		return projector.ExitConfig
	}
	if args[0] != "state" {
		_, _ = io.WriteString(stderr, "andara-projector: unknown projection "+args[0]+"\n"+usage)
		return projector.ExitConfig
	}
	cfg, err := config.ParseProjector(args[1:], env, stderr)
	if err != nil {
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return projector.ExitConfig
	}
	return runState(cfg, stderr)
}

func runState(cfg config.Projector, stderr io.Writer) int {
	tel := telemetry.Setup(cfg.Config, stderr)
	defer tel.Shutdown(context.Background())
	if tel.SetupErr != nil {
		tel.Log.Error("telemetry", "detail", tel.SetupErr.Error())
		return projector.ExitConfig
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	tel.Log.Info("andara-projector state starting", "version", version, "commit", commit,
		"group", config.ProjectorGroup(cfg.Environment), "batch_ticks", cfg.BatchTicks)

	// Content is loaded exactly as the server loads it — the same adapter,
	// validator, and Template registry — because the seed is derived from
	// the World, and a replica with another World hashes differently at
	// tick 1.
	rt := boot.New(cfg.Config, tel)
	if code := rt.LoadContent(ctx); code != boot.ExitOK {
		return projector.ExitConfig
	}
	zoneVersions := map[sim.ZoneID]string{}
	if rt.Content != nil {
		zoneVersions = rt.Content.ZoneVersions()
	}

	ws, err := store.Open(store.Options{
		Kind:       cfg.SnapshotStore,
		FSPath:     cfg.SnapshotFSPath,
		S3Bucket:   cfg.SnapshotS3Bucket,
		S3Endpoint: cfg.SnapshotS3Endpoint,
	})
	if err != nil {
		tel.Log.Error("snapshot store", "detail", err.Error())
		return projector.ExitConfig
	}

	metrics := projector.NewMetrics(tel.Reg)
	metrics.LagBudgetSeconds.Set(cfg.LagBudget.Seconds())
	var ready atomic.Bool
	srv := &http.Server{Addr: cfg.HTTPListen(), Handler: handler(tel, &ready), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		tel.Log.Info("http listen", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			tel.Log.Error("http server", "detail", err.Error())
			cancel()
		}
	}()
	defer func() {
		shctx, shcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shcancel()
		_ = srv.Shutdown(shctx)
	}()
	go topicBytes(ctx, cfg.KafkaBrokers, metrics)

	err = projector.Run(ctx, projector.RunOptions{
		Brokers:        cfg.KafkaBrokers,
		Group:          config.ProjectorGroup(cfg.Environment),
		World:          rt.World,
		Templates:      rt.Templates,
		Seed:           cfg.SimSeed,
		Store:          ws,
		Rebuild:        cfg.Rebuild,
		FromZero:       cfg.FromZero,
		BatchTicks:     cfg.BatchTicks,
		ContentVersion: func(z sim.ZoneID) string { return zoneVersions[z] },
		Metrics:        metrics,
		Log:            tel.Log,
		Tracer:         tel.Tracer,
		OnReady:        func() { ready.Store(true) },
	})
	ready.Store(false)
	code := projector.ExitCode(err)
	if err != nil {
		tel.Log.Error("state projector stopped", "detail", err.Error(), "exit_code", code)
	} else {
		tel.Log.Info("state projector stopped", "exit_code", code)
	}
	if code == projector.ExitDivergence {
		// Hold /metrics up, unready, for longer than a scrape interval before
		// exiting. andara_state_digest_mismatches_total is 1 only in this
		// process; exiting at once would restart it at 0 before any scrape
		// saw it, and StateProjectorDiverged would never fire.
		tel.Log.Error("holding /metrics for the divergence to be scraped", "for", haltLinger.String())
		select {
		case <-ctx.Done():
		case <-time.After(haltLinger):
		}
	}
	return code
}

// haltLinger is how long a diverged projector keeps serving /metrics before it
// exits 2: longer than any scrape interval the chart configures.
const haltLinger = 60 * time.Second

// handler serves /livez, /readyz (caught up to the log head), and /metrics.
func handler(tel *telemetry.Telemetry, ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "not ready\n", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(tel.Reg, promhttp.HandlerOpts{}))
	return mux
}

// topicBytes refreshes andara_state_topic_bytes every 30s: whether churn is
// outpacing the compactor is a trend, not a per-tick number.
func topicBytes(ctx context.Context, brokers []string, m *projector.Metrics) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID(projector.ClientID+"-meta"))
	if err != nil {
		return
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
		if n, err := projector.TopicBytes(qctx, adm, projector.StateTopic); err == nil {
			m.TopicBytes.Set(float64(n))
		}
		qcancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
