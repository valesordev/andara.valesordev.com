// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type credentialsInfo struct {
	path    string
	present bool
}

func inspectCredentials(path, serverAddress string) (credentialsInfo, error) {
	info := credentialsInfo{path: path}
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return info, nil
	}
	if err != nil {
		return info, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("cannot stat credential file %s", path),
			Detail:  map[string]any{"path": path},
		}
	}
	perm := fi.Mode().Perm()
	if perm&0o077 != 0 {
		return info, &AppError{
			Exit:    ExitUsage,
			Code:    CodeCredentialFileMode,
			Message: fmt.Sprintf("credential file %s has mode %04o; require 0600 or tighter", path, perm),
			Detail:  map[string]any{"path": path, "mode": fmt.Sprintf("%04o", perm)},
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return info, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("cannot read credential file %s", path),
			Detail:  map[string]any{"path": path},
		}
	}
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return info, &AppError{
			Exit:    ExitUsage,
			Code:    CodeInvalidConfig,
			Message: fmt.Sprintf("credential file %s is not a YAML mapping keyed by server address", path),
			Detail:  map[string]any{"path": path},
		}
	}
	_, info.present = top[serverAddress]
	return info, nil
}

// storedCredential is one entry of credentials.yaml, keyed by server
// address — the content AW-CLI-001 left for AW-SRV-008 to define. The
// session token is what OpenSession and the Admin bearer header carry; the
// refresh token is how `auth login` is avoided every hour. Neither is ever
// printed, logged, or included in --output json.
type storedCredential struct {
	AccountID      string `yaml:"account_id"`
	Username       string `yaml:"username"`
	SessionToken   string `yaml:"session_token"`
	SessionExpires string `yaml:"session_expires"`
	RefreshToken   string `yaml:"refresh_token"`
	RefreshExpires string `yaml:"refresh_expires"`
}

// readCredentialFile returns every entry, or an empty map when the file
// does not exist. The mode check is inspectCredentials'; this runs after it.
func readCredentialFile(path string) (map[string]storedCredential, error) {
	out := map[string]storedCredential{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidConfig, Message: fmt.Sprintf("cannot read credential file %s", path), Detail: map[string]any{"path": path}}
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidConfig, Message: fmt.Sprintf("credential file %s is not a YAML mapping keyed by server address", path), Detail: map[string]any{"path": path}}
	}
	return out, nil
}

// writeCredentialFile replaces the file atomically with mode 0600. The
// directory is created 0700 if missing.
func writeCredentialFile(path string, entries map[string]storedCredential) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return &AppError{Exit: ExitFail, Code: CodeInvalidConfig, Message: fmt.Sprintf("cannot create %s", filepath.Dir(path)), Detail: map[string]any{"path": path}}
	}
	data, err := yaml.Marshal(entries)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return &AppError{Exit: ExitFail, Code: CodeInvalidConfig, Message: fmt.Sprintf("cannot write credential file %s", path), Detail: map[string]any{"path": path}}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return &AppError{Exit: ExitFail, Code: CodeInvalidConfig, Message: fmt.Sprintf("cannot write credential file %s", path), Detail: map[string]any{"path": path}}
	}
	return nil
}

// loadCredential returns the entry for the configured server, or nil.
func (rt *runtime) loadCredential() (*storedCredential, error) {
	entries, err := readCredentialFile(rt.settings.CredentialsPath)
	if err != nil {
		return nil, err
	}
	c, ok := entries[rt.settings.ServerAddress]
	if !ok || c.SessionToken == "" {
		return nil, nil
	}
	return &c, nil
}

// storeCredential writes the entry for the configured server.
func (rt *runtime) storeCredential(c storedCredential) error {
	entries, err := readCredentialFile(rt.settings.CredentialsPath)
	if err != nil {
		return err
	}
	entries[rt.settings.ServerAddress] = c
	return writeCredentialFile(rt.settings.CredentialsPath, entries)
}

// dropCredential removes the entry for the configured server, if any.
func (rt *runtime) dropCredential() error {
	entries, err := readCredentialFile(rt.settings.CredentialsPath)
	if err != nil {
		return err
	}
	if _, ok := entries[rt.settings.ServerAddress]; !ok {
		return nil
	}
	delete(entries, rt.settings.ServerAddress)
	return writeCredentialFile(rt.settings.CredentialsPath, entries)
}
