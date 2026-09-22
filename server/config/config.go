// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is andara-server's process configuration (AW-SRV-001, AW-SRV-005).
type Config struct {
	ContentSource string
	ContentPath   string
	StrictOrphans bool
	ValidateOnly  bool
	HTTPPort      string
	ServiceName   string
	Environment   string
	LogFormat     string
	LogLevel      string
	OTLPEndpoint  string

	// Gateway (AW-SRV-005). TLS material is required to serve: there is no
	// plaintext mode and no flag to create one (ADR-0003).
	GRPCListen            string
	TLSCertFile           string
	TLSKeyFile            string
	GRPCMaxRecvBytes      int
	GRPCMaxRequestTimeout time.Duration
	GRPCDrainTimeout      time.Duration
	ProtocolMinVersion    uint32
	ProtocolMaxVersion    uint32

	// Broker (AW-SRV-008 is the first story to open a client). Comma-separated
	// in the env var and the flag, a list in the file.
	KafkaBrokers []string

	// Accounts and authentication (AW-SRV-008). AuthStore is kafka or memory;
	// memory loses every Account on restart and exists for development.
	AuthStore           string
	AuthSessionTTL      time.Duration
	AuthRefreshTTL      time.Duration
	AuthTokenKeyFile    string
	AuthArgon2MemoryKiB uint32
	AuthArgon2Time      uint32
	AuthArgon2Threads   uint32
	AuthRateLimit       string
	AuthInviteTTL       time.Duration
	AuthRecheckInterval time.Duration
	AuthK8sIssuer       string
	AuthK8sJWKSURL      string
	// AuthBootstrapOperator is `username:password` for the first operator,
	// applied only when the index holds no operator at all. Every later
	// operator is created by one through Admin.CreateAccount.
	AuthBootstrapOperator string

	// Session lifecycle (ADR-0006). Only the ceiling is read today, because
	// auth.session_ttl must exceed it; AW-SRV-015 reads the rest.
	SessionLinkdeadMax time.Duration

	// The roster (AW-SRV-014, ADR-0006): how many Characters an Account
	// holds, where a new one spawns as zone_id/room_id — the boot refuses
	// a value the loaded content does not resolve — and the RE2 rule a
	// name must match.
	CharacterMaxPerAccount int
	CharacterSpawnRoom     string
	CharacterNamePattern   string

	// The tick loop (AW-SRV-002, ADR-0008). SimSource is kafka or memory;
	// memory ticks a World with no input and exists for development.
	SimSource               string
	SimTickRate             int
	SimTickBudget           time.Duration
	SimMaxPerTick           int
	SimDrainTimeout         time.Duration
	SimSeed                 uint64
	SimPartitions           []int32
	SimCheckpointEveryTicks int

	// The command pipeline (AW-SRV-003). MaxIntentBytes bounds what parse
	// will read; VerbTablePath replaces the built-in verb table, empty
	// meaning built in.
	MaxIntentBytes int
	VerbTablePath  string

	// The Event fan-out (AW-SRV-004): Events a subscriber may leave unread
	// before it is dropped, and how many subscriptions the process accepts.
	SubscriberBuffer int
	MaxSubscribers   int

	// Command ingress (AW-SRV-010). RateLimit and AgentRateLimit are
	// N/period per Session, Burst the bucket depth; ProduceDeadline bounds
	// one produce; MaxPending bounds a Session's Submits in flight;
	// TransitHold is how long a Session's Intents wait for its Character
	// to arrive in the next Zone; IdempotencyWindow is how long a Submit's
	// outcome is remembered against its client_ref (AW-SRV-031).
	IngressRateLimit         string
	IngressAgentRateLimit    string
	IngressBurst             int
	IngressProduceDeadline   time.Duration
	IngressMaxPending        int
	IngressTransitHold       time.Duration
	IngressIdempotencyWindow time.Duration

	// Event egress (AW-SRV-011). EgressBuffer is how many Events a
	// Session's stream may leave unsent before the stream is ended;
	// EgressResumeWindow how many delivered Events the Session retains for
	// a resume; HeartbeatInterval how long a stream may be silent before a
	// heartbeat frame is sent.
	EgressBuffer       int
	EgressResumeWindow int
	HeartbeatInterval  time.Duration

	// Zone snapshots (AW-SRV-006). SnapshotInterval is the cadence of a
	// round — one consistent cut of every owned Zone at a tick boundary —
	// and it sets RTO, not RPO: RPO is zero and is decided by broker
	// settings (docs/specs/slo/recovery.md). Zero disables snapshots, which
	// is not a supported production posture; recovery then replays the log
	// from its beginning, which is correct and slow.
	//
	// SnapshotMaxStall bounds the in-tick copy — the part players can feel —
	// and is a warning threshold, not a refusal. SnapshotStore is fs or s3;
	// SnapshotUploadTimeout fails a round rather than queueing it behind the
	// next one.
	SnapshotInterval      time.Duration
	SnapshotMaxStall      time.Duration
	SnapshotStore         string
	SnapshotFSPath        string
	SnapshotS3Bucket      string
	SnapshotS3Endpoint    string
	SnapshotUploadTimeout time.Duration

	// TraceSampleRatio is the head-sampling ratio for the Game/Submit
	// trace root (AW-SRV-010); rejections are kept whatever it says.
	// TrustInboundTraceparent lets a client's traceparent parent the RPC
	// span — and carry the sampling decision. Off by default.
	TraceSampleRatio        float64
	TrustInboundTraceparent bool
}

const (
	DefaultContentSource = "kafka"
	DefaultContentPath   = "./content"
	DefaultHTTPPort      = "8080"
	DefaultServiceName   = "andara-server"
	DefaultEnvironment   = "local"
	DefaultLogFormat     = "json"
	DefaultLogLevel      = "info"
	DefaultOTLPEndpoint  = "localhost:4317"

	DefaultGRPCListen            = ":8443"
	DefaultGRPCMaxRecvBytes      = 65536
	DefaultGRPCMaxRequestTimeout = 30 * time.Second
	DefaultGRPCDrainTimeout      = 15 * time.Second
	DefaultProtocolMinVersion    = 1
	DefaultProtocolMaxVersion    = 1

	DefaultAuthStore           = "kafka"
	DefaultAuthSessionTTL      = time.Hour
	DefaultAuthRefreshTTL      = 720 * time.Hour
	DefaultAuthArgon2MemoryKiB = 65536
	DefaultAuthArgon2Time      = 3
	DefaultAuthArgon2Threads   = 4
	DefaultAuthRateLimit       = "10/m"
	DefaultAuthInviteTTL       = 168 * time.Hour
	DefaultAuthRecheckInterval = 30 * time.Second
	DefaultSessionLinkdeadMax  = 300 * time.Second

	DefaultCharacterMaxPerAccount = 5
	// DefaultCharacterSpawnRoom is the dev fixture's Room (Brian,
	// 2026-09-21); real content sets its own.
	DefaultCharacterSpawnRoom   = "town/plaza"
	DefaultCharacterNamePattern = `^[\p{L}][\p{L}' -]{2,23}$`

	DefaultSimSource               = "kafka"
	DefaultSimTickRate             = 10
	DefaultSimTickBudgetMS         = 50
	DefaultSimMaxPerTick           = 1024
	DefaultSimDrainTimeoutMS       = 5000
	DefaultSimCheckpointEveryTicks = 100

	DefaultMaxIntentBytes = 4096

	DefaultSubscriberBuffer = 1024
	DefaultMaxSubscribers   = 10000

	DefaultIngressRateLimit         = "20/s"
	DefaultIngressAgentRateLimit    = "100/s"
	DefaultIngressBurst             = 40
	DefaultIngressProduceDeadline   = 2 * time.Second
	DefaultIngressMaxPending        = 256
	DefaultIngressTransitHold       = 2 * time.Second
	DefaultIngressIdempotencyWindow = 30 * time.Second
	DefaultEgressBuffer             = 1024
	DefaultEgressResumeWindow       = 2048
	DefaultHeartbeatInterval        = 20 * time.Second

	// Snapshots (AW-SRV-006). The interval is docs/specs/slo/recovery.md's;
	// the stall budget is AC-1's threshold.
	DefaultSnapshotInterval      = 60 * time.Second
	DefaultSnapshotMaxStallMS    = 5
	DefaultSnapshotStore         = "fs"
	DefaultSnapshotFSPath        = "/var/lib/andara/snapshots"
	DefaultSnapshotUploadTimeout = 30 * time.Second

	DefaultTraceSampleRatio = 0.01
)

