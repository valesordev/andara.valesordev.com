---
id: AW-INF-021
title: dev serves its content from the content store
epic: EPIC-05
component: infra
type: infra
status: ready
size: M
depends_on: [AW-SRV-013, AW-SRV-035, AW-CLI-003, AW-INF-019, AW-SRV-037]
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
after this story there is. Architecture decided the mechanisms on 2026-09-28
(`docs/feedback/AW-INF-021-dev-content-store.md`, "Architecture's decisions").

## User story

As a builder, I want `dev` to serve what I publish and activate, so that I can walk my Zone on a
running server minutes after I write it, without a deploy.

## Scope

### In scope
- `values/dev.yaml`: `server.content.source: kafka` and `content.packs: "*"`. The chart's content
  ConfigMaps and their mounts are no longer rendered for `dev`. `local` keeps `dir`.
- Verifying on `dev` that `andara.core` is active at the version the running build carries,
  before a pod from that build reports ready, on every roll `AW-INF-019`'s Application makes. The
  server does it at boot (`AW-SRV-013`, feedback item 1). This story adds no carrier: no hook, Job,
  or image.
- `make content-seed ENV=dev`: publishes and activates the dev fixture as pack `town`, from
  `content/fixtures/town/` (`AW-SRV-037`, feedback item 3), as the operator with `override` and the
  reason `dev fixture`.
- A seed that runs once. If `town` already has an active version, whoever published it, the seed
  publishes nothing, so a Builder who has taken over `town` never has their work overwritten.
- `make world-reset ENV=dev CONFIRM=andara-dev` (feedback item 2), and one run of it to move `dev`
  off its `dir`-genesis log. It recreates the World's log topics and the Account store, and empties
  the snapshot store and the projector's state. The content topics and `andara.audit.v1` are kept.
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
6. **Given** a build whose `content/core/VERSION` is newer than the core active in `dev`'s store
   **when** `AW-INF-019` rolls it **then** the new pod's boot line reports that core published and
   activated, `server info` lists it once the pod is Ready, and every Builder pack active before
   the roll is still active. `AW-SRV-012`'s skew rule refuses only packs built against a *newer*
   core, so none is refused. Observing this needs a core bump on `main`. If none lands before
   §8, the verification record observes the first-install case (AC-7) and names this AC as owed
   to the first core bump.
7. **Given** a `dev` namespace rebuilt from nothing (`make argocd-uninstall`, fresh topics, then
   `make argocd-install ENV=dev`) **when** the Application syncs and `make content-seed ENV=dev`
   runs **then** AC-1 and AC-2 hold without another hand step. The first pod's boot line reports
   `andara.core` published and activated, into an empty store.
8. **Given** the rendered `dev` manifests (`make k8s-dry ENV=dev`) **when** they're read **then**
   there's no `andara-content` or `andara-content-templates` ConfigMap and no `/content` mount.
   `local`'s render still has both.
9. **Given** `make content-seed ENV=dev` with no operator credential in the environment **when** it
   runs **then** it exits 1 with `content-seed: ANDARA_BOOTSTRAP_OPERATOR is not set`, before any
   RPC.
10. **Given** `make world-reset` with `ENV=prod`, or with a `CONFIRM` that isn't the target
    namespace **when** it runs **then** it exits 2 before touching the cluster, naming the reason.
11. **Given** `dev` with active packs, an Account, and a Character **when**
    `make world-reset ENV=dev CONFIRM=andara-dev` runs **then** it ends with
    `world-reset: andara-dev reset; content kept, accounts and characters gone`, and exits 0.
    Once the pod is Ready:
    - `server info` lists the same packs at the same versions;
    - the bootstrap operator can log in;
    - the Account and the Character are gone;
    - `andara.audit.v1`'s high-water mark is not lower than before.

## Interface contract

- `make content-seed ENV=<env>`: `## content-seed: publish and activate the dev fixture as pack town
  in ENV's content store — idempotent — needs the operator credential`. It refuses `ENV=prod` with
  exit 2 (`content-seed: prod is not seeded with the fixture`).
- It uses the product commands only: `andara-cli content publish --path content/fixtures/town` and
  `andara-cli content activate town <m> --override --reason "dev fixture" --yes`, as the bootstrap
  operator. It uses no `kafka-console-producer` and no direct topic write (CLAUDE.md §10).
- Fixture source: `content/fixtures/town/`, which `AW-SRV-037`'s test holds equal to
  `testdata/content/valid`.
