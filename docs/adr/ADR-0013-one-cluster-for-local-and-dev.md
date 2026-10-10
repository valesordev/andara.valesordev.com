---
id: ADR-0013
title: One cluster for local and dev
status: proposed          # draft | proposed | accepted | rejected | superseded by ADR-XXXX
date: 2026-10-10
deciders: [brian]
gates: []                 # the SRE stories this ADR names (decision 12) are written by PM after acceptance
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
  `kind.yaml`'s lifecycle job creates cluster `ci` from it and installs no Kafka, object store or `k8s-monitoring`; the compose-driven `stack.yaml` and `recovery-timing.yaml` workflows carry the broker-backed CI (decision 8). On the box the
  cluster pre-existed; no script creates it.
- The topics are unprefixed and their names are Go constants (`andara.commands.v1`, 64 partitions, a permanent
  choice, `deploy/kafka/topics.yaml`; `server/ingress/producer.go:32`, `server/tickloop/kafka.go:26-27`,
    `server/projector/kafka.go:26`, `server/boot/accounts.go:18-19`, and the content topics
  `server/content/kafka.go:38-40`). There is no prefix config key.
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
- `make test-integration` runs against the compose broker today (`Makefile:196-205`); `topics.yaml` carries a
  `broker.local` Redpanda block; ADR-0002 §7 says "Redpanda locally".
- Redis and Postgres are deployed nowhere in the cluster; their projectors have no binary (`AW-SRV-017`,
  `AW-SRV-018`). Their consumer groups are named `andara-projection-redis-<env>` / `-pg-<env>`.
- Argo CD (chart 10.9.2) follows `main` for `andara-dev`; Image Updater writes `image.tag` back as an Argo
  parameter (`dev@sha256:…`), nothing is written to git. `make image-publish` pushes `sha-<12>` then moves
  `:dev`, on every push to `main`, with no path filter. `main` requires signed commits (`.github/allowed_signers`
  holds one key). `make deploy` / `make rollback` (`AW-INF-041`, ready) exist only as a spec.
- dev requests about 2.7 CPU and 8.9 Gi (brokers 1.5 CPU / 6 Gi of it); the numbers are placeholders
  (`measurements.yaml: measured: false`).