// The stores snapshot.store accepts.
const (
	SnapshotStoreFS = "fs"
	SnapshotStoreS3 = "s3"
)

// EnvLookup looks up an environment variable.
type EnvLookup func(string) (string, bool)

// Parse resolves flag > env > file > default. args must not include argv0.
func Parse(args []string, env EnvLookup, errOut io.Writer) (Config, error) {
	if env == nil {
		env = func(string) (string, bool) { return "", false }
	}
	c := Config{
		ContentSource: DefaultContentSource,
		ContentPath:   DefaultContentPath,
		HTTPPort:      DefaultHTTPPort,
		ServiceName:   DefaultServiceName,
		Environment:   DefaultEnvironment,
		LogFormat:     DefaultLogFormat,
		LogLevel:      DefaultLogLevel,
		OTLPEndpoint:  DefaultOTLPEndpoint,

		GRPCListen:            DefaultGRPCListen,
		GRPCMaxRecvBytes:      DefaultGRPCMaxRecvBytes,
		GRPCMaxRequestTimeout: DefaultGRPCMaxRequestTimeout,
		GRPCDrainTimeout:      DefaultGRPCDrainTimeout,
		ProtocolMinVersion:    DefaultProtocolMinVersion,
		ProtocolMaxVersion:    DefaultProtocolMaxVersion,

		AuthStore:           DefaultAuthStore,
		AuthSessionTTL:      DefaultAuthSessionTTL,
		AuthRefreshTTL:      DefaultAuthRefreshTTL,
		AuthArgon2MemoryKiB: DefaultAuthArgon2MemoryKiB,
		AuthArgon2Time:      DefaultAuthArgon2Time,
		AuthArgon2Threads:   DefaultAuthArgon2Threads,
		AuthRateLimit:       DefaultAuthRateLimit,
		AuthInviteTTL:       DefaultAuthInviteTTL,
		AuthRecheckInterval: DefaultAuthRecheckInterval,
		SessionLinkdeadMax:  DefaultSessionLinkdeadMax,

		CharacterMaxPerAccount: DefaultCharacterMaxPerAccount,
		CharacterSpawnRoom:     DefaultCharacterSpawnRoom,
		CharacterNamePattern:   DefaultCharacterNamePattern,

		SimSource:                DefaultSimSource,
		SimTickRate:              DefaultSimTickRate,
		SimTickBudget:            DefaultSimTickBudgetMS * time.Millisecond,
		SimMaxPerTick:            DefaultSimMaxPerTick,
		SimDrainTimeout:          DefaultSimDrainTimeoutMS * time.Millisecond,
		SimPartitions:            allPartitions(),
		SimCheckpointEveryTicks:  DefaultSimCheckpointEveryTicks,
		MaxIntentBytes:           DefaultMaxIntentBytes,
		SubscriberBuffer:         DefaultSubscriberBuffer,
		MaxSubscribers:           DefaultMaxSubscribers,
		IngressRateLimit:         DefaultIngressRateLimit,
		IngressAgentRateLimit:    DefaultIngressAgentRateLimit,
		IngressBurst:             DefaultIngressBurst,
		IngressProduceDeadline:   DefaultIngressProduceDeadline,
		IngressMaxPending:        DefaultIngressMaxPending,
		IngressTransitHold:       DefaultIngressTransitHold,
		IngressIdempotencyWindow: DefaultIngressIdempotencyWindow,
		EgressBuffer:             DefaultEgressBuffer,
		EgressResumeWindow:       DefaultEgressResumeWindow,
		HeartbeatInterval:        DefaultHeartbeatInterval,
		SnapshotInterval:         DefaultSnapshotInterval,
		SnapshotMaxStall:         DefaultSnapshotMaxStallMS * time.Millisecond,
		SnapshotStore:            DefaultSnapshotStore,
		SnapshotFSPath:           DefaultSnapshotFSPath,
		SnapshotUploadTimeout:    DefaultSnapshotUploadTimeout,
		TraceSampleRatio:         DefaultTraceSampleRatio,
	}
	configPath := peekConfigPath(args, env)
	if configPath != "" {
		if err := applyFile(&c, configPath); err != nil {
			return Config{}, err
		}
	}
	if err := applyEnv(&c, env); err != nil {
		return Config{}, err
	}

	fs := flag.NewFlagSet("andara-server", flag.ContinueOnError)
	fs.SetOutput(errOut)
	_ = fs.String("config", configPath, "YAML config file (ANDARA_CONFIG)")
	fs.StringVar(&c.ContentSource, "content-source", c.ContentSource, "content source: kafka or dir")
	fs.StringVar(&c.ContentPath, "content-path", c.ContentPath, "directory of Zone Definition JSON files (dir source)")
	fs.BoolVar(&c.StrictOrphans, "strict-orphans", c.StrictOrphans, "treat orphan Rooms as errors")
	fs.BoolVar(&c.ValidateOnly, "validate-only", c.ValidateOnly, "load and validate, then exit")
	fs.StringVar(&c.HTTPPort, "http-port", c.HTTPPort, "plain-text health and metrics port")
	fs.StringVar(&c.GRPCListen, "grpc-listen", c.GRPCListen, "TLS listen address for Game and Admin (ANDARA_GRPC_LISTEN)")
	fs.StringVar(&c.TLSCertFile, "tls-cert-file", c.TLSCertFile, "PEM server certificate; required (ANDARA_TLS_CERT_FILE)")
	fs.StringVar(&c.TLSKeyFile, "tls-key-file", c.TLSKeyFile, "PEM server private key; required (ANDARA_TLS_KEY_FILE)")
	fs.IntVar(&c.GRPCMaxRecvBytes, "grpc-max-recv-bytes", c.GRPCMaxRecvBytes, "largest accepted request message (ANDARA_GRPC_MAX_RECV_BYTES)")
	fs.DurationVar(&c.GRPCMaxRequestTimeout, "grpc-max-request-timeout", c.GRPCMaxRequestTimeout, "deadline applied to unary RPCs that carry none (ANDARA_GRPC_MAX_REQUEST_TIMEOUT)")
	fs.DurationVar(&c.GRPCDrainTimeout, "grpc-drain-timeout", c.GRPCDrainTimeout, "how long shutdown waits for in-flight RPCs (ANDARA_GRPC_DRAIN_TIMEOUT)")
	fs.Func("protocol-min-version", "lowest Protocol version accepted (ANDARA_PROTOCOL_MIN)", func(v string) error {
		return parseVersion(v, &c.ProtocolMinVersion)
	})
	fs.Func("protocol-max-version", "highest Protocol version accepted (ANDARA_PROTOCOL_MAX)", func(v string) error {
		return parseVersion(v, &c.ProtocolMaxVersion)
	})
	fs.Func("kafka-brokers", "comma-separated broker addresses (ANDARA_KAFKA_BROKERS)", func(v string) error {
		c.KafkaBrokers = splitList(v)
		return nil
	})
	fs.StringVar(&c.AuthStore, "auth-store", c.AuthStore, "account store: kafka or memory (ANDARA_AUTH_STORE)")
	fs.DurationVar(&c.AuthSessionTTL, "auth-session-ttl", c.AuthSessionTTL, "session token lifetime; must exceed session.linkdead_max (ANDARA_AUTH_SESSION_TTL)")
	fs.DurationVar(&c.AuthRefreshTTL, "auth-refresh-ttl", c.AuthRefreshTTL, "refresh token lifetime (ANDARA_AUTH_REFRESH_TTL)")
	fs.StringVar(&c.AuthTokenKeyFile, "auth-token-key-file", c.AuthTokenKeyFile, "file of `key_id: base64` signing keys, first is current; required, mode 0400 (ANDARA_AUTH_TOKEN_KEY_FILE)")
	fs.Func("auth-argon2-memory-kib", "Argon2id memory in KiB (ANDARA_AUTH_ARGON2_MEMORY_KIB)", func(v string) error {
		return parseUint32("auth.argon2.memory_kib", v, &c.AuthArgon2MemoryKiB)
	})
	fs.Func("auth-argon2-time", "Argon2id time parameter (ANDARA_AUTH_ARGON2_TIME)", func(v string) error {
		return parseUint32("auth.argon2.time", v, &c.AuthArgon2Time)
	})
	fs.Func("auth-argon2-threads", "Argon2id parallelism (ANDARA_AUTH_ARGON2_THREADS)", func(v string) error {
		return parseUint32("auth.argon2.threads", v, &c.AuthArgon2Threads)
	})
	fs.StringVar(&c.AuthRateLimit, "auth-rate-limit", c.AuthRateLimit, "auth attempts per username and per peer, N/period (ANDARA_AUTH_RATE_LIMIT)")
	fs.DurationVar(&c.AuthInviteTTL, "auth-invite-ttl", c.AuthInviteTTL, "invite code lifetime (ANDARA_AUTH_INVITE_TTL)")
	fs.DurationVar(&c.AuthRecheckInterval, "auth-recheck-interval", c.AuthRecheckInterval, "how often open Sessions re-read Account status and roles (ANDARA_AUTH_RECHECK_INTERVAL)")
	fs.StringVar(&c.AuthK8sIssuer, "auth-k8s-issuer", c.AuthK8sIssuer, "issuer of workload JWTs for agent accounts; unset disables the kind (ANDARA_AUTH_K8S_ISSUER)")
	fs.StringVar(&c.AuthK8sJWKSURL, "auth-k8s-jwks-url", c.AuthK8sJWKSURL, "JWKS endpoint for auth.k8s_issuer (ANDARA_AUTH_K8S_JWKS_URL)")
	fs.StringVar(&c.AuthBootstrapOperator, "auth-bootstrap-operator", c.AuthBootstrapOperator, "username:password for the first operator account; ignored once one exists (ANDARA_AUTH_BOOTSTRAP_OPERATOR)")
	fs.StringVar(&c.SimSource, "sim-source", c.SimSource, "command source for the tick loop: kafka or memory (ANDARA_SIM_SOURCE)")
	fs.IntVar(&c.SimTickRate, "sim-tick-rate", c.SimTickRate, "ticks per second (ANDARA_TICK_RATE)")
	fs.Func("sim-tick-budget-ms", "overrun threshold in milliseconds (ANDARA_TICK_BUDGET_MS)", func(v string) error {
		return parseMillis("sim.tick_budget_ms", v, &c.SimTickBudget)
	})
	fs.IntVar(&c.SimMaxPerTick, "sim-max-per-tick", c.SimMaxPerTick, "records applied per tick; excess deferred (ANDARA_MAX_PER_TICK)")
	fs.Func("sim-drain-timeout-ms", "graceful shutdown budget in milliseconds (ANDARA_DRAIN_TIMEOUT_MS)", func(v string) error {
		return parseMillis("sim.drain_timeout_ms", v, &c.SimDrainTimeout)
	})
	fs.Uint64Var(&c.SimSeed, "sim-seed", c.SimSeed, "PRNG seed; 0 derives one from the World (ANDARA_SIM_SEED)")
	fs.Func("sim-partitions", "assigned Partitions, e.g. 0-63 or 0,2,4 (ANDARA_SIM_PARTITIONS)", func(v string) error {
		ps, err := ParsePartitions(v)
		if err != nil {
			return err
		}
		c.SimPartitions = ps
		return nil
	})
	fs.IntVar(&c.SimCheckpointEveryTicks, "sim-checkpoint-every-ticks", c.SimCheckpointEveryTicks, "offset commit cadence in ticks (ANDARA_CHECKPOINT_EVERY_TICKS)")
	fs.IntVar(&c.MaxIntentBytes, "max-intent-bytes", c.MaxIntentBytes, "largest Intent parse will read, in bytes (ANDARA_MAX_INTENT_BYTES)")
	fs.IntVar(&c.SubscriberBuffer, "subscriber-buffer", c.SubscriberBuffer, "Events a subscriber may leave unread before it is dropped (ANDARA_SUBSCRIBER_BUFFER)")
	fs.IntVar(&c.MaxSubscribers, "max-subscribers", c.MaxSubscribers, "Event subscriptions this process accepts (ANDARA_MAX_SUBSCRIBERS)")
	fs.StringVar(&c.VerbTablePath, "verb-table", c.VerbTablePath, "JSON verb table replacing the built-in one; empty uses the built-in (ANDARA_VERB_TABLE)")
	fs.StringVar(&c.IngressRateLimit, "ingress-rate-limit", c.IngressRateLimit, "Submits per Session, N/period (ANDARA_INGRESS_RATE_LIMIT)")
	fs.StringVar(&c.IngressAgentRateLimit, "ingress-agent-rate-limit", c.IngressAgentRateLimit, "Submits per agent Session, N/period (ANDARA_AGENT_RATE_LIMIT)")
	fs.IntVar(&c.IngressBurst, "ingress-burst", c.IngressBurst, "Submit token bucket depth per Session (ANDARA_INGRESS_BURST)")
	fs.DurationVar(&c.IngressProduceDeadline, "ingress-produce-deadline", c.IngressProduceDeadline, "how long one produce to the Command log may take (ANDARA_PRODUCE_DEADLINE)")
	fs.IntVar(&c.IngressMaxPending, "ingress-max-pending", c.IngressMaxPending, "Submits one Session may have in flight (ANDARA_INGRESS_MAX_PENDING)")
	fs.DurationVar(&c.IngressTransitHold, "ingress-transit-hold", c.IngressTransitHold, "how long a Session's Intents wait for its Character to arrive in the next Zone (ANDARA_INGRESS_TRANSIT_HOLD)")
	fs.DurationVar(&c.IngressIdempotencyWindow, "ingress-idempotency-window", c.IngressIdempotencyWindow, "how long a Submit's outcome is remembered against its client_ref; must exceed the produce deadline (ANDARA_INGRESS_IDEMPOTENCY_WINDOW)")
	fs.IntVar(&c.EgressBuffer, "egress-buffer", c.EgressBuffer, "Events a Session's stream may leave unsent before it is ended (ANDARA_EGRESS_BUFFER)")
	fs.IntVar(&c.EgressResumeWindow, "egress-resume-window", c.EgressResumeWindow, "delivered Events a Session retains for a resume (ANDARA_EGRESS_RESUME_WINDOW)")
	fs.DurationVar(&c.HeartbeatInterval, "heartbeat-interval", c.HeartbeatInterval, "how long a stream may be silent before a heartbeat frame is sent (ANDARA_HEARTBEAT_INTERVAL)")
	fs.DurationVar(&c.SnapshotInterval, "snapshot-interval", c.SnapshotInterval, "how often a snapshot round runs; 0 disables snapshots (ANDARA_SNAPSHOT_INTERVAL)")
	fs.Func("snapshot-max-stall-ms", "budget for the in-tick snapshot copy in milliseconds; a warning, not a refusal (ANDARA_SNAPSHOT_MAX_STALL_MS)", func(v string) error {
		return parseMillis("snapshot.max_stall_ms", v, &c.SnapshotMaxStall)
	})
	fs.StringVar(&c.SnapshotStore, "snapshot-store", c.SnapshotStore, "where snapshot objects go: fs or s3 (ANDARA_SNAPSHOT_STORE)")
	fs.StringVar(&c.SnapshotFSPath, "snapshot-fs-path", c.SnapshotFSPath, "directory the fs store writes under (ANDARA_SNAPSHOT_FS_PATH)")
	fs.StringVar(&c.SnapshotS3Bucket, "snapshot-s3-bucket", c.SnapshotS3Bucket, "bucket the s3 store writes to; required when snapshot.store=s3 (ANDARA_SNAPSHOT_S3_BUCKET)")
	fs.StringVar(&c.SnapshotS3Endpoint, "snapshot-s3-endpoint", c.SnapshotS3Endpoint, "S3 endpoint; MinIO locally, empty for AWS (ANDARA_SNAPSHOT_S3_ENDPOINT)")
	fs.DurationVar(&c.SnapshotUploadTimeout, "snapshot-upload-timeout", c.SnapshotUploadTimeout, "a round exceeding this is failed, not queued behind the next (ANDARA_SNAPSHOT_UPLOAD_TIMEOUT)")
	fs.Float64Var(&c.TraceSampleRatio, "trace-sample-ratio", c.TraceSampleRatio, "fraction of Game/Submit traces exported; rejections always are (ANDARA_TRACE_SAMPLE_RATIO)")
	fs.BoolVar(&c.TrustInboundTraceparent, "trust-inbound-traceparent", c.TrustInboundTraceparent, "let a client's traceparent parent the RPC span and decide its sampling (ANDARA_TRUST_INBOUND_TRACEPARENT)")
	fs.DurationVar(&c.SessionLinkdeadMax, "session-linkdead-max", c.SessionLinkdeadMax, "hard ceiling on linkdead duration; auth.session_ttl must exceed it (ANDARA_LINKDEAD_MAX)")
	fs.IntVar(&c.CharacterMaxPerAccount, "character-max-per-account", c.CharacterMaxPerAccount, "Characters an Account may hold (ANDARA_CHARACTER_MAX_PER_ACCOUNT)")
	fs.StringVar(&c.CharacterSpawnRoom, "character-spawn-room", c.CharacterSpawnRoom, "zone_id/room_id a new Character spawns in; must resolve against the loaded content (ANDARA_CHARACTER_SPAWN_ROOM)")
	fs.StringVar(&c.CharacterNamePattern, "character-name-pattern", c.CharacterNamePattern, "RE2 pattern a Character name must match (ANDARA_CHARACTER_NAME_PATTERN)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if c.ContentSource != "kafka" && c.ContentSource != "dir" {
		return Config{}, fmt.Errorf("content.source must be kafka or dir, got %q", c.ContentSource)
	}
	if err := c.validateGateway(); err != nil {
		return Config{}, err
	}
	if err := c.validateAuth(); err != nil {
		return Config{}, err
	}
	if err := c.validateSim(); err != nil {
		return Config{}, err
	}
	if err := c.validateSnapshot(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validateSnapshot rejects a snapshot configuration the round cannot run
// under. A negative interval or stall budget is a typo; an s3 store with no
// bucket is a deployment that would fail on its first round, sixty seconds
// after the process looked healthy, which is exactly the kind of failure
// that belongs at startup instead.
func (c Config) validateSnapshot() error {
	switch c.SnapshotStore {
	case SnapshotStoreFS:
		if c.SnapshotInterval > 0 && c.SnapshotFSPath == "" {
			return fmt.Errorf("snapshot.store=fs requires snapshot.fs_path (ANDARA_SNAPSHOT_FS_PATH)")
		}
	case SnapshotStoreS3:
		if c.SnapshotS3Bucket == "" {
			return fmt.Errorf("snapshot.store=s3 requires snapshot.s3_bucket (ANDARA_SNAPSHOT_S3_BUCKET)")
		}
	default:
		return fmt.Errorf("snapshot.store must be %s or %s, got %q", SnapshotStoreFS, SnapshotStoreS3, c.SnapshotStore)
	}
	if c.SnapshotInterval < 0 {
		return fmt.Errorf("snapshot.interval must not be negative, got %s", c.SnapshotInterval)
	}
	// Zero is legal and the chart's registry allows it: a zero stall budget
	// warns on every round, which is a way to watch the copy's cost rather
	// than a misconfiguration.
	if c.SnapshotMaxStall < 0 {
		return fmt.Errorf("snapshot.max_stall_ms must not be negative, got %s", c.SnapshotMaxStall)
	}
	if c.SnapshotUploadTimeout <= 0 {
		return fmt.Errorf("snapshot.upload_timeout must be positive, got %s", c.SnapshotUploadTimeout)
	}
	// A round that cannot finish before the next one starts would either
	// queue or overlap; the story's answer is that it fails, so the two must
	// not be configured to guarantee it.
	if c.SnapshotInterval > 0 && c.SnapshotUploadTimeout > c.SnapshotInterval {
		return fmt.Errorf("snapshot.upload_timeout (%s) must not exceed snapshot.interval (%s)", c.SnapshotUploadTimeout, c.SnapshotInterval)
	}
	return nil
}

// validateSim rejects a tick configuration the loop cannot run. The budget
// must fit inside the interval: a budget past it means overruns can never
// warn before lag accrues, which is the point of the budget (ADR-0008).
func (c Config) validateSim() error {
	switch c.SimSource {
	case "kafka":
		if !c.ValidateOnly && len(c.KafkaBrokers) == 0 {
			return fmt.Errorf("sim.source=kafka requires kafka.brokers (ANDARA_KAFKA_BROKERS)")
		}
	case "memory":
	default:
		return fmt.Errorf("sim.source must be kafka or memory, got %q", c.SimSource)
	}
	if c.SimTickRate < 1 || c.SimTickRate > 100 {
		return fmt.Errorf("sim.tick_rate must be 1..100, got %d", c.SimTickRate)
	}
	if c.SimTickBudget <= 0 {
		return fmt.Errorf("sim.tick_budget_ms must be positive, got %s", c.SimTickBudget)
	}
	if interval := time.Second / time.Duration(c.SimTickRate); c.SimTickBudget > interval {
		return fmt.Errorf("sim.tick_budget_ms (%s) must not exceed the tick interval (%s at %d Hz)", c.SimTickBudget, interval, c.SimTickRate)
	}
	if c.SimMaxPerTick < 1 || c.SimCheckpointEveryTicks < 1 {
		return fmt.Errorf("sim.max_per_tick and sim.checkpoint_every_ticks must be positive")
	}
	if c.SimDrainTimeout < 0 {
		return fmt.Errorf("sim.drain_timeout_ms must not be negative")
	}
	if len(c.SimPartitions) == 0 {
		return fmt.Errorf("sim.partitions must name at least one Partition")
	}
	if c.MaxIntentBytes < 1 {
		return fmt.Errorf("command.max_intent_bytes must be positive, got %d", c.MaxIntentBytes)
	}
	if c.SubscriberBuffer < 1 || c.MaxSubscribers < 1 {
		return fmt.Errorf("events.subscriber_buffer and events.max_subscribers must be positive")
	}
	if c.IngressBurst < 1 || c.IngressMaxPending < 1 {
		return fmt.Errorf("ingress.burst and ingress.max_pending must be positive")
	}
	if c.IngressProduceDeadline <= 0 || c.IngressTransitHold < 0 {
		return fmt.Errorf("ingress.produce_deadline must be positive and ingress.transit_hold must not be negative")
	}
	if c.IngressIdempotencyWindow <= c.IngressProduceDeadline {
		return fmt.Errorf("ingress.idempotency_window must exceed ingress.produce_deadline")
	}
	if c.EgressBuffer < 1 || c.EgressResumeWindow < c.EgressBuffer {
		return fmt.Errorf("egress.buffer must be positive and egress.resume_window at least egress.buffer")
	}
	if c.HeartbeatInterval <= 0 {
		return fmt.Errorf("egress.heartbeat_interval must be positive")
	}
	if c.TraceSampleRatio < 0 || c.TraceSampleRatio > 1 {
		return fmt.Errorf("telemetry.trace_sample_ratio must be in [0, 1], got %g", c.TraceSampleRatio)
	}
	return nil
}

func parseMillis(key, v string, dst *time.Duration) error {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return fmt.Errorf("%s must be a non-negative integer of milliseconds, got %q", key, v)
	}
	*dst = time.Duration(n) * time.Millisecond
	return nil
}

func allPartitions() []int32 {
	out := make([]int32, 64)
	for i := range out {
		out[i] = int32(i)
	}
	return out
}

// ParsePartitions reads the two forms sim.partitions takes: a range
// (`0-63`) or a comma-separated list (`0,2,4`, which is what the chart's
// init container writes from the pod ordinal). Sorted, deduplicated, each
// in [0, 64).
func ParsePartitions(v string) ([]int32, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, fmt.Errorf("sim.partitions must not be empty")
	}
	seen := map[int32]bool{}
	add := func(n int) error {
		if n < 0 || n >= 64 {
			return fmt.Errorf("sim.partitions: %d is outside 0..63", n)
		}
		seen[int32(n)] = true
		return nil
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || a > b {
				return nil, fmt.Errorf("sim.partitions: bad range %q", part)
			}
			for n := a; n <= b; n++ {
				if err := add(n); err != nil {
					return nil, err
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("sim.partitions: %q is not a partition", part)
		}
		if err := add(n); err != nil {
			return nil, err
		}
	}
	out := make([]int32, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// validateAuth rejects a configuration the account store cannot serve.
// Like TLS, the key file is required to serve and --validate-only is
// exempt. The session_ttl > linkdead_max assertion is ADR-0006's: a token
// that expires inside the grace period turns every linkdead reconnect into
// an authentication failure.
func (c Config) validateAuth() error {
	switch c.AuthStore {
	case "kafka":
		if !c.ValidateOnly && len(c.KafkaBrokers) == 0 {
			return fmt.Errorf("auth.store=kafka requires kafka.brokers (ANDARA_KAFKA_BROKERS)")
		}
	case "memory":
	default:
		return fmt.Errorf("auth.store must be kafka or memory, got %q", c.AuthStore)
	}
	if !c.ValidateOnly && c.AuthTokenKeyFile == "" {
		return fmt.Errorf("auth.token_key_file is required (ANDARA_AUTH_TOKEN_KEY_FILE)")
	}
	if c.AuthSessionTTL <= 0 || c.AuthRefreshTTL <= 0 || c.AuthInviteTTL <= 0 || c.AuthRecheckInterval <= 0 {
		return fmt.Errorf("auth.session_ttl, auth.refresh_ttl, auth.invite_ttl, and auth.recheck_interval must be positive")
	}
	if c.SessionLinkdeadMax <= 0 {
		return fmt.Errorf("session.linkdead_max must be positive, got %s", c.SessionLinkdeadMax)
	}
	if c.AuthSessionTTL <= c.SessionLinkdeadMax {
		return fmt.Errorf("auth.session_ttl (%s) must exceed session.linkdead_max (%s), or a linkdead reconnect fails on authentication", c.AuthSessionTTL, c.SessionLinkdeadMax)
	}
	if c.CharacterMaxPerAccount < 1 {
		return fmt.Errorf("character.max_per_account must be positive, got %d", c.CharacterMaxPerAccount)
	}
	if _, _, err := c.SpawnRoom(); err != nil {
		return err
	}
	if _, err := regexp.Compile(c.CharacterNamePattern); err != nil {
		return fmt.Errorf("character.name_pattern: %w", err)
	}
	if c.AuthArgon2MemoryKiB < 8 || c.AuthArgon2Time < 1 || c.AuthArgon2Threads < 1 || c.AuthArgon2Threads > 255 {
		return fmt.Errorf("auth.argon2: memory_kib >= 8, time >= 1, 1 <= threads <= 255 required")
	}
	if (c.AuthK8sIssuer == "") != (c.AuthK8sJWKSURL == "") {
		return fmt.Errorf("auth.k8s_issuer and auth.k8s_jwks_url must be set together")
	}
	if c.AuthBootstrapOperator != "" && !strings.Contains(c.AuthBootstrapOperator, ":") {
		return fmt.Errorf("auth.bootstrap_operator must be username:password")
	}
	return nil
}

func parseUint32(key, v string, dst *uint32) error {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return fmt.Errorf("%s must be an unsigned integer, got %q", key, v)
	}
	*dst = uint32(n)
	return nil
}

// splitList reads a comma-separated list, dropping empties.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validateGateway rejects a configuration the gateway cannot serve. TLS
// material is checked here, at parse time, so that a misconfigured process
// fails before it loads content rather than after (AW-SRV-005: "starting
// without TLS material is a fatal configuration error"). --validate-only
// never opens a listener and is exempt.
func (c Config) validateGateway() error {
	if !c.ValidateOnly && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
		return fmt.Errorf("grpc.tls_cert_file and grpc.tls_key_file are required (ANDARA_TLS_CERT_FILE, ANDARA_TLS_KEY_FILE); there is no plaintext mode")
	}
	if c.GRPCListen == "" {
		return fmt.Errorf("grpc.listen must not be empty")
	}
	if c.GRPCMaxRecvBytes <= 0 {
		return fmt.Errorf("grpc.max_recv_bytes must be positive, got %d", c.GRPCMaxRecvBytes)
	}
	if c.GRPCMaxRequestTimeout <= 0 {
		return fmt.Errorf("grpc.max_request_timeout must be positive, got %s", c.GRPCMaxRequestTimeout)
	}
	if c.GRPCDrainTimeout <= 0 {
		return fmt.Errorf("grpc.drain_timeout must be positive, got %s", c.GRPCDrainTimeout)
	}
	// Version 0 is what proto3 sends when the field is omitted, so it can never
	// be a version a client meant. A range that admits it would silently accept
	// clients that never negotiated at all.
	if c.ProtocolMinVersion < 1 {
		return fmt.Errorf("protocol.min_version must be at least 1, got %d", c.ProtocolMinVersion)
	}
	if c.ProtocolMinVersion > c.ProtocolMaxVersion {
		return fmt.Errorf("protocol.min_version (%d) exceeds protocol.max_version (%d)", c.ProtocolMinVersion, c.ProtocolMaxVersion)
	}
	return nil
}

func parseVersion(v string, dst *uint32) error {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return fmt.Errorf("protocol version must be an unsigned integer, got %q", v)
	}
	*dst = uint32(n)
	return nil
}

