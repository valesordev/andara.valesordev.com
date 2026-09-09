---
id: AW-CLI-002
title: andara-cli content validate and inspect
epic: EPIC-05
component: cli
type: feature
status: draft
size: S
depends_on: [AW-CLI-001, AW-SRV-001]
blocks: [AW-CLI-003]
assignee: cursor
risk: low
---

> `status: draft` — unblocked by ADR-0004 and scoped. Groomed to `ready` when M3 approaches.

## Context

`AW-SRV-001` put the content validator in the simulation core specifically so it could be reused. This is
the first reuse: a Builder validating locally before publishing, and CI validating on every content
change.

ADR-0004 makes the server-side check at publish the authoritative gate, since Builders are untrusted. That
does not make this command redundant — it makes it the fast feedback loop. A Builder should find a dangling
Exit in a second on their laptop, not in a round trip to a server.

## User story

As a builder, I want to validate my content locally and be told exactly what is wrong, so that I never
publish a Zone that fails to load.

## Scope

### In scope
- `andara-cli content validate` against a local directory or a published version.
- `andara-cli content inspect zone|room` for reading resolved content.
- Findings rendered per `AW-CLI-001`'s output contract, human and JSON.
- Offline operation: `validate --path` must work on a laptop with no server and no cluster access, or
  Builders will not use it. `--path` names a **local working directory of authored source** — not a
  checkout of this repository, which no Builder has (decision below).

### Out of scope
- Publishing and rollback — `AW-CLI-003`.
- Any world-model validation logic of its own. It wraps `sim.BuildWorld` and adds nothing.

Once ADR-0010 is accepted, `validate` gains a **resolution stage** ahead of `sim.BuildWorld`:
resolving `extends` chains against the cached `andara.core` pack. Its errors must name the type in the
chain that is wrong, not the flattened output — a Builder debugging a three-deep hierarchy should not
be reading a resolved blob.

## Acceptance criteria (known now; completed at grooming)

1. **Given** content with a dangling Exit **when** `content validate` runs **then** it exits 1 and prints
   one line per finding naming the file, Room ID, direction, and unresolved target.
2. **Given** valid content **when** it runs **then** it exits 0 and prints a summary of Zones and Rooms.
3. **Given** `--output json` **when** validation fails **then** stdout is a JSON array of validation
   findings and nothing else.
4. **Given** the same content **when** validated by the CLI, by CI, and by the server at publish **then**
   all three produce identical findings. Divergence between the three is the failure this story exists to
   prevent, and it is asserted by a shared fixture set rather than by inspection.
5. **Given** no network access **when** `content validate --path ./mycontent` runs **then** it succeeds.

## Interface contract

To be written at grooming. Committed now: the command calls `sim.BuildWorld` and formats its output. A
rule that exists in the CLI but not at publish or at load is a defect.

Provisional surface:
```
andara-cli content validate [--path DIR | --pack ID --version N]
andara-cli content inspect zone <ZoneID>
andara-cli content inspect room <ZoneID>/<RoomID>
```

## Data / state impact

Read-only.

## Observability requirements

Per `AW-CLI-001`: no metrics, structured stderr diagnostics, `cli.command` root span. Nothing additional.

## Test plan

The three-way equivalence test from AC-4 over a shared fixture set, run in CI. Offline operation asserted
in an environment with no network.

## Definition of done

CLAUDE.md §8, plus: the equivalence test runs in CI, using the same fixtures `AW-SRV-001` and `AW-SRV-013`
use.

## Open questions

- **Resolved 2026-09-07 (Brian):** no Builder has repository access, confirming ADR-0004's assumption.
  A Builder who needs something changed in the server opens a GitHub issue; they do not open a pull
  request, because they cannot. Both surfaces stay, but they now mean different things rather than
  serving different populations: `--path` validates source a Builder is still writing, and
  `--pack/--version` validates what is already published. That also makes the content store the only
  Builder-facing write path, which is what makes publish-time authorization and audit load-bearing
  rather than defensive (`AW-SRV-013`).
- Per ADR-0007 the canonical content format is protobuf, which is not hand-authorable. The human-friendly
  authoring surface is `AW-CLI-003`'s problem, and `validate` must accept whatever that turns out to be as
  well as the canonical form.
