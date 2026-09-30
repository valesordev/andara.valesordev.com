// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/valesordev/andara/content/core"
)

func newVersionCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "version",
		Short:         "Print version, commit, and build date",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeVersion()
		},
	}
}

// writeVersion names the andara.core this binary embeds after the build, so
// that "which core does this CLI validate against" has an answer a script can
// compare with an environment's active core (AW-CLI-002 AC-9, AW-INF-022).
func (rt *runtime) writeVersion() error {
	payload := struct {
		Version     string `json:"version"`
		Commit      string `json:"commit"`
		BuiltAt     string `json:"built_at"`
		CoreVersion uint64 `json:"core_version"`
	}{Version: version, Commit: commit, BuiltAt: builtAt, CoreVersion: core.Version()}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(payload)
	}
	_, err := fmt.Fprintf(rt.stdout, "version:  %s\ncommit:   %s\nbuilt_at: %s\ncore:     %s@%d\n",
		payload.Version, payload.Commit, payload.BuiltAt, core.Pack, payload.CoreVersion)
	return err
}