- No script passes `--context` to `kubectl`; everything uses the ambient context.
- Traefik takes `hostPort` 80 and 443 on the `ingress-ready` node and the kind config publishes those ports on the
  host; the chart's hostnames are `andara.local` (local, an `/etc/hosts` line, private CA `andara-ca`),
  `andara-dev.solo7.valesordev.com` (dev, Let's Encrypt DNS-01 through Cloudflare) and
  `andara.solo7.valesordev.com` (prod).
- **New, Brian 2026-10-10:** the cluster publishes **8080 and 8443**, not 80 and 443. DNS and an `nginx` proxy stay in the
  repo that manages the workstation configuration; that proxy routes `local.andara.valesordev.com` and
  `dev.andara.valesordev.com` to the cluster (decision 11).

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
- **Shape:** `deploy/kind/cluster.yaml`: one `control-plane` node (label `ingress-ready=true`; the node's ports 80 and 443, where Traefik's `hostPort`
  sits, are published on the host as **`127.0.0.1:8080` and `127.0.0.1:8443`**, decision 11) and **three workers**, so Kafka's required hostname anti-affinity can schedule its 3 brokers. The
  three workers share one machine and one disk, as the broker contract already says of the current box
  (`docs/specs/kafka/broker-contract.md`); the spread is a scheduling property, not hardware redundancy.
  `deploy/kind/config.yaml` stays CI's single-node cluster (its lifecycle job installs no Kafka; the `drills` job of decision 8 adds a throwaway one; it publishes 80 and 443 for
  its own `--resolve` tests, and is not the box's).
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
partitions, 888 replicas (666 for `dev` at RF 3, 222 for `local` at RF 1, decision below) on 3 brokers, which a combined KRaft node handles but not with slack to waste.

**Decision (Kafka): (a).** Topic names become `<env>.andara.<name>.v<n>`: `dev.andara.commands.v1`,
`local.andara.commands.v1`. `local` topics use RF 1 and `min.insync.replicas` 1 (local is disposable, and its
RF 3 would triple the load for no property anyone tests); `dev` keeps RF 3 and ISR 2. The partition counts stay as
they are (64, permanent); a different count for `local` would test a different hash. The server reads a new
config key **`kafka.topic_prefix`** (env `ANDARA_KAFKA_TOPIC_PREFIX`, flag `--kafka-topic-prefix`), default `""`
(unprefixed, which is what `prod` keeps); the chart sets `dev.` and `local.`. Consumer groups already carry
`-<env>`. `deploy/kafka/topics.yaml` gains a `prefix:` per environment and renders the topics and principals for
each. A server change: decision 12's implementation story. The key covers **every** topic the server and the projector
name, the content topics included. With the default `""` a server would silently use unprefixed topics, so the
server's config validation fails (exit 1, naming `kafka.topic_prefix`) when `telemetry.environment` is not `prod`
and the prefix is empty, and the chart's `required` fails the render for the same case.

*What an environment can still do to the other, and what is bounded:* `topics.yaml` gives `andara.commands.v1`
`retention.ms: -1`, and brokers have a 20Gi PVC each that `dev`'s zero-RPO claim depends on, so a `local` soak
could fill them. So `local.` topics get a bounded retention (`retention.bytes` 64 MiB per partition and 7 days,
a starting value for story 3 to measure), `local`'s principals get `KafkaUser` quotas (producer and consumer byte
rates), and no environment principal may create topics or alter configs (`CreateTopics`, `AlterConfigs` are
the operator's alone, ADR-0011). What stays unbounded: a runaway `dev` producer, the partition and connection budget,
and the broker superuser deleting the other's topics. The residual is a check, `make shared-check`, that reports
each shared PVC's usage and fails over 80%.

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
`noeviction`, which the deployment must set, so the other's writes fail). Nothing deploys until `AW-SRV-017` has a binary. Service `andara-redis.andara-shared.svc:6379`, key prefix
`<env>:`, users `andara-local` and `andara-dev`.

**Postgres.** Options: (a) a database per environment, a role per environment owning it, `CONNECT` revoked
from `PUBLIC`; (b) a schema per environment in one database; (c) an instance per environment.
(b) shares the database's catalogue and default `search_path` privileges for little gain over (a).

**Decision (Postgres): (a).** Databases `andara_local` and `andara_dev`; roles `andara_local` and `andara_dev`;
the projector role is not a superuser. *Not prevented:* connection exhaustion (`max_connections` is shared), a
runaway query, or disk fill. Nothing deploys until `AW-SRV-018` has a binary. Service `andara-postgres.andara-shared.svc:5432`.

### 5. What sharing does to `dev`'s guarantees

The zero-RPO claim (`AW-INF-040`) and the broker bounce (`AW-INF-014`) are claims about the broker, and the broker
is now one broker set for both environments. The honest statement, which Brian may not like: **a broker fault
cannot be rehearsed on `dev` without disturbing `local`.** The story's AC6 ("none can disturb the other environment")
therefore cannot hold for the broker drills; this ADR deviates from it, and Brian's acceptance of the ADR is the
acceptance of that deviation (PM amends AC6 to "lists which drills disturb the other and how that is prevented").
The drills:

| Drill | Acts on | Disturbs the other environment? | Rule |
|---|---|---|---|
| `make kafka-broker-bounce` (`AW-INF-014`) | deletes `andara-log-broker-0` in `andara-shared` | yes: `local`'s RF 1 partitions on that broker are unavailable until it returns | broker drill: takes the drill lock (below) |
| `make kafka-rehearsal` (`AW-INF-040`, a target the story builds) | kills brokers | yes, as above, for longer | broker drill; its assertions read `dev.`-prefixed topics only |
| `make env-recover ENV=dev` (`AW-INF-034`) | `SIGKILL` of `andara-0`'s `server` container in `andara-dev` | no | unchanged; resolves the context and the namespace, never the node by name |
| `stack-boundary-lost` | today stops Redpanda for 90 s or more; on kind it must not stop a shared broker | no, by construction | a `NetworkPolicy` that cuts only `andara-local`'s server pods from the listener for the same period (the server sees the broker unreachable past the delivery timeout, which is the property under test), removed afterwards |
| `stack-recover` and the other `stack-*` targets | `andara-local`'s server (`ADR-0012` decision 14) | no: `andara-local` and `local.`-prefixed topics only | re-pointed by the kind-side stories |

**The guard is symmetric.** A broker drill takes a lock, the ConfigMap `drill-lock` in `andara-shared`, holding who
and until when. It refuses to start while that lock is held or while `andara-local` has a ready server, unless
`CONFIRM_LOCAL=down`, whose meaning is that the drill itself scales `andara-local` to 0 first, restores it
afterwards, and releases the lock. `make up`, `make local-down`, `make promote` and `make topics-apply` check the lock
and refuse to run while it is held, so nothing starts `local` or reconfigures the shared brokers mid-drill. Dev's own
fault drills are namespace-local and need no lock.

`dev` keeps its three brokers (`local` shares them). The alternative that removes the first two rows' cost,
a separate single-broker Kafka for `local`, is rejected: it is the second Kafka this decision exists to avoid, and
`local` needs no fault tolerance. Reopen it if the lock becomes a nuisance (Revisit when).

### 6. `ADR-0011` amended

`ADR-0011` is **amended, not superseded**, by this ADR; nothing in it is reversed and it has not been
implemented yet, so the amendment costs no migration. What changes:

- **One principal per workload per environment:** `andara-server-dev`, `andara-server-local`,
  `andara-projector-state-dev`, and so on, because the brokers are shared. Each principal's ACLs are scoped to
  its environment's topic prefix (`dev.andara.`) and group prefix (`andara-sim-dev`, `andara-content-dev`,
  `andara-projector-state-dev`). The operator superuser (`andara-operator`) is unchanged and is not
  per-environment.
