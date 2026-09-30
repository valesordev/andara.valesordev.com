# AW-INF-025: operating the state projector

Story: `AW-INF-025` (`draft`). Issue #80.

## For architecture: SRE observability review, 2026-09-28

**Amended.** The draft said "none new". But enabling the projector on `dev` is the trigger
`projection-freshness.md` deferred its absence rule to ("deferred until the chart enables the
projector anywhere"). So this story now carries:
- **`StateProjectorDown`**, a `ticket` with `slo: projection-freshness`. It keys on the
  Deployment's desired replicas and on no Ready projector being scraped, per namespace. So a
  planned `projector-stop` or `projector-rebuild` (scaled to 0) never fires it, and a crash loop
  does. It uses no `absent()` over every namespace.
- **Its runbook**, `docs/runbooks/state-projector-down.md`. The SLO's *Known gaps* bullet is
  replaced, and there's a `promtool` test for fires, silent-at-0, and silent-in-compose.
- **Target output**, with a final line naming the outcome and elapsed time per target.
- **The rebuild Job is unscraped**, deliberately. A second target under the same job would read as
  a second writer, so the rebuild's duration comes from its log line, and
  `andara_state_rebuild_duration_seconds` isn't used.
- **AC-4's evidence** is the `state projector started` line's `round_tick`, not the lag gauge. The
  gauge reads 0 before the first verified batch (SLO *Known gaps*).
- **`ProjectionStale` during a long rebuild** is left to fire. The SLO counts rebuilds against the
  budget on purpose. `projection-stale.md` gets one line telling the operator to check the rebuild
  first.

Nothing here depends on how you answer the snapshot-source question. Whichever source you choose,
AC-4 is observed the same way.

Alert evaluation in Grafana Cloud waits on `AW-INF-009`, which isn't in SPRINT-03. Until it lands,
the §8 check covers the rule under `promtool` and the series on the real backend.

## For SRE: architecture's contract review, 2026-09-28

The snapshot source is `s3`, served in-cluster by versitygw (`make objectstore-install`). The
reasoning is in the story's *Contract review*. Two things for you as you build it:
- **Before `dev` switches from `fs`,** check that the earliest offset on every
  `andara.events.v1` partition is still 0, and record it in the PR. The first `s3` boot finds no
  round and replays from the log's start. If retention has already trimmed it, the switch waits
  for `AW-INF-021`'s `world-reset`.
- **`make world-reset` (`AW-INF-021`) clears the bucket, not the PVC,** once this story lands.
  It also stops the projector and deletes its group with the recreated `andara.state.v1`. That's
  written into this story's Interface contract, so `AW-INF-021` can cite it.

## For PM: #143 is a risk to this sprint and to M2 (architecture, 2026-09-29)

#143 (a projector bootstrapped from a snapshot round diverges at round tick + 1) isn't in
SPRINT-03. It has three consequences:
- **`AW-INF-025` can't close on `dev` until #143 is fixed.** AC-4 is amended so that it can't pass
  while #143 is open, and AC-2 fails the same way on a bootstrapped rebuild. The rest of the story
  (ACs 1, 3, 5–8) can proceed, and it may reach `review` owing AC-2 and AC-4.
- **`AW-INF-008` does *not* wait on it.** Its AC-2 is observed on the projector's first Ready.
- **M2 is at risk.** The server's recovery restores the same rounds (`AW-SRV-006`). If a round
  doesn't reproduce hashed state, M2's "matching State Hash" gate and `AW-SRV-007` (SPRINT-04)
  fail the same way.

Architecture's recommendation: take #143 into implementation's SPRINT-03 list beside #128, which
is also `AW-SRV-006` recovery code, ahead of `AW-SRV-038`. The investigation starts where #143 left
off: the fixture World's round test passes, so a regression test needs state the fixture lacks.
That could be a bound, unbound, or linkdead Character across the round. Scope and ordering are
PM's call.

## For SRE: AW-INF-025 AC-4 amended (architecture, 2026-09-29)

AC-4 now also polls the lag to budget with `andara_state_digest_mismatches_total` at 0 and no
`diverged` line. Don't start the projector from zero to meet AC-2 or AC-4. Everything else can be
built now. If #143 is still open when you're done, take the story to `review` owing AC-2 and AC-4.
Separately, record `AW-INF-008` AC-2 (`up{job="andara-projector-state"} == 1`) on the projector's
first Ready on `dev`, whatever happens after.

## For SRE: AC-5 amended for ADR-0011 (architecture, 2026-09-29)

ADR-0011 (broker authentication, accepted) gives the projector its own Kafka principal. AC-5's
`kafkaCreds` is now `projectors.state.kafkaCreds.secretName`, the projector's own. It must never
fall back to the server's `secrets.kafkaCreds`. An empty value renders no mount, and that's the
expected state on `dev` until ADR-0011's SRE story turns SASL on. The story's *Contract amendment*
has the reasoning.


## For architecture: `--rebuild` never completes (SRE, 2026-09-29, while building)

The contract says `projector-rebuild` runs `andara-projector state --rebuild` "to completion as a
one-shot Job" and waits "for the Job to complete". AC-2 says "the Job completes". The binary
doesn't complete. `server/projector/run.go` logs `state projector caught up` once, then keeps
projecting until it's signalled, as the Deployment does. A Job running it never reaches
`Complete`.

**What the target does instead:** the rebuild is done when the Job's pod logs
`state projector caught up`, bounded by `PROJECTOR_REBUILD_TIMEOUT`. The target reads the tick
from that line, deletes the Job (SIGTERM, a clean stop, exit 0, with its checkpoint committed),
then runs `projector-start`. The Deployment resumes from the Job's checkpoint, with no second
`--rebuild`. The final line is still `projector-rebuild: rebuilt to tick <t> in <n>s`.
`backoffLimit: 0`, `ttlSecondsAfterFinished` and `activeDeadlineSeconds` are set as contracted,
and a Job pod that exits on its own is reported as a failure (2/3/4).

Two consequences for the contract:
- **AC-2's "the Job completes"** reads as "the Job's pod logs `caught up`, and the target deletes
  it". Nothing waits on a `Complete` condition.
- **"Deletes a finished Job before it creates its own"** still holds, but in practice the Job the
  target made is already gone. A finished Job only remains after a failure, which is kept, with
  its TTL, for the evidence.

The alternative is for `--rebuild` to exit 0 once it's caught up. That's AW-SRV-019's code, so
implementation's. It would make the Job's `Complete` literal, and the target could wait on that
condition instead. I don't think it's needed. Please rule on the reading above, or send the
exit-on-caught-up change to implementation.

## For SRE: `--rebuild` ruled, #143 live on `dev` (architecture, 2026-09-30)

**`--rebuild` never completing: your reading is accepted** and recorded as a contract amendment in
the story's §8 review. The rebuild is done at `state projector caught up`, then the target deletes
the Job and `projector-start` resumes from its checkpoint. One condition: AC-2's observation shows
the Deployment's first start after the rebuild resuming from the Job's checkpoint, not
bootstrapping from a round.

**#143 is live on `dev`.** `andara-projector-state` has crash-looped since the merge: 136 restarts
by 17:57Z, each exiting 2 on the divergence recorded at tick 132838. Whether to leave it (and it
answers AW-INF-008 AC-2 as amended, see that story) or `make projector-stop ENV=dev` until #143's
fix is yours. Either way, record it here. Architecture changed nothing on the cluster.

Runnable now, and owed in the §8 record: ACs 1 and 3, the box half of AC-8, and AC-6's round in the
bucket. AC-3's ordering is observable even though the rebuild then diverges.

## For PM: #143 is no longer a risk; it's breaking `dev` (architecture, 2026-09-30)

Since #171 enabled the projector on `dev`, it has crash-looped on #143. `AW-INF-025` ACs 2 and 4
can't pass until it's fixed, and neither can `AW-SRV-019` AC-6 or, next sprint, M2's "matching State
Hash". #143 isn't in SPRINT-03's implementation list. Architecture asks for it there, ahead of
`AW-SRV-038`. The fix belongs in the round or its restore, with the regression test named in
`AW-SRV-019`'s 2026-09-29 §8 pass.
