---
id: ADR-0013
title: One cluster for local and dev
status: proposed          # draft | proposed | accepted | rejected | superseded by ADR-XXXX
date: 2026-10-10
deciders: [brian]
gates: []                 # the SRE stories this ADR names (decision 11) are written by PM after acceptance
---

## Context

Until 2026-10-10 the kind cluster that runs `dev` belonged to another repo and was shared with six other
projects. This repo only assumed it: `scripts/kind_platform.sh` "leaves existing releases alone" for that reason,
`k8s-monitoring` was "Brian's release", and `local` was a docker compose stack with its own Redpanda, Redis,
Postgres and versitygw. Brian's direction (2026-10-10), taken as given here and in `ADR-0012`:

- **The cluster is this repo's alone.** It can be created, resized, deleted and rebuilt to a base state at will
  (`ADR-0012` decision 13). Built world content is not permanent; a rebuild returns to the base set.
- **Compose is retired; local is the `andara-local` namespace on the same cluster as `andara-dev`**
  (`ADR-0012` decision 14, Option B). Revisit trigger: `make test-integration` too slow on kind.
- **Kafka, the object store, Redis and Postgres run once and serve both namespaces.**
- **`andara-dev` is deployed by Argo CD from a `dev` Git tag**, so upgrades are deliberate.
- **Both environments ship telemetry to `solo7dev`**; `prod` ships to `solo7` (`ADR-0012` decisions 9 and 15).

Facts this ADR rests on, from the repo as of `origin/main` 437efea (read-only survey, 2026-10-10):

- `deploy/kind/config.yaml` is one `control-plane` node, label `ingress-ready=true`, host ports 80 and 443; CI's
  `kind.yaml` creates cluster `ci` from it and installs no Kafka, object store or `k8s-monitoring`. On the box the
  cluster pre-existed; no script creates it.
- The topics are unprefixed and their names are Go constants (`andara.commands.v1`, 64 partitions, a permanent
  choice, `deploy/kafka/topics.yaml`; `server/ingress/producer.go:32`, `server/tickloop/kafka.go:26-27`,
  `server/projector/kafka.go:26`, `server/boot/accounts.go:18-19`). There is no prefix config key.
  Consumer groups are already `<name>-<env>`.
- Kafka is one Strimzi cluster `andara-log` (3 combined brokers, 500m / 2Gi each, required hostname
  anti-affinity) **per `andara-<env>` namespace** (`AW-INF-014`); the Strimzi operator in namespace `strimzi`
  watches `andara-dev` and `andara-prod`; `kafka.sh` refuses `local`.
- `ADR-0011` (accepted, **not yet implemented**: `deploy/k8s/kafka/kafka.yaml` still has the `plain` listener)
  decides SASL/SCRAM-SHA-512, deny-by-default ACLs, one principal per workload with no environment in its name
  ("each environment has its own cluster"), declared in `topics.yaml`.
- The object store is versitygw (posix backend, one root credential pair per namespace, a 5Gi PVC, Service
  `andara-objectstore:9000`); the bucket is `andara-snapshots-<env>` (`scripts/objectstore.py` accepts `dev|prod`
  only); snapshot keys carry no environment (`{zone}/{tick}/{state_version}/{offset}`).
- Redis and Postgres are deployed nowhere in the cluster; their projectors have no binary (`AW-SRV-017`,
  `AW-SRV-018`). Their consumer groups are named `andara-projection-redis-<env>` / `-pg-<env>`.
- Argo CD (chart 10.9.2) follows `main` for `andara-dev`; Image Updater writes `image.tag` back as an Argo
  parameter (`dev@sha256:…`), nothing is written to git. `make image-publish` pushes `sha-<12>` then moves
  `:dev`, on every push to `main`, with no path filter. `main` requires signed commits (`.github/allowed_signers`
  holds one key). `make deploy` / `make rollback` (`AW-INF-041`, ready) exist only as a spec.
