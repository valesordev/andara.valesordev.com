---
id: AW-SRV-042
title: An empty content store waits, unready — and andara-cli --tls-server-name
epic: EPIC-05
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-012, AW-SRV-013, AW-CLI-001]
blocks: []
lane: implementation
risk: medium
---

## Context

`dev` can't be seeded. With `content.source=kafka`, a server whose store and World log hold no
Zones exits `1` with "no content in effect" (`AW-SRV-012`) before it serves Admin. Admin is the only
way to publish the first content, so nothing can be published. SRE found the deadlock while building
`AW-INF-021` (#277). Every new environment starts in that state once, and so does `AW-INF-021` AC-7's
rebuild from nothing.

Architecture ruled on 2026-09-30 (#289; `docs/feedback/AW-INF-021-dev-content-store.md`, "Architecture's
ruling: an empty store waits, unready"). A store-backed World that has never had content is a normal
first state, not a configuration error. In that state the server waits, unready, serving Admin and
refusing play, until the first Zones arrive. The seed reaches the pod over a port-forward. So the CLI
also needs to verify TLS against a name other than the one it dials. SRE builds the chart and
`content-seed` halves in `AW-INF-021` (#288). This story is the server and CLI halves, and it's on
SPRINT-03's critical path, ahead of `AW-INF-021`'s merge.

## User story

As an operator, I want a fresh environment's server to come up and wait for its first content, so
that I can seed it over Admin instead of being locked out by a server that exits before it serves.

## Scope

### In scope
- **Server (`server/boot`):**
  - the waiting-for-content state, and leaving it on the first swap that brings Zones;
  - `/startedz`, and `/readyz` reporting 503 while the server waits;
  - Game's `no_content_in_effect` refusal;
  - the state's two log lines.
- **CLI (`admin/cli`):** `server.tls_server_name`, set by `--tls-server-name`, `ANDARA_TLS_SERVER_NAME`,
  or config, and listed by `config show`.
- **Docs:** `server/README.md`'s health table and config keys, and `admin/README.md`'s config table.

### Out of scope
- The chart's `startupProbe` move to `/startedz`, `make content-seed`, and the
  `AndaraServerUnavailable` runbook line. Those are SRE's, in `AW-INF-021` (#288).
- Any change to `content.source=dir`, or to a World that has had content. Both keep today's exit
  (ACs 3 and 4).
- A new metric. `andara_content_zones_loaded` is already 0 while the server waits. SRE decides in
  `AW-INF-021` whether it wants an explicit gauge.

## Acceptance criteria

1. **Given** `content.source=kafka`, an empty store and an empty World log **when** the server boots
   **then** it stays up, and all of the following hold:
   - `/startedz` returns 200 and `/readyz` returns 503;
   - `Game.OpenSession` fails `UNAVAILABLE`, with `ErrorInfo` domain `andara.game` and reason
     `no_content_in_effect`;
   - `Admin.ListVersions` succeeds;
   - the `warn` line `waiting for content: …` appears exactly once.
2. **Given** that waiting server **when** a Zone-bearing pack is published, approved and activated over
   Admin **then** `/readyz` returns 200 with no restart, the `info` line `content in effect: leaving the
   wait` appears with `zones` and `pack@version`, and `OpenSession` succeeds.
3. **Given** a World log holding a Content Swap with Zones, whose store now refuses every version **when**
   the server boots **then** it exits `1` with "no content in effect", unchanged (`AW-SRV-012`).
4. **Given** `content.source=dir` with no Zones **when** the server boots **then** it exits `1`,
   unchanged (`AW-SRV-001` AC-9).
5. **Given** a port-forward to a pod whose certificate names `andara-0.andara.<ns>.svc` **when**
   `andara-cli --tls-server-name andara-0.andara.<ns>.svc` dials it through `localhost` **then** the TLS
   handshake verifies against `server.tls_ca` and the call succeeds. **Given** the same dial without the
   flag **then** verification fails, and the CLI exits with its connection error.
6. **Given** a waiting server **when** it restarts **then** it waits again, with no exit loop. **Given** a
   server restarted after its first swap with Zones **then** it recovers Ready.
7. **Given** a waiting server draining on SIGTERM **then** `/readyz` reports drain as today, and
   `/startedz` stays 200 until exit.

## Interface contract

**When the server waits.** With `content.source=kafka`, the server waits for content when two things
hold: the World log holds no Content Swap with Zones, and no followed pack's Active Pointer loads
Zones. Every other no-content case keeps today's exit `1`.

**Open: how the server knows a swap had Zones** (Open questions, item 1). `ContentSwap` in the log
carries only `pack_id`, `version`, `world_digest` and `base_digest`. When the store can't resolve a
logged version, which is exactly AC-3's case, the server can't tell a swap that brought Zones from
one that brought only Templates, and `andara.core`'s swap is always Templates-only. AC-3 (exit) and
AC-6 (wait again) need a discriminator that's readable during a failed recovery. Architecture
decides it before this story reaches `ready`.

**While it waits:**
- Recovery has finished, `andara.core` is in effect (`AW-SRV-013` AC-15), and the gRPC listener serves.
- Admin is fully served: the content RPCs, accounts, and `server info` (no Zone content listed).
- `Game.OpenSession` is refused `UNAVAILABLE`, with `ErrorInfo{domain: "andara.game", reason:
  "no_content_in_effect"}`. Clients may retry it.
- No Zone ticks, because there's no Zone.

**It leaves the wait** on the first Active Pointer move that brings Zones, through the existing
`FollowContent` → `ReconcileContent` path, unchanged. It never re-enters the wait in that process or
after a restart, because a swap with Zones is now in the log.

| Path (on `http.port`) | 200 when |
|---|---|
| `/livez` | unchanged |
| `/startedz` | **new.** Recovery has finished and the gRPC listener is serving, whether or not the server waits. It stays 200 until exit |
| `/readyz` | unchanged, plus 503 while waiting for content |

**CLI:**

| Flag | Env | Config key | Default |
|---|---|---|---|
| `--tls-server-name` | `ANDARA_TLS_SERVER_NAME` | `server.tls_server_name` | empty, meaning the host from `server.address` |

- Precedence follows `AW-CLI-001`, as for `server.tls_ca`.
- The flag sets only the TLS verification name. It doesn't change the dial target, or the key that
  credentials are stored under (`server.address`).
- `config show` lists it with its source.

## Data / state impact

No schema change. If architecture picks option (a) in Open questions, item 1, `ContentSwap`
gains an additive field, and older swaps read it as its zero value. Otherwise the log is unchanged.
Waiting is derived at boot from the log and the store, and nothing
persists it. A rollout to an existing `dev` that already holds a swap with Zones behaves as it does
today.

## Observability requirements

- **Metrics:** `andara_sessions_total{outcome}` gains `rejected_no_content`, for an `OpenSession`
  refused while the server waits. The closed set becomes `closed`, `dropped`, `rejected_version`,
  `rejected_auth`, `revoked` and `rejected_no_content`, so the cardinality is 6, and the new outcome is
  pre-seeded at 0 like the others (`server/gateway/metrics.go`). `andara_grpc_requests_total` also
  counts the call as `UNAVAILABLE`, but it can't say why, which is why the outcome is needed.
  `andara_content_zones_loaded` is already 0 while the server waits. SRE decides in `AW-INF-021`
  whether an explicit `andara_content_waiting` gauge is wanted too, and if so, it's added here at
  SRE's review. *(Revised after review of #292: the first draft pointed at a refusal metric with a
  `reason` label, which doesn't exist.)*
- **Logs:**
  - `warn` `waiting for content: no Zones in effect; publish and activate a pack`, once on entering the
    wait. Fields: `content_source`, `packs`, `core_version`.
  - `info` `content in effect: leaving the wait`, once. Fields: `zones` and the triggering
    `pack@version`.
  - A refused `OpenSession` logs at `debug` with `trace_id` and `reason=no_content_in_effect`.
- **Traces:** none new. The leaving `ReconcileContent` keeps its existing span.
- **Alerts:** none new. `AndaraServerUnavailable` fires while a fresh environment waits, which is true.
  Its runbook line is SRE's, in `AW-INF-021`.

## Test plan

- **Unit:**
  - the boot decision table: kafka with an empty log; kafka with a swap in the log and a refusing
    store; dir with no Zones (ACs 1, 3, 4);
  - the health endpoints in each state, including drain (AC-7);
  - the `OpenSession` refusal's `ErrorInfo`;
  - the CLI's flag, env and config precedence, and `config show`.
- **Integration**, against the local stack (Redpanda):
  - boot on an empty store, then publish, approve and activate, and assert the server becomes Ready
    with no restart (AC-2);
  - restart while waiting, and restart after the swap (AC-6);
  - a TLS dial through a forwarded port with a server-name mismatch, with and without the flag (AC-5).
- **Manual/operator:** `make content-seed ENV=dev` on a fresh `dev` (SRE's target, `AW-INF-021`) runs
  through this path end to end.

## Definition of done

CLAUDE.md §8, plus: `server/README.md` documents `/startedz` and the probe guidance, and
`admin/README.md` documents `server.tls_server_name`.

## Open questions

1. **For architecture (affects the contract): a discriminator for "this World has had Zones".**
   The ruling makes AC-3 exit and AC-6 wait. Both turn on whether a logged swap brought Zones, and
   nothing in the log records that (see the Interface contract). Found by Codex on #292. The
   options PM sees:
   - **(a) An additive `ContentSwap` field.** For example, `uint32 zone_count = 5`, the Zones in the
     World after the swap, written by the Loader. An older swap without it reads as 0, so an old log
     with a refusing store would wait rather than exit. That's the less harmful direction, but it
     weakens AC-3 for logs written before the field. This changes `log.proto`.
   - **(b) Any swap for a pack other than `andara.core`** counts as "had content". It's readable
     from `pack_id` alone, with no protocol change. But a Builder pack of Templates only would then
     count as content, and its server would exit rather than wait.
   - **(c) Zones in the last snapshot round.** Recovery restores from a round before it replays.
     That doesn't help a World whose only Zone-bearing swap came after its last round.

   PM leans to (a), since it's the only one that's exact for every new log. It's your call, and the
   story stays `draft` until it's made.
2. **For architecture: the dependency edge.** `AW-INF-021` must depend on this story. `make
   validate-stories` refuses a `ready` story that depends on a `draft`, so PM can't add the edge
   now. Add `AW-SRV-042` to `AW-INF-021`'s `depends_on` (and `AW-INF-021` to this story's `blocks`)
   in the same commit that moves this story to `ready`.

Apart from item 1, the contract is architecture's ruling of 2026-09-30, lifted as written, plus
AC-7, which makes the ruling's drain clause testable.
