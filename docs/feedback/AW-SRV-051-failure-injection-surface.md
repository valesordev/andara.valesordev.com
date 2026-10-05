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
