---
id: AW-INF-019
title: Argo CD deploys dev from main on the box's kind cluster
epic: EPIC-01
component: infra
type: infra
status: draft
size: M
depends_on: [AW-INF-013, AW-INF-014]
blocks: []
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
  `deploy/helm/andara` with `values/dev.yaml` from `main` of the public repo, into namespace
  `andara-dev`, with automated sync, `prune` and `selfHeal`.
- **A new build reaches `dev` without a commit by hand.** When `publish` pushes a new build, `dev`
  moves to it, and the Application records which `sha-` tag it runs. The mechanism is the open
  question under "For architecture" in `docs/feedback/AW-INF-019-argocd.md`.
- **What `helm_install.sh` builds outside the chart comes from git under Argo CD.** That means the
  `andara-content` and `andara-content-templates` ConfigMaps, from `testdata/content/valid` and
  `content/core/templates`. A fixture change on `main` has to reach `dev` the same way a code
  change does.
- **The Secrets stay out of git.** `andara-server-token-key` and `andara-server-bootstrap` are
  created by `make argocd-install ENV=dev`, under the same rules `helm_install.sh` applies today:
  `ANDARA_BOOTSTRAP_OPERATOR` is required, and a Secret that already exists isn't overwritten. The
  Application doesn't manage them, so a sync never prunes them.
- **One owner for `andara-dev`.** Once the Application exists, `make helm-install ENV=dev` refuses,
  pointing at `make argocd-status`.
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
5. **Given** a merge to `main` **when** `publish` pushes its build **then** `andara-dev` rolls once
   for it: one new pod, one World restart. **Given** no new build and no change to what the
   Application renders **when** Argo CD polls **then** no pod in `andara-dev` restarts. `publish`
   has no path filter today, so a merge that touches only a story file still builds a new
   `sha-` image, and `dev` still rolls for it. Whether `publish` should skip merges that can't
   change the image is `AW-INF-013`'s contract (feedback item 4). If it starts skipping them,
   this AC tightens to "a merge `publish` skips restarts nothing".
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
   `updateRevision` is still the failing one. Automating this step instead is open to
   architecture (feedback item 5).
9. **Given** `make argocd-install ENV=dev` **when** `ANDARA_BOOTSTRAP_OPERATOR` is unset and
   `andara-server-bootstrap` doesn't exist **then** it exits 1 with the same message
   `helm-install` gives today. When the Secret already exists, it's left as it is, and the run
   succeeds without the variable.
10. **Given** the Application **when** `make argocd-uninstall ENV=dev` runs **then** the Application
    is removed without cascading. Every resource in `andara-dev` keeps running, and
    `make helm-install ENV=dev` works again. This is the rollback path.

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
    `https://github.com/valesordev/andara.valesordev.com`, `targetRevision: main`;
  - `syncPolicy.automated: {prune: true, selfHeal: true}`;
  - Argo CD's resource tracking is by annotation, so resources `make kafka-install` labels
    `app.kubernetes.io/*` are never taken as the Application's.
- **Image:** the StatefulSet always names an immutable reference, a `sha-` tag or a digest, never
  the moving `:dev`. This is the same rule `AW-INF-013` gave `helm-install`. The Application's
  status shows which one.
- **Environment:** `ANDARA_BOOTSTRAP_OPERATOR` (existing). No new variables beyond
  `ARGOCD_UI_PORT`.
- Exit codes: 0 success; 1 a precondition missing, a refusal, or not `Synced`/`Healthy`.

## Data / state impact

- **What persists across the move:** the `andara` StatefulSet's snapshot PVC, the namespace Kafka
  (`andara-log`, the command log and the Account store) and its topics. The Application adopts the
  chart's resources by name. Neither the PVC (a `volumeClaimTemplate`) nor the Kafka CR is in the
  chart's render, so `prune` can't delete them. AC-2 proves it by UID.
- **The stale Helm release:** once Argo CD owns the resources, the old release's `sh.helm.release`
  Secret in `andara-dev` is removed by `make argocd-install ENV=dev`, with `helm uninstall` never
  run, because that would delete the resources Argo CD just adopted. Architecture states the exact
  step.
- **Rollback:** `make argocd-uninstall ENV=dev` (AC-10), then `make helm-install ENV=dev`, which
  takes the resources back as a fresh release. No data moves either way.
- **Live Sessions:** each sync that changes the pod template restarts the World (ADR-0001, until
  sharding). Every merge publishes a new image (AC-5), so with merges landing several times a day,
  `dev` restarts that often, docs-only merges included, until `publish` skips them. That's expected on
  `dev`, and it's the reason `prod` isn't in scope.

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
  - the Secret rule (AC-9).
- **Render:** `make k8s-dry` validates `deploy/argocd/` against the cluster API version and Argo CD's
  CRDs. `make helm-test` asserts the Application's render names `values/dev.yaml`, `main`, and
  annotation tracking.
- **Integration (CI's kind job):**
  - `make argocd-install` twice (AC-1);
  - an Application pointed at the PR's own commit syncs `Healthy` against `local`-shaped values (no
    broker, kind-loaded image).
  This proves the install and render path. It can't prove the follow-`main` loop, which needs
  `publish`.
- **Integration (box, recorded):**
  - AC-2 on the real `andara-dev`;
  - AC-3 and AC-4 on the first two merges after the move, with the times;
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
- For architecture, in `docs/feedback/AW-INF-019-argocd.md`:
  1. how a new build reaches the Application;
  2. `AW-INF-007`'s "only deploy path";
  3. rendering the content ConfigMaps from git;
  4. whether `publish` skips merges that can't change the image;
  5. automating the forced-rollback step.