- dev requests about 2.7 CPU and 8.9 Gi (brokers 1.5 CPU / 6 Gi of it); the numbers are placeholders
  (`measurements.yaml: measured: false`).
- No script passes `--context` to `kubectl`; everything uses the ambient context.

## Decisions

Each heading carries its options and trade-offs, and one **Decision** line.

### 1. The cluster as code

**Options.** (a) the box's cluster stays hand-made and this repo documents its shape; (b) this repo creates it
from a committed config with a `make` target; (c) a cluster-API or Terraform definition.
(a) is today, and a rebuild is then folklore. (c) puts cluster credentials into Terraform, which
`ADR-0012` decision 6 forbids (Terraform is only for what is deployed to Grafana Cloud).

**Decision: (b).**

- **Name and context:** the cluster is `andara`, its kubeconfig context `kind-andara`. Every script passes
  `--context kind-andara` (or reads it from one variable, `ANDARA_KUBE_CONTEXT`, default `kind-andara`);
  none uses the ambient context. A script that finds the context missing exits `3` naming it.
- **Shape:** `deploy/kind/cluster.yaml`: one `control-plane` node (label `ingress-ready=true`, host ports 80
  and 443) and **three workers**, so Kafka's required hostname anti-affinity can schedule its 3 brokers. The
  three workers share one machine and one disk, as the broker contract already says of the current box
  (`docs/specs/kafka/broker-contract.md`); the spread is a scheduling property, not hardware redundancy.
  `deploy/kind/config.yaml` stays CI's single-node cluster (CI installs no Kafka) and is not the box's.
- **Targets:** `make cluster-up` (create if absent; idempotent), `make cluster-down` (delete, after a
  confirmation naming the cluster, `CONFIRM=andara`), `make cluster-rebuild` (down, up, platform, shared
  services; the one-command rebuild `ADR-0012` decision 13 needs). Names are final.
- **Not shared:** a rebuild deletes everything on it, with no tenant check. `kind_platform.sh`'s "leave
  existing releases alone" rule is replaced by reconcile (decision 2).
- **`make bootstrap` installs `kind` and `kubectl`, pinned in `bin/`** (it only reports them today), because the
  repo now creates the cluster. Docker stays a prerequisite it checks.

### 2. The platform, and the other repo

**Options.** (a) platform items stay in the other repo; (b) all of it moves here, installed by `make` with
pinned versions in one file; (c) all of it moves here and is installed by Argo CD (app-of-apps).
(a) leaves the rebuild dependent on a repo this one cannot see. (c) is attractive and costs ordering: Strimzi and
cert-manager CRDs must exist before the apps that use them, Argo CD itself cannot install itself, and a rebuild
should be one command whose failure is a stopped script, not a sync loop.

**Decision: (b) for the platform, Argo CD for the applications.** Versions live in `deploy/platform/versions.yaml`
(Traefik chart, cert-manager, Strimzi, Argo CD, Argo Image Updater is **removed**, the collectors' chart, kind
node image); `make platform-up` is `helm upgrade --install` for each in dependency order and is idempotent.

| Item | Lives in | Reason |
|---|---|---|
| `kind` cluster | this repo (`deploy/kind/cluster.yaml`) | decision 1 |
| Traefik, cert-manager, the `andara-ca` and `letsencrypt` ClusterIssuers | this repo | the edge is Andara's only now; the ClusterIssuer secrets (Cloudflare token) come from `.local/box.env`, never committed |
| Strimzi operator | this repo; `watchNamespaces={andara-shared}` | decision 3 |
| Argo CD | this repo; installed by `make platform-up`, owns only `andara-dev` | the dev tag (decision 7) |
| Collectors, kube-state-metrics, node-exporter | this repo; the pipelines are Fleet's (`ADR-0012` decisions 13 and 15) | `ADR-0012` |
| Shared services (Kafka, object store; later Redis, Postgres) | this repo, namespace `andara-shared` | decision 3 |

