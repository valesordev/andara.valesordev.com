---
id: AW-INF-021
title: dev serves its content from the content store
epic: EPIC-05
component: infra
type: infra
status: review
size: M
depends_on: [AW-SRV-013, AW-SRV-035, AW-CLI-003, AW-INF-019, AW-SRV-037, AW-SRV-042]
blocks: [AW-INF-022, AW-INF-023, AW-SRV-045]
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
7. **Given** a `dev` namespace rebuilt from nothing, by these targets in order and no other step:
   `make env-destroy ENV=dev CONFIRM=andara-dev` (deletes `andara-dev` with its Secrets and topics),
   `make kafka-install ENV=dev`, `make objectstore-install ENV=dev`, then
   `make argocd-install ENV=dev`, **when** the Application syncs and `make content-seed ENV=dev`
   runs **then** AC-1 and AC-2 hold without another hand step. The first pod's boot line reports
   `andara.core` published and activated, into an empty store. *(Amended 2026-10-02, from Codex on
   #325: the procedure said `make argocd-uninstall` and fresh topics. That doesn't cascade, and it
   keeps the Secrets, so it never exercised a fresh namespace or Secret provisioning. Deleting the
   namespace is what found the RoleBinding defect.)*
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
  as **two traces joined by a span link**. The first runs from the CLI's `cli.command` through
  `ActivateVersion`. The second is the Loader's `content.load` → the tick's `content.swap`, and it
  links to the first through `ActiveVersion.trace_parent` (`AW-SRV-045`). Until that field ships,
  the two join on `pack@version` and time. *(Amended 2026-10-01: this said one trace, which can't
  hold: one debounced load can serve several activations. See the feedback file.)*
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