type fileConfig struct {
	Content *struct {
		Source        *string `yaml:"source"`
		Path          *string `yaml:"path"`
		StrictOrphans *bool   `yaml:"strict_orphans"`
	} `yaml:"content"`
	HTTP *struct {
		Port *string `yaml:"port"`
	} `yaml:"http"`
	Telemetry *struct {
		OTLPEndpoint            *string  `yaml:"otlp_endpoint"`
		ServiceName             *string  `yaml:"service_name"`
		Environment             *string  `yaml:"environment"`
		LogFormat               *string  `yaml:"log_format"`
		LogLevel                *string  `yaml:"log_level"`
		TraceSampleRatio        *float64 `yaml:"trace_sample_ratio"`
		TrustInboundTraceparent *bool    `yaml:"trust_inbound_traceparent"`
	} `yaml:"telemetry"`
	GRPC *struct {
		Listen            *string `yaml:"listen"`
		TLSCertFile       *string `yaml:"tls_cert_file"`
		TLSKeyFile        *string `yaml:"tls_key_file"`
		MaxRecvBytes      *int    `yaml:"max_recv_bytes"`
		MaxRequestTimeout *string `yaml:"max_request_timeout"`
		DrainTimeout      *string `yaml:"drain_timeout"`
	} `yaml:"grpc"`
	Protocol *struct {
		MinVersion *uint32 `yaml:"min_version"`
		MaxVersion *uint32 `yaml:"max_version"`
	} `yaml:"protocol"`
	Kafka *struct {
		Brokers []string `yaml:"brokers"`
	} `yaml:"kafka"`
	Auth *struct {
		Store        *string `yaml:"store"`
		SessionTTL   *string `yaml:"session_ttl"`
		RefreshTTL   *string `yaml:"refresh_ttl"`
		TokenKeyFile *string `yaml:"token_key_file"`
		Argon2       *struct {
			MemoryKiB *uint32 `yaml:"memory_kib"`
			Time      *uint32 `yaml:"time"`
			Threads   *uint32 `yaml:"threads"`
		} `yaml:"argon2"`
		RateLimit         *string `yaml:"rate_limit"`
		InviteTTL         *string `yaml:"invite_ttl"`
		RecheckInterval   *string `yaml:"recheck_interval"`
		K8sIssuer         *string `yaml:"k8s_issuer"`
		K8sJWKSURL        *string `yaml:"k8s_jwks_url"`
		BootstrapOperator *string `yaml:"bootstrap_operator"`
	} `yaml:"auth"`
	Session *struct {
		LinkdeadMax *string `yaml:"linkdead_max"`
	} `yaml:"session"`
	Character *struct {
		MaxPerAccount *int    `yaml:"max_per_account"`
		SpawnRoom     *string `yaml:"spawn_room"`
		NamePattern   *string `yaml:"name_pattern"`
	} `yaml:"character"`
	Sim *struct {
		Source               *string `yaml:"source"`
		TickRate             *int    `yaml:"tick_rate"`
		TickBudgetMS         *int    `yaml:"tick_budget_ms"`
		MaxPerTick           *int    `yaml:"max_per_tick"`
		DrainTimeoutMS       *int    `yaml:"drain_timeout_ms"`
		Seed                 *uint64 `yaml:"seed"`
		Partitions           *string `yaml:"partitions"`
		CheckpointEveryTicks *int    `yaml:"checkpoint_every_ticks"`
	} `yaml:"sim"`
	Command *struct {
		MaxIntentBytes *int    `yaml:"max_intent_bytes"`
		VerbTablePath  *string `yaml:"verb_table_path"`
	} `yaml:"command"`
	Events *struct {
		SubscriberBuffer *int `yaml:"subscriber_buffer"`
		MaxSubscribers   *int `yaml:"max_subscribers"`
	} `yaml:"events"`
	Ingress *struct {
		RateLimit         *string `yaml:"rate_limit"`
		AgentRateLimit    *string `yaml:"agent_rate_limit"`
		Burst             *int    `yaml:"burst"`
		ProduceDeadline   *string `yaml:"produce_deadline"`
		MaxPending        *int    `yaml:"max_pending"`
		TransitHold       *string `yaml:"transit_hold"`
		IdempotencyWindow *string `yaml:"idempotency_window"`
	} `yaml:"ingress"`
	Egress *struct {
		Buffer            *int    `yaml:"buffer"`
		ResumeWindow      *int    `yaml:"resume_window"`
		HeartbeatInterval *string `yaml:"heartbeat_interval"`
	} `yaml:"egress"`
	Snapshot *struct {
		Interval      *string `yaml:"interval"`
		MaxStallMS    *int    `yaml:"max_stall_ms"`
		Store         *string `yaml:"store"`
		FSPath        *string `yaml:"fs_path"`
		S3Bucket      *string `yaml:"s3_bucket"`
		S3Endpoint    *string `yaml:"s3_endpoint"`
		UploadTimeout *string `yaml:"upload_timeout"`
	} `yaml:"snapshot"`
}

