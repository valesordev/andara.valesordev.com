package config

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestParse_Defaults(t *testing.T) {
	c, err := Parse(nil, func(string) (string, bool) { return "", false }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != DefaultContentSource {
		t.Errorf("source = %q", c.ContentSource)
	}
	if c.ContentPath != DefaultContentPath {
		t.Errorf("path = %q", c.ContentPath)
	}
	if c.StrictOrphans {
		t.Error("strict orphans default is false")
	}
	if c.HTTPListen() != ":8080" {
		t.Errorf("listen = %q", c.HTTPListen())
	}
}

func TestParse_FlagBeatsEnv(t *testing.T) {
	env := func(k string) (string, bool) {
		if k == "ANDARA_CONTENT_SOURCE" {
			return "kafka", true
		}
		if k == "ANDARA_CONTENT_PATH" {
			return "/from-env", true
		}
		return "", false
	}
	c, err := Parse([]string{"--content-source=dir", "--content-path=/from-flag", "--validate-only"}, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != "dir" {
		t.Errorf("source = %q, want dir", c.ContentSource)
	}
	if c.ContentPath != "/from-flag" {
		t.Errorf("path = %q", c.ContentPath)
	}
	if !c.ValidateOnly {
		t.Error("validate-only not set")
	}
}

func TestParse_EnvBeatsDefault(t *testing.T) {
	env := func(k string) (string, bool) {
		switch k {
		case "ANDARA_CONTENT_SOURCE":
			return "dir", true
		case "ANDARA_STRICT_ORPHANS":
			return "true", true
		case "ANDARA_HTTP_PORT":
			return "9099", true
		default:
			return "", false
		}
	}
	c, err := Parse(nil, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != "dir" || !c.StrictOrphans || c.HTTPPort != "9099" {
		t.Errorf("%+v", c)
	}
}

func TestParse_UnknownSource(t *testing.T) {
	var buf bytes.Buffer
	_, err := Parse([]string{"--content-source=s3"}, nil, &buf)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "kafka or dir") {
		t.Errorf("err = %v", err)
	}
}

func TestParse_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/server.yaml"
	body := []byte("content:\n  source: dir\n  path: /from-file\n  strict_orphans: true\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]string{"--config", path}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContentSource != "dir" || c.ContentPath != "/from-file" || !c.StrictOrphans {
		t.Errorf("%+v", c)
	}
}

func TestContentSourceName(t *testing.T) {
	c := Config{ContentSource: "dir", ContentPath: "/c"}
	if c.ContentSourceName() != "dir:/c" {
		t.Errorf("got %q", c.ContentSourceName())
	}
	c.ContentSource = "kafka"
	if c.ContentSourceName() != "kafka" {
		t.Errorf("got %q", c.ContentSourceName())
	}
}
