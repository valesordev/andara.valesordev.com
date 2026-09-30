// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

package cli

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/valesordev/andara/content/core"
	logv1 "github.com/valesordev/andara/gen/go/andara/log/v1"
	"github.com/valesordev/andara/server/auth"
	"github.com/valesordev/andara/server/content"
	"github.com/valesordev/andara/server/gateway"
	"github.com/valesordev/andara/server/recordlog"
	"github.com/valesordev/andara/server/sim"
)

// contentStack is a content.source=kafka server's publish path, in process
// (AW-CLI-003): the gateway, AW-SRV-013's Admin over a Registry, the Account
// store as what authorizes each pack, and a Loader that follows Active
// Pointer moves, debounced, through an Engine that applies each swap. So
// `server info` reports what the Engine applied, not what a test set.
type contentStack struct {
	*liveServer
	reg      *content.Registry
	loader   *content.Loader
	auditor  *auth.Auditor
	debounce time.Duration
}

// stackDebounce is content.reload_debounce for these tests.
const stackDebounce = 100 * time.Millisecond

// startContentStack opens the stack over logs. store reads blobs and
// manifests back and watcher reports pointer moves; nil for both reads the
// Registry itself, as the in-memory harness does.
func startContentStack(t *testing.T, logs func(name string) recordlog.Log, store content.Store, watcher content.Watcher) *contentStack {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cache := content.BlobCache{Dir: t.TempDir()}
	reg, err := content.OpenRegistry(ctx, content.RegistryOptions{
		Blobs: logs("blobs"), Versions: logs("versions"), Active: logs("active"), Audit: logs("audit"), Cache: cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		store = registryStore{reg, cache}
	}
	if watcher == nil {
		watcher = &registryWatcher{reg: reg}
	}
	loader := content.NewLoader(content.LoaderOptions{Store: store, Packs: []string{content.AllPacks}})
	eng := &stepEngine{l: loader}
	eng.e = sim.NewEngine(sim.EmptyWorld(), nil, sim.Config{Seed: 1, Partitions: []int32{sim.WorldPartition}, Content: loader})
	loader.SetProducer(eng)

	auditor := auth.NewAuditor(logs("audit"), slog.New(slog.DiscardHandler), nil, nil)
	if _, err := content.BootCore(ctx, content.CoreBootOptions{Registry: reg, Auditor: auditor, Pack: content.CorePack,
		Version: core.Version(), Blobs: core.Blobs(), Build: "test"}); err != nil {
		t.Fatal(err)
	}
	if rejects, err := loader.LoadAll(ctx); err != nil || len(rejects) > 0 {
		t.Fatalf("boot load: %v %v", rejects, err)
	}
	go func() { _ = loader.Follow(ctx, watcher, stackDebounce, nil) }()

	s := startServerWith(t, nil, func(o *gateway.Options) {
		accounts := o.Verifier.(*auth.Store)
		admin, err := content.NewAdmin(content.AdminOptions{
			Registry: reg, Loader: loader, Blobs: store, Auditor: auditor,
			Accounts:     packHolderFunc(accounts.BuilderPacks),
			MaxBlobBytes: 8 << 20, MaxPackBytes: 256 << 20, OperatorSelfApproval: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		o.ContentAdmin = admin
		o.Content = loader.InEffect
	})
	return &contentStack{liveServer: s, reg: reg, loader: loader, auditor: auditor, debounce: stackDebounce}
}

// bootCore is a newer server's boot: it publishes andara.core@version, the
// embedded Templates with one byte of provenance changed so the bytes
// differ, and moves core's pointer to it.
func (s *contentStack) bootCore(t *testing.T, version uint64) error {
	t.Helper()
	blobs := core.Blobs()
	p := "templates/andara.core.Item.json"
	blobs[p] = bytes.Replace(blobs[p], []byte(`"line": 1`), []byte(`"line": 2`), 1)
	_, err := content.BootCore(context.Background(), content.CoreBootOptions{Registry: s.reg, Auditor: s.auditor,
		Pack: content.CorePack, Version: version, Blobs: blobs, Build: "test-newer"})
	return err
}

type packHolderFunc func(string) []string

func (f packHolderFunc) BuilderPacks(id string) []string { return f(id) }

// stepEngine is Partition 0's tick, for one Command at a time: each swap the
// Loader produces is applied by the Engine as soon as it's produced, and the
// Loader hears what was applied or refused, as it does from the tick loop.
type stepEngine struct {
	mu sync.Mutex
	e  *sim.Engine
	l  *content.Loader
}

func (s *stepEngine) ProduceSwap(_ context.Context, cmd *logv1.LoggedCommand) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := sim.CommandPartition(cmd)
	res, err := s.e.Step(sim.TickInput{Records: []sim.Record{{Partition: p, Offset: s.e.State().Offsets[p], Command: cmd}}})
	if err != nil {
		return err
	}
	s.l.Applied(res.Swaps)
	s.l.Refused(res.SwapsRefused)
	return nil
}

// registryWatcher reports pointer moves by polling the Registry: the
// in-memory stand-in for the active topic's consumer.
type registryWatcher struct{ reg *content.Registry }

func (w *registryWatcher) Watch(ctx context.Context) (<-chan content.PointerMove, error) {
	out := make(chan content.PointerMove)
	seen := w.reg.Pointers()
	go func() {
		defer close(out)
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			for pack, v := range w.reg.Pointers() {
				if seen[pack] == v {
					continue
				}
				seen[pack] = v
				select {
				case out <- content.PointerMove{Pack: pack, Version: v}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// identity is one person's andara-cli: their own credential file.
func (s *contentStack) identity(t *testing.T, username, password string) map[string]string {
	t.Helper()
	env := s.env(t)
	env["ANDARA_CONTENT_CACHE"] = t.TempDir()
	if res := runWithStdin(t, []string{"auth", "login", "--username", username, "--password-stdin"}, env, password+"\n"); res.exit != ExitOK {
		t.Fatalf("login as %s: exit=%d stderr=%q", username, res.exit, res.stderr)
	}
	return env
}

// builder creates a Builder holding packs and logs them in.
func (s *contentStack) builder(t *testing.T, username string, packs ...string) (string, map[string]string) {
	t.Helper()
	op := auth.WithPrincipal(context.Background(), auth.Principal{AccountID: "op", Roles: []auth.Role{auth.RoleOperator}})
	id, err := s.store.CreateAccount(op, username, "builder-password", []auth.Role{auth.RoleBuilder})
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) > 0 {
		if _, err := s.store.SetBuilderPacks(op, id, packs, 0); err != nil {
			t.Fatal(err)
		}
	}
	return id, s.identity(t, username, "builder-password")
}