- `make world-reset ENV=<env> CONFIRM=<namespace>`: `## world-reset: recreate ENV's World log and
  Account store, keeping content — destroys every Character and Account`.
  - Refusals, exit 2: `ENV=prod`, a missing `CONFIRM`, or a `CONFIRM` that isn't the namespace.
  - Steps, in order: scale the server StatefulSet and the projector Deployment to 0; delete and
    recreate the topics it resets (the list below); empty the snapshot PVC and the projector's
    store; scale back up; wait for Ready. The projector steps are skipped, with a line saying so,
    while `dev` runs no projector (`AW-INF-008` AC-2 and `AW-INF-025` put it there). After
    `AW-INF-025`, the snapshot store is its S3 bucket rather than the PVC. The projector's state
    to clear is its consumer group, as `AW-INF-025`'s contract lists. `world-reset` empties
    whichever of these `dev` has when it runs.
  - Topics it resets, named explicitly from `deploy/kafka/topics.yaml`: `andara.commands.v1`,
    `andara.events.v1`, `andara.state.v1`, `andara.accounts.v1`. It never touches
    `andara.content.*` or `andara.audit.v1`. A topic added to `topics.yaml` later is kept unless
    it's added to this list.
  - Exit codes: 0 reset; 1 a step failed, naming it; 2 usage or refusal.
- Environment read: `ANDARA_BOOTSTRAP_OPERATOR` (existing). None added.
- Exit codes: 0 seeded or already seeded; 1 a precondition or a command failed; 2 usage.
- Server configuration on `dev`, all existing keys (`AW-SRV-012`):
  `content.source=kafka`, `content.packs=*`. `character.spawn_room` is whatever `AW-INF-024` sets,
  and this story doesn't change it.

## Data / state impact

- **`dev`'s log.** Its genesis swaps name `pack="dir"`, version 0 (`AW-SRV-012`, Data/state).
  Recovery on a store-backed server builds only from replayed swaps, so the switch runs
  `make world-reset` once, in the same roll as the values change. Everything in the World and the
  Account store goes: Characters, Brian's Builder Account, and its grants. The bootstrap
  operator comes back by itself. A Builder Account comes back with `account create` and
  `account set-packs`. No player World exists on `dev` before M2, so this is acceptable here and
  nowhere else.
- **Why the Account store resets too:** the Character roster lives in it (`AW-SRV-014`), and a
  roster kept across a log reset points at bodies that no longer exist.
- **The content topics** on `dev`'s broker already exist (`AW-INF-004`, `AW-INF-014`). Blobs and
  versions are never deleted (ADR-0004), so every fixture and Builder version stays in history.
- **Rollback:** set `content.source: dir` back in `values/dev.yaml`, restore the ConfigMap render,
  and run `make world-reset` again. A store-backed log replayed with `content.source=dir` fails the
  genesis digest check, just as the forward direction does. So a rollback costs `dev`'s World a
  second time. The content topics keep everything published, so going forward again loses no
  content.

## Observability requirements

