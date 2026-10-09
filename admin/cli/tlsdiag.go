// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
)

// tlsFailure is a certificate-verification failure on the way to the server,
// carrying what the CLI trusted and where each setting came from (AW-CLI-011).
// A pinned CA replaces the system trust store, so a stale pin fails a public
// certificate with nothing in x509's message to say a pin exists.
type tlsFailure struct {
	cause error
	// hostname: the certificate is valid but not for the name verified.
	hostname bool

	ca, caSrc               string
	configPath, configSrc   string
	serverName, serverNmSrc string
}

func (e *tlsFailure) Error() string { return e.cause.Error() }
func (e *tlsFailure) Unwrap() error { return e.cause }

// verificationFailure reports whether err is a certificate that didn't verify:
// an unknown authority, an invalid chain (expired, wrong usage) or a name
// mismatch. Nothing else (refused, DNS, timeout) is one.
func verificationFailure(err error) (hostname, ok bool) {
	var ua x509.UnknownAuthorityError
	var inv x509.CertificateInvalidError
	var hn x509.HostnameError
	switch {
	case errors.As(err, &hn):
		return true, true
	case errors.As(err, &ua), errors.As(err, &inv):
		return false, true
	}
	return false, false
}

// hintTransport tags a certificate-verification failure from base with the
// settings in force, so rpcError can say which CA and which name were used.
type hintTransport struct {
	base http.RoundTripper
	rt   *resolved
}

func (h *hintTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := h.base.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	hostname, ok := verificationFailure(err)
	if !ok {
		return resp, err
	}
	s := h.rt
	return resp, &tlsFailure{
		cause: err, hostname: hostname,
		ca: s.TLSCA, caSrc: string(s.TLSCASrc),
		configPath: s.ConfigPath, configSrc: string(s.ConfigSource),
		serverName: s.TLSServerName, serverNmSrc: string(s.TLSServerNameSrc),
	}
}

func (h *hintTransport) CloseIdleConnections() {
	if c, ok := h.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// source renders where a setting came from, in the words `config show` and the
// docs use: the flag, the variable, or the config file with whatever chose it.
func (e *tlsFailure) source(src, flag, env string) string {
	switch Source(src) {
	case SourceFlag:
		return flag
	case SourceEnv:
		return env
	}
	from := "config file " + e.configPath
	switch Source(e.configSrc) {
	case SourceEnv:
		from += " (ANDARA_CONFIG)"
	case SourceFlag:
		from += " (--config)"
	}
	return from
}

// suffix is the sentence fragment AW-CLI-011 appends to the connect error. A
// name mismatch on an explicit server.tls_server_name names that name, since the
// CA isn't what failed; every other verification failure names the CA.
func (e *tlsFailure) suffix() string {
	if e.hostname && e.serverName != "" {
		return fmt.Sprintf("; expected the name %s (server.tls_server_name, from %s)",
			e.serverName, e.source(e.serverNmSrc, "--tls-server-name", "ANDARA_TLS_SERVER_NAME"))
	}
	if e.hostname {
		// The CA verified; the certificate just isn't for the host dialed.
		// Naming the CA would point at the wrong cause, so x509's own text
		// stands (a gap in the contract, raised with architecture).
		return ""
	}
	if e.ca == "" {
		return "; trusted the system trust store"
	}
	return fmt.Sprintf("; trusted only the CA in %s (server.tls_ca, from %s)",
		e.ca, e.source(e.caSrc, "--tls-ca", "ANDARA_TLS_CA_FILE"))
}

// detail is the JSON side of suffix: every fact the human message states, and
// the config file's selector, so a consumer can tell ANDARA_CONFIG from the
// default (AC-3).
func (e *tlsFailure) detail(into map[string]any) {
	into["tls_ca"] = e.ca
	into["tls_ca_source"] = e.caSrc
	into["config_path"] = e.configPath
	into["config_path_source"] = e.configSrc
	if e.serverName != "" {
		into["tls_server_name"] = e.serverName
		into["tls_server_name_source"] = e.serverNmSrc
	}
}
