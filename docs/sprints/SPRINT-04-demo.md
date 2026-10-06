# SPRINT-04 demo — `kill -9` the server; the World comes back as it was

**Goal (from `SPRINT-04.md`):** on a fresh local stack, an operator runs `make stack-recover`. Two
players are in the World, and one has moved away from the spawn Room. The target waits for a complete
Snapshot Round, then sends `SIGKILL` to `andara-server` and starts it again. It asserts that:
- the server is ready within 120 s of the kill;
- recovery used that snapshot plus the log tail, and the State Hash matched
  (`andara_recovery_state_hash_match 1`), with no acknowledged Command lost;
- both players' `play` clients reconnect, and their Characters rebind where they stood. Neither
  despawns.

This is `docs/roadmap.md`'s **M2 gate**, "`kill -9` the server; the World returns within 120 s with a
matching State Hash, and every linkdead Character rebinds rather than despawning". SPRINT-02
delivered its linkdead half (`make stack-linkdead`). It's also Phase 1 exit criterion 3, at M2's RTO
of 120 s. The `stack` workflow runs it on every merge, after `stack-linkdead`.

**Result: passed, on CI's evidence, with no §9 defect.** Every step below is a `make` target.
- **What PM ran:** nothing locally. The only stack on this host is the SRE clone's, on ports 8080 and
  8443, and a clean checkout's `make up` would collide with it. Brian chose to accept CI's run in
  place of a PM re-run (2026-10-05, his answer "c"), as he did for SPRINT-03's `dev` steps.
- **The run quoted below:** the `stack` workflow on `main`, run 37392791066, at `e007216` (the merge of
  #442, the last code merge before this close-out; every merge after it is docs). The job's steps 5
  to 22 are the steps below, in order. The run is
  https://github.com/valesordev/andara.valesordev.com/actions/runs/37392791066.
- A reader can re-run it on any clean clone of `origin/main` with the steps below.

## Preconditions

- Docker with Compose, and `make`. No cluster, and no broker beyond what `make up` starts.
- Nothing else on ports 8080 and 8443. If another clone's stack is running, `make down` it first.
- A clean checkout of `origin/main`. No Go toolchain beyond what `make bootstrap` verifies.

## Steps

1. **Bootstrap the toolchain.** `make bootstrap`.
   - **Expect:** the toolchain and hooks verified, exit 0. It is idempotent.
   - **Run (CI):** step 5, `bootstrap`, success.

2. **Bring up the local stack.** `make up`.
   - **Expect:** the server, its datastores and the observability stack healthy. The server runs this
     commit.
   - **Run (CI):** step 6, `up`, success, and step 7, "the server runs this commit", success.

3. **Run the M1 and linkdead gates the M2 gate builds on.** `make stack-play`, then
   `make stack-linkdead`.
   - **Expect:** both pass. `stack-linkdead` drops a player's stream, reconnects and shows a bystander
     both.
   - **Run (CI):** steps 20 and 21, success.

4. **Kill the server and recover.** `make stack-recover`.
   - **Expect:** two players spawn and move, the target waits for a complete round past the last
     one, then `SIGKILL`s the server and starts it. The server is ready inside the 120 s RTO from a
     snapshot, `andara_recovery_state_hash_match` is 1, and both players rebind and read the Town
     Hall. The last line reads `M2 gate — killed, recovered from a snapshot, hash matched, both rebound — passes`.
   - **Run (CI, step 22):**
     ```
     stack-recover: newest complete round before the run: 3002
     stack-recover: waiting up to 90s for a complete round past 3002 ...
     stack-recover: round R = 4239
     stack-recover: Recoveredtwqyzmzx walks north to the Town Hall, after the round ...
     stack-recover: SIGKILL to andara-server, then start ...
     stack-recover: ready 1.3s after the kill (RTO 120s), round 4239, hash match
     stack-recover: kill-to-ready 1.3s; process-start-to-ready (andara_recovery_duration_seconds{phase="total"}) 0.055668292s; replayed ticks 15; restore ok 1; recovery.run trace eaee839f555d00644da81d124b373249
     stack-recover: RecoveryStateMismatch is absent from ALERTS through the recovery (Prometheus scraped the 1)
     stack-recover: both rebound; both read the Town Hall (A's is the tail move)
     stack-recover: both quit ...
     stack-recover: M2 gate — killed, recovered from a snapshot, hash matched, both rebound — passes
     ```
     Ready 1.3 s after the kill against the 120 s RTO. 15 ticks replayed past round 4239 is the log
     tail, and Player A ending in the Town Hall proves the tail move was replayed.

5. **See the refused-recovery alert fire.** `make stack-recover-mismatch`.
   - **Expect:** the server restarts on another `sim.seed`, refuses its round (exit 6), and
     `RecoveryStateMismatch` fires in Prometheus. Restarting on the server's own seed recovers.
   - **Run (CI, step 23):** `RecoveryStateMismatch — fired on a refused recovery, outlived the process, cleared by the right seed — passes`.

6. **Take the stack down.** `make down`.
   - **Expect:** nothing left behind.
   - **Run (CI):** step 26, `down leaves nothing behind`, success.

## What this demo is not
- A kill on `dev`. A pod restart on Kubernetes is the deploy lifecycle, and the M2 gate on `dev`
  (`make env-recover`, `AW-INF-034`) is SPRINT-05's demo.
- Phase 1's 60 s RTO. The Makefile's `STACK_RECOVER_RTO` defaults to 120.
- The Redis and Postgres projections, or ADR-0011's broker authentication chain.