*(SRE observability review, 2026-09-28: `pack` cardinality stated, `ContentLoadFailing` made live
on `dev`, and the core carrier's signals added.)*

- **Metrics:** none new in code. AC-1, AC-4 and AC-6 read these from `dev`'s server, polled per
  `live-assertions.md`:
  - `andara_content_active_version{pack}`;
  - `andara_content_pending_seconds{pack}`;
  - `andara_content_load_failures_total{reason}`;
  - `andara_build_info{pack,content_version}`.

  With `content.packs: "*"`, `pack` has one value per pack that has an active version:
  `andara.core`, `town`, and one per granted Builder pack (`AW-SRV-035`). That's bounded by the
  Operator's grants, expected under 20 on `dev`. It's never a per-version or per-Builder label.
  AC-6's core-skew case reads `andara_content_load_failures_total{reason="core_version"}` and
  `andara_content_pending_seconds{pack}` on the held pack. `content-freshness.md` counts that as
  unfresh (system reason), and so does this story's verification.
- **Logs:**
  - `content-seed: <step>` progress lines, ending in exactly one of:
    - `content-seed: town@<m> published and active`
    - AC-3's `already active` line
    - AC-9's refusal
  - The server's existing `content.load`, `content swap applied` and `error` lines, each with
    `pack`, `version` and `trace_id`.
  - The server's boot line for `andara.core` (`AW-SRV-013`) names the version and what it did:
    published, found, activated, or left an Operator's pointer alone. A failure is a boot exit
    `1` with an `error` line. It never leaves a pod unready with nothing in the log.
  - `world-reset: <step>` progress lines, ending in exactly one of AC-11's line, a named step
    failure, or AC-10's refusal.
- **Traces:** the server's existing `content.load` → `content.resolve`, `content.validate` →
  `content.build`, and `content.swap` spans, now emitted from `dev`. This is `AW-SRV-012`'s §8 line
  "not emittable from the compose server, because `content.source=dir` has no Active Pointer". This
  story carries that live observation, recorded in its verification record. AC-4's activation shows
  as one trace, from the CLI's `cli.command` through `ActivateVersion`, to the Loader's
  `content.load`, and on to the tick's `content.swap`.
- **Alerts:** none new. **`ContentLoadFailing` goes live on `dev` with this story.** It was silent
  there, because `content.source=dir` exports no `andara_content_pending_seconds`
  (`content-freshness.md`, *Known gaps*). This story changes, in the same PR:
  - *Known gaps* in `content-freshness.md`: the `dir` bullet names `local` and compose only.
  - `docs/runbooks/content-load-failing.md`: one line for `dev`. A pending `town` means the fixture
    seed, and a pending Builder pack means that Builder's version. Diagnosis is the same.
  - `docs/runbooks/server-unavailable.md`: the core carrier gates readiness, so it gains a
    diagnostic step for "no ready pod because `andara.core` isn't active". That failure pages as
    `AndaraServerUnavailable`, and today's runbook doesn't name it. The step includes the image
    rollback order from `AW-SRV-013`'s Data/state impact: the dependent packs, then the core
    pointer, then the image. *(Corrected 2026-09-30; the runbook already has it.)*
  - Evaluation in Grafana Cloud waits on `AW-INF-009`, as in `AW-INF-025`. The §8 record says
    whether the rule was evaluated or only the series was observed.

## Test plan

- **Unit:** none; the seed is exercised by running it.
- **Integration:** `helm-test` asserts AC-8 on the rendered `dev` manifests. `stack.yaml` doesn't
  change (`local` stays `dir`).
- **Manual/operator**, on the box, recorded in the verification record:
  ```
  make world-reset ENV=dev CONFIRM=andara-dev   # once, with the values change
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

**Inherited from `AW-SRV-013`'s §8 review (2026-09-30).** This is the first story whose server
publishes and activates against a store on a running cluster. Its §8 record shows from `dev`, or
names the carrier for each series it can't produce:
- the rest of `AW-SRV-012`'s deferred list, as `AW-SRV-013`'s Definition of done inherited it:
  - `andara_content_pending_seconds{pack}` rising, then clearing on apply;
  - `andara_build_info{pack,content_version}` moving on the swap;
  - `andara_content_reload_stall_seconds` and
    `andara_content_load_phase_duration_seconds{phase}` observing resolve, validate, build and
    swap;
  - `andara_content_cache_hits_total{outcome}`;
  - a refused version on `andara_content_load_failures_total{reason}`;
  - `andara_content_relocations_total{zone}` with its `warn` line, which needs a version that
    removes an occupied Room (a fixture seed can't produce one, so name the carrier);
- `AW-SRV-013`'s own series, from the `dev` server that publishes the seed:
  - `andara_content_publishes_total{outcome}`, `approvals_total{outcome}`,
    `pointer_moves_total{direction,override}` and `blob_bytes_total` moving;
  - `content.publish` → `content.validate`, `content.write_manifest` in Tempo;
  - the boot-time `content.core_boot` root span.

## Open questions

- **Resolved 2026-09-28 (architecture):** the fixture's source is its own copy,
  `content/fixtures/town/`, which `AW-SRV-037` adds (feedback item 3).
- `[ASSUMPTION]` The fixture keeps pack ID `town` and Zone IDs `town`, `docks`, `wilds`, and adds
  `purgatory`. A Builder's packs can't reuse those Zone IDs while the fixture is
  active. The guide says so (`AW-INF-023`).
- **Resolved 2026-09-28 (architecture):** items 1–3 in
  `docs/feedback/AW-INF-021-dev-content-store.md`. They are the core carrier (the server at boot),
  `make world-reset`, and the fixture's source.

## Contract review (architecture, 2026-09-28)

SRE's observability review is in the story and in `docs/feedback/AW-INF-021-dev-content-store.md`,
and SRE's answers to items 1, 2 and 4 are there too. The story is `ready`.

1. **No carrier in this story.** The server publishes and activates its own core at boot
   (`AW-SRV-013`, item 1). This story verifies it on `dev` (AC-6, AC-7) and adds none of the
   options it offered: a hook, a Job, or an `andara-cli` image.
2. **`make world-reset` is in scope,** with SRE's shape and one change. The Account store resets
   too, because the roster lives there (item 2). AC-10 and AC-11 are new. The list of topics it
   resets is explicit, so a new topic is kept by default, not destroyed by default.
3. **AC-6 is rewritten.** It tested a carrier's ordering. Now it tests what the boot rules
   promise, and it names what to do if no core bump lands in time: owe it to the first one, as
   CLAUDE.md §8 allows for a caller that doesn't exist yet.
4. **The spawn Room line contradicted `AW-INF-024`,** which moves it to `purgatory/start`. It now
   defers to that story.
5. **`AW-SRV-037` is a new dependency,** for `content/fixtures/town/`.
6. **AC-4's "second identity"** can be Brian's Operator approving his own build, once
   `AW-SRV-013` adopts the self-approval rule. Two identities are still involved, as the roadmap
   says, even though they're one person.

## `world-reset` delivered early (SRE, 2026-09-29)

**Why now.** #157 (AW-SRV-037) added Purgatory to the `dir` fixture, which `dev` still boots from,
because this story hasn't moved it to the store. `dev`'s log no longer replayed. `andara-0` went
to CrashLoopBackOff with `recovery: replay: tick 3: content digest mismatch at tick 3: dir@0
recorded 77f962b854e1ed23, built 6b130c9b90951d86`. AW-SRV-037's Data section expected `dev` to
be unaffected, but that assumed this story had already landed. Brian chose to build
`make world-reset` to this story's contract and run it, rather than roll `dev` back or reset it by
hand.

**What was built:** `make world-reset` and `scripts/world_reset.py`, to the Interface contract.
It adds one step the contract didn't name: it suspends Argo CD's automated sync on the
Application for the duration, and restores it on the way out whatever happens. Without that,
selfHeal scales the server back up mid-reset. It also refuses `ENV=local` with exit 2, naming
`make down VOLUMES=1`. The rest of the story (`content-seed`, the store source, `argocd-status`'s
pack lines) is untouched. **Status stays `ready`**, and this part ships ahead on
`sre/aw-inf-021-world-reset`.

**The run, 23:52Z:**
`make world-reset ENV=dev CONFIRM=andara-dev` exited 0, ending
`world-reset: andara-dev reset; content kept, accounts and characters gone`.

| Check | Before | After |
|-------|--------|-------|
| `andara-0` | CrashLoopBackOff, 6 restarts | `1/1 Running`, 0 restarts. `tick loop started` at tick 0; `bootstrap operator created` |
| `andara.audit.v1` high-water mark | 1 | 2 (not lower) |
| `andara.accounts.v1` | 6 records | 1: the bootstrap operator |
| `andara.content.*` | 0 each (`dev` is still `dir`) | kept, 0 each |
| Argo CD | automated, prune, selfHeal | the same, restored. `argocd-status`: Synced, Healthy, `main@4576941` |

- **AC-10: pass.** `scripts/tests/test_world_reset.py` covers `prod`, `local`, a missing `CONFIRM`,
  a `CONFIRM` that isn't the namespace, and no `ENV`. Each exits 2, with no `kubectl` on `PATH`.
- **AC-11: partly observed.** The line, the exit code and the audit mark held. The server logged
  `bootstrap operator created`, but a login wasn't tried. "Same packs at the same versions" can't be observed until `dev` reads the store, since it
  had no packs. It's owed with the rest of the story.

**Review of #165 (Codex, 2026-09-29):**
- **Fixed, P1: Argo CD fails closed.** Only a confirmed `NotFound` counts as no Application. Any
  other error, such as RBAC or a timeout, stops the reset before anything changes. Test:
  `FailsClosed.test_an_unreadable_application_stops_before_any_change`.
- **Fixed: the projector is waited on.** After scaling to 0, the reset waits until its pods are
  gone and `andara-projector-state-<env>` is `Empty` before touching Kafka. `dev` runs no
  projector yet, so this path first runs after AW-INF-025.
- **Fixed by refusing: an `s3` snapshot store.** The reset reads `ANDARA_SNAPSHOT_STORE` from
  `andara-config` before any change. On `s3` it exits 1 with nothing changed, because emptying the
  bucket is AW-INF-025's to add. Test: `FailsClosed.test_an_s3_snapshot_store_stops_before_any_change`.
  On `dev` today, read-only: the Application is readable and the store is `fs`.