- **Credential path.** Strimzi's User Operator creates each principal's Secret in `andara-shared`, where the
  `KafkaUser` lives (ADR-0011). Workloads mount `secrets.kafkaCreds` in their own namespace, and Secrets do not cross
  namespaces, so a step of the shared-Kafka story copies each principal's Secret into **its own environment's
  namespace only** (`andara-server-dev` into `andara-dev`, never into `andara-local`), by a `make` target
  run by the operator, and the workloads' ServiceAccounts hold no RBAC on `andara-shared`'s Secrets. Rotation: clients
  read the credential file per connection (ADR-0011), so re-copying is the whole rotation.
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

**Decision: (a).** A **promotion** is one signed commit on a branch `release/dev`, which is **reset to `main@SHA` and given exactly
one commit each time** (so the tagged tree is that `main` commit's chart and values plus the image line, never a
stale chart), never `main` itself (so `publish`'s push-to-`main` trigger and the signed-commit rule on `main` are not
engaged and nothing loops). The commit sets `image.tag: sha-<12>@sha256:<digest>` (the digest `image-publish`
prints and `lifecycle.md` pins by) in `deploy/helm/values/dev.yaml`, and an **annotated, signed Git tag** `dev` on that commit.
Argo CD's `andara-dev` Application sets `targetRevision: dev` and is otherwise unchanged (chart path, values
file, automated sync). The tagged commit therefore carries the chart, the values and the image together: what
runs is what the tag points at.

- **Type and name:** annotated, signed tag `dev`, moved forward by force-push. Every promotion also creates an
  immutable history tag `dev-<n>` (n monotonic) at the same commit, which is what rollback names.
- **Who moves it:** **Brian**, from his own shell, with `make promote ENV=dev SHA=<main sha>`. A role session's
  push hook allows a push only for a commit that passed `/pre-pr` (`.claude/bin/role`), so a promotion is not
  something an agent session does; if that changes, the story that changes it defines how the push is cleared.
  CLAUDE.md §4's branch list gains `release/dev`, which is Brian's edit.
  It checks the image `sha-<12>` exists and pulls anonymously (`make image-check`), creates the commit and
  both tags, pushes the branch and the tags, and prints the Argo sync command (Argo syncs by itself). Nothing
  automated moves it.
- **Promotion step:** `make promote ENV=dev SHA=…`. **Rollback step:** `make promote ENV=dev TAG=dev-<n>`, which creates a **new**
  promotion commit on top of that history tag's commit, so the tag history stays linear, and sets
  `release.rolled_back_to: dev-<n>` and, when `PIN_ROUND=<round>` is given, `recovery.pin_round` in the same
  commit (the lifecycle spec's markers, as committed values instead of `--set` flags). `make promote` runs the
  same pre- and post-steps as `make deploy` (the `andara.core` step, the measured interruption against the RTO)
  through the same script, after Argo syncs.
- **Image Updater:** removed (`deploy/argocd/andara-dev-image-updater.yaml`, its install in `scripts/argocd.py`),
  and `AW-INF-019`'s "dev follows `main`" ends. `AW-INF-013`'s image publish stays: it still pushes `sha-<12>`
  (the thing a promotion names) and no longer matters for what `dev` runs, so `:dev` as a moving image tag is
  retired with it.
- **`AW-INF-041` (`make deploy` / `make rollback`):** stays for `prod`, whose release is `helm upgrade --install`
  from the box. For `ENV=dev` they become `make promote` with the parity above; `AW-INF-041`'s spec is amended by the story that
  builds the target, and `lifecycle.md`'s `deploy:<image tag>` stays unambiguous because the tag is `sha-<12>`.
- **Not decided here, a story must prove:** that tag pushes are allowed by the repository's rulesets (`main`'s
  protection targets the branch, so `release/dev` is outside it **[verify]**), that `allowed_signers` verifies an
  annotated tag signature **[verify]**, and that Argo CD notices a force-moved tag promptly (it caches refs, up to
  about three minutes; the promote script forces a hard refresh **[verify]**).
