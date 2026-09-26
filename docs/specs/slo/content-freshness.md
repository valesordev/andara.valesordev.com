<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# SLO — Content freshness

> **Status: proposed target, 2026-09-24.** Written by architecture for `AW-SRV-012`, whose alert
> it backs, because CLAUDE.md §7 has an SLO precede its alert. The SLI follows from the story and
> from implementation's handoff (`docs/feedback/AW-SRV-012-content-resolution-and-reload.md` §1).
> The target is a proposal and is `[NEEDS BRIAN]`. Nobody has measured a content swap on a running
> World yet.

## What this covers, and what it does not

A Builder publishes a version and it is activated by moving the pack's Active Pointer
(`AW-SRV-013`). The server follows the pointer, then resolves, validates and builds off-tick, and
swaps at a tick boundary (`AW-SRV-012`). **Freshness is the gap between the pointer moving and the
World serving that version.** It is a Builder-facing objective. A player sees stale content as
"the new area isn't open yet", never as a broken World: the previous version is retained and
served throughout.

The SLO measures **the system**, not the Builder. A version refused for something the Builder got
wrong never becomes served, and counting it as unfresh would make this objective a measure of
authoring quality. Implementation's failure taxonomy draws that line, and this document adopts it.

| Refused for a **Builder** reason (excluded) | Kept **pending** (counted) |
|---|---|
| `validation`, `fallback_missing`, `pack_mismatch`, `blob_too_large` | `store_unavailable`, `manifest_missing`, `blob_missing`, `blob_corrupt`, `format_version`, `core_version` |

The right-hand column is the platform failing to serve published content:
- the store not answering, or answering wrong;
- a binary too old for the content's `format_version`;
- a pack waiting on a core version that has not been activated.

The last is expected while an activation sequence is in flight. It counts because a pack held
for long is an operator problem: someone has to activate core.

## SLI

`andara_content_pending_seconds{pack}` is a gauge. For each followed pack, it holds the seconds
since the pack's Active Pointer moved to a version that is neither being served nor refused for a
Builder reason, and 0 when there is none. It starts when the resolver *reads* the pointer move,
so it includes `content.reload_debounce` (2 s). It returns to 0 when the `ContentSwap` for that
version applies, or when the version is refused for a Builder reason. A newer pointer move for
the same pack while one is pending keeps the older start: the pack has been unfresh since then.

**Good minute (per pack):** `max_over_time(andara_content_pending_seconds[1m]) ≤ 30`.

**Target (proposed):** 99.5 % of pack-minutes good over 28 days. `[NEEDS BRIAN]`

30 s is proposed for these reasons:
- 2 s of debounce;
- a resolve that fetches blobs from a local broker;
- validation and build off-tick;
- one tick to swap;
- headroom for a pack an order of magnitude past the sizing fixture.

Under about 5 s, this would measure the debounce, not the system (feedback §1).

**Error budget:** 0.5 % of 28 days, about 3.4 hours per pack.

**Policy when exhausted:**
- Changes to the resolver, the Loader or the content store wait until the budget recovers,
  unless they are the repair.
- Builders are told activations may be slow; the World keeps serving the retained version.

## Alert

`ContentLoadFailing`:
- **Expression:** `max by (namespace, pack) (andara_content_pending_seconds) > 300` for 5 m. A pointer move
  that has been unserved, for a system reason, for over five minutes. That is ten times the
  good-minute bound, so a slow but working swap does not ticket.
- **Severity:** `ticket`. No player is harmed and the previous version is served.
- **Runbook:** `docs/runbooks/content-load-failing.md`.

The story first sketched this alert as `increase(andara_content_load_failures_total[15m]) > 0`,
which would ticket an operator over a Builder's typo. That is a cause, and the wrong party's.
This SLO replaces it.

## Known gaps

- **A `dir` source exports no gauge.** `content.source=dir` has no Active Pointer, so there is no
  move to be pending on (the compose stack). The SLO applies to the `kafka` source.
  *(Corrected 2026-09-25 at AW-SRV-012's §8: the first bullet here said `Follow` was not yet
  wired. It is now.)*
- **Rejections for a Builder reason alert nobody here.** The Builder learns at publish
  (`AW-SRV-013` validates the same way). The exceptions are `zone_removed` and
  `spawn_room_removed`: they depend on what is in effect, not on the version alone, so publish
  can't catch them, and only activation can (`AW-SRV-013`'s activation check, routed to PM). A refusal at load that publish did not catch means the two
  gates disagree, which is a bug `AW-SRV-013`'s three-way equivalence test exists to prevent. It is
  still visible on `andara_content_load_failures_total{reason}` for anyone looking.
- **Per-process.** Under ADR-0001 there is one server. When the sharding story lands, every
  process that follows a pack reports its own gauge, and the rule's `max by (namespace, pack)` is the right
  aggregation.

## Revisit when

- The first measured swaps exist: set the target and the 30 s bound from them.
- `content.reload_debounce` changes, or per-Zone reload arrives (out of scope in `AW-SRV-012`).
