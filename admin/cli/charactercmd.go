// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// `andara-cli character …` is the roster (AW-CLI-007, AW-SRV-014): making a
// Character and seeing the Account's. These are roster RPCs, not world
// Commands — nothing typed into the world creates or selects — and each
// runs inside a Session of its own, so every roster action is a
// Session-correlated audit line on the server.

// error.code values the roster adds. Additive-only. The last five are the
// server's ErrorInfo reasons (domain andara.character), passed through.
const (
	CodeNoCharacter       = "no_character"
	CodeCharacterRequired = "character_required"
	CodeRosterFull        = "roster_full"
	CodeNameTaken         = "name_taken"
	CodeNameInvalid       = "name_invalid"
	CodeAlreadyLive       = "already_live"
	CodeNoSuchCharacter   = "no_such_character"
)

// rosterDomain is ErrorInfo.domain on every roster refusal.
const rosterDomain = "andara.character"

var rosterReasons = []string{CodeRosterFull, CodeNameTaken, CodeNameInvalid, CodeAlreadyLive, CodeNoSuchCharacter}

// rosterError maps a roster refusal to exit 1 with the server's reason as
// error.code and its message as the message; anything else is rpcError's.
func rosterError(err error) error {
	var ce *connect.Error
	if reason, _ := errorInfo(err); slices.Contains(rosterReasons, reason) && errors.As(err, &ce) {
		return &AppError{Exit: ExitFail, Code: reason, Message: ce.Message(),
			Detail: map[string]any{"grpc_code": ce.Code().String(), "domain": rosterDomain}}
	}
	return rpcError(err)
}

func newCharacterCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "character",
		Short:         "Create and list your Characters",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newCharacterCreateCmd(rt), newCharacterListCmd(rt))
	return cmd
}

func newCharacterCreateCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Character on your Account",
		Long: `Create a Character on the logged-in Account. Names are reserved across
every Account, case-insensitively, and forever; an Account holds up to the
server's limit. Enter the World as it with ` + "`andara-cli play --character <name>`" + `.`,
		Example:       `  andara-cli character create Aldric`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return rt.inSession(func(ctx context.Context, game gamev1connect.GameClient, session string) error {
				// The count "1 of 5" is the roster before plus the one made:
				// CreateCharacterResponse carries the cap but not the count.
				before, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: session}))
				if err != nil {
					return rosterError(err)
				}
				resp, err := game.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: session, Name: args[0]}))
				if err != nil {
					return rosterError(err)
				}
				c, limit := resp.Msg.GetCharacter(), resp.Msg.GetMaxPerAccount()
				count := len(active(before.Msg.GetCharacters())) + 1
				if rt.settings.Output == outputJSON {
					return rt.writeJSON(map[string]any{"character": protoJSON(c), "count": count, "max_per_account": limit})
				}
				_, err = fmt.Fprintf(rt.stdout, "%s (%d of %d)\n", c.GetName(), count, limit)
				return err
			})
		},
	}
}

func newCharacterListCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your Characters: name, live or dormant, and where",
		Long: `List the logged-in Account's Characters, one per line and sorted by name:
the name, whether it is live (bound to a Session now) or dormant, and the
zone/room where the server last knew it to be. The server is asked every
time; nothing is cached.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return rt.inSession(func(ctx context.Context, game gamev1connect.GameClient, session string) error {
				resp, err := game.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: session}))
				if err != nil {
					return rosterError(err)
				}
				cs := byName(active(resp.Msg.GetCharacters()))
				if rt.settings.Output == outputJSON {
					out := make([]json.RawMessage, 0, len(cs))
					for _, c := range cs {
						out = append(out, protoJSON(c))
					}
					return rt.writeJSON(map[string]any{"characters": out, "max_per_account": resp.Msg.GetMaxPerAccount()})
				}
				if len(cs) == 0 {
					_, err := fmt.Fprintln(rt.stdout, "No characters yet; create one with `andara-cli character create <name>`.")
					return err
				}
				_, err = fmt.Fprint(rt.stdout, formatRoster(cs))
				return err
			})
		},
	}
}

// formatRoster is list's human form: name, live or dormant, zone/room, in
// aligned columns, one Character a line, in the order given.
func formatRoster(cs []*gamev1.CharacterSummary) string {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, c := range cs {
		state := "dormant"
		if c.GetLive() {
			state = "live"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s/%s\n", c.GetName(), state, c.GetZoneId(), c.GetRoomId())
	}
	_ = tw.Flush()
	return b.String()
}

// active drops the Characters a later `character delete` will have ended
// (AW-SRV-032): a deleted Character is neither listed nor playable.
func active(cs []*gamev1.CharacterSummary) []*gamev1.CharacterSummary {
	return slices.DeleteFunc(slices.Clone(cs), func(c *gamev1.CharacterSummary) bool {
		return c.GetStatus() == accountsv1.CharacterStatus_CHARACTER_STATUS_DELETED
	})
}

// byName sorts by name as a person reads it: case folded, then as stored.
func byName(cs []*gamev1.CharacterSummary) []*gamev1.CharacterSummary {
	slices.SortStableFunc(cs, func(a, b *gamev1.CharacterSummary) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.GetName()), strings.ToLower(b.GetName())), cmp.Compare(a.GetName(), b.GetName()))
	})
	return cs
}

// resolveCharacter picks the Character play enters the World as (AC-5,
// AC-6): the one named, case-insensitively; with no name, the Account's
// only one. Names are what a person types; character_id never is.
func resolveCharacter(cs []*gamev1.CharacterSummary, name string) (*gamev1.CharacterSummary, error) {
	cs = byName(active(cs))
	if name != "" {
		for _, c := range cs {
			if strings.EqualFold(c.GetName(), name) {
				return c, nil
			}
		}
		return nil, &AppError{Exit: ExitFail, Code: CodeNoSuchCharacter,
			Message: fmt.Sprintf("this account has no character named %q; `andara-cli character list` shows yours", name),
			Detail:  map[string]any{"character": name, "characters": names(cs)}}
	}
	switch len(cs) {
	case 0:
		return nil, &AppError{Exit: ExitUsage, Code: CodeNoCharacter,
			Message: "this account has no character; create one with `andara-cli character create <name>`",
			Detail:  map[string]any{}}
	case 1:
		return cs[0], nil
	}
	return nil, &AppError{Exit: ExitUsage, Code: CodeCharacterRequired,
		Message: fmt.Sprintf("this account has %d characters; choose one with --character: %s", len(cs), strings.Join(names(cs), ", ")),
		Detail:  map[string]any{"characters": names(cs)}}
}

func names(cs []*gamev1.CharacterSummary) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.GetName())
	}
	return out
}

// inSession runs fn inside a Session of its own: OpenSession with the
// stored credential, fn, CloseSession — whatever fn returned. Every call
// is bounded by --timeout and parented by the cli.command span.
func (rt *runtime) inSession(fn func(ctx context.Context, game gamev1connect.GameClient, session string) error) error {
	game, cred, err := rt.gameClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	resp, err := game.OpenSession(ctx, connect.NewRequest(&gamev1.OpenSessionRequest{
		ProtocolVersion: clientProtocolVersion,
		AuthToken:       cred.SessionToken,
		ClientName:      clientName(),
	}))
	if err != nil {
		if ve := versionError(err); ve != nil {
			return ve
		}
		return rpcError(err)
	}
	session := resp.Msg.GetSessionId()
	defer func() {
		cctx, ccancel := rt.callCtx()
		defer ccancel()
		if _, err := game.CloseSession(cctx, connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: session})); err != nil {
			rt.log("debug", "CloseSession: "+err.Error())
		}
	}()
	return fn(ctx, game, session)
}

// protoJSON is a message in its canonical JSON form, field names as in the
// .proto, for embedding in a command's JSON output.
func protoJSON(m *gamev1.CharacterSummary) json.RawMessage {
	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(m)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}