- **`make test-integration`'s tests do not use the `dev` tag.** See decision 8.

### 8. `andara-local`'s path

**Decision.** After the compose-retirement story (10) lands, `make up` means: the cluster exists (`cluster-up`), the platform and shared services are up
(`platform-up`, `shared-up`), the server image is built and loaded (`make image kind-load`), and `andara-local` is
installed (`make helm-install ENV=local`), behind one target. Until story 10, stories 6a and 7a add `make local-up` and `make local-down` beside the compose `make up` and `make down`, which keep their compose meaning so `stack.yaml` and `recovery-timing.yaml` stay green; **nothing removes or re-points compose until 6b, 6c, 7a and 7b have landed.** `make down` removes `andara-local` only (the same
as `make local-down`); `make local-reset` additionally deletes the `local.`-prefixed topics and empties the
`andara-snapshots-local` bucket, and its scripts refuse any name that does not carry the `local` prefix, so it
cannot touch `dev`. `values/local.yaml` gains the shared Kafka and object store (it is broker-free today) and the host
`local.andara.valesordev.com` (decision 11). **CI keeps its single-node cluster for the lifecycle job, broker-free,** with a new
`values/ci.yaml` overlay (host `andara.local`, memory sources, no broker): the lifecycle test coverage that CI
had stays. **CI also needs a broker for the work compose's `min` and `full` profiles carried there**, and today two
workflows depend on compose: `stack.yaml` (the four `stack-*` drills, the M2 gate, the observability checks, which
`make up` and `docker compose` drive) and `recovery-timing.yaml` (`make up PROFILE=min`, then `make recovery-timing`
against `localhost:9092`, creating `andara.test.rec.*` topics). Both are retired and replaced in stories 6b, 6c, 7a and 7b: a new
`drills` job in `kind.yaml` creates a throwaway, **unshared, single-broker Kafka** (Strimzi, RF 1, one replica) in the CI
cluster for the run, with a CI principal that may create any topic because it is destroyed afterwards; the `stack-*`
drills run against `andara-ci` there, and `recovery-timing` runs against that broker from an in-cluster Job (below) with the test's own topic names, so the `it-` prefix rule does not apply to it. The `andara-it`
principal below is for the box only. A manifest guard in `make check` fails if the CI Kafka manifest (the principal that may
create any topic) is referenced by anything that applies to the box.

*What `stack.yaml` becomes.* Every step is kept, moved or dropped; nothing disappears silently.

| `stack.yaml` step | Disposition |
|---|---|
| bootstrap, `up`, "the server runs this commit" (build stamp) | kept in `drills`: `make image` stamps, the chart installs it into `andara-ci` |
| topics match the declaration; `topics-apply` aligns drift and refuses data loss | kept in `drills`, `topics.py` through `kubectl exec` into the throwaway Kafka's tools pod |
| subjects registered and match the declaration (`schemas-apply` idempotent) | kept in `drills`, against the registry of the next paragraph |
| build info and scrape targets up; a Session is seen by Prometheus; dashboard provisioned; alert rules load and `AndaraServerUnavailable` follows the server | dropped: the compose Prometheus and Grafana are gone. Replaced by `ADR-0012`: the rules' expressions by `promtool test rules` in `make check`, rule state by `gcx` on `solo7dev` (drills with secrets only), the dashboard by the `terraform test` of its three variables |
| the record log and the tick loop on the broker (`make test-integration`); snapshot round | kept in `drills` |
| the tick loop survives a broker outage | moved to 7a: a broker drill, unshared in CI so no lock is needed |
| the log exporter survives a collector outage; a trace round-trips | dropped unless Brian wants a static CI collector (story 8 would add it); the trace round-trip becomes a `gcx` read on `solo7dev` where secrets exist |
| `stack-boundary-lost`, `stack-play`, `stack-linkdead`, `stack-recover` (the M2 gate), `stack-projector-check` | kept in `drills`; 7b parameterises the context and namespace and 6b's job passes `andara-ci`; `stack-boundary-lost` is the NetworkPolicy drill of 7a. Their Prometheus reads (`stack_recover.sh`, `stack_smoke.sh`) become the server's own `/metrics` for counters and gauges, and `gcx` on `solo7dev` for anything that needs Grafana Cloud, as `ADR-0012` decision 14 already says |
| `stack-recover-mismatch` | kept in `drills` **only where the `solo7dev` secrets exist**: it asserts rule state (`RecoveryStateMismatch`, `keep_firing_for`) and the only rule evaluator now is Grafana Cloud's (`ADR-0012` decision 2, read through `gcx`); a fork's pull request skips it and says so (exit `3` locally, a notice in CI); the expression itself stays covered by `promtool test rules` in `make check` |
| `diagnostics on failure` | replaced: on failure the job dumps `kubectl get pods`, `describe` and `logs` for `andara-ci` and the throwaway Kafka's namespace before the cluster is torn down |
| `down` leaves nothing behind | replaced by deleting the throwaway Kafka's namespace and asserting nothing remains |

