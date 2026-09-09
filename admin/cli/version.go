package cli

import (
	"fmt"

	"github.com/spf13/cobra"
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

func (rt *runtime) writeVersion() error {
	payload := struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		BuiltAt string `json:"built_at"`
	}{Version: version, Commit: commit, BuiltAt: builtAt}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(payload)
	}
	_, err := fmt.Fprintf(rt.stdout, "version:  %s\ncommit:   %s\nbuilt_at: %s\n", payload.Version, payload.Commit, payload.BuiltAt)
	return err
}
