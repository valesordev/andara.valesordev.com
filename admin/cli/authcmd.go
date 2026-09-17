// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	authv1 "github.com/valesordev/andara/gen/go/andara/auth/v1"
)

// newAuthCmd is `andara-cli auth`: login, logout, refresh, and whoami
// against andara.auth.v1.Auth (AW-SRV-008).
func newAuthCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "auth",
		Short:         "Log in, log out, and inspect the stored credential",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newAuthLoginCmd(rt), newAuthLogoutCmd(rt), newAuthRefreshCmd(rt), newAuthWhoamiCmd(rt))
	return cmd
}

// readSecret reads a password or key: from stdin when --password-stdin is
// set, else from the terminal without echo. A non-terminal stdin without
// the flag is a usage error rather than a silent read of whatever is there.
func (rt *runtime) readSecret(prompt string, fromStdin bool) (string, error) {
	in := rt.stdin
	if in == nil {
		in = os.Stdin
	}
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "cannot read the password from stdin", Detail: map[string]any{}}
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "stdin is not a terminal; pass --password-stdin", Detail: map[string]any{}}
	}
	fmt.Fprint(rt.stderr, prompt)
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(rt.stderr)
	if err != nil {
		return "", &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "cannot read the password", Detail: map[string]any{}}
	}
	return string(b), nil
}

func credentialFrom(username string, pair *authv1.TokenPair) storedCredential {
	return storedCredential{
		Username:       username,
		SessionToken:   pair.GetSessionToken(),
		SessionExpires: unixTime(pair.GetSessionExpiresUnix()),
		RefreshToken:   pair.GetRefreshToken(),
		RefreshExpires: unixTime(pair.GetRefreshExpiresUnix()),
	}
}

type loginView struct {
	Server         string `json:"server"`
	Username       string `json:"username"`
	SessionExpires string `json:"session_expires"`
	RefreshExpires string `json:"refresh_expires"`
	Credentials    string `json:"credentials"`
}

func newAuthLoginCmd(rt *runtime) *cobra.Command {
	var username string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:           "login",
		Short:         "Authenticate and store the token pair (mode 0600)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if username == "" {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--username is required", Detail: map[string]any{"flag": "--username"}}
			}
			password, err := rt.readSecret("Password: ", passwordStdin)
			if err != nil {
				return err
			}
			client, err := rt.authClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.Authenticate(ctx, connect.NewRequest(&authv1.AuthenticateRequest{Username: username, Password: password}))
			if err != nil {
				return rpcError(err)
			}
			cred := credentialFrom(username, resp.Msg.GetTokens())
			if err := rt.storeCredential(cred); err != nil {
				return err
			}
			view := loginView{Server: rt.settings.ServerAddress, Username: username, SessionExpires: cred.SessionExpires, RefreshExpires: cred.RefreshExpires, Credentials: rt.settings.CredentialsPath}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(view)
			}
			_, err = fmt.Fprintf(rt.stdout, "logged in to %s as %s; session valid until %s, stored in %s\n", view.Server, view.Username, view.SessionExpires, view.Credentials)
			return err
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "account username")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin instead of the terminal")
	return cmd
}

func newAuthLogoutCmd(rt *runtime) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:           "logout",
		Short:         "Revoke the stored refresh token and forget the credential",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cred, err := rt.loadCredential()
			if err != nil {
				return err
			}
			revoked := uint32(0)
			if cred != nil && cred.RefreshToken != "" {
				client, err := rt.authClient()
				if err != nil {
					return err
				}
				ctx, cancel := rt.callCtx()
				defer cancel()
				resp, err := client.Revoke(ctx, connect.NewRequest(&authv1.RevokeRequest{RefreshToken: cred.RefreshToken, All: all}))
				// A token the server no longer knows is already revoked;
				// the local copy is dropped either way.
				if err != nil && connect.CodeOf(err) != connect.CodeUnauthenticated {
					return rpcError(err)
				}
				if err == nil {
					revoked = resp.Msg.GetRevoked()
				}
			}
			if err := rt.dropCredential(); err != nil {
				return err
			}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(map[string]any{"server": rt.settings.ServerAddress, "revoked": revoked})
			}
			_, err = fmt.Fprintf(rt.stdout, "logged out of %s (%d refresh token(s) revoked)\n", rt.settings.ServerAddress, revoked)
			return err
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "revoke every refresh token on the account, not only this one")
	return cmd
}

func newAuthRefreshCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "refresh",
		Short:         "Exchange the stored refresh token for a fresh pair",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cred, err := rt.loadCredential()
			if err != nil {
				return err
			}
			if cred == nil || cred.RefreshToken == "" {
				return &AppError{Exit: ExitUsage, Code: CodeNotLoggedIn, Message: fmt.Sprintf("no credential for %s; run `andara-cli auth login`", rt.settings.ServerAddress), Detail: map[string]any{"server": rt.settings.ServerAddress}}
			}
			client, err := rt.authClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.Refresh(ctx, connect.NewRequest(&authv1.RefreshRequest{RefreshToken: cred.RefreshToken}))
			if err != nil {
				return rpcError(err)
			}
			pair := resp.Msg.GetTokens()
			cred.SessionToken, cred.SessionExpires = pair.GetSessionToken(), unixTime(pair.GetSessionExpiresUnix())
			cred.RefreshToken, cred.RefreshExpires = pair.GetRefreshToken(), unixTime(pair.GetRefreshExpiresUnix())
			if err := rt.storeCredential(*cred); err != nil {
				return err
			}
			view := loginView{Server: rt.settings.ServerAddress, Username: cred.Username, SessionExpires: cred.SessionExpires, RefreshExpires: cred.RefreshExpires, Credentials: rt.settings.CredentialsPath}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(view)
			}
			_, err = fmt.Fprintf(rt.stdout, "refreshed %s as %s; session valid until %s\n", view.Server, view.Username, view.SessionExpires)
			return err
		},
	}
}

func newAuthWhoamiCmd(rt *runtime) *cobra.Command {
	return &cobra.Command{
		Use:           "whoami",
		Short:         "Show which account the stored credential is for, never the credential",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cred, err := rt.loadCredential()
			if err != nil {
				return err
			}
			if cred == nil {
				return &AppError{Exit: ExitUsage, Code: CodeNotLoggedIn, Message: fmt.Sprintf("no credential for %s; run `andara-cli auth login`", rt.settings.ServerAddress), Detail: map[string]any{"server": rt.settings.ServerAddress}}
			}
			view := loginView{Server: rt.settings.ServerAddress, Username: cred.Username, SessionExpires: cred.SessionExpires, RefreshExpires: cred.RefreshExpires, Credentials: rt.settings.CredentialsPath}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(view)
			}
			_, err = fmt.Fprintf(rt.stdout, "%s as %s; session valid until %s, refresh until %s\n", view.Server, view.Username, view.SessionExpires, view.RefreshExpires)
			return err
		},
	}
}