*The schema registry.* Compose's Redpanda provides one (`ANDARA_SCHEMA_REGISTRY`, `make schemas-apply`, `ADR-0007`); a
Strimzi Kafka does not, and neither does `dev` today. It becomes a shared service in `andara-shared`
(Karapace or Apicurio **[verify: which, and that the server needs none at runtime, since only `scripts/schemas.py` reads it
by `ANDARA_SCHEMA_REGISTRY_URL`]**), subjects carry the environment prefix with the topic they describe,
`make schemas-apply ENV=` registers them, and the CI `drills` job gets its own throwaway instance. Story 3c.

*Recovery timing.* The weekly cron, `workflow_dispatch`, path filters, the `recovery-timing.json` artifact and
`recovery-timing-previous` (and `startup-budget-check`, `AW-INF-011`) keep their triggers and names in a `recovery-timing`
workflow that stays separate from `kind.yaml`, so the previous-run lookup is not re-keyed; only its set-up changes, from
`make up PROFILE=min` to the `drills` Kafka, run from an in-cluster Job rather than a port-forward so the measured
recovery does not include forwarding latency. Story 6c.

*On the box.* Story 6a owns the `values/ci.yaml` overlay. `make test-integration` is backed by the shared Kafka and
object store through a port-forward, with a principal `andara-it` that may create and delete only
`it-<random>.`-prefixed topics and buckets, so the tests never touch `local.` or `dev.` data; story 6a owns it. The
compose `min` profile is retired until `ADR-0012` decision 14's trigger fires; **its baseline**
(`make test-integration`'s wall time on compose `min`) **is recorded by story 6a before compose is removed**, because the
trigger compares against it.

**A new machine** needs Docker, about 12 Gi of free memory, and `make bootstrap` (which now installs pinned `kind`
and `kubectl` and the other tools). `make bootstrap && make up && make check` remains the onboarding path;
`make check` needs no cluster.

### 9. Migration from today

The rebuild is one planned outage and it discards the whole box cluster.

1. The stories of decision 12 land and pass `make check` and the CI kind job; the `dev` tag exists on a promotion
   commit for the current `main`.
2. `ADR-0012`'s prerequisites are done (decision 7 steps 0 to 4(i-b): Terraform applied to both stacks, rules
   paused, the Fleet pipelines applied).
3. `make cluster-rebuild`: delete the old cluster, create `andara`, platform, shared services.
4. The `dev` certificate Secret is restored into `andara-dev` (the namespace is created empty first) **before** Argo CD
   syncs the `Certificate`; on the first rebuild after the hostname change there is nothing to restore (the old name's
   certificate is discarded) and cert-manager issues once, so cert-manager finds the Secret instead of issuing and spending a Let's Encrypt slot
   **[verify: that cert-manager adopts a pre-existing Secret whose issuer annotations match]**.
5. `andara-dev` is created from the `dev` tag by Argo CD; `andara-local` by `make up`.
6. `ADR-0012` decision 7 step 4(ii) onward runs as written (ruler deletion at the point of no return, then the
   first series, `observe-check`, `required_environments`).
7. `AW-INF-034`'s drill is rerun on the rebuilt `dev`.

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

### 11. The edge, and the workstation proxy

Brian's direction (2026-10-10): the cluster publishes **8080 and 8443**; DNS and `nginx` stay in the repo that manages
the workstation configuration, and an `nginx` proxy routes `local.andara.valesordev.com` and
`dev.andara.valesordev.com` to the cluster. This repo owns the cluster side of that line and states the contract the
other repo implements; it changes nothing there.

**Options.** (a) `nginx` passes the TLS connection through to the cluster at layer 4 (`stream` with SNI
preread, `proxy_protocol on`), so cert-manager's certificates stay authoritative; (b) `nginx` terminates TLS with its
own certificate for `*.andara.valesordev.com` and proxies HTTP/2 to `:8080`.
(b) moves certificate management and gRPC/Connect/gRPC-Web proxying (`grpc_pass`, streaming timeouts) into the
other repo, makes the chart's `Certificate` resources redundant, and hides the client address from the
admin allowlist (it would see `nginx`) unless `X-Forwarded-For` is trusted. (a) keeps the edge exactly as the CI lifecycle
test exercises it, at the cost of the PROXY protocol on the entrypoint.

