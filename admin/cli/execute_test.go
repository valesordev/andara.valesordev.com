// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type runResult struct {
	stdout string
	stderr string
	exit   int
}

func isolatedEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{
		"HOME":            dir,
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"),
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func runCLI(t *testing.T, args []string, env map[string]string) runResult {
	t.Helper()
	if env == nil {
		env = isolatedEnv(t, nil)
	}
	var stdout, stderr bytes.Buffer
	exit := execute(&runtime{
		args:      args,
		stdout:    &stdout,
		stderr:    &stderr,
		lookupEnv: lookupFrom(env),
	})
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exit: exit}
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %v\nstdout: %q", err, s)
	}
	if dec.More() {
		t.Fatalf("stdout has trailing JSON tokens: %q", s)
	}
	return v
}

func jsonError(t *testing.T, stdout string) map[string]any {
	t.Helper()
	v := decodeJSON(t, stdout)
	errObj, ok := v["error"].(map[string]any)
	if !ok {
		t.Fatalf("JSON missing error object: %#v", v)
	}
	for _, k := range []string{"code", "message", "detail"} {
		if _, ok := errObj[k]; !ok {
			t.Fatalf("error object missing %s: %#v", k, errObj)
		}
	}
	return errObj
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestNoArgsPrintsCommandTree(t *testing.T) {
	t.Parallel()
	res := runCLI(t, nil, nil)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	for _, name := range []string{"version", "config", "completion"} {
		if !strings.Contains(res.stdout, name) {
			t.Errorf("command tree missing %q:\n%s", name, res.stdout)
		}
	}
	if strings.Count(strings.TrimSpace(res.stdout), "\n") < 3 {
		t.Errorf("expected one-line description per command, got:\n%s", res.stdout)
	}
}

func TestNoArgsJSONListsCommands(t *testing.T) {
	t.Parallel()
	res := runCLI(t, []string{"--output", "json"}, nil)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	v := decodeJSON(t, res.stdout)
	cmds, ok := v["commands"].([]any)
	if !ok || len(cmds) == 0 {
		t.Fatalf("commands: %#v", v["commands"])
	}
	names := map[string]bool{}
	for _, c := range cmds {
		m, ok := c.(map[string]any)
		if !ok {
			t.Fatalf("command entry: %#v", c)
		}
		name, _ := m["name"].(string)
		desc, _ := m["description"].(string)
		if name == "" || desc == "" {
			t.Fatalf("command missing name or description: %#v", m)
		}
		names[name] = true
	}
	for _, want := range []string{"version", "config", "completion"} {
		if !names[want] {
			t.Errorf("JSON tree missing %q: %#v", want, names)
		}
	}
}

func TestVersionHumanAndJSON(t *testing.T) {
	t.Parallel()

	human := runCLI(t, []string{"version"}, nil)
	if human.exit != ExitOK {
		t.Fatalf("human exit=%d stderr=%q", human.exit, human.stderr)
	}
	for _, want := range []string{version, commit, builtAt} {
		if !strings.Contains(human.stdout, want) {
			t.Errorf("human version missing %q:\n%s", want, human.stdout)
		}
	}

	res := runCLI(t, []string{"version", "--output", "json"}, nil)
	if res.exit != ExitOK {
		t.Fatalf("json exit=%d stderr=%q", res.exit, res.stderr)
	}
	v := decodeJSON(t, res.stdout)
	if v["version"] != version || v["commit"] != commit || v["built_at"] != builtAt {
		t.Errorf("version JSON: %#v", v)
	}
}

func TestConfigShowPrecedenceFlagWins(t *testing.T) {
	t.Parallel()
	env := isolatedEnv(t, nil)
	cfg := filepath.Join(env["XDG_CONFIG_HOME"], "andara", "cli.yaml")
	writeFile(t, cfg, "server:\n  address: file-address:1\n  tls_ca: /file/ca.pem\nlog_level: debug\n", 0o644)
	env["ANDARA_SERVER_ADDRESS"] = "env-address:2"
	env["ANDARA_LOG_LEVEL"] = "info"

	res := runCLI(t, []string{"config", "show", "--server-address", "flag-address:3", "--output", "json"}, env)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q stdout=%q", res.exit, res.stderr, res.stdout)
	}
	v := decodeJSON(t, res.stdout)
	settings, ok := v["settings"].([]any)
	if !ok {
		t.Fatalf("settings: %#v", v["settings"])
	}
	got := map[string]map[string]any{}
	for _, s := range settings {
		m, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("setting: %#v", s)
		}
		key, _ := m["key"].(string)
		got[key] = m
		if m["value"] == nil || m["source"] == nil {
			t.Fatalf("setting %q missing value or source: %#v", key, m)
		}
	}
	if got["server.address"]["value"] != "flag-address:3" || got["server.address"]["source"] != "flag" {
		t.Errorf("server.address=%#v", got["server.address"])
	}
	if got["log_level"]["value"] != "info" || got["log_level"]["source"] != "env" {
		t.Errorf("log_level=%#v", got["log_level"])
	}
	if got["server.tls_ca"]["value"] != "/file/ca.pem" || got["server.tls_ca"]["source"] != "file" {
		t.Errorf("server.tls_ca=%#v", got["server.tls_ca"])
	}
	if got["timeout"]["value"] != "30s" || got["timeout"]["source"] != "default" {
		t.Errorf("timeout=%#v", got["timeout"])
	}
}

