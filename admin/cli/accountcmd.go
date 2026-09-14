// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	accountsv1 "github.com/valesordev/andara/gen/go/andara/accounts/v1"
	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
)

// `andara-cli account …`, `andara-cli invite …`, and `andara-cli registration …`
// wrap the account-administration Admin RPCs one-to-one (AW-SRV-008). Every
// one needs a stored operator credential; every one is audited server-side.

// The CLI speaks only the generated protocol (CLAUDE.md §10: no privileged
// back door), so the name tables live here rather than being imported from
// the server's auth package.
var roleNames = map[string]accountsv1.Role{
	"player":      accountsv1.Role_PLAYER,
	"builder":     accountsv1.Role_BUILDER,
	"game_master": accountsv1.Role_GAME_MASTER,
	"operator":    accountsv1.Role_OPERATOR,
}

func parseRoles(in []string) ([]accountsv1.Role, error) {
	var out []accountsv1.Role
	for _, r := range in {
		for _, part := range strings.Split(r, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			role, known := roleNames[part]
			if !known {
				return nil, &AppError{Exit: ExitUsage, Code: CodeInvalidValue,
					Message: fmt.Sprintf("unknown role %q; roles are player, builder, game_master, operator (agent accounts use `account create-agent`)", part),
					Detail:  map[string]any{"role": part}}
			}
			if !slices.Contains(out, role) {
				out = append(out, role)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func parseStatus(s string) (accountsv1.AccountStatus, bool) {
	switch s {
	case "active":
		return accountsv1.AccountStatus_ACTIVE, true
	case "disabled":
		return accountsv1.AccountStatus_DISABLED, true
	}
	return accountsv1.AccountStatus_ACCOUNT_STATUS_UNSPECIFIED, false
}

var modeNames = map[string]accountsv1.RegistrationMode{
	"closed": accountsv1.RegistrationMode_CLOSED,
	"invite": accountsv1.RegistrationMode_INVITE,
	"open":   accountsv1.RegistrationMode_OPEN,
}

func newAccountCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "account",
		Short:         "Create and administer accounts (operator)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newAccountCreateCmd(rt), newAccountResetPasswordCmd(rt), newAccountSetRolesCmd(rt),
		newAccountSetStatusCmd(rt), newAccountCreateAgentCmd(rt))
	return cmd
}

func (rt *runtime) writeResult(human string, v any) error {
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(v)
	}
	_, err := fmt.Fprintln(rt.stdout, human)
	return err
}

func newAccountCreateCmd(rt *runtime) *cobra.Command {
	var username string
	var roles []string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:           "create",
		Short:         "Create a password account with the given roles",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if username == "" {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--username is required", Detail: map[string]any{"flag": "--username"}}
			}
			protoRoles, err := parseRoles(roles)
			if err != nil {
				return err
			}
			password, err := rt.readSecret("Password for "+username+": ", passwordStdin)
			if err != nil {
				return err
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.CreateAccount(ctx, connect.NewRequest(&adminv1.CreateAccountRequest{Username: username, Password: password, Roles: protoRoles}))
			if err != nil {
				return rpcError(err)
			}
			id := resp.Msg.GetAccountId()
			return rt.writeResult(fmt.Sprintf("created account %s (%s)", id, username),
				map[string]any{"account_id": id, "username": username})
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "username, 3-32 characters of a-z 0-9 _ -")
	cmd.Flags().StringSliceVar(&roles, "role", nil, "role to grant (repeatable): player, builder, game_master, operator; default player")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin instead of the terminal")
	return cmd
}

func newAccountResetPasswordCmd(rt *runtime) *cobra.Command {
	var passwordStdin bool
	var version uint64
	cmd := &cobra.Command{
		Use:           "reset-password <account-id>",
		Short:         "Replace an account's password and revoke its refresh tokens",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			password, err := rt.readSecret("New password: ", passwordStdin)
			if err != nil {
				return err
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.ResetPassword(ctx, connect.NewRequest(&adminv1.ResetPasswordRequest{AccountId: args[0], NewPassword: password, ExpectedRecordVersion: version}))
			if err != nil {
				return rpcError(err)
			}
			return rt.writeResult(fmt.Sprintf("password reset for %s (record version %d)", args[0], resp.Msg.GetRecordVersion()),
				map[string]any{"account_id": args[0], "record_version": resp.Msg.GetRecordVersion()})
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin instead of the terminal")
	cmd.Flags().Uint64Var(&version, "expected-version", 0, "refuse unless the record is at this version (0: any)")
	return cmd
}

func newAccountSetRolesCmd(rt *runtime) *cobra.Command {
	var roles []string
	var version uint64
	cmd := &cobra.Command{
		Use:           "set-roles <account-id>",
		Short:         "Replace an account's role set",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			protoRoles, err := parseRoles(roles)
			if err != nil {
				return err
			}
			if len(protoRoles) == 0 {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "at least one --role is required", Detail: map[string]any{"flag": "--role"}}
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.SetRoles(ctx, connect.NewRequest(&adminv1.SetRolesRequest{AccountId: args[0], Roles: protoRoles, ExpectedRecordVersion: version}))
			if err != nil {
				return rpcError(err)
			}
			return rt.writeResult(fmt.Sprintf("roles of %s set to %s (record version %d)", args[0], strings.Join(roles, ","), resp.Msg.GetRecordVersion()),
				map[string]any{"account_id": args[0], "roles": roles, "record_version": resp.Msg.GetRecordVersion()})
		},
	}
	cmd.Flags().StringSliceVar(&roles, "role", nil, "role to hold (repeatable): player, builder, game_master, operator")
	cmd.Flags().Uint64Var(&version, "expected-version", 0, "refuse unless the record is at this version (0: any)")
	return cmd
}

func newAccountSetStatusCmd(rt *runtime) *cobra.Command {
	var version uint64
	cmd := &cobra.Command{
		Use:           "set-status <account-id> <active|disabled>",
		Short:         "Enable or disable an account; disabling closes its sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			status, ok := parseStatus(args[1])
			if !ok {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "status must be active or disabled", Detail: map[string]any{"status": args[1]}}
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.SetAccountStatus(ctx, connect.NewRequest(&adminv1.SetAccountStatusRequest{AccountId: args[0], Status: status, ExpectedRecordVersion: version}))
			if err != nil {
				return rpcError(err)
			}
			return rt.writeResult(fmt.Sprintf("%s is now %s (record version %d)", args[0], args[1], resp.Msg.GetRecordVersion()),
				map[string]any{"account_id": args[0], "status": args[1], "record_version": resp.Msg.GetRecordVersion()})
		},
	}
	cmd.Flags().Uint64Var(&version, "expected-version", 0, "refuse unless the record is at this version (0: any)")
	return cmd
}

func newAccountCreateAgentCmd(rt *runtime) *cobra.Command {
	var username, pack, kind, subject string
	cmd := &cobra.Command{
		Use:           "create-agent",
		Short:         "Create an agent account scoped to one content pack",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if username == "" || pack == "" {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--username and --pack are required", Detail: map[string]any{}}
			}
			var ck accountsv1.CredentialKind
			switch kind {
			case "api-key":
				ck = accountsv1.CredentialKind_API_KEY
			case "workload-jwt":
				ck = accountsv1.CredentialKind_WORKLOAD_JWT
			default:
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--credential must be api-key or workload-jwt", Detail: map[string]any{"credential": kind}}
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.CreateAgentAccount(ctx, connect.NewRequest(&adminv1.CreateAgentAccountRequest{Username: username, PackId: pack, CredentialKind: ck, WorkloadSubject: subject}))
			if err != nil {
				return rpcError(err)
			}
			id := resp.Msg.GetAccountId()
			// The API key is shown exactly once, here, and is the one thing a
			// human must copy from this output; it is never stored by the CLI.
			view := map[string]any{"account_id": id, "username": username, "pack_id": pack, "credential": kind}
			human := fmt.Sprintf("created agent account %s (%s) for pack %s", id, username, pack)
			if key := resp.Msg.GetApiKey(); key != "" {
				view["api_key"] = key
				human += "\napi key (shown once, never again): " + key
			}
			return rt.writeResult(human, view)
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "agent username")
	cmd.Flags().StringVar(&pack, "pack", "", "content pack the agent may drive")
	cmd.Flags().StringVar(&kind, "credential", "api-key", "api-key (returned once) or workload-jwt (projected service-account token)")
	cmd.Flags().StringVar(&subject, "workload-subject", "", "expected JWT sub for workload-jwt")
	return cmd
}

func newInviteCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "invite",
		Short:         "Issue and revoke invite codes (operator)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newInviteIssueCmd(rt), newInviteRevokeCmd(rt))
	return cmd
}

func newInviteIssueCmd(rt *runtime) *cobra.Command {
	var count uint32
	cmd := &cobra.Command{
		Use:           "issue",
		Short:         "Mint invite codes on your account; codes are shown once",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.IssueInvite(ctx, connect.NewRequest(&adminv1.IssueInviteRequest{Count: count}))
			if err != nil {
				return rpcError(err)
			}
			expires := unixTime(resp.Msg.GetExpiresUnix())
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(map[string]any{"codes": resp.Msg.GetCodes(), "expires": expires})
			}
			for _, c := range resp.Msg.GetCodes() {
				fmt.Fprintln(rt.stdout, c)
			}
			_, err = fmt.Fprintf(rt.stderr, "%d code(s), expire %s\n", len(resp.Msg.GetCodes()), expires)
			return err
		},
	}
	cmd.Flags().Uint32Var(&count, "count", 1, "how many codes to issue (1..100)")
	return cmd
}

func newInviteRevokeCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "revoke <code>",
		Short:         "Make an unredeemed invite code unusable",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			if _, err := client.RevokeInvite(ctx, connect.NewRequest(&adminv1.RevokeInviteRequest{Code: args[0]})); err != nil {
				return rpcError(err)
			}
			return rt.writeResult("invite revoked", map[string]any{"revoked": true})
		},
	}
}

func newRegistrationCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "registration",
		Short:         "Switch the registration mode (operator)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:           "set <closed|invite|open>",
		Short:         "Set the registration mode; an audited write, not a deploy",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			mode, ok := modeNames[strings.ToLower(args[0])]
			if !ok {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "mode must be closed, invite, or open", Detail: map[string]any{"mode": args[0]}}
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.SetRegistrationMode(ctx, connect.NewRequest(&adminv1.SetRegistrationModeRequest{Mode: mode}))
			if err != nil {
				return rpcError(err)
			}
			prev := strings.ToLower(strings.TrimPrefix(resp.Msg.GetPrevious().String(), "REGISTRATION_MODE_"))
			return rt.writeResult(fmt.Sprintf("registration mode is now %s (was %s)", args[0], prev),
				map[string]any{"mode": args[0], "previous": prev})
		},
	})
	return cmd
}
