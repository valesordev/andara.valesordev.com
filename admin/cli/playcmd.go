// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"time"

	"github.com/spf13/cobra"
)

// play is the Text Interface (AW-CLI-004): a real Protocol client that
// opens a Session, subscribes, and turns typed lines into Intents and
// Events into prose. It is permanent, text-only, and an operator and
// developer tool as much as a player one: seeing the Intents and Events go
// by is a first-class feature (Brian, 2026-09-07), toggled with /protocol.
func newPlayCmd(rt *runtime) *cobra.Command {
	var o playOptions
	cmd := &cobra.Command{
		Use:   "play",
		Short: "Enter the world: a text interface over the Protocol",
		Long: `Open a Session on the server, subscribe to its Events, and read Intents
from the terminal. What the world says arrives as prose; what you type is
sent as typed, and the server parses it — this client decides nothing.

Lines starting with / are for the client, not the world:
  /protocol [on|off]   show the Intents sent and the Events received
  /help                list these
  /quit                leave (as does Ctrl-C, or Ctrl-D at an empty line)

A dropped connection is announced and retried with backoff; the stream
resumes from the last Event seen, and a resume the server cannot honor is
announced too — the transcript never silently skips. --output json writes
the raw Event stream to stdout, one envelope per line, and everything else
to stderr.

You enter the World as one of your Characters: the one --character names,
or, with no flag, your only one. Make one first with
` + "`andara-cli character create <name>`" + `. A reconnect selects the same Character
again, waiting for the previous session to let it go if it must.

A stored credential is required: run ` + "`andara-cli auth login`" + ` first.`,
		Example: `  andara-cli play --character Aldric
  > look
  > north
  > /protocol on
  > west
  ^C

  printf 'look\n' | andara-cli play --output json | jq .`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.clientTimeout <= 0 {
				o.clientTimeout = rt.settings.Timeout
			}
			return rt.play(o)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&o.character, "character", "", "enter the World as this Character (by name, any case); optional when the Account has exactly one")
	fs.StringVar(&o.as, "as", "", "open the Session acting as this account ID (operator or game master; audited)")
	fs.BoolVar(&o.reconnect, "reconnect", true, "retry a dropped connection with backoff and resume the stream")
	fs.BoolVar(&o.world, "world", false, "ask for World visibility: every Event, wherever it happens (game master or operator)")
	fs.BoolVar(&o.showProtocol, "show-protocol", false, "start with protocol visibility on (/protocol toggles it)")
	fs.BoolVar(&o.noHistory, "no-history", false, "do not read or write $XDG_STATE_HOME/andara/history")
	fs.DurationVar(&o.clientTimeout, "client-timeout", 0, "how long one Submit may wait for its answer (0 means --timeout)")
	return cmd
}

// playOptions is what the flags resolve to.
type playOptions struct {
	character     string
	as            string
	reconnect     bool
	world         bool
	showProtocol  bool
	noHistory     bool
	clientTimeout time.Duration
}
