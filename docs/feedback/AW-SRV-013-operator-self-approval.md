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

## For architecture: SRE observability review, 2026-09-28

The CLAUDE.md §7 review of `AW-SRV-013` and `AW-CLI-003`, both `ready`, for SPRINT-03's contract
review. Only their Observability sections changed. The metric names are unchanged.

- **`AW-SRV-013`: amended.**
  - RED for the eight new `Admin` RPCs is named: the Gateway's existing `andara_grpc_*` series.
  - Every label set is closed and pre-seeded. `stale_parent` is added to publishes' outcomes.
  - "Pack ID is a bounded label" is removed. No counter in the story carries it, and none needs to.
  - Correlation fields (`session_id`, `trace_id`, `acting_as_account_id`) are required on every
    line.
  - Persistence-write spans are named: `content.write_blob`, `content.write_manifest`,
    `content.write_pointer`, and `audit.write`.
  - Two additions depend on your decisions:
    - **`andara_content_approvals_total{outcome="self_operator"}`** exists only if you adopt this
      file's rule. It's kept apart from `ok`, so the temporary rule's use shows on a dashboard.
    - **`andara_content_activations_refused_total{reason}`** is new. Its `reason` is `unapproved`,
      plus the activation-refusal codes if you adopt
      `docs/feedback/AW-SRV-013-activation-refusals.md` item 1.
  - `content.validate` keeps the Loader's span name. The parent tells publish-time and load-time
    apart. If you'd rather it were distinct, say so at contract review.
- **`AW-CLI-003`: amended.**
  - No metrics, since the CLI is short-lived.
  - `pack`, `version` and `override`/`reason` on the `cli.command` span.
  - One span per blob stream, never per chunk.
  - `trace_id` in the logged confirmation line and in the `--output json` envelope, so a Builder's
    report joins the server's audit record on one ID.

## Architecture's contract review (2026-09-28)

The rule is adopted as PM read it, items 1–4, with the switch: `content.operator_self_approval`,
default `true`. It's recorded in `AW-SRV-013` (AC-4 narrowed, AC-13 new), in `AW-CLI-003`, and as a
dated note in ADR-0004. The same review adopted `AW-SRV-013-activation-refusals.md` item 1 (AC-14)
and made the server publish `andara.core` at boot (AC-15–17, AC-19;
`AW-INF-021-dev-content-store.md` item 1). PM can update `docs/roadmap.md`'s "two identities" wording.

### For PM: split `AW-SRV-013`

It was the size of the sprint before this review. It's now an `L`. Architecture recommends lifting
the boot publish into its own story. The contract is already written as a separate section, so the
split moves text and doesn't change it:
- **`AW-SRV-013`** keeps the Admin RPCs, the two-person rule and self-approval, activation refusals,
  and `GetBlob`: AC-1 to AC-14, and AC-18.
- **New story, "the server publishes its `andara.core` at boot"**: AC-15 to AC-17, AC-19, the
  *`andara.core` at boot* section, `content/core/VERSION` and `VERSIONS`, and the image-rollback
  note. It depends on `AW-SRV-013`, since it writes through the same store adapter and audit path.
  `AW-INF-021` depends on it, and `AW-CLI-002` needs its `VERSION` file. It's implementation lane,
  size S–M.

If PM would rather not split mid-sprint, `AW-SRV-013` stays `ready` as it is. The core section is
self-contained, and implementation can deliver it as a second PR on the same story.

### For SRE

- The boot publish has a log line and a `content.core_boot` trace, and counts on
  `pointer_moves_total`. There's no metric of its own. If SRE wants one, for example
  `andara_content_core_boot_total{outcome}` with `published`, `present`, `held`, it can be added
  under this heading before implementation starts.
- The image-rollback order (pointer first, then image) needs a line in
  `docs/runbooks/server-unavailable.md`. It's in `AW-SRV-013`'s Data/state impact.
