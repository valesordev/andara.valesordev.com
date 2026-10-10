---
id: ADR-0011
title: Broker authentication and authorization
status: accepted          # draft | proposed | accepted | rejected | superseded by ADR-XXXX
date: 2026-09-29
deciders: [brian]
gates: []                 # epic/story IDs that cannot reach `ready` until this is accepted
---

## Context

ADR-0002 makes Kafka the World's write-ahead log, and it's the ordering authority for everything:
Commands, Events, Accounts, audit, content. Today any process that can reach the broker can write
any of it. The broker authenticates nobody. The only barrier is the listener's NetworkPolicy,
which admits the chart's pods and the `rpk` toolbox (`AW-INF-014` AC-6).

That was a deliberate deferral. `AW-INF-014` put TLS and SASL out of scope, and the chart's
`secrets.kafkaCreds` mount has sat unused since. The deferral now has a cost:

- **`AW-SRV-019` AC-9 can't be met or tested.** The state projector is `andara.state.v1`'s only
  legitimate writer. A second writer would break the projector's single-hash assertion without
  the projector ever noticing, because it doesn't read its own output. The write ACL is declared
  in `deploy/kafka/topics.yaml` and enforced nowhere. PM asked on 2026-09-25 which mechanism, and
  under which ADR, so the story that carries AC-9 can be groomed
  (`docs/feedback/AW-SRV-019-state-projector.md` §5).
- **More writers are coming.** The Redis and Postgres projections (`AW-SRV-017`, `AW-SRV-018`) are
  read-only consumers of `andara.state.v1`. Under today's model they're full writers of every
  topic, including the Command log and the audit trail. An index projector with a bug, or a
  compromised pod, could append Commands the World would then apply.
- **The audit topic's value depends on it.** `andara.audit.v1` is "the record of who did what with
  operator authority". Anyone in the namespace can append to it or rewrite it.

The constraint that makes this non-obvious is ADR-0002 §7: **Redpanda locally, Kafka on `dev` and
`prod`.** The mechanism has to exist on both brokers, or AC-9 is asserted only on the box, by hand,
and `make check` never sees it. This rules out anything only one broker supports. What's left in
common is SASL/SCRAM-SHA-512 and mutual TLS.

What talks to the broker today:

| Workload | Kafka clients | Topics |
|----------|---------------|--------|
| `andara-server` | `ingress`, `tickloop`, `recordlog`, `auth`, `content` (20 `kgo.NewClient` sites across the server and projector code) | writes commands, events, accounts, audit, and the three content topics; reads the same |
| `andara-projector state` | `server/projector` | reads commands and events; writes and reads state |
| the `rpk` toolbox (`andara-kafka-tools`) | `scripts/topics.py`, `make world-reset`, operators | creates, alters, and deletes topics; reads anything |

`andara-cli` never talks to Kafka (CLAUDE.md §10: no back door), and neither do Behavior Agents,
which submit over `andara.game.v1.Game` (ADR-0005). Neither needs a principal.

## Options considered

### Option A: keep NetworkPolicy only

Admission by pod label, as now. Declare ACLs as documentation.

**Good:** Nothing to build or operate. No credentials to rotate.
**Bad:** A NetworkPolicy authenticates a *pod label*, and anything carrying the label is admitted
with full rights. It can't express "the projector may write `andara.state.v1` and the server may
not", so AC-9 stays unmeetable. It also isn't testable on the compose stack, which has no
NetworkPolicy.
**Costs us:** every future consumer is a writer of the Command log.

### Option B: SASL/SCRAM-SHA-512 on the in-namespace listener, with ACLs

Each workload is a principal with a password. The listener requires SCRAM, and the broker
authorizes each request against ACLs, denying by default.
- **Kafka (Strimzi):** listener `authentication: scram-sha-512` and `authorization: simple`. Users
  are Strimzi `KafkaUser`s, which the User Operator turns into SCRAM credentials, ACLs, and a
  generated password Secret per user.
- **Redpanda:** SASL enabled on the listener, users and ACLs created with `rpk security`. Redpanda
  supports SCRAM-SHA-512, the same mechanism.
- **Clients:** franz-go's `sasl/scram`, with the password read from a mounted file.

**Good:**
- One mechanism on both brokers, so the compose stack and CI enforce exactly what `dev` enforces.
  AC-9 becomes an integration test.
- The Strimzi User Operator already owns password generation and the Secret, which is the part
  that's easy to get wrong.
- SCRAM is challenge-response, so the password never crosses the wire.
- Rotation is changing a Secret: clients that read the password per connection pick it up
  without a restart.

**Bad:**
- The payload isn't encrypted. That's acceptable only while every client and broker share a
  namespace, which is true today (`AW-INF-014`: one cluster per `andara-<env>` namespace; see the ADR-0013 amendment below).
- Two brokers means two ways to create a user: Strimzi's operator and `rpk`.

**Costs us:** a password Secret per principal per environment, and a local stack where every
`rpk` call needs credentials.

