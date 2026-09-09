package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Source names the winner in the flag > env > file > default chain.
type Source string

const (
	SourceFlag    Source = "flag"
	SourceEnv     Source = "env"
	SourceFile    Source = "file"
	SourceDefault Source = "default"
)

// Setting is one effective configuration value and where it came from.
type Setting struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source Source `json:"source"`
}

type fileConfig struct {
	Server *struct {
		Address *string `yaml:"address"`
		TLSCA   *string `yaml:"tls_ca"`
	} `yaml:"server"`
	Output      *string `yaml:"output"`
	LogLevel    *string `yaml:"log_level"`
	Timeout     *string `yaml:"timeout"`
	NoColor     *bool   `yaml:"no_color"`
	Credentials *string `yaml:"credentials"`
}

type resolved struct {
	ConfigPath       string
	ConfigSource     Source
	ServerAddress    string
	ServerAddressSrc Source
	TLSCA            string
	TLSCASrc         Source
	Output           string
	OutputSrc        Source
	LogLevel         string
	LogLevelSrc      Source
	Timeout          time.Duration
	TimeoutSrc       Source
	NoColor          bool
	NoColorSrc       Source
	CredentialsPath  string
	CredentialsSrc   Source
}

func (r *resolved) list() []Setting {
	return []Setting{
		{Key: "config", Value: r.ConfigPath, Source: r.ConfigSource},
		{Key: "server.address", Value: r.ServerAddress, Source: r.ServerAddressSrc},
		{Key: "server.tls_ca", Value: r.TLSCA, Source: r.TLSCASrc},
		{Key: "output", Value: r.Output, Source: r.OutputSrc},
		{Key: "log_level", Value: r.LogLevel, Source: r.LogLevelSrc},
		{Key: "timeout", Value: r.Timeout.String(), Source: r.TimeoutSrc},
		{Key: "no_color", Value: fmt.Sprintf("%t", r.NoColor), Source: r.NoColorSrc},
		{Key: "credentials", Value: r.CredentialsPath, Source: r.CredentialsSrc},
	}
}

var knownConfigKeys = map[string]bool{
	"server":         true,
	"server.address": true,
	"server.tls_ca":  true,
	"output":         true,
	"log_level":      true,
	"timeout":        true,
	"no_color":       true,
	"credentials":    true,
}

func loadConfigFile(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("cannot read config file %s", path),
			Detail:  map[string]any{"file": path},
		}
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("invalid YAML in config file %s", path),
			Detail:  map[string]any{"file": path},
		}
	}
	if raw == nil {
		return &fileConfig{}, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("config file %s must be a YAML mapping", path),
			Detail:  map[string]any{"file": path},
		}
	}
	if key := firstUnknownConfigKey(m, ""); key != "" {
		return nil, &AppError{
			Exit:    ExitUsage,
			Code:    CodeUnknownConfigKey,
			Message: fmt.Sprintf("unknown configuration key %q in %s", key, path),
			Detail:  map[string]any{"key": key, "file": path},
		}
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var fc fileConfig
	if err := dec.Decode(&fc); err != nil && err != io.EOF {
		return nil, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("invalid configuration in %s", path),
			Detail:  map[string]any{"file": path},
		}
	}
	return &fc, nil
}

func firstUnknownConfigKey(m map[string]any, prefix string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if !knownConfigKeys[path] {
			return path
		}
		if nested, ok := asStringMap(m[k]); ok {
			if u := firstUnknownConfigKey(nested, path); u != "" {
				return u
			}
		}
	}
	return ""
}

func asStringMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

func resolveString(flagChanged bool, flagVal string, envSet bool, envVal string, fileVal *string, def string) (string, Source) {
	if flagChanged {
		return flagVal, SourceFlag
	}
	if envSet {
		return envVal, SourceEnv
	}
	if fileVal != nil {
		return *fileVal, SourceFile
	}
	return def, SourceDefault
}