Nothing stays in the other repo; the dependency is cut.

### 3. The shared services' home

**Options.** (a) inside `andara-dev`, with local reaching across; (b) a namespace per service; (c) one namespace,
`andara-shared`.
(a) makes `dev` own what `local` depends on, so resetting `dev` breaks `local`. (b) is more manifests, more
NetworkPolicies and more Strimzi `watchNamespaces`, for no isolation gain on one cluster.

**Decision: (c).** Namespace `andara-shared` holds the Strimzi Kafka cluster `andara-log` (the 3-broker shape of
`AW-INF-014`, unchanged), the object store `andara-objectstore`, and Redis and Postgres when their projectors
exist. Their lifecycle belongs to `make platform-up` / `make shared-up`, **not** to either application
namespace: resetting `andara-local` or `andara-dev` never deletes a shared service, and only
`make cluster-rebuild` or `make shared-down` (`CONFIRM=andara-shared`) does. Service names keep their current
short form and are addressed by FQDN from the environments:
`andara-log-kafka-bootstrap.andara-shared.svc:9092`, `andara-objectstore.andara-shared.svc:9000`.
Each service's NetworkPolicy admits the application namespaces by label (`andara.valesor/env in {local, dev}`)
and the workload pods by name, replacing "same namespace only".

### 4. Isolation, per service

The rule: use the mechanism the service provides, and say what it does not prevent.

**Kafka.** Options: (a) a topic prefix per environment, with per-environment principals whose ACLs are
prefix-scoped (`ADR-0011`); (b) one Kafka cluster per environment, as today; (c) shared brokers with unprefixed
names (impossible: two environments cannot both own `andara.commands.v1`).
(b) isolates fully and costs a second set of brokers (about another 1.5 CPU and 6 Gi) on a machine that
cannot spare it, which is the complaint that started this direction. (a) shares the broker's partition budget,
disk and failure domain: with 64 partitions per hot topic and 222 per environment, two environments are 444
partitions, 1,332 replicas at RF 3 on 3 brokers, which a combined KRaft node handles but not with slack to waste.

**Decision (Kafka): (a).** Topic names become `<env>.andara.<name>.v<n>`: `dev.andara.commands.v1`,
`local.andara.commands.v1`. `local` topics use RF 1 and `min.insync.replicas` 1 (local is disposable, and its
RF 3 would triple the load for no property anyone tests); `dev` keeps RF 3 and ISR 2. The partition counts stay as
they are (64, permanent); a different count for `local` would test a different hash. The server reads a new
config key **`kafka.topic_prefix`** (env `ANDARA_KAFKA_TOPIC_PREFIX`, flag `--kafka-topic-prefix`), default `""`
(unprefixed, which is what `prod` keeps); the chart sets `dev.` and `local.`. Consumer groups already carry
`-<env>`. `deploy/kafka/topics.yaml` gains a `prefix:` per environment and renders the topics and principals for
each. A server change: decision 11's implementation story.

*What an environment can still do to the other:* fill the brokers' disk (a retention-1 topic does it), exhaust the
partition or connection budget, or, with the broker superuser, delete the other's topics. The ACLs stop an
environment's workloads from touching the other's topics, not from starving them.

**Object store.** Options: (a) one bucket per environment, a distinct credential per environment, on the
shared gateway; (b) one bucket with an environment key prefix; (c) a gateway per environment.
(b) needs a server change to the snapshot key (a contract) for no isolation, because one credential still
reaches both. (c) is today's shape and duplicates the gateway.

