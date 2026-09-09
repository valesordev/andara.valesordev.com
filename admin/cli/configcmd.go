package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type credView struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
}

type configShowJSON struct {
	Settings    []Setting `json:"settings"`
	Credentials credView  `json:"credentials"`
}

func newConfigCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "config",
		Short:         "Show and inspect CLI configuration",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newConfigShowCmd(rt))
	return cmd
}

func newConfigShowCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "show",
		Short:         "Print effective settings and their sources",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeConfigShow()
		},
	}
}

func (rt *runtime) writeConfigShow() error {
	settings := rt.settings.list()
	view := credView{Path: rt.creds.path, Present: rt.creds.present}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(configShowJSON{Settings: settings, Credentials: view})
	}
	w := tabwriter.NewWriter(rt.stdout, 0, 8, 2, ' ', 0)
	for _, s := range settings {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", s.Key, s.Value, s.Source)
	}
	_, _ = fmt.Fprintf(w, "credentials.path\t%s\n", view.Path)
	_, _ = fmt.Fprintf(w, "credentials.present\t%t\n", view.Present)
	return w.Flush()
}
