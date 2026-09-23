// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/hex"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/valesordev/andara/server/config"
	"github.com/valesordev/andara/server/sim"
	"github.com/valesordev/andara/server/store"
)

// snapshot list reads the snapshot store directly, the way `sim repl` reads
// content on disk: nothing here talks to a server, so the global
// --server-address and credentials are read and unused.
//
// Direct rather than over Admin deliberately. AW-SRV-007's interface contract
// puts `snapshot list` and `snapshot verify` behind `Admin.ListSnapshotRounds`,
// and defining that RPC is that story's to do — spending its field numbers here
// would be this lane writing the contract. What AW-SRV-006 needs is the
// operator check its test plan names and its runbook leans on, and a store an
// operator can already reach is enough for that. The two are not redundant: the
// Admin path answers "what does the running server see", this one answers "what
// is actually in the bucket", and a runbook wants the second when the first
// disagrees with it.
func newSnapshotCmd(rt *runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "snapshot",
		Short:         "Inspect Zone snapshots in the configured store (operator)",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return rt.writeCommandTree(cmd)
		},
	}
	cmd.AddCommand(newSnapshotListCmd(rt))
	return cmd
}

// snapshotRow is one object, as `snapshot list` prints it.
type snapshotRow struct {
	Zone         string `json:"zone"`
	Tick         uint64 `json:"tick"`
	StateVersion uint32 `json:"state_version"`
	Offset       int64  `json:"offset"`
	Key          string `json:"key"`
	Bytes        int    `json:"bytes"`
	StateHash    string `json:"state_hash"`
	TakenAt      string `json:"taken_at,omitempty"`
	// Error is set when the object could not be read or decoded. The row is
	// still printed: "there is an object here and it is unreadable" is the
	// answer an operator came for, and dropping it would make a corrupt
	// round look like a missing one.
	Error string `json:"error,omitempty"`
}

func newSnapshotListCmd(rt *runtime) *cobra.Command {
	var (
		zone       string
		storeKind  string
		fsPath     string
		s3Bucket   string
		s3Endpoint string
		limit      int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a Zone's snapshot objects, newest offset first",
		Long: "List the snapshot objects a Zone has in the configured store, newest offset first.\n\n" +
			"Reads the store directly rather than asking a server, so it answers what is in the\n" +
			"bucket or on the volume even when no server is running.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if zone == "" {
				return fmt.Errorf("--zone is required")
			}
			ws, err := store.Open(store.Options{
				Kind:       storeKind,
				FSPath:     fsPath,
				S3Bucket:   s3Bucket,
				S3Endpoint: s3Endpoint,
			})
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), rt.settings.Timeout)
			defer cancel()

			keys, err := ws.List(ctx, sim.ZoneID(zone))
			if err != nil {
				return err
			}
			if limit > 0 && len(keys) > limit {
				keys = keys[:limit]
			}
			rows := make([]snapshotRow, 0, len(keys))
			for _, key := range keys {
				rows = append(rows, readSnapshotRow(ctx, ws, zone, key))
			}
			if rt.settings.Output == outputJSON {
				return rt.writeJSON(struct {
					Zone  string        `json:"zone"`
					Count int           `json:"count"`
					Rows  []snapshotRow `json:"snapshots"`
				}{Zone: zone, Count: len(rows), Rows: rows})
			}
			return writeSnapshotTable(rt, zone, rows)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&zone, "zone", "", "Zone to list (required)")
	fs.StringVar(&storeKind, "store", config.DefaultSnapshotStore, "snapshot store: fs or s3")
	fs.StringVar(&fsPath, "fs-path", config.DefaultSnapshotFSPath, "directory the fs store writes under")
	fs.StringVar(&s3Bucket, "s3-bucket", "", "bucket the s3 store writes to")
	fs.StringVar(&s3Endpoint, "s3-endpoint", "", "S3 endpoint; MinIO locally, empty for AWS")
	fs.IntVar(&limit, "limit", 0, "show at most this many objects; 0 shows all")
	return cmd
}

// readSnapshotRow reads one object and describes it, turning a failure into a
// row rather than into an aborted listing: one corrupt object should not hide
// the rounds either side of it.
func readSnapshotRow(ctx context.Context, ws sim.WorldStore, zone, key string) snapshotRow {
	row := snapshotRow{Zone: zone, Key: key}
	b, err := ws.Get(ctx, key)
	if err != nil {
		row.Error = err.Error()
		return row
	}
	row.Bytes = len(b)
	env, _, err := store.Decode(b)
	if err != nil {
		// Including ErrStateVersion, which is the case an operator
		// rolling a binary back most needs to see: the row says the object
		// is there and this binary will not read it.
		row.Error = err.Error()
		return row
	}
	row.Tick = env.GetTick()
	row.StateVersion = env.GetStateVersion()
	row.StateHash = hex.EncodeToString(env.GetStateHash())
	if ns := env.GetTakenAtUnixNano(); ns > 0 {
		row.TakenAt = time.Unix(0, ns).UTC().Format(time.RFC3339)
	}
	for _, po := range env.GetOffsets() {
		if po.GetPartition() == sim.PartitionFor(sim.ZoneID(zone)) {
			row.Offset = po.GetOffset()
		}
	}
	return row
}

func writeSnapshotTable(rt *runtime, zone string, rows []snapshotRow) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintf(rt.stdout, "no snapshots for zone %s\n", zone)
		return err
	}
	w := tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "TICK\tVERSION\tOFFSET\tBYTES\tTAKEN AT\tSTATE HASH\tKEY"); err != nil {
		return err
	}
	for _, r := range rows {
		if r.Error != "" {
			if _, err := fmt.Fprintf(w, "-\t-\t-\t%d\t-\t-\t%s\t(%s)\n", r.Bytes, r.Key, r.Error); err != nil {
				return err
			}
			continue
		}
		// Twelve hex characters of the hash: enough to compare two rows by
		// eye, short enough that the table still fits a terminal. The full
		// value is in --output json.
		short := r.StateHash
		if len(short) > 12 {
			short = short[:12]
		}
		if _, err := fmt.Fprintf(w, "%d\t%d\t%d\t%d\t%s\t%s\t%s\n",
			r.Tick, r.StateVersion, r.Offset, r.Bytes, dashIfEmpty(r.TakenAt), short, r.Key); err != nil {
			return err
		}
	}
	return w.Flush()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
