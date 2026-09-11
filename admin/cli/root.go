// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultServerAddress = "localhost:8443"
	defaultLogLevel      = "warn"
	defaultTimeout       = 30 * time.Second
)

// Set via -ldflags on `make build`.
var (
	version = "dev"
	commit  = "unknown"
	builtAt = "unknown"
)

type runtime struct {
	args         []string
	stdout       io.Writer
	stderr       io.Writer
	lookupEnv    func(string) (string, bool)
	gf           globalFlags
	peekedOutput string
	settings     *resolved
	creds        credentialsInfo
	command      string
	span         trace.Span
	tp           *sdktrace.TracerProvider
	started      time.Time
}

type globalFlags struct {
	config        string
	serverAddress string
	tlsCA         string
	output        string
	logLevel      string
	timeout       time.Duration
	noColor       bool
	credentials   string
	insecure      bool
	tlsSkipVerify bool
}

// Execute runs andara-cli with the given arguments (no argv0) and returns an exit code.
func Execute(args []string) int {
	return execute(&runtime{
		args:      args,
		stdout:    os.Stdout,
		stderr:    os.Stderr,
		lookupEnv: os.LookupEnv,
	})
}

func execute(rt *runtime) int {
	if rt.lookupEnv == nil {
		rt.lookupEnv = os.LookupEnv
	}
	if rt.stdout == nil {
		rt.stdout = os.Stdout
	}
	if rt.stderr == nil {
		rt.stderr = os.Stderr
	}
	rt.peekedOutput = peekOutput(rt.args, rt.lookupEnv)
	root := newRoot(rt)
	root.SetArgs(rt.args)
	root.SetOut(rt.stdout)
	root.SetErr(rt.stderr)
	err := root.Execute()
	code := ExitOK
	if err != nil {
		code = rt.writeError(err)
	}
	rt.finish(code)
	return code
}

func newRoot(rt *runtime) *cobra.Command {
	root := &cobra.Command{
		Use:           "andara-cli",
		Short:         "Operator, builder, and player tooling for Andara's World",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return rt.prepare(cmd)
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true

	fs := root.PersistentFlags()
	fs.StringVar(&rt.gf.config, "config", "", "config file path (default $XDG_CONFIG_HOME/andara/cli.yaml)")
	fs.StringVar(&rt.gf.serverAddress, "server-address", defaultServerAddress, "server endpoint (gRPC/Connect over TLS)")
	fs.StringVar(&rt.gf.tlsCA, "tls-ca", "", "CA bundle to trust (empty uses the system trust store)")
	fs.StringVarP(&rt.gf.output, "output", "o", outputHuman, "output format: human or json")
	fs.StringVar(&rt.gf.logLevel, "log-level", defaultLogLevel, "CLI diagnostic level")
	fs.DurationVar(&rt.gf.timeout, "timeout", defaultTimeout, "per-command deadline")
	fs.BoolVar(&rt.gf.noColor, "no-color", false, "disable ANSI output")
	fs.StringVar(&rt.gf.credentials, "credentials", "", "credential file path (default $XDG_CONFIG_HOME/andara/credentials.yaml)")
	fs.BoolVar(&rt.gf.insecure, "insecure", false, "")
	fs.BoolVar(&rt.gf.tlsSkipVerify, "tls-skip-verify", false, "")
	_ = fs.MarkHidden("insecure")
	_ = fs.MarkHidden("tls-skip-verify")

	root.AddCommand(newVersionCmd(rt))
	root.AddCommand(newConfigCmd(rt))
	root.AddCommand(newCompletionCmd(rt))
	return root
}

func (rt *runtime) prepare(cmd *cobra.Command) error {
	rt.command = commandName(cmd)
	cf := rt.readChangedFlags(cmd)
	if cf.insecureChanged {
		return unsupportedFlag("--insecure")
	}
	if cf.tlsSkipVerifyChanged {
		return unsupportedFlag("--tls-skip-verify")
	}

	// Resolve the config path first so we know which file to load.
	configEnv, configEnvSet := rt.lookupEnv("ANDARA_CONFIG")
	configPath, configSrc := resolveString(cf.configChanged, cf.config, configEnvSet, configEnv, nil, rt.defaultConfigPath())
	file, err := rt.loadFileForPath(configPath, configSrc)
	if err != nil {
		return err
	}
	settings, err := rt.resolveSettings(cf, file)
	if err != nil {
		return err
	}
	rt.settings = settings

	creds, err := inspectCredentials(settings.CredentialsPath, settings.ServerAddress)
	if err != nil {
		return err
	}
	rt.creds = creds
	rt.startSpan()
	return nil
}

func (rt *runtime) readChangedFlags(cmd *cobra.Command) changedFlags {
	fs := cmd.Flags()
	changed := func(name string) bool {
		f := fs.Lookup(name)
		return f != nil && f.Changed
	}
	return changedFlags{
		config:               rt.gf.config,
		configChanged:        changed("config"),
		serverAddress:        rt.gf.serverAddress,
		serverAddressChanged: changed("server-address"),
		tlsCA:                rt.gf.tlsCA,
		tlsCAChanged:         changed("tls-ca"),
		output:               rt.gf.output,
		outputChanged:        changed("output"),
		logLevel:             rt.gf.logLevel,
		logLevelChanged:      changed("log-level"),
		timeout:              rt.gf.timeout,
		timeoutChanged:       changed("timeout"),
		noColor:              rt.gf.noColor,
		noColorChanged:       changed("no-color"),
		credentials:          rt.gf.credentials,
		credentialsChanged:   changed("credentials"),
		insecureChanged:      changed("insecure"),
		tlsSkipVerifyChanged: changed("tls-skip-verify"),
	}
}

func commandName(cmd *cobra.Command) string {
	path := strings.TrimPrefix(cmd.CommandPath(), "andara-cli")
	path = strings.TrimSpace(path)
	if path == "" {
		return "andara-cli"
	}
	return path
}

func (rt *runtime) writeCommandTree(cmd *cobra.Command) error {
	type item struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var items []item
	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() || c.Hidden {
			continue
		}
		items = append(items, item{Name: c.Name(), Description: c.Short})
	}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(map[string]any{"commands": items})
	}
	var b strings.Builder
	b.WriteString(cmd.Short + "\n\n")
	for _, it := range items {
		fmt.Fprintf(&b, "  %-12s %s\n", it.Name, it.Description)
	}
	_, err := io.WriteString(rt.stdout, b.String())
	return err
}