func (rt *runtime) resolveSettings(cmdFlags changedFlags, file *fileConfig) (*resolved, error) {
	r := &resolved{}

	configEnv, configEnvSet := rt.lookupEnv("ANDARA_CONFIG")
	r.ConfigPath, r.ConfigSource = resolveString(cmdFlags.configChanged, cmdFlags.config, configEnvSet, configEnv, nil, rt.defaultConfigPath())

	var fileServerAddr, fileTLSCA, fileOutput, fileLogLevel, fileTimeout, fileCreds *string
	var fileNoColor *bool
	if file != nil {
		if file.Server != nil {
			fileServerAddr = file.Server.Address
			fileTLSCA = file.Server.TLSCA
		}
		fileOutput = file.Output
		fileLogLevel = file.LogLevel
		fileTimeout = file.Timeout
		fileNoColor = file.NoColor
		fileCreds = file.Credentials
	}

	addrEnv, addrEnvSet := rt.lookupEnv("ANDARA_SERVER_ADDRESS")
	r.ServerAddress, r.ServerAddressSrc = resolveString(cmdFlags.serverAddressChanged, cmdFlags.serverAddress, addrEnvSet, addrEnv, fileServerAddr, defaultServerAddress)

	tlsEnv, tlsEnvSet := rt.lookupEnv("ANDARA_TLS_CA_FILE")
	r.TLSCA, r.TLSCASrc = resolveString(cmdFlags.tlsCAChanged, cmdFlags.tlsCA, tlsEnvSet, tlsEnv, fileTLSCA, "")

	outEnv, outEnvSet := rt.lookupEnv("ANDARA_OUTPUT")
	r.Output, r.OutputSrc = resolveString(cmdFlags.outputChanged, cmdFlags.output, outEnvSet, outEnv, fileOutput, outputHuman)

	logEnv, logEnvSet := rt.lookupEnv("ANDARA_LOG_LEVEL")
	r.LogLevel, r.LogLevelSrc = resolveString(cmdFlags.logLevelChanged, cmdFlags.logLevel, logEnvSet, logEnv, fileLogLevel, defaultLogLevel)

	timeoutEnv, timeoutEnvSet := rt.lookupEnv("ANDARA_TIMEOUT")
	switch {
	case cmdFlags.timeoutChanged:
		r.Timeout, r.TimeoutSrc = cmdFlags.timeout, SourceFlag
	case timeoutEnvSet:
		d, err := time.ParseDuration(timeoutEnv)
		if err != nil {
			return nil, &AppError{
				Exit:    ExitUsage,
				Code:    CodeInvalidValue,
				Message: fmt.Sprintf("invalid timeout %q", timeoutEnv),
				Detail:  map[string]any{"flag": "--timeout", "env": "ANDARA_TIMEOUT"},
			}
		}
		r.Timeout, r.TimeoutSrc = d, SourceEnv
	case fileTimeout != nil:
		d, err := time.ParseDuration(*fileTimeout)
		if err != nil {
			return nil, &AppError{
				Exit:    ExitUsage,
				Code:    CodeInvalidValue,
				Message: fmt.Sprintf("invalid timeout %q", *fileTimeout),
				Detail:  map[string]any{"key": "timeout"},
			}
		}
		r.Timeout, r.TimeoutSrc = d, SourceFile
	default:
		r.Timeout, r.TimeoutSrc = defaultTimeout, SourceDefault
	}

	noColorEnv, noColorEnvSet := rt.lookupEnv("NO_COLOR")
	switch {
	case cmdFlags.noColorChanged:
		r.NoColor, r.NoColorSrc = cmdFlags.noColor, SourceFlag
	case noColorEnvSet:
		r.NoColor, r.NoColorSrc = noColorEnv != "", SourceEnv
	case fileNoColor != nil:
		r.NoColor, r.NoColorSrc = *fileNoColor, SourceFile
	default:
		r.NoColor, r.NoColorSrc = false, SourceDefault
	}

	credEnv, credEnvSet := rt.lookupEnv("ANDARA_CREDENTIALS")
	r.CredentialsPath, r.CredentialsSrc = resolveString(cmdFlags.credentialsChanged, cmdFlags.credentials, credEnvSet, credEnv, fileCreds, rt.defaultCredentialsPath())

	if err := validateOutput(r.Output); err != nil {
		return nil, err
	}
	if err := validateLogLevel(r.LogLevel); err != nil {
		return nil, err
	}
	return r, nil
}

func validateOutput(v string) error {
	switch v {
	case outputHuman, outputJSON:
		return nil
	default:
		return &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidValue,
			Message: fmt.Sprintf("invalid output format %q (want human or json)", v),
			Detail:  map[string]any{"value": v},
		}
	}
}

func validateLogLevel(v string) error {
	switch v {
	case "debug", "info", "warn", "error":
		return nil
	default:
		return &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidValue,
			Message: fmt.Sprintf("invalid log level %q (want debug, info, warn, or error)", v),
			Detail:  map[string]any{"value": v},
		}
	}
}

func (rt *runtime) loadFileForPath(path string, source Source) (*fileConfig, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		if source == SourceFlag || source == SourceEnv {
			return nil, &AppError{
				Exit:    ExitUsage,
				Code:    CodeConfigNotFound,
				Message: fmt.Sprintf("config file not found: %s", path),
				Detail:  map[string]any{"file": path},
			}
		}
		return nil, nil
	}
	if err != nil {
		return nil, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("cannot stat config file %s", path),
			Detail:  map[string]any{"file": path},
		}
	}
	return loadConfigFile(path)
}

type changedFlags struct {
	config               string
	configChanged        bool
	serverAddress        string
	serverAddressChanged bool
	tlsCA                string
	tlsCAChanged         bool
	output               string
	outputChanged        bool
	logLevel             string
	logLevelChanged      bool
	timeout              time.Duration
	timeoutChanged       bool
	noColor              bool
	noColorChanged       bool
	credentials          string
	credentialsChanged   bool
	insecureChanged      bool
	tlsSkipVerifyChanged bool
}
