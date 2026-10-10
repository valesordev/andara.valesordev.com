// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package main

import (
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/valesordev/andara/server/boot"
	"github.com/valesordev/andara/server/config"
)

// runConfigAssert is `andara-server config-assert` (AW-SRV-053): print the
// Kafka client settings in effect for every client the server and projector
// build, and exit 1 when one deviates from docs/specs/kafka/client-contract.md.
// It reaches no broker and needs no certificate or key: the configuration is
// parsed as --validate-only is, for the client.id base and the produce
// deadline it reads. The arguments are the server's own configuration flags.
func runConfigAssert(args []string, env config.EnvLookup, stdout, stderr io.Writer) int {
	cfg, err := config.Parse(validateOnly(args), env, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return boot.ExitOK
		}
		_, _ = io.WriteString(stderr, err.Error()+"\n")
		return boot.ExitFail
	}
	return boot.ConfigAssert(boot.KafkaSites(cfg), stdout, stderr)
}

// validateOnly is args with --validate-only forced on: an explicit
// --validate-only=false from a wrapper would otherwise switch the server's
// startup validation (brokers, TLS material, auth key) back on.
func validateOnly(args []string) []string {
	out := []string{"--validate-only"}
	for _, a := range args {
		if a == "--validate-only" || a == "-validate-only" ||
			strings.HasPrefix(a, "--validate-only=") || strings.HasPrefix(a, "-validate-only=") {
			continue
		}
		out = append(out, a)
	}
	return out
}
