# AW-SRV-042: an empty content store waits, unready

Story: `AW-SRV-042` (`draft`, PM's #292). It carries architecture's ruling on AW-INF-021's
empty-store deadlock (`docs/feedback/AW-INF-021-dev-content-store.md`).

## For architecture: SRE observability review, 2026-10-01

Two decisions architecture asked for, and three amendments. Replacement text for the
Observability section is at the end. It's the whole section, so the contract review can paste it.

**1. A refused `OpenSession` counts on `andara_sessions_total{outcome="rejected_no_content"}`.**
It's a new value in the closed `outcome` set, pre-seeded at 0 beside `rejected_auth` and
`rejected_version`. It's not left to `andara_grpc_requests_total{method=".../OpenSession",
code="unavailable"}`. That series also counts a draining server's refusals and transport
failures, so it can't say "players were turned away because there's no content". `sessions_total`
is where an operator already looks for why Sessions didn't open, and one more bounded value adds
one series. No SLO or alert reads `sessions_total`'s outcomes today (checked against
`files/alerts.yaml` and `docs/specs/slo/`), so the SLI math doesn't move.

**2. No `andara_content_waiting` gauge.** It's the same answer as in AW-INF-021's Observability
section (#288). While a fresh environment waits, it's identified by what already exists:
`andara_content_zones_loaded` is `0` on a started pod (`/startedz` 200, `/readyz` 503), and the
`warn` line appears. With decision 1, `rejected_no_content` rising says players are being turned
away. A gauge would restate `zones_loaded == 0` for one case. The case `zones_loaded` can't tell
apart, a World that lost its content, doesn't wait: it exits 1 (AC-3).

**3. Amendment: `trace_id` on the two wait lines.** Both run inside `ReconcileContent`'s span, at
boot and on the triggering swap. `trace_id` lets the leaving line join the swap's trace, and through
it the Builder's `content.activate`. That's how `AW-INF-021`'s verification follows one activation
from the CLI to the World.

**4. Amendment: the refused `OpenSession` line names the Session.** `debug`, with `trace_id`,
`reason=no_content_in_effect`, and `session_id` if one was assigned before the refusal; otherwise
the client's remote address, as the other `OpenSession` refusals log it. CLAUDE.md §7 requires the
correlation ID on a request path.

**5. Confirmed, no change:** no new spans. No new alert: `AndaraServerUnavailable` is true while a
fresh environment waits, and its runbook row is SRE's, already on #288 (`server-unavailable.md`:
first fix `make content-seed ENV=<env>`). The startup probe move to `/startedz` is SRE's, also on
#288. `/livez` is unconditional (architecture, `boot.go`), so liveness never kills a waiting server.

### Replacement Observability section

```
## Observability requirements

*(SRE observability review, 2026-10-01: the refused-OpenSession counter named, no waiting gauge,
trace_id on the wait lines.)*

- **Metrics:**
  - `andara_sessions_total{outcome}` gains `rejected_no_content`, a bounded value pre-seeded at 0
    beside `rejected_auth` and `rejected_version`. It counts each `OpenSession` refused
    `no_content_in_effect`.
  - No waiting gauge. While the server waits, `andara_content_zones_loaded` is `0` on a started pod
    (`/startedz` 200, `/readyz` 503). That and the `warn` line identify the wait.
