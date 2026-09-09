package cli

import (
	"fmt"
	"os"

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
