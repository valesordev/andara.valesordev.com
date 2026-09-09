package cli

import (
	"os"
	"path/filepath"
	"strings"
)

func (rt *runtime) configHome() string {
	if v, ok := rt.lookupEnv("XDG_CONFIG_HOME"); ok && strings.TrimSpace(v) != "" {
		return v
	}
	if home, ok := rt.lookupEnv("HOME"); ok && home != "" {
		return filepath.Join(home, ".config")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config")
	}
	return ""
}

func (rt *runtime) defaultConfigPath() string {
	return filepath.Join(rt.configHome(), "andara", "cli.yaml")
}

func (rt *runtime) defaultCredentialsPath() string {
	return filepath.Join(rt.configHome(), "andara", "credentials.yaml")
}