func TestUnknownFlagHuman(t *testing.T) {
	t.Parallel()
	res := runCLI(t, []string{"--bogus"}, nil)
	if res.exit != ExitUsage {
		t.Fatalf("exit=%d want %d", res.exit, ExitUsage)
	}
	if res.stdout != "" {
		t.Errorf("stdout must be empty, got %q", res.stdout)
	}
	line := strings.TrimSuffix(res.stderr, "\n")
	if strings.Contains(line, "\n") {
		t.Errorf("stderr must be one line, got %q", res.stderr)
	}
	if !strings.Contains(res.stderr, "--bogus") {
		t.Errorf("stderr must name the flag, got %q", res.stderr)
	}
}

func TestUnknownFlagJSON(t *testing.T) {
	t.Parallel()
	res := runCLI(t, []string{"--output", "json", "--bogus"}, nil)
	if res.exit != ExitUsage {
		t.Fatalf("exit=%d stderr=%q stdout=%q", res.exit, res.stderr, res.stdout)
	}
	errObj := jsonError(t, res.stdout)
	if errObj["code"] != CodeUnknownFlag {
		t.Errorf("code=%v", errObj["code"])
	}
	if !strings.Contains(errObj["message"].(string), "--bogus") {
		t.Errorf("message=%v", errObj["message"])
	}
}

func TestJSONSuccessStdoutOnlyJSON(t *testing.T) {
	t.Parallel()
	res := runCLI(t, []string{"version", "-o", "json"}, nil)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	decodeJSON(t, res.stdout)
	dec := json.NewDecoder(strings.NewReader(res.stdout))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	if dec.More() {
		t.Fatalf("trailing data on stdout: %q", res.stdout)
	}
}

func TestUnknownConfigKey(t *testing.T) {
	t.Parallel()
	env := isolatedEnv(t, nil)
	cfg := filepath.Join(env["XDG_CONFIG_HOME"], "andara", "cli.yaml")
	writeFile(t, cfg, "server:\n  address: localhost:8443\nnot_a_real_key: true\n", 0o644)

	res := runCLI(t, []string{"version"}, env)
	if res.exit != ExitUsage {
		t.Fatalf("exit=%d stderr=%q stdout=%q", res.exit, res.stderr, res.stdout)
	}
	if res.stdout != "" {
		t.Errorf("stdout=%q", res.stdout)
	}
	if !strings.Contains(res.stderr, "not_a_real_key") {
		t.Errorf("stderr must name the key: %q", res.stderr)
	}
	if !strings.Contains(res.stderr, cfg) {
		t.Errorf("stderr must name the file: %q", res.stderr)
	}

	jsonRes := runCLI(t, []string{"version", "-o", "json"}, env)
	if jsonRes.exit != ExitUsage {
		t.Fatalf("json exit=%d stderr=%q", jsonRes.exit, jsonRes.stderr)
	}
	errObj := jsonError(t, jsonRes.stdout)
	if errObj["code"] != CodeUnknownConfigKey {
		t.Errorf("code=%v", errObj["code"])
	}
}

func TestCompletionZsh(t *testing.T) {
	t.Parallel()
	res := runCLI(t, []string{"completion", "zsh"}, nil)
	if res.exit != ExitOK {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	if !strings.Contains(res.stdout, "#compdef") && !strings.Contains(res.stdout, "compdef") {
		t.Errorf("not a zsh completion script:\n%s", res.stdout[:min(len(res.stdout), 400)])
	}
}

func TestBannedTLSFlags(t *testing.T) {
	t.Parallel()
	for _, flag := range []string{"--insecure", "--tls-skip-verify"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			res := runCLI(t, []string{"version", flag}, nil)
			if res.exit != ExitUsage {
				t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
			}
			if res.stdout != "" {
				t.Errorf("stdout=%q", res.stdout)
			}
			if !strings.Contains(res.stderr, flag) {
				t.Errorf("must name %s: %q", flag, res.stderr)
			}
			if !strings.Contains(res.stderr, "--tls-ca") {
				t.Errorf("must mention --tls-ca: %q", res.stderr)
			}
		})
	}
}

