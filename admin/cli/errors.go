package cli

import (
	"errors"
	"fmt"
	"strings"
)

// Exit codes shared by every andara-cli command (AW-CLI-001).
const (
	ExitOK      = 0
	ExitFail    = 1
	ExitUsage   = 2
	ExitConnect = 3
	ExitTimeout = 4
)

// error.code values. Additive-only; never remove or repurpose.
const (
	CodeUnknownFlag        = "unknown_flag"
	CodeUnknownConfigKey   = "unknown_config_key"
	CodeUnsupportedFlag    = "unsupported_flag"
	CodeInvalidValue       = "invalid_value"
	CodeConfigNotFound     = "config_not_found"
	CodeInvalidConfig      = "invalid_config"
	CodeCredentialFileMode = "credential_file_mode"
)

// AppError is the CLI's error taxonomy. Exit maps to the process exit code.
type AppError struct {
	Exit    int
	Code    string
	Message string
	Detail  map[string]any
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func classify(err error) *AppError {
	if err == nil {
		return nil
	}
	var ae *AppError
	if errors.As(err, &ae) {
		if ae.Detail == nil {
			ae.Detail = map[string]any{}
		}
		return ae
	}
	msg := firstLine(err.Error())
	if strings.Contains(msg, "unknown flag") || strings.Contains(msg, "unknown shorthand") {
		return &AppError{
			Exit:    ExitUsage,
			Code:    CodeUnknownFlag,
			Message: msg,
			Detail:  map[string]any{"flag": extractFlag(msg)},
		}
	}
	return &AppError{
		Exit:    ExitUsage,
		Code:    CodeInvalidValue,
		Message: msg,
		Detail:  map[string]any{},
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func extractFlag(msg string) string {
	if _, rest, ok := strings.Cut(msg, "unknown flag: "); ok {
		return strings.TrimSpace(rest)
	}
	if _, rest, ok := strings.Cut(msg, "unknown shorthand flag: "); ok {
		return strings.TrimSpace(rest)
	}
	return msg
}

func unsupportedFlag(name string) *AppError {
	return &AppError{
		Exit:    ExitUsage,
		Code:    CodeUnsupportedFlag,
		Message: fmt.Sprintf("flag %s is unsupported; configure the CA with --tls-ca", name),
		Detail:  map[string]any{"flag": name},
	}
}