**Decision: (a).**

| Concern | Contract |
|---|---|
| Ports | the kind control-plane node's 80 and 443 are published as `127.0.0.1:8080` and `127.0.0.1:8443` (`deploy/kind/cluster.yaml`); bound to loopback because the proxy runs on the same host. Traefik's `hostPort` 80/443 inside the node is unchanged. Ports 80 and 443 on the host belong to `nginx`. |
| Hostnames | `local.andara.valesordev.com` (replaces `andara.local` and its `/etc/hosts` line) and `dev.andara.valesordev.com` (replaces `andara-dev.solo7.valesordev.com`); set in `values/local.yaml` and `values/dev.yaml`. `prod`'s `andara.solo7.valesordev.com` and CI's `andara.local` are unchanged. |
| TLS | `nginx` does not terminate. `dev`: the `letsencrypt` ClusterIssuer (DNS-01 through Cloudflare, unaffected by ports). `local`: the private CA `andara-ca`, with its CA written to `.local/tls/cluster/local/ca.pem` as today (the browser trusts it; the issuer needs no internet). |
| Client address | Traefik's `websecure` entrypoint trusts the PROXY protocol from the `kind` docker network's gateway, which `platform-up` reads with `docker network inspect kind` (it differs per machine; it is never a committed constant). The admin allowlist (`AW-INF-006`) then sees the real client, so `admin.allowedCIDRs` changes with it: `local` and `dev` carry `127.0.0.0/8` (a CLI on the workstation resolves the name to loopback and arrives as `127.0.0.1`), `dev` keeps `100.64.0.0/10` and adds the tailnet IPv6 range if used, and `172.16.0.0/12` stays only in `local` and CI for the `--resolve` path that bypasses `nginx` (it is no longer needed to admit docker-proxy's masked address on `dev`; remove it there, the 2026-10-02 403 of #305 was that masking). **Residual risk, stated:** any local process or container that can reach the published port arrives as the trusted gateway and can forge a PROXY header (claiming a tailnet or private address too), so the allowlist is a network-trust control, not authentication. **[verify first, before stories 2 and 6a start]** that Traefik accepts a connection with no PROXY header from a trusted peer (the `--resolve` and CI paths depend on it); if it requires the header, the fallback is: CI keeps trust off and the PROXY test runs against a separate test entrypoint, and `local` is reached through a local `nginx` instead of `--resolve`. CI's Traefik runs with trust on, with its own computed gateway; a CI test sends PROXY-protocol requests with forged client addresses and asserts a `403` for an address outside the list and a `200` for one inside. Owned by story 2, which lands the trust and adds `127.0.0.0/8` in one change; **removing `172.16.0.0/12` from `dev` is story 5's, after the CI PROXY test and the rebuild's resolution check pass**, so the #305 403 cannot return in between. `dev` reached by `--resolve` on the box is unsupported after the change (it arrives masked as the gateway); use the public name. |
| What the other repo does | DNS for the two names to the workstation; `nginx` `stream` blocks that `ssl_preread` the SNI and forward to `127.0.0.1:8443` with `proxy_protocol on`, with `proxy_timeout` long (12 hours; the default is 10 minutes of silence in both directions, which would cut a quiet `Subscribe` that the server holds open for hours) and `proxy_connect_timeout` 10 s; port 80 handled by an `http` redirect to HTTPS only (the `web` entrypoint trusts no PROXY header, so `proxy_pass` to `:8080` is not offered). It does not manage certificates for these names. |
| Zone and DNS scope | **[verify]** that the Cloudflare DNS-01 token covers the zone for `*.andara.valesordev.com`; the existing comment says only "under the box's solo7.valesordev.com zone". `dev`'s reachability (tailnet only or not) is decided in the other repo, with its DNS. |
| Scripts that talk to the edge | direct callers (`stream-soak`, the CLI against `local`) use the public name through `nginx`, or `--resolve <name>:8443:127.0.0.1` to skip it; no script hard-codes 443: `scripts/helm_install.sh`, `scripts/env_recover.py`, `scripts/stream_soak.sh` and `internal/smoke/soak_test.go` take `ANDARA_EDGE_PORT`, default 8443 (a direct dial to the box cluster); `values/ci.yaml` and the kind workflow set it to 443, because CI's cluster still publishes 80 and 443, and the CI kind job passes only with it set to 443 (an acceptance criterion of story 6a); story 6a owns them, with the values files' hostnames and the comments in `values/dev.yaml` and `values/local.yaml` that explain the docker-bridge source address, `deploy/helm/andara/values.yaml:109`, `deploy/helm/andara/README.md` and the Builder's Guide (`docs/builders/02`, `03`, `04`, `09`), which architecture amends in the same change. A soak through `nginx` of more than 10 minutes is a story 6a acceptance criterion. |

*Rebuilds and Let's Encrypt.* Rebuilding the cluster is routine, and Let's Encrypt allows five duplicate certificates for the
same names per week, so `make cluster-rebuild` exports to `.local/tls/` (gitignored, mode 0600) the issued `dev` certificate Secret **and
cert-manager's private CA root Secret `andara-ca-tls`** (which holds a CA private key, kept to the same standard as the other
local secrets) before deleting the cluster, and restores the CA root into the `cert-manager` namespace **after the namespace exists and before the first apply of
`deploy/k8s/cert-manager/andara-ca.yaml`** (the file holds the self-signed issuer, the `andara-ca` Certificate and the
`andara-ca` ClusterIssuer together; it is the Certificate existing without its Secret that generates a new root, not
the ClusterIssuer). The restored Secret keeps its `cert-manager.io/issuer-name`, `issuer-kind` and `certificate-name`
annotations and drops `resourceVersion`, `uid` and `ownerReferences`; `rotationPolicy: Never` governs renewal, not
adoption, so adoption is **[verify]**, and an acceptance test asserts the CA's serial and `notBefore` are identical
before and after a rebuild. The browser and CLI keep the CA they already trust. It falls back to the
staging issuer when the limit is hit. The certificate for the retired name `andara-dev.solo7.valesordev.com` is not
restored: **the first rebuild after the hostname change issues once for `dev.andara.valesordev.com`** (decision 9 step 4 is a
no-op that time), and the export runs again after it.

