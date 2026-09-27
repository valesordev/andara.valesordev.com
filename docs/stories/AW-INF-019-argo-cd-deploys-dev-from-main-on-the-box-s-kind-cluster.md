---
id: AW-INF-019
title: Argo CD deploys dev from main on the box's kind cluster
epic: EPIC-01
component: infra
type: infra
status: review
size: M
depends_on: [AW-INF-013, AW-INF-014]
blocks: [AW-INF-021]
lane: architecture
risk: medium
---

## Context

SPRINT-01 closed with a server an operator can play (the M1 gate), and `AW-INF-013` publishes it to
ghcr as `:sha-<12-hex>` and `:dev` on every merge to `main`. Nothing moves `dev` onto that build
afterwards, though. `dev` runs whatever `make helm-install ENV=dev` last installed, by hand, from
whichever checkout ran it. While sprints run, Brian wants a World on the box that follows `main` on
its own, so he can play and poke at what each merge landed without redeploying.

**Decided 2026-09-26 (Brian): Argo CD, installed in the kind cluster on the box, keeps `dev` in step
with `main`.** That makes the deploy a pull: the cluster reconciles itself from git and the
registry, and nothing on CI's side reaches into the box. The box isn't reachable from GitHub, so
this is also the only shape that works without opening it up. `prod` stays on `make deploy`
(`AW-INF-007`), and so does `local`, which runs a kind-loaded image and never comes from `main`.
This story reconciles the two deploy paths. `AW-INF-007` names `make deploy` "the only deploy path",
and `docs/feedback/AW-INF-007-deploy-lifecycle.md` asks architecture to scope that line to `prod`.

## User story

As Brian, operating the box, I want `dev` to run the build of every merge to `main` without anyone
running a deploy, so that I can play the current World while the sprint is still under way.

## Scope

### In scope
- `make argocd-install`: installs Argo CD into namespace `argocd` from its Helm chart, at a
  version pinned in the script, as `scripts/kafka.sh` pins Strimzi. It's idempotent, and a no-op
  when the pinned version is already installed.
- One Argo CD `Application`, `andara-dev`, declared in `deploy/argocd/`. It renders
  `deploy/helm/andara` with `deploy/helm/values/dev.yaml` from `main` of the public repo, into
  namespace `andara-dev`, with automated sync, `prune` and `selfHeal`.
- **A new build reaches `dev` without a commit by hand.** When `publish` moves `:dev`, `dev` moves
  to it, and the Application records which build it runs. The mechanism is Argo CD Image Updater,
  tracking `:dev` by digest, with `argocd` write-back (contract review, item 1). Nothing writes to
  `main`.
- **What `helm_install.sh` builds outside the chart comes from git under Argo CD.** That means the
  `andara-content` and `andara-content-templates` ConfigMaps, from `testdata/content/valid` and
  `content/core/templates`. A fixture change on `main` has to reach `dev` the same way a code
  change does. The chart renders both, through symlinks under `deploy/helm/andara/files/`, for
  `helm-install` and Argo CD alike (contract review, item 3).
- **The Secrets stay out of git.** `andara-server-token-key` and `andara-server-bootstrap` are
  created by `make argocd-install ENV=dev`, under the same rules `helm_install.sh` applies today:
  `ANDARA_BOOTSTRAP_OPERATOR` is required, and a Secret that already exists isn't overwritten. The
  Application doesn't manage them, so a sync never prunes them.
- **One owner for `andara-dev`.** Once the Application exists, `make helm-install ENV=dev` refuses,
  pointing at `make argocd-status`.
- **The `app.kubernetes.io/version` label is label-safe.** Today the chart writes `image.tag`
  into it verbatim. That includes a digest-pinned `dev@sha256:…`, which the API server rejects
  (not a valid label value, and over 63 bytes), so `make helm-install ENV=dev` can't apply as
  `AW-INF-013` left it. The label takes the tag with any `@digest` removed, truncated to 63 bytes
  and trimmed to end on an alphanumeric. This is `AW-INF-013`'s defect, #106. It's
  listed here because this story's image references are digests too, and whichever story lands
  first fixes it.
