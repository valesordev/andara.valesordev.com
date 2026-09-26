# AW-SRV-013 — an Operator approves their own build

Stories: `AW-SRV-013`, content publish path (`ready`); `AW-CLI-003`, the content commands
(`ready`). Neither has started.

Raised: 2026-09-26, PM, from Brian's decision while grooming Builder content on `dev` (SPRINT-03).

## Brian's decision (2026-09-26)

> There should be an admin approval function to allow me to act as a builder and then use the admin
> to approve my own build. We can update this later if we get others working on this as well.

Brian is the only Builder. He publishes as a Builder, either with his own `builder` Account or as an
Operator acting as one (`andara-cli --as`, `AW-SRV-008`). He then approves that version with his
Operator identity. This is an **approval**, recorded on the manifest, not the activation `override`
that skips approval.

This amends the 2026-09-07 rule in ADR-0004 ("activating content requires a second approver") for
Operators only. It's explicitly temporary: Brian will revisit it when other Builders join.

## For architecture

`AW-SRV-013` and `AW-CLI-003` are `ready`, so PM doesn't amend them. Please record the change in both
story bodies at SPRINT-03's contract review, and add a dated note to ADR-0004's resolution list.

PM's reading of what changes:

1. **`ApproveVersion`'s self-approval rule.** Today, the same Account that published gets
   `PERMISSION_DENIED` (AC-4), and the matrix gives `operator` "✓ if ≠ publisher". The proposed rule:
   - A caller holding `operator` may approve any version, including one they published, or published
     while acting as another Account.
   - The manifest's `approved_by` is the Operator.
   - The audit record carries `self_approval=true` whenever the approver is the publisher, or is the
     real actor behind the publisher.
   - A `builder` without `operator` still can't approve their own version. AC-4's refusal stands for
     Builders.
2. **Which identity "published" when acting as.** The manifest names one author. With `--as`, the
   audit record has `actor_account_id` (the Operator) and `acting_as_account_id` (the Builder).
   Self-approval should compare the approver against both, so the audit flag is true whether Brian
   published as `brian-builder` directly or as `operator --as brian-builder`. That keeps the record
   honest about one person doing both.
3. **The CLI.** `content approve <pack> <version>` works unchanged for an Operator. On a
   self-approval it asks for confirmation (`--yes` on a non-TTY) and prints
   `town@8 approved by <operator> (self-approval: you published it)`. `AW-CLI-003` AC-3's message
   ("needs approval by a second builder…") gains `…or an operator`.
4. **A switch to turn it off later.** A config key such as `content.operator_self_approval`
   (default `true`), so that when other Builders join, turning it off is a values change, not a
   code change. PM's suggestion only; architecture decides whether a switch is worth it now.

## Consequences worth stating

- Phase 1 exit criterion 4 and the M3 gate say that demonstrating publish-and-activate "needs two
  identities". With this rule, one person with an Operator identity passes. The roadmap's wording
  stays true, since two identities are still involved, but it's no longer two people. PM will note
  that in `docs/roadmap.md` once architecture confirms the rule.
- `AW-INF-023`'s guide documents this as how Brian works on `dev`. It isn't how it teaches other
  Builders.