*Port collision.* The compose `stack-*` flow already uses host 8080 and 8443 (`ANDARA_HTTP_PORT`, `ANDARA_GRPC_PORT`).
Stories 6a and 7b retire it; until they land, `make cluster-up` checks that 8080 and 8443 are free and refuses, naming the
`ANDARA_*_PORT` variable that holds them, rather than failing inside Docker.

*Needs the other repo, which is Brian's:* the DNS records and the `nginx` `stream` configuration above. Until they exist, a
developer reaches `local` with `--resolve` against `:8443` and an `/etc/hosts` line for the name, so nothing here is
blocked by it; the first rebuild is not complete until `dev.andara.valesordev.com` resolves through `nginx`.

### 12. The follow-on stories

PM writes them after acceptance. Each is sized `S` or `M`, lane `sre` unless noted.

| # | Story | Size |
|---|---|---|
| 1 | Cluster as code: `deploy/kind/cluster.yaml` (ports 8080/8443 on loopback), `cluster-up/down/rebuild`, certificate Secret export and restore, `make bootstrap` installs `kind` and `kubectl` | M |
| 1b | Contexts: every script takes `ANDARA_KUBE_CONTEXT` instead of the ambient context (18 scripts), with a test that fails on a bare `kubectl` | M |
| 2 | Platform reconcile: `deploy/platform/versions.yaml`, `platform-up`, rewrite `kind_platform.sh`, both ClusterIssuers, Strimzi watching `andara-shared`, Traefik PROXY-protocol trust and the allowlist CIDRs, the CI PROXY-protocol test | M |
| 3 | Shared Kafka: `andara-shared`, prefixed topics, bounded `local.` retention, quotas and per-environment principals rendered from `topics.yaml`, NetworkPolicies, chart FQDN values and the `andara.valesor/env` namespace labels, `make shared-check` | M |