func peekConfigPath(args []string, env EnvLookup) string {
	if v, ok := env("ANDARA_CONFIG"); ok && v != "" {
		return v
	}
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--config=") {
			return strings.TrimPrefix(a, "--config=")
		}
	}
	return ""
}

func applyFile(c *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read config file %s: %w", path, err)
	}
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return fmt.Errorf("invalid YAML in config file %s: %w", path, err)
	}
	if fc.Content != nil {
		if fc.Content.Source != nil {
			c.ContentSource = *fc.Content.Source
		}
		if fc.Content.Path != nil {
			c.ContentPath = *fc.Content.Path
		}
		if fc.Content.StrictOrphans != nil {
			c.StrictOrphans = *fc.Content.StrictOrphans
		}
	}
	if fc.HTTP != nil && fc.HTTP.Port != nil {
		c.HTTPPort = *fc.HTTP.Port
	}
	if fc.Telemetry != nil {
		if fc.Telemetry.OTLPEndpoint != nil {
			c.OTLPEndpoint = *fc.Telemetry.OTLPEndpoint
		}
		if fc.Telemetry.ServiceName != nil {
			c.ServiceName = *fc.Telemetry.ServiceName
		}
		if fc.Telemetry.Environment != nil {
			c.Environment = *fc.Telemetry.Environment
		}
		if fc.Telemetry.TraceSampleRatio != nil {
			c.TraceSampleRatio = *fc.Telemetry.TraceSampleRatio
		}
		if fc.Telemetry.TrustInboundTraceparent != nil {
			c.TrustInboundTraceparent = *fc.Telemetry.TrustInboundTraceparent
		}
		if fc.Telemetry.LogFormat != nil {
			c.LogFormat = *fc.Telemetry.LogFormat
		}
		if fc.Telemetry.LogLevel != nil {
			c.LogLevel = *fc.Telemetry.LogLevel
		}
	}
	if fc.GRPC != nil {
		if fc.GRPC.Listen != nil {
			c.GRPCListen = *fc.GRPC.Listen
		}
		if fc.GRPC.TLSCertFile != nil {
			c.TLSCertFile = *fc.GRPC.TLSCertFile
		}
		if fc.GRPC.TLSKeyFile != nil {
			c.TLSKeyFile = *fc.GRPC.TLSKeyFile
		}
		if fc.GRPC.MaxRecvBytes != nil {
			c.GRPCMaxRecvBytes = *fc.GRPC.MaxRecvBytes
		}
		if fc.GRPC.MaxRequestTimeout != nil {
			if err := parseDuration("grpc.max_request_timeout", *fc.GRPC.MaxRequestTimeout, &c.GRPCMaxRequestTimeout); err != nil {
				return fmt.Errorf("config file %s: %w", path, err)
			}
		}
		if fc.GRPC.DrainTimeout != nil {
			if err := parseDuration("grpc.drain_timeout", *fc.GRPC.DrainTimeout, &c.GRPCDrainTimeout); err != nil {
				return fmt.Errorf("config file %s: %w", path, err)
			}
		}
	}
	if fc.Protocol != nil {
		if fc.Protocol.MinVersion != nil {
			c.ProtocolMinVersion = *fc.Protocol.MinVersion
		}
		if fc.Protocol.MaxVersion != nil {
			c.ProtocolMaxVersion = *fc.Protocol.MaxVersion
		}
	}
	if fc.Kafka != nil && fc.Kafka.Brokers != nil {
		c.KafkaBrokers = fc.Kafka.Brokers
	}
	if a := fc.Auth; a != nil {
		if a.Store != nil {
			c.AuthStore = *a.Store
		}
		if a.TokenKeyFile != nil {
			c.AuthTokenKeyFile = *a.TokenKeyFile
		}
		if a.RateLimit != nil {
			c.AuthRateLimit = *a.RateLimit
		}
		if a.K8sIssuer != nil {
			c.AuthK8sIssuer = *a.K8sIssuer
		}
		if a.K8sJWKSURL != nil {
			c.AuthK8sJWKSURL = *a.K8sJWKSURL
		}
		if a.BootstrapOperator != nil {
			c.AuthBootstrapOperator = *a.BootstrapOperator
		}
		if a.Argon2 != nil {
			if a.Argon2.MemoryKiB != nil {
				c.AuthArgon2MemoryKiB = *a.Argon2.MemoryKiB
			}
			if a.Argon2.Time != nil {
				c.AuthArgon2Time = *a.Argon2.Time
			}
			if a.Argon2.Threads != nil {
				c.AuthArgon2Threads = *a.Argon2.Threads
			}
		}
		for _, d := range []struct {
			key string
			v   *string
			dst *time.Duration
		}{
			{"auth.session_ttl", a.SessionTTL, &c.AuthSessionTTL},
			{"auth.refresh_ttl", a.RefreshTTL, &c.AuthRefreshTTL},
			{"auth.invite_ttl", a.InviteTTL, &c.AuthInviteTTL},
			{"auth.recheck_interval", a.RecheckInterval, &c.AuthRecheckInterval},
		} {
			if d.v != nil {
				if err := parseDuration(d.key, *d.v, d.dst); err != nil {
					return fmt.Errorf("config file %s: %w", path, err)
				}
			}
		}
	}
	if fc.Session != nil && fc.Session.LinkdeadMax != nil {
		if err := parseDuration("session.linkdead_max", *fc.Session.LinkdeadMax, &c.SessionLinkdeadMax); err != nil {
			return fmt.Errorf("config file %s: %w", path, err)
		}
	}
	if ch := fc.Character; ch != nil {
		if ch.MaxPerAccount != nil {
			c.CharacterMaxPerAccount = *ch.MaxPerAccount
		}
		if ch.SpawnRoom != nil {
			c.CharacterSpawnRoom = *ch.SpawnRoom
		}
		if ch.NamePattern != nil {
			c.CharacterNamePattern = *ch.NamePattern
		}
	}
	if sm := fc.Sim; sm != nil {
		if sm.Source != nil {
			c.SimSource = *sm.Source
		}
		if sm.TickRate != nil {
			c.SimTickRate = *sm.TickRate
		}
		if sm.TickBudgetMS != nil {
			c.SimTickBudget = time.Duration(*sm.TickBudgetMS) * time.Millisecond
		}
		if sm.MaxPerTick != nil {
			c.SimMaxPerTick = *sm.MaxPerTick
		}
		if sm.DrainTimeoutMS != nil {
			c.SimDrainTimeout = time.Duration(*sm.DrainTimeoutMS) * time.Millisecond
		}
		if sm.Seed != nil {
			c.SimSeed = *sm.Seed
		}
		if sm.Partitions != nil {
			ps, err := ParsePartitions(*sm.Partitions)
			if err != nil {
				return fmt.Errorf("config file %s: %w", path, err)
			}
			c.SimPartitions = ps
		}
		if sm.CheckpointEveryTicks != nil {
			c.SimCheckpointEveryTicks = *sm.CheckpointEveryTicks
		}
	}
	if cm := fc.Command; cm != nil {
		if cm.MaxIntentBytes != nil {
			c.MaxIntentBytes = *cm.MaxIntentBytes
		}
		if cm.VerbTablePath != nil {
			c.VerbTablePath = *cm.VerbTablePath
		}
	}
	if ev := fc.Events; ev != nil {
		if ev.SubscriberBuffer != nil {
			c.SubscriberBuffer = *ev.SubscriberBuffer
		}
		if ev.MaxSubscribers != nil {
			c.MaxSubscribers = *ev.MaxSubscribers
		}
	}
	if in := fc.Ingress; in != nil {
		if in.RateLimit != nil {
			c.IngressRateLimit = *in.RateLimit
		}
		if in.AgentRateLimit != nil {
			c.IngressAgentRateLimit = *in.AgentRateLimit
		}
		if in.Burst != nil {
			c.IngressBurst = *in.Burst
		}
		if in.MaxPending != nil {
			c.IngressMaxPending = *in.MaxPending
		}
		for _, d := range []struct {
			key string
			v   *string
			dst *time.Duration
		}{
			{"ingress.produce_deadline", in.ProduceDeadline, &c.IngressProduceDeadline},
			{"ingress.transit_hold", in.TransitHold, &c.IngressTransitHold},
			{"ingress.idempotency_window", in.IdempotencyWindow, &c.IngressIdempotencyWindow},
		} {
			if d.v != nil {
				if err := parseDuration(d.key, *d.v, d.dst); err != nil {
					return fmt.Errorf("config file %s: %w", path, err)
				}
			}
		}
	}
	if eg := fc.Egress; eg != nil {
		if eg.Buffer != nil {
			c.EgressBuffer = *eg.Buffer
		}
		if eg.ResumeWindow != nil {
			c.EgressResumeWindow = *eg.ResumeWindow
		}
		if eg.HeartbeatInterval != nil {
			if err := parseDuration("egress.heartbeat_interval", *eg.HeartbeatInterval, &c.HeartbeatInterval); err != nil {
				return fmt.Errorf("config file %s: %w", path, err)
			}
		}
	}
	if sn := fc.Snapshot; sn != nil {
		for _, sv := range []struct {
			v   *string
			dst *string
		}{
			{sn.Store, &c.SnapshotStore},
			{sn.FSPath, &c.SnapshotFSPath},
			{sn.S3Bucket, &c.SnapshotS3Bucket},
			{sn.S3Endpoint, &c.SnapshotS3Endpoint},
		} {
			if sv.v != nil {
				*sv.dst = *sv.v
			}
		}
		for _, d := range []struct {
			key string
			v   *string
			dst *time.Duration
		}{
			{"snapshot.interval", sn.Interval, &c.SnapshotInterval},
			{"snapshot.upload_timeout", sn.UploadTimeout, &c.SnapshotUploadTimeout},
		} {
			if d.v != nil {
				if err := parseDuration(d.key, *d.v, d.dst); err != nil {
					return fmt.Errorf("config file %s: %w", path, err)
				}
			}
		}
		if sn.MaxStallMS != nil {
			c.SnapshotMaxStall = time.Duration(*sn.MaxStallMS) * time.Millisecond
		}
	}
	return nil
}