- **The move from the Helm release to Argo CD keeps the data.** The existing `andara` release in
  `andara-dev` is adopted, and its snapshot PVC and namespace Kafka survive (see "Data / state
  impact").
- `make argocd-status`: prints the Application's sync and health, the revision of `main` it synced,
  and the image the `andara` StatefulSet runs.
- `make argocd-ui`: port-forwards the Argo CD UI to `localhost:8090` and prints where the initial
  admin password is read from.
- `deploy/helm/andara/README.md`: a "`dev` follows `main`" paragraph, and the Environments table
  says who deploys each environment.

### Out of scope
- **`prod`.** It stays on `make deploy` / `make rollback` (`AW-INF-007`), with its pre-stop
  snapshot, core-pack activation and interruption measurement. Whether `prod` ever moves to Argo CD
  is its own decision, taken when `prod` has users.
- **`local`.** It runs a kind-loaded image (`make image && make kind-load`), not `main`.
- **A public hostname for the Argo CD UI.** The box's Traefik and Let's Encrypt could serve one, but
  a UI that can deploy to the cluster isn't something to put on a public name in this story. A
  port-forward is enough for one operator.
- **A GitHub webhook into the box.** Argo CD polls. The box isn't reachable from GitHub.
- **`AW-INF-007`'s deploy lifecycle on `dev`.** Until that lands, a sync that changes the image is a
  StatefulSet rolling update, which is a full-World restart with recovery. That's the same thing
  `make helm-install ENV=dev` does today. When `AW-INF-007` lands, its pre-stop hook runs inside the
  pod whoever triggers the roll. Its `andara.core` activation step is the part to reconcile
  (feedback file item 2).
- **Notifications** (Slack, email) on sync or failure.
- **Argo CD managing Strimzi, the Kafka cluster, Traefik, cert-manager or the private CA.** These are
  cluster platform, owned by `make kind-platform`, `make kafka-operator` and `make kafka-install`.
  The Application's `prune` must never reach them.

## Acceptance criteria

1. **Given** the box's kind cluster with no Argo CD **when** `make argocd-install` runs **then**
   Argo CD at the pinned version is Ready in `argocd`, and a second run changes nothing and exits 0.
2. **Given** `andara-dev` installed today by `make helm-install ENV=dev`, with Characters on its
   Kafka **when** `make argocd-install ENV=dev` creates the Application **then**:
   - the Application reaches `Synced` and `Healthy`;
   - the `andara` StatefulSet's PVC, the `andara-log` Kafka and its topics are the same objects as
     before (same UIDs);
   - `andara-cli character list` for an Account made before the move shows the same Characters.
3. **Given** the Application `Synced` **when** a PR that changes `server/` merges to `main` and
   `publish` completes **then**, within 10 minutes of `publish` completing, the `andara` pod in
   `andara-dev` runs the image whose `org.opencontainers.image.revision` is the merge commit. The
   server's `andara_build_info{commit}` is a prefix of that commit, and `make argocd-status` names
   it. The time is polled to that deadline, per `live-assertions.md`.
4. **Given** the Application `Synced` **when** a PR that changes only `testdata/content/valid/`
   merges **then** the `andara-content` ConfigMap in `andara-dev` matches `main` within the same
   deadline, and the StatefulSet rolls.
5. **Given** a `publish` run that moves `:dev` **when** the merge it built changed nothing the
   Application renders **then** `andara-dev` rolls once for it: one new pod, one World restart.
   **Given** a merge that changes what the Application renders (AC-4) **then** it rolls at most
   twice. Argo CD's git poll applies the render, and Image Updater's registry poll applies the
   new digest, and the two aren't coordinated. **Given** no new build and no change to what the
   Application renders **when** Argo CD polls **then** no pod in `andara-dev` restarts.
   - `publish` has no path filter, so a merge that touches only a story file still moves `:dev`,
     and `dev` still rolls for it (contract review, item 4).
   - A merge whose pending `publish` run GitHub replaces gets no build of its own
     (`publish.yaml`'s concurrency group keeps one pending run). The run that replaced it builds
     `main`'s head, which contains that merge, so `dev` reaches it with that build: one roll for
     both. AC-3's deadline is measured from the `publish` run that built the merge.
   *(Amended at review of #107, 2026-09-26: the first draft said "rolls once" for every merge,
   which neither the render-plus-image case nor a replaced run can meet.)*
6. **Given** the Application **when** someone runs `kubectl edit` on a resource it manages, or
   deletes one **then** `selfHeal` puts it back to `main` within the deadline. When the change is
   deleting the Kafka CR, its topics, or either Secret, Argo CD leaves it alone, because it doesn't
   manage those.
7. **Given** the Application exists **when** `make helm-install ENV=dev` runs **then** it exits 1
   with `make: helm-install: andara-dev is deployed by Argo CD (application andara-dev); see make
   argocd-status`, and changes nothing.
8. **Given** a published build that fails its readiness probe **when** the Application syncs it
   **then** `make argocd-status` reports `Degraded` with the pod's reason and exits 1, and the
   previous pod's PVC is untouched. The StatefulSet is `OrderedReady`, so its rolling update stalls
   on the pod that never becomes Ready. A later good template doesn't replace that pod
   (Kubernetes' *Forced rollback*). **When** a good build has since synced, **then**
   `make argocd-recover ENV=dev` deletes the pod created from the bad revision, and `andara-dev`
   comes back on the good build. It refuses, and changes nothing, when the StatefulSet's
   `updateRevision` is still the failing one. The step stays an operator command (contract
   review, item 5), and `make argocd-status` names it when it reports the stall.
9. **Given** `make argocd-install ENV=dev` **when** `ANDARA_BOOTSTRAP_OPERATOR` is unset and
   `andara-server-bootstrap` doesn't exist **then** it exits 1 with the same message
   `helm-install` gives today. When the Secret already exists, it's left as it is, and the run
   succeeds without the variable.
10. **Given** the Application **when** `make argocd-uninstall ENV=dev` runs **then** the Application
    is removed without cascading. Every resource in `andara-dev` keeps running, and
    `make helm-install ENV=dev` works again. This is the rollback path. It holds for a resource
    the chart gained after the move, which Helm never created: `argocd-uninstall` gives every
    resource carrying the Application's tracking annotation Helm's ownership metadata
    (`meta.helm.sh/release-name: andara`, `meta.helm.sh/release-namespace: andara-dev`,
    `app.kubernetes.io/managed-by: Helm`) before it returns, so `helm upgrade --install` adopts
    them instead of refusing them.

## Interface contract

Make targets:

| Target | Behavior |
|--------|----------|
| `make argocd-install` | Argo CD, pinned chart version, namespace `argocd`; idempotent |
| `make argocd-install ENV=dev` | the above, then `dev`'s Secrets (if absent) and the `andara-dev` Application; refuses `ENV=local` and `ENV=prod` |
| `make argocd-status [ENV=dev]` | sync status, health, synced `main` revision, running image; exit 0 when `Synced`/`Healthy`, 1 otherwise |
| `make argocd-ui` | port-forward to `localhost:8090` (`ARGOCD_UI_PORT=` overrides); prints the admin-password command |
| `make argocd-recover ENV=dev` | the forced-rollback step (AC-8): deletes the `andara` pod still on a revision older than the StatefulSet's `updateRevision`, once that revision's image is the Application's current one; exit 1, changing nothing, otherwise |
| `make argocd-uninstall ENV=dev` | removes the Application without cascading; Argo CD itself stays |

- Preconditions `make argocd-install ENV=dev` checks, with the same messages `helm_install.sh` uses:
  - IngressClass `traefik` and the cert-manager CRDs exist (`make kind-platform`);
  - the `andara-ca` ClusterIssuer is Ready;
  - `kafka/andara-log` in `andara-dev` is Ready (`make kafka-install ENV=dev`).
- **The Application:**
  - `deploy/argocd/andara-dev.yaml` (or its generator), source `repoURL`
    `https://github.com/valesordev/andara.valesordev.com`, `targetRevision: main`,
    `path: deploy/helm/andara`, `helm.valueFiles: [../values/dev.yaml]`;
  - `helm.releaseName: andara`, so `.Release.Name`, and every label and name derived from it,
    render as they do under `helm-install`. Without it the release is named after the
    Application, and the adopted resources' `app.kubernetes.io/instance` changes on the first
    sync;
  - no `helm.parameters` in the file. Image Updater owns `image.tag` on the live Application, and
    a re-run of `make argocd-install` applies the file without clearing it;
  - **the first render is already pinned.** `values/dev.yaml` says `image.tag: dev`, so an
    Application created without a parameter would sync a StatefulSet naming the moving `:dev`,
    before Image Updater's first poll. That breaks the immutable-reference rule below, and the
    later rewrite to a digest is a second roll for the same build. So when `make argocd-install
    ENV=dev` creates the Application, or finds it with no `image.tag` parameter, it first:
    - resolves `:dev` to its digest with `scripts/image_digest.sh`, as `helm_install.sh` does;
    - creates or patches the Application with `image.tag=dev@<digest>`, before automated sync
      is on;
    - never overwrites an `image.tag` parameter that's already set, since that's Image
      Updater's.

    The seed is written in exactly the form Image Updater writes (`dev@sha256:<digest>`), so
    Image Updater's first poll finds the digest current and changes nothing. On the adoption in
    AC-2, the seed equals the digest `helm-install` pinned, unless `:dev` has moved since, so
    the move itself doesn't roll the pod.
  - `syncPolicy.automated: {prune: true, selfHeal: true}`;
  - Argo CD's resource tracking is by annotation, so resources `make kafka-install` labels
    `app.kubernetes.io/*` are never taken as the Application's.
- **Image:** the StatefulSet always names an immutable reference, a `sha-` tag or a digest, never
  the moving `:dev`. This is the same rule `AW-INF-013` gave `helm-install`. The Application's
  status shows which one.
  - **Argo CD Image Updater**, pinned and installed by `make argocd-install` in `argocd`, watches
    `ghcr.io/valesordev/andara-server:dev` with the `digest` update strategy and sets
    `image.tag` to `dev@sha256:<digest>` on the Application (`argocd` write-back). Anonymous
    pulls suffice, since `AW-INF-013` AC-2 makes the package public.
  - It tracks `:dev`, not the newest `sha-` tag, because `:dev` already carries `AW-INF-013`'s
    ordering guarantee: `image_publish.sh` moves it only when its commit is still `main`'s head.
    A `newest-build` sort orders by image creation time, and `publish` doesn't build in commit
    order, so it could deploy an older commit built late.
  - `make argocd-status` resolves the digest to the commit it was built from, via the image's
    `org.opencontainers.image.revision`, and prints both.
- **Content:** `deploy/helm/andara/files/content` and `files/content-templates` are relative
  symlinks to `testdata/content/valid` and `content/core/templates`. Helm follows a symlink in a
  chart directory, and Argo CD's repo server allows one that stays inside the repository.
  - With `contentVolume.render: true` (`values/local.yaml` and `values/dev.yaml`), the chart
    renders `andara-content` from `files/content/*.json`, and `andara-content-templates` from
    `files/content-templates/*.json`. The keys and bytes are the ones
    `kubectl create configmap --from-file` produces today.
  - The pod template carries `checksum/content` over both, so a fixture change rolls the pod
    (AC-4).
  - `helm_install.sh` stops creating the two ConfigMaps. One writer, whichever deploy path runs.
  - If the box's pinned Argo CD refuses the symlinks, the fallback is a generated copy under
    `files/`, which `make check` keeps byte-identical. Record it in the §8 record if used.
- **Environment:** `ANDARA_BOOTSTRAP_OPERATOR` (existing). No new variables beyond
  `ARGOCD_UI_PORT`.
- Exit codes: 0 success; 1 a precondition missing, a refusal, or not `Synced`/`Healthy`.

## Data / state impact

- **What persists across the move:** the `andara` StatefulSet's snapshot PVC, the namespace Kafka
  (`andara-log`, the command log and the Account store) and its topics. The Application adopts the
  chart's resources by name. Neither the PVC (a `volumeClaimTemplate`) nor the Kafka CR is in the
  chart's render, so `prune` can't delete them. AC-2 proves it by UID.
- **The stale Helm release:** once Argo CD owns the resources, `make argocd-install ENV=dev`
  removes the old release's history, never through `helm uninstall`, because that would delete
  the resources Argo CD just adopted. The exact step:
  - wait for the Application to report `Synced` and `Healthy`;
  - then `kubectl -n andara-dev delete secret -l owner=helm,name=andara`.
  If the Application never gets there, the release history stays, and `argocd-uninstall`
  followed by `helm-install` is a plain upgrade. On a namespace with no release, the step finds
  nothing to delete.
- **Rollback:** `make argocd-uninstall ENV=dev` (AC-10), then `make helm-install ENV=dev`, which
  takes the resources back as a fresh release. No data moves either way.
- **Live Sessions:** each sync that changes the pod template restarts the World (ADR-0001, until
  sharding). Every `publish` run moves `:dev` (AC-5), so with merges landing several times a day,
  `dev` restarts about that often, docs-only merges included. A merge that also changes the
  render can restart it twice. That's expected on `dev`, and it's the reason `prod` isn't in
  scope.

## Observability requirements

- **Metrics:** none new from the server. Argo CD's own `argocd_app_info{sync_status,health_status}`
  is scraped when `AW-INF-008`'s observability is wired, and is otherwise out of scope.
- **Logs:** the make targets print `argocd: <step>` progress lines, as `kafka.sh` prints `kafka:`.
- **Traces:** none.
- **Alerts:** none in this story. A `dev` stuck `Degraded` is visible on `make argocd-status` and the
  UI. An alert on it is `AW-INF-009`'s, if wanted.

## Test plan

- **Unit:** `scripts/tests/`:
  - the refusal in `helm_install.sh` when the Application exists (AC-7);
  - the `ENV` guard (`local` and `prod` refused);
  - the Secret rule (AC-9);
  - the image seed: an Application with no `image.tag` parameter gets `dev@<digest>` before sync is on, and one that already has a parameter keeps it.
- **Render:** `make k8s-dry` validates `deploy/argocd/` against the cluster API version and Argo CD's
  CRDs. `make helm-test` asserts:
  - the Application names `../values/dev.yaml`, `main`, `releaseName: andara`, and annotation
    tracking, and carries no `helm.parameters`;
  - the chart's `andara-content` and `andara-content-templates` equal
    `kubectl create configmap --from-file` over the same directories, key for key and byte for byte;
  - `checksum/content` changes when a fixture byte changes;
  - `app.kubernetes.io/version` is a valid label value for `image.tag=dev@sha256:<64 hex>`.
- **Integration (CI's kind job):**
  - `make argocd-install` twice (AC-1);
  - an Application pointed at the PR's own commit syncs `Healthy` against `local`-shaped values (no
    broker, kind-loaded image).
  This proves the install and render path. It can't prove the follow-`main` loop, which needs
  `publish`.
- **Integration (box, recorded):**
  - AC-2 on the real `andara-dev`;
  - AC-3 and AC-4 on the first two merges after the move, with the times, including how long Image
    Updater takes to see `:dev` move;
  - AC-6, AC-8 and AC-10.
  These go in the §8 verification record.
- **Manual/operator:**
  ```
  make argocd-install ENV=dev   # needs ANDARA_BOOTSTRAP_OPERATOR the first time
  make argocd-status            # Synced Healthy, main@<sha>, ghcr.io/…/andara-server:sha-<sha>
  make argocd-ui                # http://localhost:8090
  ```

## Definition of done

CLAUDE.md §8, plus:
- AC-3 observed on the box for a real merge, with the elapsed time recorded.
- `deploy/helm/andara/README.md` says `dev` follows `main`, and how to take it back (AC-10).
- The feedback file's `AW-INF-007` item has an answer recorded in `AW-INF-007`'s body.

## Open questions

- `[ASSUMPTION]` "The application" is the `dev` environment (`andara-dev`, `values/dev.yaml`), the
  one whose image already comes from `main` via `AW-INF-013`. `local` stays a kind-loaded
  developer loop, and `prod` stays on `make deploy`.
- `[ASSUMPTION]` 10 minutes from `publish` completing to the new pod serving is enough for "follows
  `main`". That covers Argo CD's default 3-minute poll, a registry check, and the World's restart
  and recovery. Architecture sets the real number after measuring it on the box.
- `[ASSUMPTION]` The Argo CD UI on a port-forward is enough for one operator on the box.
- ~~For architecture, in `docs/feedback/AW-INF-019-argocd.md`~~: answered in "Contract review"
  below.

## Contract review (architecture, 2026-09-26)

The answers to the five items in `docs/feedback/AW-INF-019-argocd.md`. Each is recorded in the body
above, and the story is `ready`.

1. **Image Updater, `argocd` write-back, tracking `:dev` by digest.** This is PM's (a), with one
   change: `digest` on `:dev`, not `newest-build` over `sha-` tags. `:dev` is the tag
   `AW-INF-013` keeps in commit order, and a creation-time sort isn't. Nothing writes to `main`,
   so there's no publish loop to guard. The record of what `dev` runs is `make argocd-status`,
   which resolves the digest to its commit.
2. **`AW-INF-007` governs `prod` and `local`, and `dev` is deployed by Argo CD.** The line is
   amended in `AW-INF-007`'s body, which also records what `dev` keeps and what it skips. The
   core-pack step has nothing to act on in `dev` while `dev` reads content from ConfigMaps
   (`content.source: dir`). The story that moves `dev` to the store carries it, as a chart hook
   Argo CD runs as `PreSync`.
3. **The chart renders the ConfigMaps, through in-repo symlinks,** for `helm-install` and Argo CD
   alike, with a `checksum/content` roll. A multi-source Application or a kustomize overlay would
   leave `helm-install` and Argo CD building the same objects two ways. A kustomize overlay would
   also need `LoadRestrictionsNone`, cluster-wide.
4. **`publish` keeps running on every merge**, with no `paths-ignore`. It builds every merge
   except one whose pending run GitHub replaces, which the next build contains (AC-5). A
   `paths-ignore` would break `AW-INF-013`'s `:dev` guard. Suppose a code merge A is followed at once by a docs merge B. A's run sees `main`'s head
   is B, and leaves `:dev` to B's run. B's run never happens, so `:dev`, and `dev`, stay one build
   behind until the next code merge. A World restart for a docs merge is the price on `dev`
   (AC-5).
5. **`make argocd-recover` stays an operator step.** An automated deleter on the StatefulSet would
   act on the one pod that holds the World, during the recovery it's meant to protect. It's a
   judgment call for one operator on one environment, and `argocd-status` names the command when
   it applies.

Also changed:
- `helm.releaseName: andara`.
- No `helm.parameters` in git. The first render is seeded with the resolved digest by
  `argocd-install` instead (review of #107).
- AC-5 allows two rolls for a merge that changes the render, and covers a replaced `publish` run
  (review of #107).
- Helm ownership metadata is given at `argocd-uninstall` (AC-10).
- The exact release-history step.
- The version-label defect, found while checking item 1. It's #106, against `AW-INF-013`.

## Verification record (architecture, 2026-09-26)

On `arch/aw-inf-019-argocd`. Box: `kind-solo7`, `andara-dev` on `main` at `5a9bba4`.

**Built:**
- `scripts/argocd.py` behind `make argocd-install [ENV=dev]`, `argocd-status`, `argocd-ui`,
  `argocd-recover ENV=dev` and `argocd-uninstall ENV=dev`. Argo CD's chart is pinned at 10.9.2
  (v3.5.3), and Image Updater's at 1.3.1 (v1.3.0).
- `deploy/argocd/`:
  - the Application;
  - the `ImageUpdater` (1.x configures through this CR, not Application annotations): `digest`
    on `:dev`, `argocd` write-back, `image.tag`;
  - Argo CD's settings.
- The chart renders both content ConfigMaps through `files/content` and `files/content-templates`
  (`contentVolume.render`, on for `local` and `dev`), with `checksum/content` on the pod template.
- `helm_install.sh` changes:
  - It stops creating the ConfigMaps, and hands any it created earlier to the release with Helm's
    ownership metadata, so the first upgrade adopts them instead of refusing.
  - It refuses a namespace an Application deploys.

| AC | Result |
|----|--------|
| 1 | **Pass (box).** The first `make argocd-install` installed both charts and waited for `argocd-server`. The second printed `already installed` for both, and the Helm revisions stayed at 1. CI's `kind` job runs the same pair |
| 2 | **Pass (box).** Before: a Character `Moverwyn` made on the operator Account through the public edge. Then `make argocd-install ENV=dev`: the Application was created with `image.tag` seeded `dev@sha256:55032871…` and sync off, then `deploy/argocd/` applied; the Application went `Synced`/`Healthy`, and the 3 Helm release records were removed. **Same objects by UID:** the PVC `26b813ca…`, `kafka/andara-log` `432d3198…`, the StatefulSet `9f17ec90…`, all 8 topic IDs (from `kafka-topics.sh --describe`), and **the pod `c9406373…`: the move didn't roll it.** `character list`: `Moverwyn  dormant  town/plaza`. Image Updater's first cycle: `images_considered=1 images_updated=0 errors=0`, so the seed is exactly what it writes |
| 3 | **Owed:** the first merge touching `server/` after this one |
| 4 | **Owed:** a merge touching only `testdata/content/valid/` |
| 5 | **Owed:** observed across the merges for AC-3 and AC-4 |
| 6 | **Pass (box).** An extra ingress rule patched into NetworkPolicy `andara` was gone within 5 s. The deleted PodDisruptionBudget `andara` was back within 5 s, without the label I'd added before deleting it. The Kafka CR, the topics, both Secrets and the PVC aren't in the Application's resources |
| 7 | **Pass (box).** `make helm-install ENV=dev`: exit 1, `make: helm-install: andara-dev is deployed by Argo CD (application andara-dev); see make argocd-status`, and the pod is unchanged. `test_argocd.HelmInstallRefusal` asserts the refusal is the last thing the script does (mutation-checked) |
| 8 | **Owed, after this merge.** Needs a build that fails readiness; the plan is in the PR |
| 9 | **Pass.** Box: both Secrets existed and the run left them as they were. `test_argocd.Secrets` covers the refusal when both the Secret and the variable are absent (the message is `helm-install`'s), existing Secrets untouched without the variable, and the operator going in on stdin, never argv |
| 10 | **Owed, after this merge**, so `dev` goes Argo CD → Helm → Argo CD on one chart. Run now, it would switch between this branch's chart and `main`'s and roll twice for nothing |

**Tests:**
- `scripts/tests/test_argocd.py` (9 tests: the `ENV` guard, the Secret rule, the image seed, and
  the refusal). The guard and the refusal are mutation-checked.
- `helm-test` additions:
  - `test_argocd_application`;
  - `test_content_configmaps`: key for key and byte for byte with the directories, and none for
    `prod`;
  - `test_content_checksum`: one byte changed in a copy moves `checksum/content`;
  - the existing `test_label_values` covers `dev@sha256:<64 hex>`.
- `k8s-dry [argocd]` validates:
  - the Application against the catalog schema;
  - the `ImageUpdater` against the pinned chart's own CRD, made strict. The catalog's is an older
    version's, requiring a `spec.namespace` 1.x dropped. A misspelled field fails it.
- The `kind` workflow runs `argocd-install` twice. It then applies the committed Application at
  the PR's commit with `local`'s values, adopting `andara-local`: `Synced`/`Healthy`, and the pod
  is not rolled.

**Deviations, recorded:**
- **Argo CD's settings are a values file** (`deploy/argocd/argocd-values.yaml`), and
  `argocd-install` reapplies them when the release's differ, not only when the chart version does.
- **An Ingress health check is added.** The box's Traefik publishes no Ingress status (it owns the
  host port; kind has no load balancer), and Argo CD's built-in check held both Ingresses at
  `Progressing` forever. The Application sat `Synced`/`Progressing` until the Lua check was in
  `argocd-cm` and a hard refresh ran.
- **The symlinks work in Argo CD 3.5.3's repo server.** No fallback copy was needed.
- **The pre-move Account is the operator's own.** The edge's Admin allowlist
  (`10.0.0.0/8`, `192.168.0.0/16`) refuses `account create` from the box, as AW-INF-006 AC-4
  intends. `character create` and `list` are Game API.
- **`argocd.py` is Python**, not shell like `kafka.sh`: most of it is JSON over `kubectl`.
- **Found while building it:** `make argocd-install ENV=local` installed Argo CD before refusing
  the `ENV`. It now refuses first, and `test_argocd.EnvGuard` holds it (mutation-checked).

## §8 pass (architecture, 2026-09-27): stays `review`

Against `main` at `3d34212`, #134 merged. Box: `kind-solo7`. `make check` is clean. `make argocd-status`:
`Synced main@3d34212f3b2f`, `Healthy`, the image `dev@sha256:d312259f…` `built from 3d34212f…`.

AC-3 and AC-5 are now observed, from Argo CD's sync history, Image Updater's log, the `publish`
runs, and `andara_build_info{namespace="andara-dev"}` in Grafana Cloud at a 15 s step:

| Time (UTC) | What happened |
|------------|---------------|
| 01:49:30–01:50:13 | #131, #132 and #133 merge; all three touch `server/` |
| 01:50:17 | `publish` for `2de1fe3` (#132) is cancelled, replaced by the next run |
| 01:50:36 | `publish` for `81dd3a4` (#131) succeeds. `AW-INF-013`'s guard leaves `:dev` to `main`'s newer head |
| 01:51:32 | Image Updater: `550328…` → `c079b8…` (the build of `ea7f21b`, #133). Sync history id 1, 01:51:33 |
| 01:51:34 | `publish` for `ea7f21b` completes |
| 01:52:29 → 03:08:14 | `andara_build_info{commit="ea7f21b"}`, 304 points, no gap. Sync history: nothing between id 1 and id 2 |
| 03:06:13–03:06:14 | #134 (the chart renders the content ConfigMaps) and #135 (a story file only) merge |
| 03:07:19 | Argo CD's git poll syncs `3d34212`'s render with the old digest (id 2). Roll 1 |
| 03:07:23 | `publish` for `3d34212` completes. It's the only run for #134 and #135 |
| 03:07:56 | Image Updater: `c079b8…` → `d31225…`, whose `org.opencontainers.image.revision` is `3d34212`. Roll 2; the pod started 03:07:57 |
| from 03:08:59 | `andara_build_info{commit="3d34212"}` |

| AC | Result |
|----|--------|
| 3 | **Pass (box).** `ea7f21b`, a `server/` merge, was running on `dev` within 1 min of its `publish` completing. The deadline is 10 min. The image's revision label is `ea7f21b67cac…`, and `andara_build_info{commit}` is `ea7f21b`, a prefix of it. `argocd-status` names the build on `3d34212` the same way. #131 and #132 reached `dev` in that build: one roll for three merges, as AC-5's replaced-run clause allows |
| 4 | **Owed.** It needs a merge that changes only `testdata/content/valid/`, which is implementation's path. #134 changed what's rendered, so the `andara-content` ConfigMap on `dev` now comes from `main`'s chart. The fixture-only case hasn't happened |
| 5 | **Pass (box) on all three clauses:** <br>• **No new build and no render change: no restart.** 75 min and about 25 git polls between 01:52 and 03:07. <br>• **A merge that changes the render: two rolls**, render first, then the image. That's the "at most twice" the amendment allows. <br>• **A story-only merge moves `:dev`:** #135 went out in the same build as #134 |
| 8 | **Owed.** It needs a published build that fails readiness. That's a deliberate bad `:dev`, which takes `dev` down until `argocd-recover`. It's run with Brian, at a time he picks |
| 10 | **Owed.** The Argo CD → Helm → Argo CD round trip on `main`'s chart. It's now possible, since the chart is merged. It rolls `dev` twice, so it's run in the same session as AC-8 |

The rest of the verification record stands: 1, 2, 6, 7 and 9 pass. So do the tests,
`k8s-dry [argocd]`, and the `kind` job.

**What closes it:** a fixture-only merge (AC-4), then one box session for AC-8 and AC-10. Neither
fits SPRINT-02, which ends when PM closes it. This is carried to SPRINT-03, and the feedback file
has it for PM.
