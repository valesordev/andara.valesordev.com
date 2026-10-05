// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
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
	cmd.AddCommand(newSnapshotVerifyCmd(rt))
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
		local      bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List snapshot rounds as the server sees them, or a Zone's objects with --local",
		Long: "List the snapshot rounds the server sees, newest first, with whether each is complete\n" +
			"against the Zones the server owns (Admin.ListSnapshotRounds, operator only).\n\n" +
			"With --local, list one Zone's objects newest offset first, reading the store directly\n" +
			"and asking no server: it answers what is in the bucket or on the volume even when no\n" +
			"server is running, or when the server's view disagrees.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !local {
				return rt.snapshotListRPC(zone, limit)
			}
			if zone == "" {
				return fmt.Errorf("--zone is required with --local")
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
	fs.StringVar(&zone, "zone", "", "Zone to list; required with --local, otherwise only rounds holding it")
	fs.BoolVar(&local, "local", false, "read the configured store directly and issue no RPC")
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

// snapshotListRPC is `snapshot list` over Admin.ListSnapshotRounds.
func (rt *runtime) snapshotListRPC(zone string, limit int) error {
	client, err := rt.adminClient()
	if err != nil {
		return err
	}
	ctx, cancel := rt.callCtx()
	defer cancel()
	resp, err := client.ListSnapshotRounds(ctx, connect.NewRequest(&adminv1.ListSnapshotRoundsRequest{ZoneId: zone, Limit: uint32(limit)}))
	if err != nil {
		return rpcError(err)
	}
	type roundRow struct {
		Tick         uint64   `json:"tick"`
		StateVersion uint32   `json:"state_version"`
		Zones        []string `json:"zones"`
		Complete     bool     `json:"complete"`
		TakenAt      string   `json:"taken_at,omitempty"`
		AgeSeconds   int64    `json:"age_seconds,omitempty"`
	}
	now := time.Now()
	rows := make([]roundRow, 0, len(resp.Msg.GetRounds()))
	for _, r := range resp.Msg.GetRounds() {
		row := roundRow{Tick: r.GetTick(), StateVersion: r.GetStateVersion(), Zones: r.GetZoneIds(), Complete: r.GetComplete()}
		if ns := r.GetTakenAtUnixNano(); ns > 0 {
			t := time.Unix(0, ns)
			row.TakenAt, row.AgeSeconds = t.UTC().Format(time.RFC3339), int64(now.Sub(t).Seconds())
		}
		rows = append(rows, row)
	}
	if rt.settings.Output == outputJSON {
		return rt.writeJSON(struct {
			Count  int        `json:"count"`
			Rounds []roundRow `json:"rounds"`
		}{Count: len(rows), Rounds: rows})
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(rt.stdout, "no snapshot rounds")
		return err
	}
	w := tabwriter.NewWriter(rt.stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "TICK\tVERSION\tZONES\tCOMPLETE\tAGE"); err != nil {
		return err
	}
	for _, r := range rows {
		age := "-"
		if r.TakenAt != "" {
			age = (time.Duration(r.AgeSeconds) * time.Second).String()
		}
		if _, err := fmt.Fprintf(w, "%d\t%d\t%d\t%t\t%s\n", r.Tick, r.StateVersion, len(r.Zones), r.Complete, age); err != nil {
			return err
		}
	}
	return w.Flush()
}

func newSnapshotVerifyCmd(rt *runtime) *cobra.Command {
	var round uint64
	cmd := &cobra.Command{
		Use:   "verify --round T",
		Short: "Verify one snapshot round on the server against its recorded hashes",
		Long: "Restore round T into a scratch Engine on the server, verify it at its own tick, replay\n" +
			"it to the log head, and compare with the head's recorded State Hash\n" +
			"(Admin.VerifySnapshotRound, operator only). The live Engine is never touched.\n\n" +
			"Exits 0 for a match; 1 for a mismatch (told apart by `outcome`) or a refusal (NOT_FOUND,\n" +
			"FAILED_PRECONDITION); 3 when the server can't be reached; 4 on a timeout.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if round == 0 {
				return &AppError{Exit: ExitUsage, Code: CodeInvalidValue, Message: "--round is required and must be above 0", Detail: map[string]any{"flag": "--round"}}
			}
			client, err := rt.adminClient()
			if err != nil {
				return err
			}
			ctx, cancel := rt.callCtx()
			defer cancel()
			resp, err := client.VerifySnapshotRound(ctx, connect.NewRequest(&adminv1.VerifySnapshotRoundRequest{Tick: round}))
			if err != nil {
				return rpcError(err)
			}
			m := resp.Msg
			outcome := strings.ToLower(strings.TrimPrefix(m.GetOutcome().String(), "VERIFY_OUTCOME_"))
			verdict := "mismatch"
			if m.GetMatch() {
				verdict = "match"
			}
			data := map[string]any{"result": verdict, "outcome": outcome, "round": round, "compared_tick": m.GetComparedTick()}
			text := fmt.Sprintf("%s\noutcome=%s\nround=%d", verdict, outcome, round)
			switch m.GetOutcome() {
			case adminv1.VerifyOutcome_VERIFY_OUTCOME_SEED_MISMATCH:
				data["recorded_seed"], data["configured_seed"] = m.GetRecordedSeed(), m.GetConfiguredSeed()
				text += fmt.Sprintf("\nrecorded_seed=%d\nconfigured_seed=%d", m.GetRecordedSeed(), m.GetConfiguredSeed())
			case adminv1.VerifyOutcome_VERIFY_OUTCOME_CONTENT_MISMATCH:
			default:
				data["expected_hash"], data["actual_hash"] = hex.EncodeToString(m.GetExpectedHash()), hex.EncodeToString(m.GetActualHash())
				text += fmt.Sprintf("\ncompared_tick=%d\nexpected_hash=%x\nactual_hash=%x", m.GetComparedTick(), m.GetExpectedHash(), m.GetActualHash())
			}
			if err := rt.writeResult(text, data); err != nil {
				return err
			}
			if !m.GetMatch() {
				return &AppError{Exit: ExitFail, Code: "snapshot_mismatch", Message: "round " + strconv.FormatUint(round, 10) + " does not reproduce the log: " + outcome, Rendered: true}
			}
			return nil
		},
	}
	cmd.Flags().Uint64Var(&round, "round", 0, "tick of the round to verify (required)")
	return cmd
}