- *(SRE, 2026-10-01, on architecture's ruling for the empty-store deadlock.)* **No explicit
  waiting gauge.** A fresh environment waiting for its seed is identified by what already exists:
  `andara_content_zones_loaded` `0` on a started pod, `/readyz` failing, and the server's `warn`
  line on entering the wait. `AndaraServerUnavailable` fires while it waits, which is true, and
  `server-unavailable.md`'s row for that case names `make content-seed ENV=<env>` as the first
  fix. A gauge would only restate `zones_loaded == 0` for one case.
- The chart's `startupProbe` moves to `/startedz` (ruling), with the 600 s budget unchanged. A
  waiting server is started, so the probe doesn't restart it. Readiness stays on `/readyz`.

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
- `AW-SRV-013`'s own series, from the `dev` server that publishes the seed. Run this sequence
  (SRE's §8 instrumentation check of `AW-SRV-013`, 2026-09-30) so that every family can move:
  1. a publish refused by validation;
  2. a valid publish;
  3. an activation of the unapproved version, refused;
  4. the Operator's self-approval;
  5. activate;
  6. rollback.

  Then observe:
  - `andara_content_publishes_total{outcome}` (`rejected`, `ok`), `validation_failures_total{code}`,
    `activations_refused_total{unapproved}`, `approvals_total{self_operator}`,
    `pointer_moves_total{direction,override}` and `blob_bytes_total` moving;
  - the RPC span tree in Tempo under the CLI's `cli.command`: `content.publish` →
    `content.validate`, `content.write_manifest`; `content.approve`; `content.activate` →
    `content.write_pointer`; and `audit.write` under each;
  - the `info`/`warn` lines in Loki with `actor_account_id`, `pack_id`, `version`, `session_id` and
    `trace_id`;
  - the boot-time `content.core_boot` root span. It's already observed on the local stack, so
    re-observe it on `dev`.
- `AW-CLI-003`'s own items (its §8 instrumentation check, 2026-09-30): run with `--log-level info`,
  the activation's `info` confirmation line carries the `trace_id` the CLI sent, and the trace from
  `cli.command` through `content.activate` is joined to the Loader's `content.load` → `content.swap`
  trace by a span link (`AW-SRV-045`), or, until that ships, by `pack@version` and time. *(Amended
  2026-10-01, as above.)*

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

## Build record (SRE, 2026-10-01): held for architecture's ruling

On `sre/aw-inf-021-dev-content-store`, a **draft** PR. It can't merge until architecture rules on
the empty-store deadlock (`docs/feedback/AW-INF-021-dev-content-store.md`, "an empty store can't be
seeded"). Merging the values change alone would crash `dev`, because its store is empty. Built so
far, all independent of that ruling:

| Part | What | Verified |
|------|------|----------|
| `values/dev.yaml` | `content.source: kafka`, `content.packs: "*"`, `contentVolume.render: false` | `make k8s-dry ENV=dev` |
| AC-8 | `helm_test.test_content_configmaps`: `dev` and `prod` render neither content ConfigMap, mount nothing at `/content` (server or projector), and carry no `checksum/content`. `dev` follows `kafka` and `*`. `local` still renders and mounts both. `test_content_checksum` now runs on `local` | mutation-checked: putting `dev`'s render back fails it four ways, and `source: dir` fails it once |
| `make content-seed ENV=<env>` | `scripts/content_seed.sh`, product commands only. An already-active `town` publishes nothing (AC-3). An earlier run's unactivated version is activated, not published again. `server info` is polled to a deadline | `scripts/tests/test_content_seed.py`, 8 cases against a fake `andara-cli`: the refusals and AC-9 make no call; AC-3; a fresh seed; a resumed one; a Builder's unactivated version left alone; a deadline. Mutation-checked: without AC-3's early exit, its test fails |
| `argocd-status` | one `content <pack>@<version>` line per pack, from `andara_content_active_version` on `andara-0`. It reads them through a short `kubectl port-forward`, because the namespace's NetworkPolicy admits nothing from the API server, so the pod proxy times out | 3 unit cases. Live on `dev` today: `content      dir@0`, which is right before the switch |
| Runbooks and SLO | `content-freshness.md`'s `dir` gap names `local` and compose only. `content-load-failing.md` gets the `dev` line. `server-unavailable.md` gets two rows: the boot core's exit, and the "no content in effect" exit (the open deadlock) | — |

**Still to build, once ruled:** the bootstrap route the ruling picks (in `content-seed` and,
if option 1, the startup probe), and then the rollout: merge, `world-reset`, seed, and the
verification record (ACs 1–7, 11, and the inherited observations).

**For the rollout, not yet checked:** `dev`'s `admin.allowedCIDRs` is the chart default,
`10.0.0.0/8` and `192.168.0.0/16`. On the box, `local` needed `172.16.0.0/12` because traffic reaches
Traefik from the docker bridge. If `dev` sees the same source, the seed's Admin calls are refused
at the edge. The first `content-seed` run will show it.

### Ruling applied (SRE, 2026-10-01)

Architecture ruled option 1, with route (a) and `/startedz` (#289). SRE's half is on this branch:
- **`startupProbe` → `/startedz`**, with the budget unchanged. `helm_test.test_probes` asserts the
  three paths.
- **`content-seed` dials a port-forward to `andara-0`'s gRPC port.** It verifies
  `andara-0.andara.andara-<env>.svc` (`server.tls_server_name` in its private CLI config) against
  the `ca.crt` of `andara-server-tls`, reading only that key. The edge isn't used, so the Admin CIDR
  risk noted above no longer applies to the seed. A new unit case checks the config the CLI gets.
- **`server-unavailable.md`:** a fresh environment waiting for content is its own row (true page,
  first fix `make content-seed`). The exit-1 row is now only for a World that lost its content.
- **Observability:** no waiting gauge (the section above says why).

**Still held.** This branch now needs the implementation story architecture asked PM to place:
the server's unready wait and `/startedz`, and the CLI's `server.tls_server_name`. Until it merges,
`local`'s pods would never pass `/startedz`, and the CLI would reject the config key. AW-INF-021
then depends on that story, and its rollout follows its merge.

### `dev` trusts the inbound `traceparent` (SRE, 2026-10-01)

Architecture's §8 pass (#296) left it to SRE whether `dev` sets
`telemetry.trust_inbound_traceparent`. **It does; `prod` stays `false`.** With it on, the CLI's
`cli.command` parents the server's RPC span, so the inherited observation reads **parented**: one
trace from the Builder's command through `content.activate` to the World's `content.swap`, and the
audit record's `trace_id` is the CLI's. Off, the server's span would be a root linked to the CLI's
trace, and the audit record would carry the server's own ID. That reading is allowed, but it's two
traces to join by hand. A client choosing its own sampling costs nothing on `dev`, which is reached
only over the tailnet or a port-forward. `helm_test` asserts `dev` `true` and `prod` unset or
`false` (mutation-checked). The verification record uses the parented reading.

### Review of #288 (Codex, 2026-10-01)

- **Fixed, P1: `world-reset` would time out on the first switch.** It waited up to 600 s for Ready,
  but with an empty store the restarted server waits, unready, for the seed that follows the
  reset. `wait_up` now accepts either Ready, or the server container started, not Ready, and
  `waiting for content` logged since it started. In that case it says
  `andara-0 is started and waiting for content … Next: make content-seed ENV=dev` and ends with
  AC-11's line. Anything else still fails at the deadline. 4 unit cases (`WaitUp`).
- **Fixed, P2: the seed resumed on author alone.** An unactivated `town` version by the operator is
  now resumed only if `content fetch` gives back the fixture's sources byte for byte. An operator's
  own draft stays inactive, and the fixture is published on top. Mutation-checked: without the
  comparison, the new case fails.
- **Fixed, P2: `CONTENT_SEED_TIMEOUT=1m` aborted the seed after activation.** It's parsed as a
  duration (`Ns`, `Nm`, `Nh`, or plain seconds) before any RPC, and a bad value is usage, exit 2.
  Two new cases.

## Verification record (SRE, 2026-10-01): the rollout on `dev`

`main` at `0bee3cc` (#288). Argo CD synced, and `andara-config` showed `ANDARA_CONTENT_SOURCE=kafka`.
`andara-0` then crash-looped as the Data section predicts:
`recovery: replay: tick 2: prepare dir@0: no manifest for dir@0`. The boot had already published
`andara.core@1` to the store.

**The switch, 17:35:27Z to 17:36:25Z:**
1. `make world-reset ENV=dev CONFIRM=andara-dev`, 43 s, exit 0. Its steps: Argo sync suspended, the
   projector stopped, the four World topics recreated, the bucket emptied, the group deleted, the
   server scaled up. Then `andara-0 is started and waiting for content … Next: make content-seed
   ENV=dev`, Argo restored, and AC-11's final line.
2. `make content-seed ENV=dev`, 4 s, with the operator from `.local/box.env` (Brian, this session):
   `town@1 published (14 of 14 blobs uploaded)`, activated with `--override`, then
   `published and active`, through the port-forward with `andara-0.andara.andara-dev.svc` verified.
   `andara-0` went from started, not ready, to Ready, with 0 restarts.

| AC | Observed | Result |
|----|----------|--------|
| 1 | `server info` (port-forward route): `content andara.core@1`, `content town@1`. No `dir`. `make argocd-status` lists the same | pass |
| 2 | New Account `sre-player` and Character `Verifier`: `Verifier  dormant  purgatory/start`. `andara-cli play` through the edge opens on `Purgatory` | pass |
| 3 | A second `make content-seed ENV=dev`: `town@1 is already active; nothing published` | pass |
| 4 | After #305 (Admin through the edge, from the box on the tailnet): Builder `sre-builder`, granted pack `sre.verify`, published it, the Operator approved it, and the Builder activated it at 17:55:55Z. `server info` listed `sre.verify@1` within 2 s. `andara_content_active_version{pack="sre.verify"}` is `1` in Grafana Cloud. The pack is a scratch pack, not `brian`, so Brian's namespace stays empty for the demo | pass |
| 5 | `kubectl rollout restart statefulset/andara`: Ready again, `recovered from the log` (1,794 ticks), `andara.core@1 present`, `town@1` still active. Nothing re-seeded | pass (by restart; a merge roll is the same path) |
| 6 | **owed to the first core bump**, as the AC allows. `content/core/VERSION` is still 1 |
| 7 | Partly observed: `world-reset` gave this rollout an empty World and a store with no Zone pack. Then the seed, with no other hand step, gave AC-1 and AC-2. A full rebuild from a deleted namespace wasn't run (the AC-7 procedure is now `env-destroy` onward, 2026-10-02) | partial |
| 8 | `helm_test.test_content_configmaps`, in CI | pass |
| 9 | `scripts/tests/test_content_seed.py`, no call without the credential | pass |
| 10 | `scripts/tests/test_world_reset.py` | pass |
| 11 | The final line and exit 0. The operator logs in. The old Accounts are gone (`andara.accounts.v1` recreated). `andara.audit.v1`'s summed high-water mark went 4 → 8, not lower. Packs: none had Zones before, and `andara.core@1` is kept | pass |

**Inherited observations, from Grafana Cloud** (`namespace="andara-dev"`):
- `andara_content_active_version{pack}`: `andara.core` 1, `town` 1. `andara_content_pending_seconds`:
  0 for both. `andara_build_info{pack,content_version}`: both at `content_version="1"`, `version="0bee3cc"`.
- `andara_content_load_phase_duration_seconds{phase}` counts: `resolve` 5, `validate` 4, `build` 5,
  `swap` 2. `andara_content_cache_hits_total`: `hit` 28, `miss` 4.
  `andara_content_reload_stall_seconds_count` 2. `andara_content_zones_loaded` 4.
- Tempo: `content.core_boot` from the boot. `content.reconcile` → `content.load` (`andara.core@1`) →
  `content.swap`. The seed's `town@1` load: `content.load` → `content.resolve`, `content.validate` →
  `content.build`, `log.produce`, `content.swap`.
- **The CLI half is parented** (`trust_inbound_traceparent`): the seed's trace `871ca65d…` has
  `Admin/GetVersion`, `Admin/ListVersions` and `Admin/ActivateVersion` under the CLI's remote
  `cli.command`, and `content.activate` → `content.resolve`, `content.validate`,
  `content.write_pointer` (`direction=forward`), `audit.write`.
- **Not one trace to the swap.** The Loader's `content.load` for `town@1` is a separate root
  (`18a49934…`). It doesn't continue the activation's trace, because `Loader.Follow` starts it from
  its own context, and `content.v1.ActiveVersion` carries no trace context to continue. The
  Observability section's "one trace, from the CLI's `cli.command` through `ActivateVersion`, to the
  Loader's `content.load`, and on to the tick's `content.swap`" can't hold without a contract
  change. That's routed to architecture (feedback file). Today the two halves join on
  `pack@version` and time.
- **`AW-SRV-013`'s publish-path sequence**, run at 17:55 to 17:56Z through the edge, as the inherited
  line asks:
  1. a publish refused by validation (a Zone named `town`: `duplicate_zone`);
  2. a Builder's valid publish (`sre.verify@1`);
  3. an activation refused, `unapproved`;
  4. the Operator's approval;
  5. activate;
  6. an Operator publish (`@2`) and its self-approval;
  7. activate `@2`, then `rollback` to `@1`.

  Grafana Cloud then read, matching the server's own `/metrics`:
  - `publishes_total` `ok` 2, `rejected` 1;
  - `approvals_total` `ok` 1, `self_operator` 1;
  - `pointer_moves_total` `forward,false` 2, `rollback,false` 1;
  - `activations_refused_total{unapproved}` 1;
  - `blob_bytes_total` 1,255;
  - `validation_failures_total` `duplicate_zone` 1, plus `unknown_room` 3 (see #312).

  Loki has `content published`, `content approved`, `content approved by its own publisher`
  (`self_approval=true`), `content activated` (`direction` forward or rollback),
  `content publish rejected` (`findings_count=4`, `code=duplicate_zone`) and
  `content activation refused` (`reason=unapproved`). Each carries `actor_account_id`, `pack_id`,
  `version` and `trace_id`. `session_id` is empty, as on every Admin line (noted at `AW-SRV-035`).
  Tempo has the `content.publish`, `content.publish_blob` and `content.approve` traces.
- **`AW-CLI-003`'s items:** the Builder's `activate --log-level info --output json` logged the
  confirmation at `info` with `trace_id eabb657a…`, the same ID as its JSON result. That's the join
  to the server's audit record.
- **Found: #312.** A publish shows other packs' findings as the Builder's own. The refused publish
  listed three cascade `unknown_room` from the fixture's Zones, labelled `sre.verify/docks.json:0:0`
  and so on. Every valid publish shows the fixture's Purgatory `missing_reverse_exit` the same way.
  It's noise in the demo, so it's filed for implementation.
- **Still owed:** `andara_content_pending_seconds{pack}` rising then clearing on a slow apply. Every
  apply here took under a second. Also a refused *load* on `andara_content_load_failures_total{reason}`,
  and `andara_content_relocations_total`. Their carrier is a version that removes an occupied Room,
  which a fixture seed can't make. That's named in the story, and AW-INF-023's walk-through or a
  later content change can observe it.

**Left on `dev`:** Accounts `sre-player` (Character `Verifier`, from AC-2) and `sre-builder` (`builder`,
pack `sre.verify`), and pack `sre.verify@1` active: Zone `sreverify`, two Rooms. There's no deactivate,
and the store keeps every version. The projector stays at 0 replicas until #143's fix rolls out.

## §8 review (architecture, 2026-10-01): stays `review` on AC-7

Against `main` after #288 and #305. SRE's verification record (2026-10-01, above) is accepted, AC by
AC:
- **1, 2, 3, 4, 5, 8, 9, 10 and 11 pass.**
- **AC-5 is observed by a rollout restart.** That's the same recovery path a merge roll takes.
- **AC-4 is observed after #305.** It ran the whole publish → refused → approve → activate →
  self-approve → rollback sequence through the edge from the tailnet.

**AC-6 is owed to the first core bump,** as the AC allows. Its carrier is the first story that changes
`content/core/VERSION`. That story inherits the line: a roll with the bumped core reports it published
and activated, and every Builder pack stays active.

**AC-7 is the one item that holds the story.** It's the rebuild-from-nothing case, run as AC-7 now
reads: `env-destroy`, `kafka-install`, `objectstore-install`, `argocd-install`, then the seed.
*(Amended 2026-10-02: this said `argocd-uninstall`, fresh topics, and `argocd-install`, which keeps
the namespace and its Secrets. That procedure is superseded, and a run of it doesn't satisfy AC-7.
See the contract amendment below.)*
- **What `world-reset` showed:** it gave the rollout an empty World and a store with no Zone pack, so
  the waiting, seed, ready path is observed. That's `AW-SRV-042`'s behaviour on `dev`.
- **What it didn't:** a namespace recreated from nothing: Secrets, the object store, the topic apply,
  Argo CD's first sync.
- **When:** after SPRINT-03's demo. It wipes `dev`, and the demo is about to use it. It doesn't hold
  `AW-INF-022` or `AW-INF-023`.

**The one-trace wording is ruled** (feedback, "Architecture: the activation and the swap"). It's
SRE's option 1: `ActiveVersion.trace_parent`, with the Loader's `content.load` *linking* to the
activation. That's new work, routed to PM. The Observability line, and `AW-CLI-003`'s inherited line,
read as two traces joined by a span link. Until that field ships, they join on `pack@version` and
time, as observed. It doesn't hold this story.

**Inherited lines still unobserved:**
- `andara_content_pending_seconds` rising on a slow apply;
- a refused load on `load_failures_total{reason}`;
- `relocations_total` with its `warn` line.

Their carrier is a version that removes an occupied Room, so AC-3 of the M3 walk-through
(`AW-INF-023`) or a later content change observes them. They're named here, and not owed by this
story.

**#312**, publish showing other packs' findings as the Builder's own, is filed for implementation.
It's noise on the demo path, not a correctness failure.

### `dev`'s Admin edge admits the box's docker bridge too (SRE, 2026-10-02)

Brian's M3 walk-through got `403` on `Admin/CreateAccount` at 14:53:30Z, with Traefik's
`ClientHost 172.19.0.1`, the kind network's gateway (`172.19.0.0/16`). Two minutes later the same
box's `Admin/GetServerInfo` arrived as `100.79.240.98` and passed (14:55:46Z), as #305's evidence did.
Both used `andara-dev.solo7.valesordev.com:443`. The difference is the path into kind's published
port. `docker-proxy` holds `0.0.0.0:443`:
- a connection Docker's iptables DNAT carries keeps its source, which is the tailnet's IPv4 address;
- one the userland proxy carries is re-originated from the bridge gateway. That's loopback, and
  plausibly the tailnet's IPv6 address, which MagicDNS can return, and Go may race v4 against v6.

Which path Brian's client took isn't confirmed. `admin.allowedCIDRs` now adds `172.16.0.0/12`, as
`values/local.yaml` has it, so either path is admitted. `helm_test` asserts it (mutation-checked).
**The cost:** anything that reaches the box's port 443 through Docker's proxy is admitted too, not
only the tailnet. `dev` is reachable by name only on the tailnet. A tighter fix keeps the source
address on every path, by turning off the userland proxy, or by binding kind's port to the tailnet
address only. That's a box-level change, noted here for `AW-INF-012`, which forwards the client
address.

## Contract amendment (architecture, 2026-10-02): AC-7 starts from a deleted namespace

AC-7 is amended above. The confirming run starts by deleting `andara-dev`, as SRE's first run did
and SPRINT-03's plan says.

**The target is SRE's, and it's new:** `make env-destroy ENV=<env> CONFIRM=andara-<env>`.
- It deletes the namespace and waits until it's gone, including the Secrets and Strimzi's resources
  in it.
- It refuses `ENV=prod`, and any `CONFIRM` that isn't the namespace, with exit 2, as `world-reset`
  does.
- It leaves the cluster-wide operators installed. `make kafka-install`, `make objectstore-install`
  and `make argocd-install`, in that order, recreate everything else, and `kafka-install` re-binds the
  namespace (the RoleBinding fix). *(Revised 2026-10-02, on SRE's finding: `argocd-install` needs a
  Ready `andara-log` and doesn't install the object store that `dev`'s values need, so AC-7 names both
  targets before it. Each step stays one target with one failure, rather than folding provisioning
  into `argocd-install`.)*
- It removes the Argo CD Application without cascading before the delete, and fails closed if it
  can't read it, because self-heal would otherwise recreate objects mid-delete (SRE, as built).
- Its last line is `env-destroy: andara-<env> deleted`, and it's idempotent: a missing namespace is
  exit 0.

Without the target, the step is a hand-written `kubectl delete namespace`, a CLAUDE.md §9 defect. So
the confirming run waits for it, for the RoleBinding fix, for SRE's `argocd-install` fix (accepting
"waiting for content"), and for #326.