### Option C: mutual TLS

Each workload presents a client certificate, and its subject is the principal. On Kafka, Strimzi's
Clients CA issues `KafkaUser` certificates. Locally, Redpanda maps a client certificate's CN to a
principal.

**Good:** Encryption in transit, and client auth with no shared secret. It's the natural next
step if traffic ever leaves the namespace.
**Bad:**
- The compose stack needs its own CA, issuance, and renewal, and has to match Strimzi's behavior.
- Strimzi rotates client certificates on its own schedule. Every client then needs certificate
  hot-reload, or a restart the broker didn't ask for.
- It buys encryption this deployment doesn't need yet.

**Costs us:** certificate lifecycle machinery on both brokers, built to protect traffic that never
leaves one namespace on one host.

### Option D: SCRAM over TLS

Option B plus a TLS listener.

**Good:** Everything B gives, plus encryption.
**Bad:** C's local CA problem without C's benefit, since the credential is still a password.
**Costs us:** the same as C, now.

## Decision

**Option B: SASL/SCRAM-SHA-512 on the in-namespace listener, deny by default, one principal per
workload.** It's the only option that both brokers enforce identically, so it's the only one CI can
hold to. TLS waits for a reason (*Revisit when*).

1. **The broker authenticates every client.** A SASL/SCRAM-SHA-512 listener replaces the `plain`
   listener in every environment, including the compose Redpanda. There is no unauthenticated
   listener for clients. Strimzi's own inter-broker and operator traffic keeps its internal TLS
   listener, which this ADR doesn't touch.
2. **Deny by default.** Kafka runs `authorization: simple` with `allow.everyone.if.no.acl.found`
   false, and Redpanda runs with ACLs enforced. A principal can do exactly what its ACLs grant.
3. **One principal per workload, not per environment.** Each environment has its own cluster, so
   names don't carry the environment:

   | Principal | ACLs, in outline |
   |-----------|------------------|
   | `andara-server` | Write and Read on commands, events, accounts, audit, and the three content topics; Read on the `andara-sim-` and `andara-content-` group prefixes. **No Write on `andara.state.v1`** |
   | `andara-projector-state` | Read on commands and events; Write and Read on `andara.state.v1`; Read on the `andara-projector-state-` group prefix |
   | `andara-projection-redis`, `andara-projection-pg` | Declared by `AW-SRV-017` and `AW-SRV-018` when they land: Read only, on the topics their row in `consumer_groups` names |
   | `andara-operator` | Superuser. The `rpk` toolbox only: topic apply, `world-reset`, and investigation |

   The exact operations are the implementing story's to derive from the clients, and its tests
   assert them. The rule is least privilege by topic. A new consumer declares a new principal. It
   doesn't borrow the server's.
4. **Principals and ACLs are declared in `deploy/kafka/topics.yaml`,** beside the topics they
   govern, under a `principals:` section. That file stays the one declaration (`AW-INF-004`). The
   apply path renders it into `KafkaUser` resources for Strimzi and into `rpk security` calls for
   Redpanda. Neither broker gets a hand-written user or ACL. `make topics-diff` reports drift in
   ACLs as it does in topic config.
5. **Strimzi's User Operator is enabled for users only.** `AW-INF-014` excluded both the Topic and
   User Operators to keep `topics.yaml` single-sourced. The Topic Operator stays excluded. The User
   Operator comes in, because its `KafkaUser`s are *generated from* `topics.yaml` (item 4), so they
   aren't a second declaration. Hand-editing a `KafkaUser` is drift, like hand-editing a topic.
