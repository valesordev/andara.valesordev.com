// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/valesordev/andara/gen/go/andara/admin/v1"
	"github.com/valesordev/andara/server/gateway"
)

// AW-SRV-007: `snapshot list` and `snapshot verify` over the Admin RPCs.

// snapshotRefusal is a refusal that names its gRPC status, as the gateway's
// statusError matches it by shape.
type snapshotRefusal struct {
	code   uint32
	reason string
}

func (e *snapshotRefusal) Error() string { return "refused: " + e.reason }
func (e *snapshotRefusal) Status() (uint32, string, string, proto.Message) {
	return e.code, "andara.snapshot", e.reason, nil
}

type fakeSnapshots struct {
	rounds []*adminv1.SnapshotRound
	verify func(tick uint64) (*adminv1.VerifySnapshotRoundResponse, error)
}

func (f *fakeSnapshots) ListSnapshotRounds(_ context.Context, req *adminv1.ListSnapshotRoundsRequest) (*adminv1.ListSnapshotRoundsResponse, error) {
	return &adminv1.ListSnapshotRoundsResponse{Rounds: f.rounds}, nil
}

func (f *fakeSnapshots) VerifySnapshotRound(_ context.Context, req *adminv1.VerifySnapshotRoundRequest) (*adminv1.VerifySnapshotRoundResponse, error) {
	return f.verify(req.GetTick())
}

func snapshotServer(t *testing.T, f *fakeSnapshots) map[string]string {
	t.Helper()
	s := startServerWith(t, nil, func(o *gateway.Options) { o.SnapshotAdmin = f })
	env := s.env(t)
	login(t, env)
	return env
}

func TestSnapshotList_OverTheAdminRPC(t *testing.T) {
	taken := time.Now().Add(-90 * time.Second).UnixNano()
	env := snapshotServer(t, &fakeSnapshots{rounds: []*adminv1.SnapshotRound{
		{Tick: 4200, StateVersion: 2, ZoneIds: []string{"town", "wilds"}, Complete: true, TakenAtUnixNano: taken},
		{Tick: 4100, StateVersion: 2, ZoneIds: []string{"town"}, Complete: false},
	}})
	res := runCLI(t, []string{"snapshot", "list"}, env)
	if res.exit != ExitOK {
		t.Fatalf("exit %d: %s", res.exit, res.stderr)
	}
	lines := strings.Split(strings.TrimSpace(res.stdout), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "TICK") || !strings.Contains(lines[1], "4200") || !strings.Contains(lines[1], "true") || !strings.Contains(lines[2], "false") {
		t.Fatalf("table:\n%s", res.stdout)
	}
	res = runCLI(t, []string{"snapshot", "list", "-o", "json"}, env)
	var out struct {
		Count  int `json:"count"`
		Rounds []struct {
			Tick     uint64 `json:"tick"`
			Complete bool   `json:"complete"`
		} `json:"rounds"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &out); err != nil || out.Count != 2 || out.Rounds[0].Tick != 4200 || !out.Rounds[0].Complete {
		t.Fatalf("json: %s (%v)", res.stdout, err)
	}
}

func TestSnapshotVerify_MatchAndMismatchExits(t *testing.T) {
	hash := func(b byte) []byte { return []byte{b, b, b, b} }
	env := snapshotServer(t, &fakeSnapshots{verify: func(tick uint64) (*adminv1.VerifySnapshotRoundResponse, error) {
		switch tick {
		case 1:
			return &adminv1.VerifySnapshotRoundResponse{Match: true, Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_MATCH, ExpectedHash: hash(1), ActualHash: hash(1), ComparedTick: 9000}, nil
		case 2:
			return &adminv1.VerifySnapshotRoundResponse{Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_REPLAY_MISMATCH, ExpectedHash: hash(1), ActualHash: hash(2), ComparedTick: 4321}, nil
		case 3:
			return &adminv1.VerifySnapshotRoundResponse{Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_SEED_MISMATCH, RecordedSeed: 7, ConfiguredSeed: 8}, nil
		case 4:
			return nil, &snapshotRefusal{5, "round_not_found"}
		case 5:
			return nil, &snapshotRefusal{9, "round_incomplete"}
		}
		return nil, &snapshotRefusal{4, "verify_timeout"}
	}})
	run := func(round string) runResult {
		return runCLI(t, []string{"snapshot", "verify", "--round", round}, env)
	}
	if res := run("1"); res.exit != ExitOK || !strings.HasPrefix(res.stdout, "match\n") || !strings.Contains(res.stdout, "compared_tick=9000") {
		t.Errorf("match: exit %d %q", res.exit, res.stdout)
	}
	if res := run("2"); res.exit != ExitFail || !strings.HasPrefix(res.stdout, "mismatch\noutcome=replay_mismatch") || !strings.Contains(res.stdout, "compared_tick=4321") {
		t.Errorf("replay mismatch: exit %d %q", res.exit, res.stdout)
	}
	if res := run("3"); res.exit != ExitFail || !strings.Contains(res.stdout, "outcome=seed_mismatch") || !strings.Contains(res.stdout, "recorded_seed=7") || !strings.Contains(res.stdout, "configured_seed=8") {
		t.Errorf("seed mismatch: exit %d %q", res.exit, res.stdout)
	}
	for round, code := range map[string]string{"4": "NotFound", "5": "FailedPrecondition"} {
		res := runCLI(t, []string{"snapshot", "verify", "--round", round, "-o", "json"}, env)
		var e jsonErrorEnvelope
		if res.exit != ExitFail || json.Unmarshal([]byte(res.stdout), &e) != nil || !strings.EqualFold(strings.ReplaceAll(e.Error.Detail["grpc_code"].(string), "_", ""), code) {
			t.Errorf("round %s: exit %d stdout %s", round, res.exit, res.stdout)
		}
	}
	if res := run("6"); res.exit != ExitTimeout {
		t.Errorf("deadline exceeded: exit %d, want %d", res.exit, ExitTimeout)
	}
	if res := runCLI(t, []string{"snapshot", "verify"}, env); res.exit != ExitUsage {
		t.Errorf("no --round: exit %d, want %d", res.exit, ExitUsage)
	}
}

// A verify replays to the log head, so it isn't bound by the default --timeout.
func TestSnapshotVerify_OutlivesTheDefaultTimeout(t *testing.T) {
	env := snapshotServer(t, &fakeSnapshots{verify: func(uint64) (*adminv1.VerifySnapshotRoundResponse, error) {
		time.Sleep(1500 * time.Millisecond)
		return &adminv1.VerifySnapshotRoundResponse{Match: true, Outcome: adminv1.VerifyOutcome_VERIFY_OUTCOME_MATCH}, nil
	}})
	res := runCLI(t, []string{"snapshot", "verify", "--round", "1", "--timeout", "1s"}, env)
	if res.exit != ExitOK {
		t.Fatalf("exit %d: %s", res.exit, res.stderr)
	}
}