**Decision (S3): (a).** versitygw is run with its IAM directory (`--iam-dir`) **[verify in the object-store
story: that the pinned v1.8.0 enforces per-user bucket ownership]**; the root pair is held by the operator
only; two users, `andara-local` and `andara-dev`, each own their bucket: `andara-snapshots-local` and
`andara-snapshots-dev`. The existing Secret name `andara-snapshot-s3` stays in each environment's namespace,
holding that environment's pair. The snapshot key is unchanged (the bucket is the separation).
`scripts/objectstore.py` accepts `local`. The PVC grows to 10Gi (two environments' snapshots share one disk).
*What it does not prevent:* one environment filling the PVC, and the root credential being used to delete
either bucket.

**Redis.** Options: (a) a key prefix per environment with an ACL user per environment (key pattern
`~<env>:*`, command categories restricted so `FLUSHALL` and `CONFIG` are refused); (b) a logical database
per environment; (c) an instance per environment.
(b) isolates in name only: databases share memory, persistence and `FLUSHALL`. (c) is a second instance for a
cache of derived data.

**Decision (Redis): (a).** ADR-0002 already rebuilds a projection "into a new key prefix and cuts over", so the
prefix convention costs nothing. *Not prevented:* one environment exhausting `maxmemory` (the policy is
`noeviction`, so the other's writes fail). Nothing deploys until `AW-SRV-017` has a binary.

**Postgres.** Options: (a) a database per environment, a role per environment owning it, `CONNECT` revoked
from `PUBLIC`; (b) a schema per environment in one database; (c) an instance per environment.
(b) shares the database's catalogue and default `search_path` privileges for little gain over (a).

**Decision (Postgres): (a).** Databases `andara_local` and `andara_dev`; roles `andara_local` and `andara_dev`;
the projector role is not a superuser. *Not prevented:* connection exhaustion (`max_connections` is shared), a
runaway query, or disk fill. Nothing deploys until `AW-SRV-018` has a binary.

### 5. What sharing does to `dev`'s guarantees

The zero-RPO claim (`AW-INF-040`) and the broker bounce (`AW-INF-014`) are claims about the broker, and the broker
is now one broker set for both environments. The honest statement, which Brian may not like: **a broker fault
cannot be rehearsed on `dev` without disturbing `local`.** The drills:

| Drill | Acts on | Disturbs the other environment? | Rule |
|---|---|---|---|
| `make kafka-broker-bounce` (`AW-INF-014`) | deletes `andara-log-broker-0` in `andara-shared` | yes: `local`'s RF 1 partitions on that broker are unavailable until it returns | the drill refuses to start while `andara-local` has a ready server, unless `CONFIRM_LOCAL=down` is given; `make local-down` scales it to 0 |
| `make kafka-rehearsal` (`AW-INF-040`) | kills brokers | yes, as above, and for longer | same refusal; its assertions read `dev.`-prefixed topics only |
| `make env-recover ENV=dev` (`AW-INF-034`) | `SIGKILL` of `andara-0`'s `server` container in `andara-dev` | no | unchanged; resolves the context and the namespace, never the node by name |
| `stack-recover` and the other `stack-*` targets | `andara-local`'s server (per `ADR-0012` decision 14) | no: they touch `andara-local` and `local.`-prefixed topics only | re-pointed by the kind-side stories after this ADR |

`dev` keeps its three brokers (`local` shares them). The alternative that removes the first two rows' cost,
a separate single-broker Kafka for `local`, is rejected: it is the second Kafka this decision exists to avoid, and
`local` needs no fault tolerance. Reopen it if the refusal in rows 1 and 2 becomes a nuisance (Revisit when).

### 6. `ADR-0011` amended

`ADR-0011` is **amended, not superseded**, by this ADR; nothing in it is reversed and it has not been
implemented yet, so the amendment costs no migration. What changes:

- **One principal per workload per environment:** `andara-server-dev`, `andara-server-local`,
  `andara-projector-state-dev`, and so on, because the brokers are shared. Each principal's ACLs are scoped to
  its environment's topic prefix (`dev.andara.`) and group prefix (`andara-sim-dev`, `andara-content-dev`,
  `andara-projector-state-dev`). The operator superuser (`andara-operator`) is unchanged and is not
  per-environment.
- **The NetworkPolicy** on the listener admits pods by namespace label (`local`, `dev`) and name, in
  `andara-shared`, instead of "same namespace".
- **The encryption sentence** ("acceptable only while every client and broker share a namespace") becomes "while
  every client and broker share one cluster on one host". Traffic now crosses namespaces on the kind node's docker
  network, unencrypted, under SCRAM. That is a weaker position than ADR-0011 described and Brian should know it.
- **"Revisit when"** is answered as follows. The first trigger ("any client or broker leaves the namespace") is
  re-read as "leaves the cluster or the host": `prod` on a separate cluster, a client on another host, or
  cross-cluster replication. Crossing a namespace inside this cluster no longer triggers TLS.
- `ADR-0011` gains a dated annotation pointing here, in the style of `ADR-0002`'s annotation.

### 7. The `dev` tag

**Options.** (a) a Git tag `dev` that Argo CD's `targetRevision` follows, pointing at a promotion commit that
carries the image; (b) a Git tag on a `main` commit, with the image chosen out-of-band by Image Updater as today;
(c) a branch.
(b) keeps the hidden state (`image.tag` as an Argo parameter that is in no commit), so "which image is dev?" has
two answers. (c) is a branch, which moves on every push and is not deliberate. The lifecycle spec
(`docs/specs/deploy/lifecycle.md`) also needs the image tag to be unambiguous: a bare `dev` tag, which two builds
can share, is the failure it names.

**Decision: (a).** A **promotion** is one signed commit on a branch `release/dev` (never `main`, so `publish`'s
push-to-`main` trigger and the signed-commit rule on `main` are not engaged and nothing loops) that sets
`image.tag: sha-<12>` in `deploy/helm/values/dev.yaml`, and an **annotated, signed Git tag** `dev` on that commit.
Argo CD's `andara-dev` Application sets `targetRevision: dev` and is otherwise unchanged (chart path, values
file, automated sync). The tagged commit therefore carries the chart, the values and the image together: what
runs is what the tag points at.

