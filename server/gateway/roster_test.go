// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	gamev1 "github.com/valesordev/andara/gen/go/andara/game/v1"
	"github.com/valesordev/andara/gen/go/andara/game/v1/gamev1connect"
)

// recordingRoster captures what the Session looked like when its teardown
// told the roster, and answers the RPCs.
type recordingRoster struct {
	mu       sync.Mutex
	released []rosterRelease
	created  []string
	selected []string
}

type rosterRelease struct {
	id      string
	closing bool
	// canceled is whether the Session's context was already done when the
	// roster was told; it must not be — the roster reads the routing
	// table, and everything working on the Session's behalf derives from
	// that context.
	canceled bool
}

func (r *recordingRoster) ListCharacters(context.Context, *Session) (*gamev1.ListCharactersResponse, error) {
	return &gamev1.ListCharactersResponse{MaxPerAccount: 5}, nil
}

func (r *recordingRoster) CreateCharacter(_ context.Context, s *Session, name string) (*gamev1.CreateCharacterResponse, error) {
	r.mu.Lock()
	r.created = append(r.created, name)
	r.mu.Unlock()
	return &gamev1.CreateCharacterResponse{Character: &gamev1.CharacterSummary{CharacterId: "ch-1", Name: name}, MaxPerAccount: 5}, nil
}

func (r *recordingRoster) SelectCharacter(_ context.Context, s *Session, id string) (*gamev1.SelectCharacterResponse, error) {
	r.mu.Lock()
	r.selected = append(r.selected, id)
	r.mu.Unlock()
	return &gamev1.SelectCharacterResponse{Partition: 3, AcceptedOffset: 7}, nil
}

func (r *recordingRoster) ReleaseSession(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, rosterRelease{id: s.ID, closing: s.Closing(), canceled: s.Context().Err() != nil})
}

func (r *recordingRoster) releases() []rosterRelease {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]rosterRelease(nil), r.released...)
}

// The ordering the roster's live flag depends on (AW-SRV-014, review of
// PR #43): by the time a Session's teardown tells the roster, the Session
// reports Closing — so a SelectCharacter registering under the roster's
// lock is refused rather than left behind — and its context is not yet
// canceled, so the teardown can still read and produce on its behalf.
// Every way a Session ends goes through the same path; a CloseSession and
// a dropped connection are both checked here.
func TestRoster_ReleasedBeforeTheContextIsCanceled(t *testing.T) {
	rr := &recordingRoster{}
	h := start(t, func(o *Options) { o.Roster = rr })
	client := h.game()

	closed := h.open(t, client)
	if _, err := client.CloseSession(context.Background(), connect.NewRequest(&gamev1.CloseSessionRequest{SessionId: closed.SessionId})); err != nil {
		t.Fatal(err)
	}
	// A connection that goes away: the same teardown, no RPC at all.
	hc, tr := h.httpClient()
	dropClient := gamev1connect.NewGameClient(hc, h.baseURL())
	dropped := h.open(t, dropClient)
	tr.CloseIdleConnections()

	waitFor(t, 5*time.Second, func() bool { return len(rr.releases()) == 2 }, "both Sessions released")
	seen := map[string]rosterRelease{}
	for _, rel := range rr.releases() {
		seen[rel.id] = rel
	}
	for _, id := range []string{closed.SessionId, dropped.SessionId} {
		rel, ok := seen[id]
		if !ok {
			t.Fatalf("session %s was never released to the roster", id)
		}
		if !rel.closing {
			t.Errorf("session %s: the roster was told before Closing was set", id)
		}
		if rel.canceled {
			t.Errorf("session %s: the roster was told after the context was canceled", id)
		}
	}
	// A Session that ended reports Closing for good, which is what the
	// roster reads when a Select races the teardown.
	if s, ok := h.srv.sessions.get(closed.SessionId); ok {
		t.Fatalf("closed session still resolvable: %v", s)
	}
}

// The roster seam carries the three RPCs, and the stub refuses them with
// UNIMPLEMENTED rather than pretending.
func TestRoster_SeamCarriesTheRPCs(t *testing.T) {
	rr := &recordingRoster{}
	h := start(t, func(o *Options) { o.Roster = rr })
	client := h.game()
	sess := h.open(t, client)
	ctx := context.Background()
	if _, err := client.CreateCharacter(ctx, connect.NewRequest(&gamev1.CreateCharacterRequest{SessionId: sess.SessionId, Name: "Aldric"})); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListCharacters(ctx, connect.NewRequest(&gamev1.ListCharactersRequest{SessionId: sess.SessionId})); err != nil {
		t.Fatal(err)
	}
	resp, err := client.SelectCharacter(ctx, connect.NewRequest(&gamev1.SelectCharacterRequest{SessionId: sess.SessionId, CharacterId: "ch-1"}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetPartition() != 3 || resp.Msg.GetAcceptedOffset() != 7 {
		t.Fatalf("SelectCharacter = %v", resp.Msg)
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if len(rr.created) != 1 || rr.created[0] != "Aldric" || len(rr.selected) != 1 {
		t.Fatalf("the seam saw created=%v selected=%v", rr.created, rr.selected)
	}
}
