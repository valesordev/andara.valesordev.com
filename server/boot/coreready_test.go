// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package boot

import (
	"context"
	"strings"
	"testing"

	"github.com/valesordev/andara/server/content"
)

// AW-SRV-013 AC-15's readiness half, boot rule 5: a server reports ready only
// once the World in effect includes the core its boot activated.
//
// A content.source=kafka boot needs a broker, so this runs on a directory and
// names its pack (dir@0) as the core the boot activated. The rule is the same
// one: coreInEffect compares rt.core with the World's in-effect versions, and
// it doesn't care where the versions came from.
//   - Before the World has content, the core isn't in effect: not ready.
//   - ReconcileContent brings it into effect, and the process may become ready.
//   - A boot whose active core this build's World doesn't hold (here dir@1,
//     with dir@0 in effect) fails reconcile and never becomes ready.
func TestReadiness_WaitsForTheCoreInEffect(t *testing.T) {
	t.Run("in effect after reconcile", func(t *testing.T) {
		rt, logs := runtime(t, fixture(t, "valid"), false)
		if code := rt.LoadContent(context.Background()); code != ExitOK {
			t.Fatalf("load: %s", logs.String())
		}
		rt.Cfg.ContentCorePack = content.DirPack
		rt.core = &content.CoreBoot{Active: 0}
		startMemoryLoop(t, rt)
		if err := rt.coreInEffect(); err == nil {
			t.Fatal("the core reads as in effect before the World has any content")
		}
		if rt.Ready() {
			t.Fatal("ready before reconcile")
		}
		if code := rt.ReconcileContent(context.Background()); code != ExitOK {
			t.Fatalf("reconcile: exit %d\n%s", code, logs.String())
		}
		if err := rt.coreInEffect(); err != nil {
			t.Fatalf("after reconcile: %v", err)
		}
	})
	t.Run("never in effect", func(t *testing.T) {
		rt, logs := runtime(t, fixture(t, "valid"), false)
		if code := rt.LoadContent(context.Background()); code != ExitOK {
			t.Fatalf("load: %s", logs.String())
		}
		rt.Cfg.ContentCorePack = content.DirPack
		rt.core = &content.CoreBoot{Active: 1}
		startMemoryLoop(t, rt)
		if code := rt.ReconcileContent(context.Background()); code != ExitFail {
			t.Fatalf("reconcile with the active core not in effect: exit %d, want %d", code, ExitFail)
		}
		if rt.Ready() {
			t.Fatal("ready with its core not in effect")
		}
		if !strings.Contains(logs.String(), "content core not in effect") {
			t.Errorf("no error line naming it:\n%s", logs.String())
		}
	})
}