- **Type and name:** annotated, signed tag `dev`, moved forward by force-push. Every promotion also creates an
  immutable history tag `dev-<n>` (n monotonic) at the same commit, which is what rollback names.
- **Who moves it:** Brian, or the SRE role at his instruction, with `make promote ENV=dev SHA=<main sha>`.
  It checks the image `sha-<12>` exists and pulls anonymously (`make image-check`), creates the commit and
  both tags, pushes the branch and the tags, and prints the Argo sync command (Argo syncs by itself). Nothing
  automated moves it.
- **Promotion step:** `make promote ENV=dev SHA=…`. **Rollback step:** `make promote ENV=dev TAG=dev-<n>`, which
  moves `dev` back to that history tag's commit (no new commit). The lifecycle spec's pinned-round rollback
  (`recovery.pin_round`) is unchanged and is a Helm value, not a tag.
- **Image Updater:** removed (`deploy/argocd/andara-dev-image-updater.yaml`, its install in `scripts/argocd.py`),
  and `AW-INF-019`'s "dev follows `main`" ends. `AW-INF-013`'s image publish stays: it still pushes `sha-<12>`
  (the thing a promotion names) and no longer matters for what `dev` runs, so `:dev` as a moving image tag is
  retired with it.
- **`AW-INF-041` (`make deploy` / `make rollback`):** stays for `prod`, whose release is `helm upgrade --install`
  from the box. For `ENV=dev` they become `make promote`; 041's spec gets that one line.
- **Not decided here, a story must prove:** that tag pushes are allowed by `main`'s protection rules (they apply
  to branches; this is **[verify]**), and that `allowed_signers` verifies an annotated tag signature.

### 8. `andara-local`'s path

