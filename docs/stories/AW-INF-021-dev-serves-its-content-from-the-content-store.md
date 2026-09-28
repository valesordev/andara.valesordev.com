---
id: AW-INF-021
title: dev serves its content from the content store
epic: EPIC-05
component: infra
type: infra
status: draft
size: M
depends_on: [AW-SRV-013, AW-SRV-035, AW-CLI-003, AW-INF-019]
blocks: [AW-INF-022, AW-INF-023]
lane: sre
risk: medium
---

## Context

Brian wants to start building content for the `dev` instance once SPRINT-02 has it running
(2026-09-26). ADR-0004 decided where content lives: the content store's three compacted topics, and
a Builder publishes to them with `andara-cli`. `AW-SRV-012` built the server's store-backed read
side (`content.source=kafka`), and `AW-SRV-013` and `AW-CLI-003` build the publish side. But `dev`
doesn't read the store. `values/dev.yaml` sets `content.source: dir`, and the Zones come from a
ConfigMap of `testdata/content/valid`, which `AW-INF-019` renders from git. So anything a Builder
publishes to `dev` today is written and never served.

This story switches `dev` to the store. Brian decided the dev fixture stays (2026-09-26): the town,
docks, and wilds Zones that `town/plaza` spawns into are published into `dev`'s store as a pack, so
`dev` stays playable before any Builder content exists. The fixture includes Purgatory, the spawn
Zone every new Character enters (Brian, 2026-09-26; `AW-SRV-037`, `AW-INF-024`). A Builder's packs load beside it. It also
gives `AW-INF-007`'s `andara.core` activation step a carrier on `dev`. Architecture's review of
`AW-INF-019` (#107) found "nothing to act on while `dev` reads its content from ConfigMaps", and
after this story there is. The open mechanism questions are in
`docs/feedback/AW-INF-021-dev-content-store.md`.

## User story

As a builder, I want `dev` to serve what I publish and activate, so that I can walk my Zone on a
running server minutes after I write it, without a deploy.

## Scope

### In scope
- `values/dev.yaml`: `server.content.source: kafka` and `content.packs: "*"`. The chart's content
  ConfigMaps and their mounts are no longer rendered for `dev`. `local` keeps `dir`.
- `andara.core` active in `dev`'s store at the version the running build carries, before a pod
  from that build reports ready, on every roll `AW-INF-019`'s Application makes. The mechanism is
  feedback item 1.
- `make content-seed ENV=dev`: publishes and activates the dev fixture as pack `town`, from Content
  Language source, as the operator with `override` and the reason `dev fixture`.
- A seed that runs once. If `town` already has an active version, whoever published it, the seed
  publishes nothing, so a Builder who has taken over `town` never has their work overwritten.
- The move of `dev`'s existing log and Account store from `dir` genesis swaps to store-backed
  content, per feedback item 2.
- `make argocd-status ENV=dev` (from `AW-INF-019`) adds one line per active pack:
  `content <pack>@<version>`.

### Out of scope
- `prod`'s content source. It stays as it is until a Builder's content is meant for players.
- The content repository and its CI: `AW-INF-022`.
- Granting a Builder their packs: `AW-SRV-035`.
- `local` and compose. Both keep `dir`, so `make up` needs no broker-side content to boot.

## Acceptance criteria

1. **Given** `dev` after this story's roll **when** `andara-cli server info` runs against it
   **then** the content in effect lists `andara.core@<n>` and `town@<m>`, and none is `dir`.
2. **Given** a new Character on `dev` **when** `andara-cli play --character <name>` enters **then**
   the first Room is `dev`'s configured spawn Room: `Purgatory` (`purgatory/start`) once
   `AW-INF-024` has landed, and `Market Plaza` (`town/plaza`) before that.
3. **Given** `town` with any active version **when** `make content-seed ENV=dev` runs **then** it
   prints `content-seed: town@<m> is already active; nothing published`, exits 0, and
   `content history town` shows no new version. That holds for a second seed run and for a `town`
   version a Builder published.
4. **Given** a Builder granted pack `brian` (`AW-SRV-035`) **when** they publish a Zone `start`, a
   second identity approves, and they activate it **then**, polled to `content.reload_debounce + 2 s`,
   `server info` lists `brian@1` and `andara_content_active_version{pack="brian"}` is `1`.
5. **Given** a merge to `main` that rolls `dev` **when** the new pod reports ready **then** every
   pack active before the roll is still active at the same version. A roll never re-seeds or moves
   a Builder's pointer.
6. **Given** a build whose `andara.core` is newer than the one active in the store **when**
   `AW-INF-019` rolls it **then** that core version is active before the pod reports ready, and a
   Builder pack compiled against the older core still loads, unless `AW-SRV-012`'s core-skew rule
   refuses it. In that case the refusal is visible in `make argocd-status` and in the server's
   `error` line, not as a crash loop.
7. **Given** a `dev` namespace rebuilt from nothing (`make argocd-uninstall`, fresh topics, then
   `make argocd-install ENV=dev`) **when** the Application syncs and `make content-seed ENV=dev`
   runs **then** AC-1 and AC-2 hold without another hand step.
8. **Given** the rendered `dev` manifests (`make k8s-dry ENV=dev`) **when** they're read **then**
   there's no `andara-content` or `andara-content-templates` ConfigMap and no `/content` mount.
   `local`'s render still has both.
9. **Given** `make content-seed ENV=dev` with no operator credential in the environment **when** it
   runs **then** it exits 1 with `content-seed: ANDARA_BOOTSTRAP_OPERATOR is not set`, before any
   RPC.

## Interface contract

- `make content-seed ENV=<env>`: `## content-seed: publish and activate the dev fixture as pack town
  in ENV's content store — idempotent — needs the operator credential`. It refuses `ENV=prod` with
  exit 2 (`content-seed: prod is not seeded with the fixture`).
- It uses the product commands only: `andara-cli content publish --path <fixture>` and
  `andara-cli content activate town <m> --override --reason "dev fixture" --yes`, as the bootstrap
  operator. It uses no `kafka-console-producer` and no direct topic write (CLAUDE.md §10).
- Fixture source: Content Language, compiling to the same Zone Definitions as
  `testdata/content/valid` (`[ASSUMPTION]` below).
- Environment read: `ANDARA_BOOTSTRAP_OPERATOR` (existing). None added.
- Exit codes: 0 seeded or already seeded; 1 a precondition or a command failed; 2 usage.
- Server configuration on `dev`, all existing keys (`AW-SRV-012`):
  `content.source=kafka`, `content.packs=*`. `character.spawn_room` stays `town/plaza`.

## Data / state impact

- **`dev`'s log.** Its genesis swaps name `pack="dir"`, version 0 (`AW-SRV-012`, Data/state).
  Recovery on a store-backed server builds only from replayed swaps, so whether the switch needs
  fresh topics, and what survives (Accounts, Characters), is feedback item 2. No player World
  exists on `dev`, and `AW-SRV-012` already names "fresh topics for `dev`" as the recovery for a
  pre-rule log.
- **The content topics** on `dev`'s broker already exist (`AW-INF-004`, `AW-INF-014`). Blobs and
  versions are never deleted (ADR-0004), so every fixture and Builder version stays in history.
- **Rollback:** set `content.source: dir` back in `values/dev.yaml` and restore the ConfigMap
  render. That's the same log question in reverse, and feedback item 2 answers it too.

## Observability requirements

- **Metrics:** none new. AC-1 and AC-6 read `andara_content_active_version{pack}` and
  `andara_content_load_failures_total{reason}` from `dev`'s server. `pack` is bounded by the
  packs followed.
- **Logs:** `content-seed: <step>` progress lines. The server's existing `content.load` and swap
  lines.
- **Traces:** the server's existing `content.load` and `content.swap` spans, now emitted from `dev`.
  This is `AW-SRV-012`'s §8 line "not emittable from the compose server, because
  `content.source=dir` has no Active Pointer". This story carries that live observation, recorded
  in its verification record.
- **Alerts:** none.

## Test plan

- **Unit:** none; the seed is exercised by running it.
- **Integration:** `helm-test` asserts AC-8 on the rendered `dev` manifests. `stack.yaml` doesn't
  change (`local` stays `dir`).
- **Manual/operator**, on the box, recorded in the verification record:
  ```
  make content-seed ENV=dev              # town@1 published and active
  make content-seed ENV=dev              # "town@1 is already active; nothing published"
  make argocd-status ENV=dev             # content andara.core@<n>, content town@1
  andara-cli --config <dev> server info  # content in effect: andara.core@<n>, town@1
  ```
  Then AC-4's two-identity walk-through, and AC-7's rebuild.

## Definition of done

CLAUDE.md §8, plus: `AW-SRV-012`'s deferred live observation of the `content.load` and
`content.swap` spans and of `andara_content_active_version` from a store-backed server is recorded
against `dev`.

## Open questions

- `[ASSUMPTION]` The fixture's Content Language source starts from the spec corpus's
  `docs/specs/content-language/v1/corpus/valid/town/`, pack `town`, plus a `purgatory` Zone to match
  `AW-SRV-037`. The corpus case lacks Purgatory, so the seed probably needs its own copy (item 3 in
  the feedback file).
- `[ASSUMPTION]` The fixture keeps pack ID `town` and Zone IDs `town`, `docks`, `wilds`, and adds
  `purgatory`. A Builder's packs can't reuse those Zone IDs while the fixture is
  active. The guide says so (`AW-INF-023`).
- Items 1–3 in `docs/feedback/AW-INF-021-dev-content-store.md` affect the interface contract.
  Architecture answers them at contract review.
