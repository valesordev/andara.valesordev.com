package config

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is andara-server's process configuration (AW-SRV-001).
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
	}
	configPath := peekConfigPath(args, env)
	if configPath != "" {
		if err := applyFile(&c, configPath); err != nil {
			return Config{}, err
		}
	}
	applyEnv(&c, env)

	fs := flag.NewFlagSet("andara-server", flag.ContinueOnError)
	fs.SetOutput(errOut)
	_ = fs.String("config", configPath, "YAML config file (ANDARA_CONFIG)")
	fs.StringVar(&c.ContentSource, "content-source", c.ContentSource, "content source: kafka or dir")
	fs.StringVar(&c.ContentPath, "content-path", c.ContentPath, "directory of Zone Definition JSON files (dir source)")
	fs.BoolVar(&c.StrictOrphans, "strict-orphans", c.StrictOrphans, "treat orphan Rooms as errors")
	fs.BoolVar(&c.ValidateOnly, "validate-only", c.ValidateOnly, "load and validate, then exit")
	fs.StringVar(&c.HTTPPort, "http-port", c.HTTPPort, "plain-text health and metrics port")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if c.ContentSource != "kafka" && c.ContentSource != "dir" {
		return Config{}, fmt.Errorf("content.source must be kafka or dir, got %q", c.ContentSource)
	}
	return c, nil
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
	return nil
}

func applyEnv(c *Config, env EnvLookup) {
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