**Decision.** `make up` means: the cluster exists (`cluster-up`), the platform and shared services are up
(`platform-up`, `shared-up`), the server image is built and loaded (`make image kind-load`), and `andara-local` is
installed (`make helm-install ENV=local`), behind one target. `make down` removes `andara-local` only (the same
as `make local-down`); `make local-reset` additionally deletes the `local.`-prefixed topics and empties the
`andara-snapshots-local` bucket, and its scripts refuse any name that does not carry the `local` prefix, so it
cannot touch `dev`. `values/local.yaml` gains the shared Kafka and object store (it is broker-free today). The
compose `min` profile is retired until `ADR-0012` decision 14's trigger fires.

**A new machine** needs Docker, about 12 Gi of free memory, and `make bootstrap` (which now installs pinned `kind`
and `kubectl` and the other tools). `make bootstrap && make up && make check` remains the onboarding path;
`make check` needs no cluster.

### 9. Migration from today

The rebuild is one planned outage and it discards the whole box cluster.

1. The stories of decision 11 land and pass `make check` and the CI kind job; the `dev` tag exists on a promotion
   commit for the current `main`.
2. `ADR-0012`'s prerequisites are done (decision 7 steps 0 to 4(i-b): Terraform applied to both stacks, rules
   paused, the Fleet pipelines applied).
3. `make cluster-rebuild`: delete the old cluster, create `andara`, platform, shared services.
4. `andara-dev` is created from the `dev` tag by Argo CD; `andara-local` by `make up`.
5. `ADR-0012` decision 7 step 4(ii) onward runs as written (ruler deletion at the point of no return, then the
   first series, `observe-check`, `required_environments`).
6. `AW-INF-034`'s drill is rerun on the rebuilt `dev`.

**Discarded:** `dev`'s World, its Kafka log, snapshots, Accounts, the old cluster and everything else on it
(the other projects' workloads, which are Brian's to reinstall elsewhere), Argo CD's state, Image Updater.
**Carried over:** nothing. `dev` starts from the base content set, which is the persistence stance Brian set
(built content is for building and playtesting). `AW-INF-034`'s rerun needs: Kafka `Ready` in `andara-shared`,
`dev.`-prefixed topics created, the `andara-dev` bucket and credential, the server at `Ready`, and the
Grafana Cloud reads of `ADR-0012` decision 2 for `solo7dev`.

### 10. Prod

**Decision.** `prod` is **not part of this move.** `prod` is not installed today; `values/prod.yaml` and its
`make deploy` path are untouched. The boundary: **the shared services are non-production only.** Production never
shares a broker, object store or database with `local` or `dev`; when `prod` is installed it gets its own
namespace and its own Kafka, **unprefixed** (`kafka.topic_prefix` default `""`), under `AW-INF-014`'s
one-Kafka-per-namespace, on this cluster's successor or another cluster as Brian decides then. Strimzi's
`watchNamespaces` is `{andara-shared}` now and gains `andara-prod` when prod is installed. `ADR-0012` stays
true: `prod` ships to `solo7`.

### 11. The follow-on stories

PM writes them after acceptance. Each is sized `S` or `M`, lane `sre` unless noted.

