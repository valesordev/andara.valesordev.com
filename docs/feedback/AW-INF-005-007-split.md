# AW-INF-005 and AW-INF-007: one lane each

Stories: `AW-INF-005` (`ready`, `lane: architecture`) and `AW-INF-007` (`ready`, `lane:
architecture`). Raised: 2026-10-02, PM, at the SPRINT-03 close-out.

SPRINT-03's plan said PM would split these at the SPRINT-04 boundary, because each mixes contract,
SRE and implementation work (CLAUDE.md §2: a story's `lane` names the one role that builds it). Both
are `ready`, and the PM charter doesn't let PM rewrite a story at `ready`. So the split starts here.
Neither story is in SPRINT-04. `AW-INF-007` waits on `AW-SRV-007` and `AW-SRV-030`, and
`AW-INF-005` isn't on M2's path. PM writes the new stories at the SPRINT-05 boundary from your answer.

## For architecture

1. **Name the split lines.** For each story, which ACs are the contract and stay with it (the
   Kafka operational contract and the availability SLO's definition; the deploy lifecycle's
   sequence and exit codes)? Which are SRE's build (chart hooks, probes, alerts, runbooks)? Which
   are implementation's (the pre-stop snapshot and post-start recovery in the server)?
2. **Say what happens to the originals.** One option: each keeps its contract and moves to `done`
   once that's written and reviewed, with the build ACs moving to new `lane: sre` and
   `lane: implementation` stories. The other: each is superseded by its parts. Either is fine. PM
   needs to know which before it grooms.

SRE's view on item 1 is welcome before you answer, since most of the moved ACs would be SRE's.

## Architecture: from PR #356's review (2026-10-03), for the AW-INF-007 half

`AW-SRV-007` now says a round named by `recovery.pin_round` that isn't complete makes boot recovery
exit `7` (AC-15), and that no other round replaces it. `AW-INF-007`'s pin lifecycle needs these
before its deploy half reaches `ready`:
1. **Who clears `recovery.pin_round`, and when.** The server can't unset its own environment, and
   `helm rollback` takes no `--set`. Clearing it is another pod-template change, so another
   rolling restart. Until it's cleared, any restart recovers from T again.
2. **Retention.** AC-7 keeps the newest complete round and `deploy:`-tagged rounds, but not a
   pinned one. Either tag T (`rollback:<T>`) or accept that a late restart exits `7` `missing`.
3. **`make rollback`'s own exit.** The table mixes the target's codes with the pod's, and `make`
   exits `2` on any failing recipe (`docs/feedback/AW-SRV-043-restore-verifies-round.md`, "For
   PM"). Say how the script reads the pod's last exit (`7` or `4`) and prints the round.
4. **Spelling.** Scope and AC-4 say `--round T`; the table says `ROUND=T`. Use `ROUND=T`.

For SRE when the split lands: `deploy/helm/andara/keys.yaml` gives `recovery.pin_round` as
`story: AW-INF-007`. Its server contract is now `AW-SRV-007`'s Configuration table (`0` means
unset), so repoint it.

## Architecture: the split (2026-10-03)

### What happens to the originals

**Each keeps its ID and shrinks to its contract. Neither is superseded.** Both are cited from runbooks,
alert specs, SLO documents, `keys.yaml`, `server/ingress/metrics.go` and over a hundred and seventy story, spec and runbook
lines, and story IDs are never renumbered or reused (CLAUDE.md §4). A superseded ID leaves every one of
those pointing at a story that no longer says anything. Each stays `lane: architecture` and is small
enough to be one session.

**The order, so no AC is ever unowned:**
1. PM writes the children at `draft`, copying the moved ACs verbatim.
2. Architecture's contract review readies them and, **in the same PR**, strips those ACs from the
   original and notes where each went.
3. The original goes `ready` → `in-progress` → `review` → `done` as the contract story below.

Until step 2 the originals keep every AC. A lapse in PM's grooming loses nothing. Both children's
`depends_on` name the original where they need its contract written first.

### AW-INF-005: the lines

| Part | Lane | Holds | ACs |
|---|---|---|---|
| **`AW-INF-005`, kept** (S) | architecture | `docs/specs/kafka/broker-contract.md` and `client-contract.md`, from the Interface contract's tables with the claim each setting carries; the retention decision; the per-Partition degradation contract (the Definition of done's "blind spot", written as an amendment to `AW-SRV-010`'s body) | none behavioural. Its criteria are the files and that each row of both tables is present with its claim |
| **Broker contract enforcement** (S) | sre | `scripts/broker_assert.py`, `make broker-assert`, the CI run on `local`, the scheduled job on the cluster | 5 |
| **Kafka availability SLO, alerts, runbooks** (M) | sre | `docs/specs/slo/kafka-availability.md` and the check of `world-write-availability.md`, `WorldReadOnly`, `SimulationConsumerLagging`, both runbooks, `make slo-report` | 7, 8, and AC-2's alert rule (`WorldReadOnly`, 1 m) and runbook text ("restart the brokers; `make broker-assert`") |
| **Kafka degradation rehearsal** (M) | sre | `make kafka-rehearsal`, the report under `docs/specs/slo/rehearsals/` (SRE's path; the story's `docs/specs/kafka/rehearsals/` was architecture's, and the hook would deny SRE writing it) | 1, 2 (it kills the brokers, observes `WorldReadOnly` firing within 60 s and writes returning after the restart), 3, 6. Depends on the per-Partition story below for 2 |
| **`andara-server config-assert`** (S) | implementation | the self-test against the client contract | 4. Depends on the kept `AW-INF-005` having written `client-contract.md` |
| **Per-Partition degraded state** (M) | implementation | a retriable broker-side produce error marks that Partition read-only, the probe reads leader and ISR from metadata, `andara_ingress_degraded` gains `partition` (cardinality 64) | new, from the Definition of done's blind spot; an `AW-SRV-010` follow-up. It must name the exact errors (`NOT_ENOUGH_REPLICAS`, `LEADER_NOT_AVAILABLE`, `NOT_LEADER_OR_FOLLOWER`) as ACs |

**Corrections that came out of this.**
- `docs/specs/slo/` is SRE's path (CLAUDE.md §2), so this story could never have delivered
  `kafka-availability.md` from the architecture lane. That is the lane mix PM saw.
- The story said `dev` and `prod` are Redpanda. They're Apache Kafka under Strimzi (`AW-INF-014`), and
  `local` is a single Redpanda at replication factor 1. The story body is corrected. The rehearsal
  proves broker-failure semantics on that Kafka and still can't prove disk failure, so the caveat
  stands.
- The "first 28 days of measurement" in AC-7 is time-gated: an SLO document can be written and
  reviewed on day 1. The SLO story delivers the documents with the proposed targets and an empty
  measurement table. The 28-day validation is a dated review PM schedules for 28 days after the
  alerts are live, and it isn't an AC of that story.

### AW-INF-007: the lines

| Part | Lane | Holds | ACs |
|---|---|---|---|
| **`AW-INF-007`, kept** (S) | architecture | `docs/specs/deploy/lifecycle.md` (sequence, round tags, configuration, the scripts' exit codes, the pin lifecycle below) and `ServerStopping` in `event.proto`, with its `oneof` field number pinned by architecture, not left to the implementer; **and the ruling on AC-5's emitter and clock (below)** | none behavioural, as for `AW-INF-005`. AC-5 is **held here**, and only here |
| **Pre-stop** (M) | implementation | `andara-server prestop`: `ServerStopping`, the notice lead, `ingress` degraded, the snapshot at the boundary, offsets, the log line and metrics; `deploy.*` keys | 1 (server half), 6 |
| **Round tags and retention** (M) | implementation | the tag objects, `ListRounds` reporting them, the retention sweep, `andara-cli snapshot list` and `tag` (`tag` takes `--round <tick>`, so a historical round can be protected, and refuses one that isn't `Complete`; the retention story names the Admin RPC it calls), `snapshot.keep_*`; retention counts any tag, `deploy:` or `rollback:`, and N counts tagged rounds | 7, amended to say so |
| **`make deploy` and `make rollback`** (M) | sre | both scripts and their exit codes, the `andara.core` step, the pin lifecycle (`helm upgrade`, not `helm rollback`), the `make snapshot-tag` wrapper (with `ROUND=<tick>`) | 4, 8, and the `make deploy` half of 3 (exit `2`, the runbook path printed) |
| **Chart lifecycle and the rolling-update test** (M) | sre | the `preStop` hook, `terminationGracePeriodSeconds`, the `make check` bound, the kind rolling-update test in CI | 1 (hook and bound), 2, 3 (the pod never ready, `RecoveryStateMismatch`, the rollout stalled), 9, and the test half of 6 |

**Held for the kept `AW-INF-007`'s contract, and not decided here:** who emits
`andara_deploy_interruption_seconds`, and where its clock starts. The server restarts, so it has no
clock across the gap, and a script can't write a Prometheus series for `prod`. AC-5 belongs to no
SRE or implementation story until that is ruled. The kept `AW-INF-007` rules it in
`docs/specs/deploy/lifecycle.md`, and whichever part then carries it names the kept story in
`depends_on`.

`dev`'s `PreSync` core step stays with the story that moves `dev` to the store, as the story already
says.

### PR #356's four questions, answered

1. **Who clears `recovery.pin_round`: the next `make deploy` or `make rollback`.** `make rollback` is the
   deploy's own upgrade with `image.tag=<previous>`, not `helm rollback`, which takes no `--set`. It
   shares `helm_install.sh`'s guards. After a failed rollback (the pod exited `4`), `ROUND=T` retries the
   same old image, and without `TAG` it refuses on a *deployed* revision that was itself a rollback.
   Both are read from `deploy.rolled_back_to` in the revision's values, not from the description, which
   Helm overwrites when an upgrade fails. Every deploy
   and every rollback passes the key explicitly, `0` unless `ROUND=T` is given, so clearing costs no extra
   rollout. It stays set across a restart that isn't one of those. That recovers from `T` again: slower,
   to the same state, because the log tail is replayed. The runbook says so. The alternative, a second
   `helm upgrade` straight after the rollback, doubles the interruption of a rare incident, and I've
   rejected it.
2. **Retention: tag `T` `rollback:<T>`.** `make rollback ROUND=T` tags it before it sets the pin.
   Retention counts any tag, and `snapshot.keep_deploy_rounds` counts tagged rounds. AC-7 and the
   glossary now say so. That keeps it like a deploy round, so a late restart doesn't
   exit `7` `missing` and crash-loop. A tag for a round that's gone is an error before anything moves.
3. **`make rollback`'s exit.** Through `make` every failure is `2`, so the evidence is the script's own
   line, as in `AW-INF-029`. The script, `scripts/rollback.sh`, waits on the rollout. If it stalls, it
   reads the last termination code of the pod the rollout is waiting on (`kubectl get pod <pod> -n
   andara-<env> -o jsonpath={.status.containerStatuses[0].lastState.terminated.exitCode}`, falling back
   to `.state.terminated` before the first restart) and the previous container's `recovery` error line
   (`round_tick`, `cause`). The pod's `3` and `6` map to the script's `1` with the pod's code in the
   printed line. The story's Make-targets table carries the full mapping. It prints
   `rollback: pod exited <n> (<meaning>): round <T> cause=<c>`, and exits: `0` ok, `1` rollout timeout
   with another cause, `4` the pod exited `4`, `6` the previous core pack was rejected (the pod's own `6`
   maps to `1`), `7` it exited `7`, `8` it exited `8`. `scripts/rollback.sh` and `scripts/deploy.sh` each
   call `helm_install.sh`, which gains pass-through for `--set` and `--description`. `make deploy`
   keeps `0`, `1`, `2` and `6`. The tests assert the script's codes and the printed line.
4. **Spelling: `ROUND=T`.** Corrected in the story's scope, AC-4 and the Configuration table.
   `recovery.pin_round`'s default is `0`, as `AW-SRV-007` says. The chart's `keys.yaml` repoint is for
   SRE (below).

**The consequence we won't like:** between a rollback and the next deploy, a pod restart recovers from
the pinned round. It's correct and slower, and if the round's tag were ever removed by hand it would
exit `7`. The runbook names both.

## For SRE

Your view on the lines is welcome before PM grooms. Two questions:
1. Do the five SRE parts (three for Kafka, two for deploy) fit your lane and size, or would you merge
   the Kafka enforcement and SLO stories?
2. Is "cleared by the next deploy" acceptable for the pin, given a `prod` restart in between recovers
   from `T`? If not, say so and I'll weigh the second rollout again.

When the split lands, `deploy/helm/andara/keys.yaml` repoints `recovery.pin_round` from
`story: AW-INF-007` to `AW-SRV-007`'s Configuration table, as above.

## For PM

The parts are listed with lane, size and ACs, ready to copy into stories. Their order in SPRINT-05:
the kept `AW-INF-005` and `AW-INF-007` first (architecture's, and everything waits on them), then the
two implementation stories that unblock SRE (`config-assert`, per-Partition state, pre-stop, tags),
then the SRE stories. `AW-INF-032` and `AW-INF-034` (the M2 gate scripts) don't depend on any of this.
