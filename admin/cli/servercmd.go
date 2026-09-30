// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"encoding/hex"
	"fmt"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
)

// `andara-cli server info` (AW-CLI-003 AC-13): the build, the protocol range,
// and the content in effect, one pack per line. The demo and AW-INF-021 read
// it at every step of a publish.
func newServerCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "server",
		Short:         "Read the server's state",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:           "info",
		Short:         "Print the server's build, protocol range, and the content in effect",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return rt.serverInfo()
		},
	})
	return cmd
}

func (rt *runtime) serverInfo() error {
	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	resp, err := client.GetServerInfo(ctx, connect.NewRequest(&adminv1.GetServerInfoRequest{}))
	if err != nil {
		return rt.withTrace(rpcError(err))
	}
	m := resp.Msg
	type pack struct {
		Pack    string `json:"pack"`
		Version uint64 `json:"version"`
	}
	packs := make([]pack, 0, len(m.GetContent()))
	for _, pv := range m.GetContent() { // sorted by pack_id, server-side
		packs = append(packs, pack{Pack: pv.GetPackId(), Version: pv.GetVersion()})
	}
	digest := hex.EncodeToString(m.GetContentDigest())
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(map[string]any{
			"version": m.GetVersion(), "commit": m.GetCommit(), "environment": m.GetEnvironment(),
			"protocol_min": m.GetProtocolMinVersion(), "protocol_max": m.GetProtocolMaxVersion(),
			"content": packs, "content_digest": digest, "trace_id": rt.traceID(),
		})
	}
	fmt.Fprintf(rt.stdout, "version %s\ncommit %s\nenvironment %s\nprotocol %d-%d\n",
		m.GetVersion(), m.GetCommit(), m.GetEnvironment(), m.GetProtocolMinVersion(), m.GetProtocolMaxVersion())
	for _, p := range packs {
		fmt.Fprintf(rt.stdout, "content %s@%d\n", p.Pack, p.Version)
	}
	_, err = fmt.Fprintf(rt.stdout, "content_digest %s\n", digest)
	return err
}
