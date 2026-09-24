// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// ProjectorServiceName is the service the state projector logs, traces, and
// exports metrics under (AW-SRV-019). Fixed rather than read from
// ANDARA_SERVICE_NAME: the projector's Deployment shares the server's
// ConfigMap (AW-INF-003), and inheriting "andara-server" would put the
// replica's lines and spans under the server's name.
const ProjectorServiceName = "andara-projector-state"

// Defaults for the projector.state.* keys (AW-SRV-019).
const (
	DefaultProjectorBatchTicks = 10
	DefaultProjectorLagBudget  = 5 * time.Second
)

// Projector is the state projector's configuration: the keys it shares with
// the server — content, broker, seed, snapshot store, telemetry — plus its
// own. It reads the shared keys the way the server does, because a replica
// that loaded different content or derived a different seed would diverge
// at tick 1.
type Projector struct {
	Config

	// BatchTicks is how many ticks are replayed between produce flushes
	// (projector.state.batch_ticks).
	BatchTicks int
	// LagBudget is the ProjectionStale threshold (projector.state.lag_budget).
	// The projector exports its lag; the alert compares it with this.
	LagBudget time.Duration
	// Rebuild wipes the consumer group and bootstraps from the newest
	// complete round (--rebuild).
	Rebuild bool
	// FromZero bootstraps from offset zero even when a round exists
	// (--from-zero).
	FromZero bool
}

// ParseProjector resolves the projector's configuration: flag > env > file >
// default, like Parse. The projector.state.* keys are env and flag only; the
// chart configures through env.
//
// Only what a replica reads is validated. The server's TLS, auth, and ingress
// keys are not: the projector serves no Protocol, and refusing to start for
// want of a certificate it never loads would be a misconfiguration of its own.
func ParseProjector(args []string, env EnvLookup, errOut io.Writer) (Projector, error) {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	c := defaults()
	configPath := peekConfigPath(args, env)
	if configPath != "" {
		if err := applyFile(&c, configPath); err != nil {
			return Projector{}, err
		}
	}
	if err := applyEnv(&c, env); err != nil {
		return Projector{}, err
	}
	p := Projector{Config: c, BatchTicks: DefaultProjectorBatchTicks, LagBudget: DefaultProjectorLagBudget}
	if v, ok := env("ANDARA_PROJECTOR_BATCH_TICKS"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return Projector{}, fmt.Errorf("projector.state.batch_ticks must be an integer, got %q", v)
		}
		p.BatchTicks = n
	}
	if v, ok := env("ANDARA_PROJECTOR_LAG_BUDGET"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return Projector{}, fmt.Errorf("projector.state.lag_budget must be a duration, got %q", v)
		}
		p.LagBudget = d
	}

	fs := flag.NewFlagSet("andara-projector state", flag.ContinueOnError)
	fs.SetOutput(errOut)
	_ = fs.String("config", configPath, "YAML config file (ANDARA_CONFIG)")
	fs.StringVar(&p.ContentSource, "content-source", p.ContentSource, "content source: kafka or dir (ANDARA_CONTENT_SOURCE)")
	fs.StringVar(&p.ContentPath, "content-path", p.ContentPath, "directory of Zone Definition JSON files (dir source)")
	fs.Func("content-packs", "packs to follow; \"*\" for all (ANDARA_CONTENT_PACKS)", func(v string) error {
		p.ContentPacks = splitList(v)
		return nil
	})
	fs.StringVar(&p.ContentCacheDir, "content-cache-dir", p.ContentCacheDir, "hash-keyed blob cache directory (ANDARA_CONTENT_CACHE_DIR)")
	fs.Func("kafka-brokers", "comma-separated broker addresses (ANDARA_KAFKA_BROKERS)", func(v string) error {
		p.KafkaBrokers = splitList(v)
		return nil
	})
	fs.Uint64Var(&p.SimSeed, "sim-seed", p.SimSeed, "the server's PRNG seed; 0 derives it from the World, as the server does (ANDARA_SIM_SEED)")
	fs.StringVar(&p.SnapshotStore, "snapshot-store", p.SnapshotStore, "where snapshot rounds are read from: fs or s3 (ANDARA_SNAPSHOT_STORE)")
	fs.StringVar(&p.SnapshotFSPath, "snapshot-fs-path", p.SnapshotFSPath, "directory the fs store reads under (ANDARA_SNAPSHOT_FS_PATH)")
	fs.StringVar(&p.SnapshotS3Bucket, "snapshot-s3-bucket", p.SnapshotS3Bucket, "bucket the s3 store reads (ANDARA_SNAPSHOT_S3_BUCKET)")
	fs.StringVar(&p.SnapshotS3Endpoint, "snapshot-s3-endpoint", p.SnapshotS3Endpoint, "S3 endpoint (ANDARA_SNAPSHOT_S3_ENDPOINT)")
	fs.StringVar(&p.HTTPPort, "http-port", p.HTTPPort, "health and metrics port (ANDARA_HTTP_PORT)")
	fs.IntVar(&p.BatchTicks, "batch-ticks", p.BatchTicks, "ticks replayed between produce flushes (ANDARA_PROJECTOR_BATCH_TICKS)")
	fs.DurationVar(&p.LagBudget, "lag-budget", p.LagBudget, "ProjectionStale threshold (ANDARA_PROJECTOR_LAG_BUDGET)")
	fs.BoolVar(&p.Rebuild, "rebuild", false, "wipe the consumer group and bootstrap from the newest complete snapshot round")
	fs.BoolVar(&p.FromZero, "from-zero", false, "bootstrap from offset zero even when a snapshot round exists")
	if err := fs.Parse(args); err != nil {
		return Projector{}, err
	}
	if fs.NArg() > 0 {
		return Projector{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	p.ServiceName = ProjectorServiceName

	if p.ContentSource != "kafka" && p.ContentSource != "dir" {
		return Projector{}, fmt.Errorf("content.source must be kafka or dir, got %q", p.ContentSource)
	}
	if len(p.ContentPacks) == 0 {
		return Projector{}, fmt.Errorf("content.packs must name at least one pack, or %q for all", "*")
	}
	if len(p.KafkaBrokers) == 0 {
		return Projector{}, fmt.Errorf("the state projector requires kafka.brokers (ANDARA_KAFKA_BROKERS)")
	}
	switch p.SnapshotStore {
	case SnapshotStoreFS, SnapshotStoreS3:
	default:
		return Projector{}, fmt.Errorf("snapshot.store must be %s or %s, got %q", SnapshotStoreFS, SnapshotStoreS3, p.SnapshotStore)
	}
	if p.SnapshotStore == SnapshotStoreS3 && p.SnapshotS3Bucket == "" {
		return Projector{}, fmt.Errorf("snapshot.store=s3 requires snapshot.s3_bucket (ANDARA_SNAPSHOT_S3_BUCKET)")
	}
	if p.BatchTicks < 1 {
		return Projector{}, fmt.Errorf("projector.state.batch_ticks must be at least 1, got %d", p.BatchTicks)
	}
	if p.LagBudget <= 0 {
		return Projector{}, fmt.Errorf("projector.state.lag_budget must be positive, got %s", p.LagBudget)
	}
	return p, nil
}

// ProjectorGroup is the state projector's consumer group for an environment
// (AW-SRV-019): the offsets it has produced through are committed under it.
func ProjectorGroup(env string) string { return "andara-projector-state-" + env }
