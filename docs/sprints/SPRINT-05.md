# SPRINT-05 — The M2 gate on `dev`
Status: active
Dates: 2026-10-05 →

## Demo goal
**`SIGKILL` `dev`'s server pod; the World comes back as it was.** On `dev`, an operator runs
`make env-recover ENV=dev CONFIRM=andara-dev` (`AW-INF-034`). Two players are in the World, and one has
moved away from the spawn Room. The target waits for a complete Snapshot Round, kills the server from the
node (a kill from inside the container is ignored by PID 1), and asserts that:
- the pod is Ready again within 120 s of the kill, through the startup probe's full budget and not a
  kubelet kill mid-replay (`AW-INF-011`);
- recovery used that snapshot plus the log tail, and the State Hash matched;
- both players' `play` clients reconnect through the edge, and their Characters rebind where they stood.
  Neither despawns.

It's `docs/roadmap.md`'s **M2 gate** again, on the cluster. SPRINT-04 proved it on the local stack with
`make stack-recover` (`SPRINT-04-demo.md`), and `dev` is the only cluster environment running. It's also
Phase 1 exit criterion 3 at M2's RTO of 120 s.

Why this goal (Brian, 2026-10-03): the compose stack never exercises the kubelet restart, the startup
probe, the edge returning players only once the pod is Ready, or `RecoveryStateMismatch` firing from a
pod that never becomes Ready (`AW-INF-009`'s cluster clause). None is proven on a cluster today.

**What it's not:**
- Phase 1's 60 s RTO.
- The deploy lifecycle: a planned SIGTERM with a pre-stop snapshot, `make deploy` and `make rollback`
  (`AW-INF-007`). A SIGKILL is the crash path.
- The Redis and Postgres projections (`AW-SRV-017`, `AW-SRV-018`).
- ADR-0011's broker authentication chain (`AW-SRV-044`, `AW-INF-030`, `AW-INF-031`). Brian OK'd holding it
  (2026-10-03).
- M3 or M4 content. M4's loop is combat; its mechanics are Brian's.

**Before the demo can run (Brian, 2026-10-05):** `AW-SRV-028` changed what a log may hold, so a log written
before it that contains a cross-Zone move won't replay (recovery exits 6). `dev` is reset with the deploy
that carries it: `make world-reset ENV=dev CONFIRM=andara-dev`, which destroys `dev`'s Characters and
Accounts. Brian confirmed this reset. It is SRE's item 1.

## Contract review (SRE observability, then architecture — first)
SRE reviews each draft's Observability requirements (§7) first. Architecture's contract review then moves
each draft to `ready` or `blocked`.

- **AW-INF-034** — `make env-recover`, the M2 gate on `dev` (SRE). **Review it first.** It's the demo.
  Its dependencies, `AW-INF-011` and `AW-INF-009`, are ahead of it in this sprint.
- **AW-SRV-041** — a bystander reads *has arrived* when there's no Exit back.
- **AW-SRV-045** — an activation's trace links to the content load that applies it.
- **AW-CLI-010** — the compiler reports unportable names offline.
- **AW-CLI-011** — a TLS verification failure names the CA in use and where it came from.
- **AW-SRV-040** — every Admin account write audits outside the Account write lock.
- **AW-INF-037** — `make server-stop` and `make server-start` (SRE; #362, a §9 defect in
  `docs/runbooks/server-crashlooping.md`). It's off the demo's path: `AW-INF-034` kills from the node and
  never scales the StatefulSet. Its Open question 1 (what a manual sync during a stop does) is SRE's to
  rule before it's `ready`.
- **AW-SRV-039** — Admin acting-as. The sprint's slack, and last.

## Architecture backlog (pickup order)
After the contract review above:

1. **#363**: rule how `dev` boots once `andara.events.v1`'s retention has expired its first segment (a
   whole-log recovery with nothing to read). It's `AW-INF-005`'s retention decision and
   `AW-SRV-007`'s `recovery.require_snapshot`. It can break the demo on `dev`: the issue was written before
   `AW-SRV-007` shipped, and its trigger is about 30 days after the last `dev` reset. Rule whether it still
   holds. If it does, implementation takes the code half.
2. **`docs/feedback/AW-SRV-049-roster-observation.md`, `AW-SRV-048-transit-orphan-design.md`:** rule the
   questions in them (the Character-level settlement signal; the wall-clock bound against #114; Roster
   protocol or sim-level guard). The stories are held, below.
3. **`docs/feedback/AW-SRV-051-failure-injection-surface.md`:** its rulings are recorded. Pin the exact
   `sim.handoff_fault` shape at the story's contract review, once PM has folded them in.
4. **Re-size `AW-SRV-027`:** it grew with `AW-SRV-028` (it now owns `HandoffRejected{zone_faulted}`), and is
   still `ready` at S. Write the re-size in `docs/feedback/` before anyone picks it up.