| # | Story | Size |
|---|---|---|
| 1 | Cluster as code: `deploy/kind/cluster.yaml`, `cluster-up/down/rebuild`, `ANDARA_KUBE_CONTEXT`, bootstrap installs `kind` and `kubectl`, every script takes `--context` | M |
| 2 | Platform reconcile: `deploy/platform/versions.yaml`, `platform-up`, rewrite `kind_platform.sh`, both ClusterIssuers, Strimzi watching `andara-shared` | M |
| 3 | Shared Kafka: `andara-shared`, prefixed topics and per-environment principals in `topics.yaml`, ADR-0011's SASL implementation for them, NetworkPolicies | M |
| 4 | Shared object store: versitygw IAM users, a bucket and Secret per environment, `objectstore.py local`, 10Gi | S |
| 5 | The `dev` tag: `make promote`, the Application's `targetRevision`, remove Image Updater, `AW-INF-041` amendment, tag-protection check | M |
| 6 | `andara-local`: `values/local.yaml`, `make up/down/local-reset`, CI | M |
| 7 | Drills re-pointed: contexts, the local refusal in the broker drills, `env-recover` context, the `stack-*` kind-side set (with `ADR-0012`'s replacements for AW-INF-048) | M |
| 8 | The rebuild run (decision 9) | S |
| I | **implementation:** `kafka.topic_prefix` in the server and the projector, defaults and tests | S |
| later | Redis and Postgres isolation and deployment, when `AW-SRV-017` / `AW-SRV-018` have binaries | S each |

## Agreement with `ADR-0012`

- **Decision 13** (the cluster and the platform live here): agreed and completed. Its open item, "whether Argo CD or
  `make` installs the chart", is decided in decision 2: `make` for the platform and the collectors, Argo CD for
  `andara-dev` only.
- **Decision 14** (Option B, compose retired): agreed. Its `make up` meaning is made exact in decision 8, and
  its revisit trigger is unchanged.
- **Decision 9 / 15** (the `environment` stamp): one amendment. `andara-shared` is a platform namespace with no
  environment, so the `stamp` pipeline leaves it unlabelled (`unknown`) and `observe-check --keep-list`'s `unknown`
  guard excludes `namespace="andara-shared"` as it excludes `certmanager_*` and `traefik_*`. None of the 12 rules
  in `alerts.yaml` reads a shared-service series today, so no rule is affected; a rule that does must derive the
  environment from the topic prefix, the consumer-group suffix or the bucket, and cannot from a label.
- **Decision 7** (cutover) gains one prerequisite, "`dev` is deployed from the tag", folded into decision 9 above.

## Consequences

- **A broker fault now disturbs both environments.** The drills that cause one refuse to start while `local` is up
  (decision 5). `dev`'s fault evidence is as strong as before; `local` pays for it.
- **ADR-0011's isolation is weaker than it read.** Traffic crosses namespaces unencrypted, under SCRAM, on one
  docker network. Isolation between environments on the shared brokers is by ACL and prefix, not by a boundary.
- **One environment can starve the other** of disk, partitions, connections or memory in every shared service.
  The mechanisms stop an environment from reading or deleting the other's data, not from filling the machine.
- **Dev only changes when someone promotes it.** The convenience of "merge and it is on dev" ends. A merge to
  `main` still publishes `sha-<12>`; nothing runs it until `make promote`.
- **A rebuild deletes the other projects** that used to share the cluster. That was ruled, and it is in the
  migration.
- **The machine needs about 12 Gi free** to run both environments, the shared services and the platform. The
  chart's measurements are placeholders, so this is an estimate.
- **Unprefixed topic names disappear from `dev` and `local`.** Anything that hard-codes `andara.commands.v1`
  (scripts, runbooks, dashboards, `topics.py`) changes; `prod` keeps the unprefixed names.
- **The tag mechanism is new and its protections are unverified** (tag rules on `main`, signed-tag
  verification); a story proves them before `dev` depends on it.
- **Foreclosed:** a second Kafka for `local`; Image Updater; compose; a platform managed from another repo.

## Revisit when

- The broker-drill refusal (decision 5) is hit more than twice in a month: give `local` its own single-broker Kafka.
- A client or broker leaves the cluster or the host (decision 6): TLS on the listener, as `ADR-0011` says.
- Two environments starve each other once (disk, memory, partitions): split the shared service that did it.
- `make test-integration` on kind exceeds `ADR-0012` decision 14's threshold: bring back compose `min` for the
  tests, which this ADR's shared services then no longer back for that profile.
- `prod` is installed (decision 10), or a third non-production environment (`staging`) is wanted: a prefix, a
  principal, a bucket and a role are added per environment, which is the cost of each one.
- Argo CD's automated sync on a tag misbehaves twice (a tag moved mid-sync, a rollback that does not converge):
  move the promotion to a pull request instead of a force-moved tag.