6. **Credentials are Kubernetes Secrets, one per principal, mounted as files.** A workload's Secret
   is the one Strimzi's User Operator generates for its `KafkaUser`, named after the principal,
   with the password under `password`. The chart gives **each workload its own Secret
   reference**: `secrets.kafkaCreds.secretName` is the server's (`andara-server`), and each
   projector has `projectors.<name>.kafkaCreds.secretName` (`andara-projector-state`, and so on).
   No workload falls back to another's reference. A projector with no reference of its own renders
   no mount, and fails to authenticate. Every workload mounts its Secret at the same path,
   `/etc/andara/secrets/kafka`, so the client code is identical. Clients read the username from
   `ANDARA_KAFKA_SASL_USERNAME` and the password from the file `ANDARA_KAFKA_SASL_PASSWORD_FILE`
   names, **on each new connection**, so a rotated Secret takes effect without a restart. On the
   compose stack the passwords are fixed and local-only, and they live in the compose file. A
   process given brokers and no username connects without SASL, and any SASL listener then
   refuses it at the first request, loudly.
   *(Amended 2026-09-29, from Codex on #163: the first text pointed every workload at the one
   existing `secrets.kafkaCreds`, which would have given the server the projector's rights.)*
7. **One place builds a Kafka client's options.** Every `kgo.NewClient` in the server and projector
   takes its seed brokers, client ID, and SASL mechanism from one shared constructor. A client site
   that forgets SASL must not be able to compile past review, and the compose stack running with
   SASL on is the test that catches one.

## Consequences

- **`AW-SRV-019` AC-9 gets a carrier, as two stories.** PM grooms them:
  - an **implementation** story for decision items 6 and 7: the shared client constructor, SASL
    config, the password file read per connection, and every `kgo.NewClient` moved onto it;
  - an **SRE** story for items 1–5: the Strimzi listener, authorization and User Operator, the
    `principals:` section and its apply path on both brokers, a Secret reference per workload in
    the chart (decision 6), the compose Redpanda with SASL,
    `make` wrappers so an `rpk` call carries the operator's credentials, and the `dev` migration.
    AC-9 is its inherited Definition-of-done line, asserted on the compose stack in CI.
- **The local loop gets heavier.** Every `rpk` call against the compose stack needs credentials,
  so the targets that wrap `rpk` supply them. A developer running `rpk` by hand passes
  `-X user=… -X pass=…` or uses the target. That's the price of CI enforcing what `dev` does.
- **`dev` migrates, and no client is denied at any step.** Kafka refuses to create an ACL while no
  authorizer is configured (`SecurityDisabledException`), so authorization has to come first, and
  the clients still on `plain` have to be let through while it does. The SRE story pins the
  details. The order is fixed here:
  1. **Authorizer on, nobody restricted yet.** Turn on `authorization: simple`, with
     `andara-operator` and `ANONYMOUS` as super users. Clients on `plain` authenticate as
     `ANONYMOUS`, so they keep every right they have today. Brokers roll.
  2. **Principals and ACLs installed and verified.** Add the SCRAM listener beside `plain`, and
     enable the User Operator, which now can create the `KafkaUser`s' credentials and ACLs. Check
     them with `make topics-diff` before any client depends on them. Brokers roll.
  3. **Clients move.** Each workload redeploys onto the SCRAM listener with its own Secret, and is
     now held to its ACLs. They're in place since step 2, so nothing is denied.
  4. **The bootstrap access goes.** Remove `plain`, and `ANONYMOUS` from the super users. Brokers
     roll.

  Each step is a separate apply, and each can be undone by restoring the previous one. From step 4,
  rollback is restoring `plain` with `ANONYMOUS` as a super user. No topic, offset, or record
  changes at any step. *(Amended 2026-09-29, from Codex on #163: the first order installed the
  ACLs before the authorizer, which Kafka refuses, then turned authorization on last, which would
  have denied every client until reconciliation.)* The compose Redpanda starts empty, with SASL
  and ACLs on from its first boot, so it has no migration.
- **Traffic is still unencrypted inside the namespace** (ADR-0013: now inside one cluster on one host). The NetworkPolicy stays as a second,
  independent barrier. Anyone who can capture the namespace's pod traffic can read the World's
  records. They can't write them without a password, which is the property this ADR is for.
- **The audit trail's integrity improves, but it isn't complete.** Only `andara-server` and the
  operator superuser can append to `andara.audit.v1`. The superuser can still do anything, so
  operator actions *on the broker itself* aren't audited by the World. That's a known gap. The
  operator principal is used through `make` targets, not handed out.
- **Forecloses:** a consumer sharing another workload's credentials to save a Secret. Every new
  Kafka client arrives with a principal declared in `topics.yaml`, or it can't connect.

> **Amended by ADR-0013, 2026-10-10 — the SASL/SCRAM decision stands.** Local and dev now share one Strimzi
> cluster in the namespace `andara-shared`, so the premises "each environment has its own cluster" and "every client
> and broker share a namespace" no longer hold. Principals gain an environment (`andara-server-dev`,
> `andara-server-local`, …) with ACLs scoped to that environment's topic and group prefix; the listener's
> NetworkPolicy admits the environments' namespaces by label; the encryption sentence reads "one cluster on one
> host"; and the first "Revisit when" trigger reads "leaves the cluster or the host". Nothing here was implemented
> yet, so the amendment costs no migration. The clause "including the compose Redpanda" lapses with compose (ADR-0012 decision 14): SASL applies to the Strimzi broker in
> `andara-shared` and to CI's throwaway Kafka. Recorded as an amendment, not a supersession, because the mechanism is
> unchanged.

## Revisit when

- **Any Kafka client or broker leaves the namespace** (ADR-0013 reads this as leaving the cluster or the host)**:** `prod` on a separate cluster, a client on
  another host, cross-cluster replication, or a managed Kafka. That's the signal for TLS on the
  listener. SCRAM over TLS (Option D) is the smallest step. mTLS (Option C) is the step if we
  also want to drop passwords.
- **More than one human operator** needs to act on the broker. Then the shared `andara-operator`
  superuser stops being attributable, and it becomes per-person principals.
- **Either broker drops SCRAM-SHA-512,** or Strimzi's User Operator can no longer be driven from a
  generated declaration.
- **A requirement for encryption in transit** appears, from compliance or from hosting.