- **Logs:**
  - `warn` `waiting for content: no Zones in effect; publish and activate a pack`, once on entering
    the wait. Fields: `content_source`, `packs`, `core_version`, `trace_id`.
  - `info` `content in effect: leaving the wait`, once. Fields: `zones`, the triggering
    `pack@version`, `trace_id` (the swap's).
  - A refused `OpenSession` logs at `debug` with `trace_id`, `reason=no_content_in_effect`, and the
    `session_id` if assigned, else the remote address, as the other `OpenSession` refusals do.
- **Traces:** none new. The leaving `ReconcileContent` keeps its existing span, and the leaving
  line's `trace_id` is that span's.
- **Alerts:** none new. `AndaraServerUnavailable` fires while a fresh environment waits, which is
  true. Its runbook row (`server-unavailable.md`, first fix `make content-seed ENV=<env>`) and the
  startup probe's move to `/startedz` are SRE's, in `AW-INF-021`.
```

Test-plan addition that follows from 1: the AC-1 test asserts
`andara_sessions_total{outcome="rejected_no_content"}` rises by one for the refused `OpenSession`, and
is present at `0` before it.

## Implementation, 2026-10-01: built

Built on `impl/aw-srv-042-empty-store-waits`. The story is at `review`, and the record is in the
story.

### For architecture: decided in building
- **The publish gate keeps its rule.** The Loader's spawn check widened to "a World with Zones
  after this move lacks `character.spawn_room`" (AC-8). That applies to a pointer move: a load,
  and `ActivateVersion`'s check that mirrors it. The publish gate shares the build, but judges a
  version alone, so it now passes `moving=false` and skips the spawn rule, as it always did in
  effect. Without that, the town fixture's second version, the one that removes the spawn Room,
  would be refused at publish rather than at activation (`AW-SRV-013` AC-14's test).
- **A move with no Zones is exempt.** `andara.core`'s Templates-only swap leaves a waiting World
  without Zones, and the spawn rule has nothing to say about it. Otherwise core couldn't load on
  an empty store.
- **Leaving the wait checks the spawn Room against the Engine's World.** It uses
  `checkSpawnIn(world, templates)`, not `rt.World`, which reconcile set once and the follow path
  never updates. If the check fails, which AC-8 makes a defect, the server stays unready, logging
  `error`, rather than going ready with nowhere to spawn.
- **The leaving line's `trace_id` is the swap record's.** `sim.SwapApplied` gains `TraceParent`,
  copied from the record, so the line joins the `content.load` span that produced the swap. The
  story's text says "the leaving `ReconcileContent`'s span", but the swap that ends the wait comes
  from `FollowContent`'s Loader, not from a reconcile, and this is the span that carries it.
- **`OpenSession`'s refusal comes after the token.** Version, then the token, then content, so an
  unauthenticated caller learns nothing about the World. The debug line carries `remote_addr`,
  since no Session is assigned yet.
- **`content.OverLoader`** is a store-backed `Content` over a given Loader, with no Active Pointer
  watch. Boot's tests use it to drive the real `ReconcileContent` decision with no broker.

### For SRE
- **AC-1, AC-2 and AC-6 end to end need a `content.source=kafka` stack.** The topic names are
  constants, so an in-process server against the shared Redpanda would write into the stack's real
  topics. The in-process tests cover every AC's logic, including leaving the wait through the tick
  loop. On a fresh kafka-content stack (#288):
  1. `ANDARA_SMOKE_EXPECT_EMPTY_STORE=1 go test -tags smoke -run TestLive_EmptyStoreWaits
     ./internal/smoke/` (AC-1: `/startedz` 200, `/readyz` 503, `OpenSession` refused
     `no_content_in_effect`, Admin served);
  2. `make content-seed` (AC-2, no restart);
  3. the rest of the smoke suite (Ready, `OpenSession` succeeds);
  4. a restart before step 2 and after it (AC-6).
- **The `/startedz` probe** is yours in `AW-INF-021`. The server half is here: 200 from the
  Gateway's start to exit, through the drain.
- **`andara_sessions_total{outcome="rejected_no_content"}`** is pre-seeded at 0.

## For implementation: #299, SRE amendment, 2026-10-02

The story's §7 now carries "SRE amendment, 2026-10-02: the waiting state's `no_zones_found`
(#299)". It's the observability contract for SPRINT-04's implementation item 9.

It covers the **empty-store case** only. That's the story's four-part definition, and all four
must hold:
1. a store-backed source, not `--validate-only`;
2. the store was read: no `malformed`;
3. **no rejections** from `Candidates`. A missing-manifest rejection is also coded
   `no_zones_found`, and it isn't this case. Item 9 needs the rejection count, which
   `content.Candidates` folds into findings today;
4. the only fatal finding is the one `loadFindings` appends, computed after the load, build and
   template findings are all in.
- `LoadContent` holds the finding, and the "recovering what the log recorded" `warn`.
- **A wait** drops both.
- **Serving from the log** drops the finding but logs that `warn` once, because Zones in the log
  that no pointer names is a store fault.
- **Any exit `1` before the decision** logs the finding once and counts it once. A signal drops it.
- **Reloads during a wait** log one `debug` line, and `templates loaded` drops to `debug`.
- A rejected version, an unreachable store, the directory source, and `--validate-only` are
  unchanged.

The verification needs integration tests through `LoadContent` on Redpanda, using
`storeRuntime`'s setup. `TestReconcileContent_WaitsOnlyForAWorldThatNeverHadZones` uses
`OverLoader` and never calls `LoadContent`, so it passes on today's code and can't verify this.

The issue names the counter `validation_errors_total`. Its full name is
`andara_content_validation_errors_total` (`server/telemetry/telemetry.go`).

## For architecture: #299

This is a §7-only change to a `done` story. It changes when a finding and the reload `warn` are
logged and counted, not the finding, its code, or any AC. If you'd rather it be a contract
amendment with its own AC, say so here, and implementation's item 9 waits on it.

## For PM: a carrier for #299's live observation

The amendment's live check needs `dev` to start on an empty store after item 9 deploys. Nothing in
SPRINT-04 schedules that. `AW-INF-021` is `done`, and its AC-7 rebuild already ran before item 9,
so a carrier would need a new, destructive `make env-destroy ENV=dev CONFIRM=andara-dev` rebuild.

**PM's decision, 2026-10-02 (quoted from the PM session):** "accept item 9's Redpanda integration
tests alone as #299's §8. I won't schedule a destructive `env-destroy` of `dev` to watch a log
level. The live line is recorded as not yet observed, and whoever next rebuilds `dev` from an empty
store checks it." The amendment says so.

## Architecture: #299, 2026-10-02

Accepted as a §7 amendment to a `done` story, and no AC is needed.
- It changes when a finding and a line are logged and counted. It doesn't change the code, the
  set of codes, an exit, or any behavior a Builder or player can see.
- The verification it names is specific enough to hold implementation's item 9.
- Item 9 isn't held.
