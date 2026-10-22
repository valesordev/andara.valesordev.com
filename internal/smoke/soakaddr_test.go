// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// No build tag: the address the soak dials is checked in every `go test` run,
// though the soak itself runs only under `soak`.
package smoke

import "testing"

// soakAddr is the edge address the soak dials (AW-SRV-058, ADR-0013
// Amendment 8): ANDARA_SOAK_ADDR whole when it is set, or else
// andara.local at ANDARA_EDGE_PORT, 443 when that is unset too.
func soakAddr(getenv func(string) string) string {
	if v := getenv("ANDARA_SOAK_ADDR"); v != "" {
		return v
	}
	port := getenv("ANDARA_EDGE_PORT")
	if port == "" {
		port = "443"
	}
	return "andara.local:" + port
}

// AC-11.
func TestSoakAddr(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"nothing set", nil, "andara.local:443"},
		{"edge port", map[string]string{"ANDARA_EDGE_PORT": "8443"}, "andara.local:8443"},
		{"soak addr wins", map[string]string{"ANDARA_SOAK_ADDR": "h:9", "ANDARA_EDGE_PORT": "8443"}, "h:9"},
		{"soak addr alone", map[string]string{"ANDARA_SOAK_ADDR": "h:9"}, "h:9"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := soakAddr(func(k string) string { return c.env[k] }); got != c.want {
				t.Errorf("soakAddr = %q, want %q", got, c.want)
			}
		})
	}
}