func parseDuration(key, v string, dst *time.Duration) error {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return fmt.Errorf("%s must be a duration such as 30s, got %q", key, v)
	}
	*dst = d
	return nil
}

func applyEnv(c *Config, env EnvLookup) error {
	if v, ok := env("ANDARA_CONTENT_SOURCE"); ok {
		c.ContentSource = v
	}
	if v, ok := env("ANDARA_CONTENT_PATH"); ok {
		c.ContentPath = v
	}
	if v, ok := env("ANDARA_STRICT_ORPHANS"); ok {
		c.StrictOrphans = parseBool(v)
	}
	if v, ok := env("ANDARA_HTTP_PORT"); ok {
		c.HTTPPort = v
	}
	if v, ok := env("ANDARA_SERVICE_NAME"); ok {
		c.ServiceName = v
	}
	if v, ok := env("ANDARA_ENV"); ok {
		c.Environment = v
	}
	if v, ok := env("ANDARA_LOG_FORMAT"); ok {
		c.LogFormat = v
	}
	if v, ok := env("ANDARA_LOG_LEVEL"); ok {
		c.LogLevel = v
	}
	if v, ok := env("ANDARA_OTLP_ENDPOINT"); ok {
		c.OTLPEndpoint = v
	}
	if v, ok := env("ANDARA_GRPC_LISTEN"); ok {
		c.GRPCListen = v
	}
	if v, ok := env("ANDARA_TLS_CERT_FILE"); ok {
		c.TLSCertFile = v
	}
	if v, ok := env("ANDARA_TLS_KEY_FILE"); ok {
		c.TLSKeyFile = v
	}
	if v, ok := env("ANDARA_GRPC_MAX_RECV_BYTES"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("ANDARA_GRPC_MAX_RECV_BYTES must be an integer, got %q", v)
		}
		c.GRPCMaxRecvBytes = n
	}
	if v, ok := env("ANDARA_GRPC_MAX_REQUEST_TIMEOUT"); ok {
		if err := parseDuration("ANDARA_GRPC_MAX_REQUEST_TIMEOUT", v, &c.GRPCMaxRequestTimeout); err != nil {
			return err
		}
	}
	if v, ok := env("ANDARA_GRPC_DRAIN_TIMEOUT"); ok {
		if err := parseDuration("ANDARA_GRPC_DRAIN_TIMEOUT", v, &c.GRPCDrainTimeout); err != nil {
			return err
		}
	}
	for _, dv := range []struct {
		name string
		dst  *time.Duration
	}{
		{"ANDARA_PRODUCE_DEADLINE", &c.IngressProduceDeadline},
		{"ANDARA_INGRESS_TRANSIT_HOLD", &c.IngressTransitHold},
		{"ANDARA_INGRESS_IDEMPOTENCY_WINDOW", &c.IngressIdempotencyWindow},
		{"ANDARA_HEARTBEAT_INTERVAL", &c.HeartbeatInterval},
	} {
		if v, ok := env(dv.name); ok {
			if err := parseDuration(dv.name, v, dv.dst); err != nil {
				return err
			}
		}
	}
	if v, ok := env("ANDARA_TRUST_INBOUND_TRACEPARENT"); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("ANDARA_TRUST_INBOUND_TRACEPARENT must be true or false, got %q", v)
		}
		c.TrustInboundTraceparent = b
	}
	if v, ok := env("ANDARA_TRACE_SAMPLE_RATIO"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("ANDARA_TRACE_SAMPLE_RATIO must be a number in [0, 1], got %q", v)
		}
		c.TraceSampleRatio = f
	}
	if v, ok := env("ANDARA_PROTOCOL_MIN"); ok {
		if err := parseVersion(v, &c.ProtocolMinVersion); err != nil {
			return fmt.Errorf("ANDARA_PROTOCOL_MIN: %w", err)
		}
	}
	if v, ok := env("ANDARA_PROTOCOL_MAX"); ok {
		if err := parseVersion(v, &c.ProtocolMaxVersion); err != nil {
			return fmt.Errorf("ANDARA_PROTOCOL_MAX: %w", err)
		}
	}
	if v, ok := env("ANDARA_KAFKA_BROKERS"); ok {
		c.KafkaBrokers = splitList(v)
	}
	for _, sv := range []struct {
		name string
		dst  *string
	}{
		{"ANDARA_AUTH_STORE", &c.AuthStore},
		{"ANDARA_AUTH_TOKEN_KEY_FILE", &c.AuthTokenKeyFile},
		{"ANDARA_AUTH_RATE_LIMIT", &c.AuthRateLimit},
		{"ANDARA_AUTH_K8S_ISSUER", &c.AuthK8sIssuer},
		{"ANDARA_AUTH_K8S_JWKS_URL", &c.AuthK8sJWKSURL},
		{"ANDARA_AUTH_BOOTSTRAP_OPERATOR", &c.AuthBootstrapOperator},
		{"ANDARA_INGRESS_RATE_LIMIT", &c.IngressRateLimit},
		{"ANDARA_AGENT_RATE_LIMIT", &c.IngressAgentRateLimit},
		{"ANDARA_SNAPSHOT_STORE", &c.SnapshotStore},
		{"ANDARA_SNAPSHOT_FS_PATH", &c.SnapshotFSPath},
		{"ANDARA_SNAPSHOT_S3_BUCKET", &c.SnapshotS3Bucket},
		{"ANDARA_SNAPSHOT_S3_ENDPOINT", &c.SnapshotS3Endpoint},
	} {
		if v, ok := env(sv.name); ok {
			*sv.dst = v
		}
	}
	for _, dv := range []struct {
		name string
		dst  *time.Duration
	}{
		{"ANDARA_AUTH_SESSION_TTL", &c.AuthSessionTTL},
		{"ANDARA_AUTH_REFRESH_TTL", &c.AuthRefreshTTL},
		{"ANDARA_AUTH_INVITE_TTL", &c.AuthInviteTTL},
		{"ANDARA_AUTH_RECHECK_INTERVAL", &c.AuthRecheckInterval},
		{"ANDARA_LINKDEAD_MAX", &c.SessionLinkdeadMax},
		{"ANDARA_SNAPSHOT_INTERVAL", &c.SnapshotInterval},
		{"ANDARA_SNAPSHOT_UPLOAD_TIMEOUT", &c.SnapshotUploadTimeout},
	} {
		if v, ok := env(dv.name); ok {
			if err := parseDuration(dv.name, v, dv.dst); err != nil {
				return err
			}
		}
	}
	if v, ok := env("ANDARA_SIM_SOURCE"); ok {
		c.SimSource = v
	}
	for _, iv := range []struct {
		name string
		dst  *int
	}{
		{"ANDARA_TICK_RATE", &c.SimTickRate},
		{"ANDARA_MAX_PER_TICK", &c.SimMaxPerTick},
		{"ANDARA_CHECKPOINT_EVERY_TICKS", &c.SimCheckpointEveryTicks},
		{"ANDARA_MAX_INTENT_BYTES", &c.MaxIntentBytes},
		{"ANDARA_SUBSCRIBER_BUFFER", &c.SubscriberBuffer},
		{"ANDARA_MAX_SUBSCRIBERS", &c.MaxSubscribers},
		{"ANDARA_INGRESS_BURST", &c.IngressBurst},
		{"ANDARA_INGRESS_MAX_PENDING", &c.IngressMaxPending},
		{"ANDARA_CHARACTER_MAX_PER_ACCOUNT", &c.CharacterMaxPerAccount},
		{"ANDARA_EGRESS_BUFFER", &c.EgressBuffer},
		{"ANDARA_EGRESS_RESUME_WINDOW", &c.EgressResumeWindow},
	} {
		if v, ok := env(iv.name); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("%s must be an integer, got %q", iv.name, v)
			}
			*iv.dst = n
		}
	}
	if v, ok := env("ANDARA_TICK_BUDGET_MS"); ok {
		if err := parseMillis("ANDARA_TICK_BUDGET_MS", v, &c.SimTickBudget); err != nil {
			return err
		}
	}
	if v, ok := env("ANDARA_DRAIN_TIMEOUT_MS"); ok {
		if err := parseMillis("ANDARA_DRAIN_TIMEOUT_MS", v, &c.SimDrainTimeout); err != nil {
			return err
		}
	}
	if v, ok := env("ANDARA_SNAPSHOT_MAX_STALL_MS"); ok {
		if err := parseMillis("ANDARA_SNAPSHOT_MAX_STALL_MS", v, &c.SnapshotMaxStall); err != nil {
			return err
		}
	}
	if v, ok := env("ANDARA_VERB_TABLE"); ok {
		c.VerbTablePath = v
	}
	if v, ok := env("ANDARA_CHARACTER_SPAWN_ROOM"); ok {
		c.CharacterSpawnRoom = v
	}
	if v, ok := env("ANDARA_CHARACTER_NAME_PATTERN"); ok {
		c.CharacterNamePattern = v
	}
	if v, ok := env("ANDARA_SIM_SEED"); ok {
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return fmt.Errorf("ANDARA_SIM_SEED must be an unsigned integer, got %q", v)
		}
		c.SimSeed = n
	}
	if v, ok := env("ANDARA_SIM_PARTITIONS"); ok {
		ps, err := ParsePartitions(v)
		if err != nil {
			return fmt.Errorf("ANDARA_SIM_PARTITIONS: %w", err)
		}
		c.SimPartitions = ps
	}
	for _, uv := range []struct {
		name string
		dst  *uint32
	}{
		{"ANDARA_AUTH_ARGON2_MEMORY_KIB", &c.AuthArgon2MemoryKiB},
		{"ANDARA_AUTH_ARGON2_TIME", &c.AuthArgon2Time},
		{"ANDARA_AUTH_ARGON2_THREADS", &c.AuthArgon2Threads},
	} {
		if v, ok := env(uv.name); ok {
			if err := parseUint32(uv.name, v, uv.dst); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseBool(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// HTTPListen is the address passed to net.Listen.
func (c Config) HTTPListen() string {
	p := c.HTTPPort
	if p == "" {
		p = DefaultHTTPPort
	}
	if strings.Contains(p, ":") {
		return p
	}
	return ":" + p
}

// ContentSourceName is the name printed in no_zones_found findings.
func (c Config) ContentSourceName() string {
	if c.ContentSource == "dir" {
		return "dir:" + c.ContentPath
	}
	return c.ContentSource
}

// SpawnRoom splits character.spawn_room into its Zone and Room. Whether
// the pair resolves against the loaded content is the boot's check.
func (c Config) SpawnRoom() (zone, room string, err error) {
	zone, room, ok := strings.Cut(strings.TrimSpace(c.CharacterSpawnRoom), "/")
	if !ok || zone == "" || room == "" || strings.Contains(room, "/") {
		return "", "", fmt.Errorf("character.spawn_room must be zone_id/room_id, got %q", c.CharacterSpawnRoom)
	}
	return zone, room, nil
}
