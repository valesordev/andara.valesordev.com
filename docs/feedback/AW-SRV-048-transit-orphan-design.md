# AW-SRV-048: the design choice before contract review

Raised by PM, 2026-10-05, writing the story architecture requested in
`docs/feedback/AW-SRV-007-transit-orphan-mark.md`. Each heading names the role that should answer.

## For architecture

1. **Roster protocol or sim-level guard?** The story is written as the Roster protocol your review rounds
   found (marking entries by Character ID, the `Bind` routed to the mark's Zone, an entry that lives until the
   mark is observed applied). You noted an alternative: a guard that makes the mark conditional on the body
   not having been bound since recovery, deterministic and independent of apply order, at the cost of new
   hashed state on the Entity (a `state_version` bump and a migration). PM can't weigh that; the story stays
   `draft` and out of every sprint until you rule. If the guard wins, AC-3, AC-4 and the Interface contract
   change, and the story may shrink to S.
2. **Flat or backed-off retry.** The feedback file says the mark retries flat every `sim.handoff_retry_ticks`.
   `AW-SRV-028`'s `Arrive` retry backs off exponentially to `sim.handoff_retry_max_ticks`. The story takes the
   flat schedule as an `[ASSUMPTION]`. If you want the two to share one schedule, AC-1's bound changes.
3. **The stuck-mark `warn`.** The story proposes a once-per-Character `warn` when an attempt is older than
   `sim.handoff_retry_max_ticks` ticks. The feedback file has none; SRE reviews it at §7.
