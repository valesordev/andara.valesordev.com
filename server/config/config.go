package config

import (
	"flag"
	"fmt"
	"io"
	"os"
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
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if c.ContentSource != "kafka" && c.ContentSource != "dir" {
		return Config{}, fmt.Errorf("content.source must be kafka or dir, got %q", c.ContentSource)
	}
	if err := c.validateGateway(); err != nil {
		return Config{}, err
	}
	return c, nil
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
		OTLPEndpoint *string `yaml:"otlp_endpoint"`
		ServiceName  *string `yaml:"service_name"`
		Environment  *string `yaml:"environment"`
		LogFormat    *string `yaml:"log_format"`
		LogLevel     *string `yaml:"log_level"`
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
