---
id: AW-INF-023
title: The Builder's Guide — from no access to a live Zone on dev
epic: EPIC-06
component: infra
type: infra
status: blocked
size: M
depends_on: [AW-CLI-003, AW-SRV-035, AW-SRV-036, AW-INF-020, AW-INF-021, AW-INF-022, AW-INF-024]
blocks: []
lane: architecture
risk: low
---

## Context

Brian wants to start building content for `dev` once SPRINT-02 has it running (2026-09-26). The
documentation a Builder has today is the Content Language specification
(`docs/specs/content-language/v1/`). It's normative, and it's written for the compiler's authors:
it says what a parse means and which protobuf field it produces. The Builder commands' contracts
live in stories (`AW-CLI-002`, `AW-CLI-003`). Nothing says how to get an account and a pack, where
the source lives, how to publish and get a second approval, or what an error means and how to fix
it. The M3 gate ("a Builder with no repository access publishes a Zone, sees it live, and rolls it
back") needs someone to be able to do that from documentation alone.

Brian decided (2026-09-26) that architecture writes the guide, from this story, as the commands
land. It's written against what exists, not ahead of it. The guide explains and links. It never
restates a normative rule from the spec, so the two can't drift. The reference tables that would
drift (Directions, Component types, `andara.core` Templates, diagnostic codes) are generated or
checked by `make check`.

## User story

As a builder with no access to the code, I want one guide that takes me from nothing to my Zone
live on `dev` and back, so that I spend my time writing the World, not working out the toolchain.

## Scope

### In scope
The guide, at `docs/builders/` (decided 2026-09-28, item 5 in
`docs/feedback/AW-INF-021-dev-content-store.md`), in these sections:

1. **What a Builder does here.** Packs, versions, and the Active Pointer in the Builder's words.
   Publish is one person and activation is two. What content can express today: Zones, Rooms,
   Exits, Components on Rooms and Zones, and Templates extending `andara.core`. What waits:
   Behaviors (`AW-SRV-016`), Items and NPCs in play (M4).
2. **Getting access.** What to ask an Operator for: an Account with `builder`, a pack
   (`AW-SRV-035`), the Content Repository, and a place on the tailnet. `dev`'s edge resolves only
   inside Brian's tailnet (SRE, 2026-09-28), so a Builder who can't reach it can write and
   `validate` but can't publish. The Operator's side of each request is written as the commands the
   Operator runs. After a `make world-reset`, the Builder Account and its grants are re-created the
   same way (`AW-INF-021`).
3. **Installing `andara-cli`.** The `cli-dev` download, checksum, and first login to `dev`
   (`AW-INF-020`). macOS's quarantine prompt. The core the binary carries (`andara-cli version`),
   and what a `core_version_mismatch` after a core bump asks of the Builder (`AW-INF-022` AC-8).
4. **Your first Zone.** A tutorial: clone the Content Repository, copy `content/example`, add a Room,
   `fmt`, `validate`, open a pull request, publish, have it approved, activate, see it in
   `server info`, walk it with `andara-cli play`, change it, and roll it back.
5. **The everyday loop.** `history`, `diff`, `fetch`, and rollback, plus what last-pointer-move-wins
   means when two Builders share a pack.
6. **The language, by example.** A guided tour that links into `semantics.md` for every rule it
   mentions and restates none of them.
7. **Reference**, generated from the code and the spec by `make builder-reference`: the Direction
   set with reverses, the Component types with their fields, the `andara.core` Templates with their
   chains, and every diagnostic code with its severity, what triggers it, and the usual fix.
8. **Building on `dev`.** Covers:
   - the fixture pack `town` and the Zone IDs it reserves;
   - Purgatory, the spawn Zone, with its Exit to the town (`AW-SRV-037`, `AW-INF-024`);
   - `goto <zone>/<room>` to reach a Zone nothing links to yet (`AW-SRV-036`).
9. **When something fails.** `andara-cli`'s exit codes 1–4 for the content commands, each with its
   usual cause, and how to ask for a server change (a GitHub issue on this public repository).

Also in scope:
- `make builder-reference` writes section 7's tables. `make check` fails if they're stale.
- `make guide-check`, part of `make check`, asserts that:
  - every `andara-cli …` command in the guide's code blocks exists (`<command path> --help` exits 0);
  - every diagnostic code in `errors.md` appears in section 7;
  - every link resolves inside this repository.

### Out of scope
- The Content Language specification itself. It stays normative in `docs/specs/content-language/`.
- Writing lore, Zones, or names for the World. The tutorial's Rooms are placeholder text, and the
  World is Brian's.
- Behavior authoring (Python). It gets a guide section when `AW-SRV-016` lands.
- A web-hosted docs site. GitHub renders the Markdown, and this repository is public.

## Acceptance criteria

1. **Given** the guide **when** `make guide-check` runs **then** every `andara-cli` command it shows
   exits 0 for `--help`, every `errors.md` code appears in the reference, every relative link
   resolves, and it exits 0. Removing any one of those makes it exit 1, naming the command, code,
   or link.
2. **Given** a Component type added to the server's closed table **when** `make check` runs without
   `make builder-reference` **then** it fails with `builder-reference: stale; run make
   builder-reference`.
3. **Given** a person with no clone of this repository, access to the Content Repository and the
   tailnet, and only the guide **when** they follow sections 2–4 against `dev` **then** they
   publish, get approval for, activate, walk, and roll back a Zone of their own. Until `dev` has a
   public edge, that person is Brian. The verification record has the
   transcript and names each point where they needed anything the guide didn't say. There must be
   none at close.
4. **Given** section 4 **when** it's read **then** every step is a product command
   (`andara-cli …`, `git …` on the Content Repository) or a `make` target, with its expected output
   (CLAUDE.md §9).
5. **Given** the guide **when** it names a rule the spec defines (a naming rule, a warning's
   trigger) **then** it links to the spec section rather than restating the rule. `guide-check`
   can't assert this; the §8 review does.
6. **Given** a Builder working alone on `dev` **when** they read section 4 **then** it shows the
   Operator approving their own build (`docs/feedback/AW-SRV-013-operator-self-approval.md`),
   labeled as how `dev` works while one person builds, not as the process for a team.

## Interface contract

- Location: `docs/builders/README.md` (the entry point), with one file per section above.
- `make builder-reference`: `## builder-reference: regenerate the Builder's Guide reference tables
  from the server's Direction and Component tables, andara.core, and errors.md`.
- `make guide-check`: `## guide-check: every andara-cli command in the Builder's Guide exists,
  every diagnostic code is documented, every link resolves`. Added to `make check`.
- Exit codes: 0 ok; 1 a check failed, naming it.
- The Content Repository's `README.md` (`AW-INF-022`) links to `docs/builders/README.md` on `main`.

## Data / state impact

None.

## Observability requirements

- **Metrics / Traces / Alerts:** none; documentation and a check target.
- **Logs:** `guide-check: <what failed>` lines. `builder-reference` names the files it wrote.

## Test plan

- **Unit:** `guide-check` against fixtures: a missing command, a missing code, and a broken link each
  fail (AC-1).
- **Integration:** `make check` in CI runs `guide-check` and the staleness check (AC-2).
- **Manual/operator:** AC-3's walk-through on `dev`, by someone other than the guide's author. Brian
  is the intended first reader.

## Definition of done

CLAUDE.md §8, plus AC-3's transcript in the verification record.

## Open questions

1. **Resolved 2026-09-26 (Brian): how a Builder reaches a new Zone.** A `goto` command for Builders
   (`AW-SRV-036`). Until the base content exists, new Characters spawn in Purgatory, which has an
   Exit into the test town (`AW-SRV-037`, `AW-INF-024`).
2. **Resolved 2026-09-26 (Brian): one Builder and the two-person rule.** An Operator may approve a
   build they published as a Builder, audited as a self-approval. It's temporary, until others
   build. The contract change to `AW-SRV-013` and `AW-CLI-003` is in
   `docs/feedback/AW-SRV-013-operator-self-approval.md`.
3. **Resolved 2026-09-28 (architecture): the guide lives in `docs/builders/` in this public
   repository.** Brian adds the path to CLAUDE.md §2's architecture row and to the architecture
   charter's writable paths upstream (item 5 in `docs/feedback/AW-INF-021-dev-content-store.md`).
4. **For PM: the story's tooling isn't architecture's to build.** See Blocked by.

## Blocked by

Three things, none of them another story's code:

1. **Brian: `docs/builders/` isn't writable by any role yet.** CLAUDE.md §2's architecture row and
   the architecture charter's writable paths (installed from automate.bashburn.com) both need it.
   *(2026-09-29: CLAUDE.md §2 and §3 now name it. The charter's writable paths are upstream, so
   this item clears when `make install` brings a charter that lists `docs/builders/`.)*
2. **PM: `make builder-reference` and `make guide-check` are SRE's,** since they live in
   `Makefile` and `scripts/`. Architecture can't write either, and can't commit to an SRE branch.
3. **PM: the Component table has no source outside the code.** Section 7's Component types come
   from `sim.ComponentTypes()`, the server-defined registry (`semantics.md`). Reading that is
   implementation's. The Directions (glossary), the core Templates (`content/core/`) and the
   codes (`errors.md`) can be read from docs.

The split architecture recommends, for PM to write:
- an implementation story for `andara-cli content reference --output json`, which prints the
  Direction set, the Component types with their fields, the embedded core's Templates with their
  chains, and every diagnostic code with its severity. It's useful to a Builder offline in its own
  right, and it's the one source `builder-reference` reads;
- an SRE story for `make builder-reference` (renders that JSON into section 7) and
  `make guide-check`, both in `make check`.

This story then depends on both, and architecture writes only `docs/builders/`. It stays last in
the sprint either way, so the split costs no order.

## Contract review (architecture, 2026-09-28)

SRE's review is in `docs/feedback/AW-INF-021-dev-content-store.md`: no change to Observability, plus
the tailnet finding. The story is `blocked` on the three items above. None of them affects the
guide's content contract.

1. **Location decided:** `docs/builders/` (item 5).
2. **The tailnet is part of getting access.** SRE found `dev`'s edge is tailnet-only, so section 2
   covers it, and AC-3's reader is Brian until `dev` has a public edge.
3. **Core bumps and `world-reset` get a line each,** in sections 2 and 3. Both are things a Builder
   meets on `dev` that the guide didn't mention.
4. **The tooling moves out** to the stories PM writes (Blocked by, 2 and 3). The guide's ACs 1 and
   2 stand. They name targets that other lanes build.
