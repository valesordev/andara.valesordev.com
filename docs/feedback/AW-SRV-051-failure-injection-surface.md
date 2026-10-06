# AW-SRV-051: the failure-injection surface

Raised by PM, 2026-10-05, writing the carrier story architecture asked for at `AW-SRV-028`'s §8 review
(`docs/feedback/AW-SRV-028-handoff-contract.md`, "For PM: the failure-injection story"). Each heading names
the role that should answer.

## For architecture

1. **Where does the mode live?** The story proposes a server config key set in the local stack's compose file
   (`[ASSUMPTION]`, `sim.handoff_fault`). Your request said "a flag for `sim repl` or the stack". `sim repl`
   runs in-process against a fake log, so it has no running server and no `/metrics`: a flag there can't carry
   the live observation. An admin RPC would be a back door into a running process (CLAUDE.md §10) and needs
   an ADR-level answer.
2. **How is it kept out of production?** The story says the mode is refused outside the local stack. Say how:
   refuse the key unless the environment is the local stack, a build tag, or both.
3. **The `error` line.** Dropping or delaying an `Arrive` can't produce `AW-SRV-028` AC-9's `error` log
   (an `Arrive` rejected `entity_present` or `invalid_arrival`; the `error` level is in its §7 Logs). Do you want a third mode (a malformed `Arrive`) in this story, or is that line left
   to tests, with the §8 record saying so?
4. **Which story is the carrier you cite?** Because the Makefile is SRE's, the live observation is a target,
   so the carrier is split: `AW-SRV-051` (the mode, `lane: implementation`) and `AW-INF-036` (`make
   stack-handoff-fault`, `lane: sre`, which depends on 051). `AW-INF-036`'s Definition of done names the
   deferred series and lines and is the story that observes them. Cite `AW-INF-036` as the carrier when you
   move `AW-SRV-028` to `done`, or tell PM if you want 051 to be it.

5. **Sequencing on #409.** `AW-INF-036`'s AC-3 asserts the amended summary `warn`, which #409 builds. The
   story says it isn't pickable until #409 merges. Tell PM if you'd rather AC-3 take today's per-tick shape.

Both stories stay `draft` and out of every sprint until you rule on 1 to 3.

## Architecture: rulings, 2026-10-05

PM asked for these in the repository before grooming the stories at the SPRINT-05 boundary. They answer the stories'
Open questions: **1a** is ruling 1 (where the mode lives), **1b** is ruling 2 (refused outside the local stack), and
**1c** is ruling 3 (the `error` line, which `AW-INF-036`'s Definition of done cites). PM folds them into both
stories, and drops the stale "held at `review`" and "isn't pickable until #409 merges" text, at that boundary.

1. **Where the mode lives:** in the server, as a config key set in the local compose file: `sim.handoff_fault`
   (`off` | `drop` | `delay`, with the delay length and the count or fraction of `Arrive`s affected; the exact shape
   is pinned at the contract review). It acts where the `Arrive` is produced, not inside the sim, so the sim stays
   deterministic and no hash changes. A `sim repl` flag can't carry the observation (no running server, no
   `/metrics`), and an admin RPC would be a back door (CLAUDE.md §10), so neither.
2. **Kept out of production:** a startup refusal, not a build tag (a second binary would differ from the tested
   image). The server exits with the existing configuration-error code (`1`, as `AW-SRV-051`'s draft already says) unless `telemetry.environment` is `local` **and was set explicitly**
   (`ANDARA_ENV=local`; the default is also `local`, so an unset value must not be enough). The key is not exposed
   through the Helm chart; `AW-SRV-051` names how `values-schema-check` treats a key the chart must not carry, and
   `AW-INF-036` makes it so. When on, a `warn` names the mode at startup. ACs: refused with `ANDARA_ENV` unset, `dev`
   and `prod`; allowed when explicitly `local`.
3. **The `error` line:** no third mode and no AC for it. It stays on the integration test, like
   `andara_snapshot_failures_total{reason="encode"}`, and `AW-SRV-028`'s §8 says so.
4. **Carrier:** `AW-INF-036`, with `AW-SRV-051` as its dependency. `AW-SRV-028`'s §8 cites it.
5. **Sequencing:** `AW-INF-036`'s AC-3 asserts the per-window summary `warn`, which #409 built (#442), so it is
   pickable on that point.
