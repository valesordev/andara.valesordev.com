// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"
)

func newCompletionCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "completion [bash|zsh|fish|powershell]",
		Short:         "Generate shell completion scripts",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		ValidArgs:     []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			var buf bytes.Buffer
			root := cmd.Root()
			switch args[0] {
			case "bash":
				if err := root.GenBashCompletion(&buf); err != nil {
					return err
				}
			case "zsh":
				if err := root.GenZshCompletion(&buf); err != nil {
					return err
				}
			case "fish":
				if err := root.GenFishCompletion(&buf, true); err != nil {
					return err
				}
			case "powershell":
				if err := root.GenPowerShellCompletion(&buf); err != nil {
					return err
				}
			default:
				return &AppError{
					Exit:    ExitUsage,
					Code:    CodeInvalidValue,
					Message: fmt.Sprintf("unknown shell %q", args[0]),
					Detail:  map[string]any{"shell": args[0]},
				}
			}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(map[string]string{"shell": args[0], "script": buf.String()})
			}
			_, err := rt.stdout.Write(buf.Bytes())
			return err
		},
	}
}