| 3b | ADR-0011's SASL for the broker (`ANDARA_KAFKA_SASL_*`), the per-environment Secret copy and RBAC, `kind.yaml` assertions | M |
| 3c | Shared schema registry (Karapace or Apicurio) in `andara-shared`, `schemas.py` takes `ENV`, prefixed subjects, NetworkPolicy | M |
| 4 | Shared object store: versitygw IAM users, a bucket and Secret per environment, `objectstore.py local`, 10Gi | S |
| 5 | The `dev` tag: `make promote`, the Application's `targetRevision`, deploy/rollback parity, remove Image Updater (and `kind.yaml`'s `already installed == 2`), the three `[verify]` items, `AW-INF-041` amendment | M |
| 6a | `andara-local`: `values/local.yaml` and `values/ci.yaml`, hostnames and `ANDARA_EDGE_PORT` in the scripts, the values-file comments, README and Builder's Guide, `make up/down/local-reset`, the `andara-it` principal and `make test-integration`'s backing, the compose-`min` baseline, the through-`nginx` soak | M |
| 6b | CI `drills` job: the throwaway single-broker Kafka and its operator install, its registry, the `kind.yaml` timeout, the manifest guard in `make check`, and the migration of `stack.yaml`'s kept steps (table above). `stack.yaml` is deleted only after 7a and 7b have landed | M |
| 6c | `recovery-timing` workflow set-up on the `drills` Kafka: an in-cluster Job with a built test binary and the sizing fixture, the result JSON copied back with `kubectl cp` for the artifact, `compare` and `startup-budget-check` (`AW-INF-011`), and the 90 s bound re-measured on kind | M |
| 7a | Broker drills: the drill lock (`drill-lock` ConfigMap and the checks in `make up`, `local-down`, `promote`, `topics-apply`) and `CONFIRM_LOCAL`, `kafka-broker-bounce` and `kafka-rehearsal` against `andara-shared`, `stack-boundary-lost` as a NetworkPolicy | M |
| 7b | `env-recover`, `observe-*` and the `stack-*` targets re-pointed at contexts and `andara-local` (with `ADR-0012`'s replacements for `AW-INF-048`) | M |
| 8 | Collectors and the telemetry token install on the new platform (`ADR-0012` decisions 13 and 15), the `observe_keep_list.py` exclusion for `andara-shared` | M |
| 9 | The rebuild run (decision 9), including the `dev.andara.valesordev.com` resolution check | S |
| 10 | Compose retirement, only after 6b, 6c, 7a and 7b: delete `deploy/compose`, the compose targets and drivers, `stack.yaml` and the old `recovery-timing` set-up; `make up` and `make down` take their kind meaning | S |
| I1 | **implementation:** `kafka.topic_prefix` in the server and the projector, every topic constant including `server/content/kafka.go`, the config validation of decision 4, tests | S |
| I2 | **implementation:** ADR-0011's SASL client in the shared constructor and its 20 `kgo.NewClient` sites | M |
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
  `ADR-0012` gets a dated annotation under decision 9 pointing at the `andara-shared` exception.
- **What keeps `local` series from reaching `solo7`** (the story's open question): the cluster holds only
  `solo7dev`'s telemetry token (the prod token lives on the prod cluster, never here), its collectors register with
  `stack=solo7dev`, and `environment` comes from the `stamp` table; there is no path from this cluster to `solo7`.
- **A cluster with no telemetry token** (`ADR-0012` decision 14): `make platform-up` installs the collectors with an
  empty credential Secret, they ship nothing, and the target prints that on its last line.
- **Is local's broker still Redpanda?** No: it is the shared Strimzi Kafka. ADR-0002 §7's "Redpanda locally" no longer
  describes the cluster environments (it stays true of nothing once compose goes); ADR-0002 gets a dated annotation, and
  `topics.yaml`'s `broker.local` Redpanda block is removed by story 6a.

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
- **The edge depends on a second repo.** `nginx` and DNS, outside this one, must carry the SNI passthrough and the PROXY
  protocol; a mistake there is an outage of `local` and `dev` that nothing in this repo detects. The contract is the
  table in decision 11; the check is the CI PROXY-protocol test and the rebuild's resolution step.
- **Local broker drills need an empty `local`.** The drill lock (decision 5) is a convention enforced by the targets,
  not by the cluster: a hand-run `kubectl` bypasses it.
- **Foreclosed:** a second Kafka for `local`; Image Updater; compose; a platform managed from another repo.

## Revisit when

- The drill lock (decision 5) is hit more than twice in a month: give `local` its own single-broker Kafka.
- A client or broker leaves the cluster or the host (decision 6): TLS on the listener, as `ADR-0011` says.
- Two environments starve each other once (disk, memory, partitions): split the shared service that did it.
- `make test-integration` on kind exceeds `ADR-0012` decision 14's threshold: bring back compose `min` for the
  tests, which this ADR's shared services then no longer back for that profile.
- `prod` is installed (decision 10), or a third non-production environment (`staging`) is wanted: a prefix, a
  principal, a bucket and a role are added per environment, which is the cost of each one.
- `nginx` (or its PROXY-protocol path) breaks `local` or `dev` twice: terminate TLS at `nginx` (option (b) of
  decision 11) and move certificates to the other repo, or publish 80/443 again.
- Argo CD's automated sync on a tag misbehaves twice (a tag moved mid-sync, a rollback that does not converge):
  move the promotion to a pull request instead of a force-moved tag.