5. **AW-INF-005** — Kafka operational contract and the retention decision — ready, M. It shrinks to its
   contract once PM has written its children and architecture has stripped the moved ACs
   (`docs/feedback/AW-INF-005-007-split.md`). **Skip items 5 and 6 until the PM PR that writes the nine
   children has merged.** Nothing is lost by waiting: the originals keep every AC until the strip.
6. **AW-INF-007** — the deploy lifecycle contract, including who emits
   `andara_deploy_interruption_seconds` (AC-5) — ready, M, the same split and the same wait. Its
   `AW-SRV-030` dependency (held for SPRINT-06) doesn't block this contract work, which writes a spec and
   one `event.proto` message.
7. The §8 review of each story as it reaches `review`.

## SRE backlog (pickup order)
Before this list: the observability review of every draft above, then the instrumentation check of each
story at `review`.

1. **The `dev` reset**, with the deploy that carries `AW-SRV-028`: `make world-reset ENV=dev
   CONFIRM=andara-dev` (Brian confirmed it, 2026-10-05). Do it before the probe and alert work below, so
   `dev` is on a log that replays.
2. **AW-INF-011** — workload topology: probes and sizing against the real tick loop and recovery — ready, S.
   Depends on `AW-SRV-007` (`done`).
3. **AW-INF-009** — alert rule delivery, with the `RecoveryStateMismatch` cluster clause architecture
   amended on 2026-10-02 — ready, S. Depends on `AW-SRV-007` (`done`).
4. **AW-INF-034** — `make env-recover` — after its contract review, and after items 2 and 3. It waits on
   both and it's the demo.
5. **AW-INF-037** — `make server-stop` and `make server-start` (#362) — after its contract review. Not on the
   demo's path; it closes a runbook §9 defect, and goes after the demo's items.

## Implementation backlog (pickup order)
1. **AW-SRV-032** — character deletion, name retention, purge, and switching bodies — ready, M. Depends on
   `AW-SRV-014` and `AW-SRV-007` (both `done`). It must never reuse an Entity ID
   (`AW-SRV-028`'s placed marks are kept for good).
2. **#363**: the code half, after architecture's item 1.
3. **AW-SRV-041** — after its contract review. S.
4. **AW-SRV-045** — after its contract review. S.
5. **AW-CLI-010** — after its contract review. S.
6. **AW-CLI-011** — after its contract review. S.
7. **AW-SRV-040** — after its contract review. S.
8. **AW-SRV-039** — slack, after its contract review. M.

**Risk:** the demo's path is SRE's items 1–4 and architecture's item 1, with implementation's item 2 behind
it if #363 still holds. Implementation's list is a builder-polish queue and doesn't gate the demo. If SRE
runs short, item 5 (`AW-INF-037`) carries over; items 1–4 don't. If implementation runs short, item 8
carries over first, then item 7, then item 6.

## Carryover from SPRINT-04
None. Every SPRINT-04 story is `done` (see its close-out).

## Groomed, not in this sprint
These drafts are for later sprints, in prose because `project-mirror` counts a list item that opens with a
story ID in a Carryover or backlog section as sprint work.

Held pending architecture's rulings above: AW-SRV-048 (the in-transit orphan mark), AW-SRV-049 (the roster
and Gateway hold for a handoff), AW-SRV-050 (the sizing fixture with marks), AW-SRV-051 (the
failure-injection mode) and AW-INF-035 and AW-INF-036 (the `make` targets behind 050 and 051). AW-INF-036 is
the carrier `AW-SRV-028`'s §8 cites for the live observation of its handoff series. AW-SRV-027 (per-Zone
quarantine) is `ready` and waits on its re-size.

Held for SPRINT-06 (Brian, 2026-10-03): the ADR-0011 chain (AW-SRV-044, AW-INF-030, AW-INF-031), AW-SRV-017,
AW-SRV-018 and AW-SRV-030. The Items stories (AW-CLI-012, AW-CLI-013, AW-SRV-047) are held unless content
needs them sooner, and no `content-need` issue asks for them; AW-SRV-047 also waits on AW-SRV-050, and
AW-CLI-012 (architecture) can enter the contract review early if content asks.

**Owed by PM, not yet written:** the nine children of the AW-INF-005 and AW-INF-007 split
(`docs/feedback/AW-INF-005-007-split.md`), copied verbatim from the originals' ACs. Architecture expected them
at this boundary. They aren't in this PR because the demo doesn't need them (`AW-INF-032` and `AW-INF-034`
don't depend on the split), and the originals keep every AC until architecture's strip, so a delay loses
nothing. They land in the next PM PR, which re-plans this sprint as SPRINT-03 did,
so architecture's items 5 and 6 can strip the originals.

## Close-out
(filled in by the next PM session)
