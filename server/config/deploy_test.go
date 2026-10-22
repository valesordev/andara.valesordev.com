// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AW-SRV-054: the five deploy.* keys default as the contract's table says,
// and each resolves flag > env > file.
func TestDeployKeys_DefaultsAndPrecedence(t *testing.T) {
	def, err := Parse(nil, withTLS(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if def.DeployNoticeLead != 10*time.Second || def.DeployExpectedBack != 60*time.Second || def.DeployAdmitWait != 5*time.Second ||
		def.DeployImageTag != "dev" || def.DeployControlSocket != "/run/andara/control.sock" {
		t.Fatalf("defaults: notice_lead %s expected_back %s admit_wait %s image_tag %q control_socket %q",
			def.DeployNoticeLead, def.DeployExpectedBack, def.DeployAdmitWait, def.DeployImageTag, def.DeployControlSocket)
	}

	path := filepath.Join(t.TempDir(), "andara.yaml")
	file := "deploy:\n  notice_lead: 1s\n  admit_wait: 1s\n  expected_back: 1s\n  image_tag: file\n  control_socket: /file.sock\n"
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	type keys struct {
		lead, admit, back time.Duration
		tag, socket       string
	}
	get := func(c Config) keys {
		return keys{c.DeployNoticeLead, c.DeployAdmitWait, c.DeployExpectedBack, c.DeployImageTag, c.DeployControlSocket}
	}
	env := func(on bool) EnvLookup {
		vals := map[string]string{
			"ANDARA_DEPLOY_NOTICE_LEAD": "2s", "ANDARA_DEPLOY_ADMIT_WAIT": "2s", "ANDARA_DEPLOY_EXPECTED_BACK": "2s",
			"ANDARA_DEPLOY_IMAGE_TAG": "env", "ANDARA_DEPLOY_CONTROL_SOCKET": "/env.sock",
		}
		return withTLS(func(k string) (string, bool) {
			if !on {
				return "", false
			}
			v, ok := vals[k]
			return v, ok
		})
	}
	flags := []string{"--deploy-notice-lead=3s", "--deploy-admit-wait=3s", "--deploy-expected-back=3s",
		"--deploy-image-tag=flag", "--deploy-control-socket=/flag.sock"}
	for _, c := range []struct {
		name string
		args []string
		env  EnvLookup
		want keys
	}{
		{"file", []string{"--config", path}, env(false), keys{time.Second, time.Second, time.Second, "file", "/file.sock"}},
		{"env over file", []string{"--config", path}, env(true), keys{2 * time.Second, 2 * time.Second, 2 * time.Second, "env", "/env.sock"}},
		{"flag over env and file", append([]string{"--config", path}, flags...), env(true), keys{3 * time.Second, 3 * time.Second, 3 * time.Second, "flag", "/flag.sock"}},
	} {
		got, err := Parse(c.args, c.env, io.Discard)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if g := get(got); g != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, g, c.want)
		}
	}
}

// validateDeploy: a negative duration, an image tag outside the round-tag
// grammar (an unstripped digest's colon among them) and an empty socket are
// refused, each naming its key.
func TestDeployKeys_InvalidIsRefused(t *testing.T) {
	for _, c := range []struct{ key, env, value string }{
		{"deploy.notice_lead", "ANDARA_DEPLOY_NOTICE_LEAD", "-1s"},
		{"deploy.admit_wait", "ANDARA_DEPLOY_ADMIT_WAIT", "-1s"},
		{"deploy.expected_back", "ANDARA_DEPLOY_EXPECTED_BACK", "-1s"},
		{"deploy.image_tag", "ANDARA_DEPLOY_IMAGE_TAG", "sha256:0123"},
		{"deploy.image_tag", "ANDARA_DEPLOY_IMAGE_TAG", "a b"},
		{"deploy.image_tag", "ANDARA_DEPLOY_IMAGE_TAG", strings.Repeat("a", 201)},
	} {
		env := withTLS(func(k string) (string, bool) {
			if k == c.env {
				return c.value, true
			}
			return "", false
		})
		if _, err := Parse(nil, env, io.Discard); err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s=%q: err = %v, want one naming %s", c.env, c.value, err, c.key)
		}
	}
	// An empty value in the environment means unset, so the empty socket
	// comes from a flag.
	if _, err := Parse([]string{"--deploy-control-socket="}, withTLS(nil), io.Discard); err == nil || !strings.Contains(err.Error(), "deploy.control_socket") {
		t.Errorf("empty control_socket: err = %v, want one naming deploy.control_socket", err)
	}
	if _, err := Parse([]string{"--deploy-image-tag="}, withTLS(nil), io.Discard); err == nil || !strings.Contains(err.Error(), "deploy.image_tag") {
		t.Errorf("empty image_tag: err = %v, want one naming deploy.image_tag", err)
	}
	// The edges that are legal: a 200-character tag, and zero for every
	// duration (no lead, no drain wait, no expected time back).
	if _, err := Parse([]string{"--deploy-image-tag=" + strings.Repeat("a", 200)}, withTLS(nil), io.Discard); err != nil {
		t.Errorf("a 200-character image tag is legal: %v", err)
	}
	if _, err := Parse([]string{"--deploy-notice-lead=0s", "--deploy-admit-wait=0s", "--deploy-expected-back=0s"}, withTLS(nil), io.Discard); err != nil {
		t.Errorf("zero durations are legal: %v", err)
	}
}