func TestBannedTLSFlagJSON(t *testing.T) {
	t.Parallel()
	res := runCLI(t, []string{"version", "--output", "json", "--insecure"}, nil)
	if res.exit != ExitUsage {
		t.Fatalf("exit=%d stderr=%q stdout=%q", res.exit, res.stderr, res.stdout)
	}
	errObj := jsonError(t, res.stdout)
	if errObj["code"] != CodeUnsupportedFlag {
		t.Errorf("code=%v", errObj["code"])
	}
}

func TestCredentialModeTooPermissive(t *testing.T) {
	t.Parallel()
	env := isolatedEnv(t, nil)
	path := filepath.Join(env["XDG_CONFIG_HOME"], "andara", "credentials.yaml")
	secret := "sekrit-token-DO-NOT-LEAK"
	writeFile(t, path, "localhost:8443:\n  token: "+secret+"\n", 0o644)

	res := runCLI(t, []string{"version"}, env)
	if res.exit != ExitUsage {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	if res.stdout != "" {
		t.Errorf("stdout=%q", res.stdout)
	}
	if !strings.Contains(res.stderr, path) {
		t.Errorf("must name file: %q", res.stderr)
	}
	if !strings.Contains(res.stderr, "0644") && !strings.Contains(res.stderr, "644") {
		t.Errorf("must name mode: %q", res.stderr)
	}
	if strings.Contains(res.stderr, secret) {
		t.Errorf("must not print credential: %q", res.stderr)
	}
}

func TestConfigShowRedactsCredentials(t *testing.T) {
	t.Parallel()
	env := isolatedEnv(t, nil)
	path := filepath.Join(env["XDG_CONFIG_HOME"], "andara", "credentials.yaml")
	secret := "sekrit-token-DO-NOT-LEAK"
	writeFile(t, path, "localhost:8443:\n  token: "+secret+"\n", 0o600)

	human := runCLI(t, []string{"config", "show", "--log-level", "debug"}, env)
	if human.exit != ExitOK {
		t.Fatalf("human exit=%d stderr=%q stdout=%q", human.exit, human.stderr, human.stdout)
	}
	if !strings.Contains(human.stdout, path) {
		t.Errorf("human output should include credentials path:\n%s", human.stdout)
	}
	combined := human.stdout + human.stderr
	if strings.Contains(combined, secret) {
		t.Errorf("secret leaked in human output")
	}

	res := runCLI(t, []string{"config", "show", "-o", "json", "--log-level", "debug"}, env)
	if res.exit != ExitOK {
		t.Fatalf("json exit=%d stderr=%q stdout=%q", res.exit, res.stderr, res.stdout)
	}
	v := decodeJSON(t, res.stdout)
	creds, ok := v["credentials"].(map[string]any)
	if !ok {
		t.Fatalf("credentials: %#v", v["credentials"])
	}
	if creds["path"] != path {
		t.Errorf("path=%v", creds["path"])
	}
	if creds["present"] != true {
		t.Errorf("present=%v", creds["present"])
	}
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) || strings.Contains(res.stderr, secret) {
		t.Errorf("secret leaked in JSON path")
	}
}

func TestGlobalFlagsOnEveryCommand(t *testing.T) {
	t.Parallel()
	env := isolatedEnv(t, nil)
	root := newRoot(&runtime{
		lookupEnv: lookupFrom(env),
		stdout:    io.Discard,
		stderr:    io.Discard,
	})
	required := []string{
		"config", "server-address", "tls-ca", "output",
		"log-level", "timeout", "no-color", "credentials",
	}
	var missing []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, name := range required {
			if c.Flags().Lookup(name) == nil && c.PersistentFlags().Lookup(name) == nil && c.InheritedFlags().Lookup(name) == nil {
				missing = append(missing, c.CommandPath()+" --"+name)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	if len(missing) > 0 {
		t.Fatalf("commands missing global flags:\n%s", strings.Join(missing, "\n"))
	}
}

func TestExplicitConfigMissing(t *testing.T) {
	t.Parallel()
	env := isolatedEnv(t, nil)
	missing := filepath.Join(env["HOME"], "no-such-cli.yaml")
	res := runCLI(t, []string{"version", "--config", missing}, env)
	if res.exit != ExitUsage {
		t.Fatalf("exit=%d stderr=%q", res.exit, res.stderr)
	}
	if !strings.Contains(res.stderr, missing) {
		t.Errorf("must name missing file: %q", res.stderr)
	}
}
