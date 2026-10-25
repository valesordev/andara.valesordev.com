# Kafka credentials on the shared broker

No alert fires this. It is the procedure for the shared broker `andara-log`'s credentials:
- what they are;
- how a workload gets its credential;
- how to rotate one;
- what to do when a client can't authenticate or is refused.

Since `AW-INF-056` the broker authenticates every client with SASL/SCRAM-SHA-512 and denies by
default (`ADR-0011`, as `ADR-0013` decision 6 amends it).

## What exists

| Principal | Who uses it | Where its Secret lives |
|-----------|-------------|------------------------|
| `andara-server-<env>`, `andara-projector-state-<env>` | that environment's server and state projector | Written by the User Operator in `andara-shared`, then copied by `make kafka-creds ENV=<env>` into `andara-<env>` only |
| `andara-schema-registry` | Karapace (`AW-INF-057`) | `andara-shared`, where Karapace runs, so it isn't copied |
| `andara-operator` | the one superuser, for the operator's tools only: the rpk toolbox, `topics-apply`, `world-reset`, `shared-check`, `kafka-broker-bounce` | `andara-shared`. No workload mounts it, and `make kafka-users-check` fails if a chart value names it |

All of these are declared in `deploy/kafka/topics.yaml` (`principals`, `platform_principals`). `make shared-up` renders each into a Strimzi `KafkaUser` and applies it. The User Operator writes the SCRAM credential to the broker, writes the ACLs, and writes a Secret named after the principal, with the password under `password`. Hand-editing a `KafkaUser` or an ACL is drift: `make topics-diff` reports it.

A workload's copy holds `password` only. The chart mounts it at `/etc/andara/secrets/kafka` and names the principal in `ANDARA_KAFKA_SASL_USERNAME`:
- the server's from `server.kafka.sasl_username`;
- each projector's from its own `projectors.<name>.kafkaCreds.secretName`, never the server's.

Clients read the password file on each new connection.

## Give an environment its credentials

```
make kafka-creds ENV=dev
```

This copies dev's principals into `andara-dev`, and removes any copy whose principal `topics.yaml` no longer declares. It prints one line per principal, `kafka-creds: andara-server-dev -> andara-dev created|updated|unchanged`, and never a value.

Exit codes:
- `1`: a source Secret is missing in `andara-shared`. Run `make shared-up`, which applies the KafkaUsers and waits until each one is Ready.
- `2`: usage.
- `3`: no cluster.

Run it after `make shared-up` on a new cluster, and before the environment's server starts. A server that starts without its copy fails to mount the Secret and stays `ContainerCreating`.

## Rotate a credential

```
make kafka-creds-rotate ENV=dev PRINCIPAL=andara-server
```

1. The target deletes the User Operator's Secret for `andara-server-dev`.
2. It annotates the `KafkaUser` so the operator reconciles at once. The User Operator generates a new password and writes it to the broker, and from then on the broker refuses the old one.
3. The target waits up to 180 s for the new Secret, then runs `make kafka-creds ENV=dev`.

A running server or projector keeps working, with no restart:
- its open connections stay up;
- the kubelet refreshes the mounted Secret within about a minute;
- the next connection reads the new password.

In the gap between the broker taking the new password and the kubelet refreshing the file, a *new* connection is refused. The client logs it at `error` with `reason="sasl_failed"` (`andara_kafka_client_auth_failures_total`) and retries.

Rotate the operator's own credential (`andara-operator`) or the registry's by deleting its Secret in `andara-shared` and re-running `make shared-up`. Their consumers read the Secret directly:
- the `make` targets read it on each run;
- Karapace reads it at start, so `kubectl --context kind-andara -n andara-shared rollout restart deploy/andara-schema-registry`.

## A client can't authenticate, or is refused

- **`reason="sasl_failed"`** in a server or projector log, or in `andara_kafka_client_auth_failures_total`: the username and password don't match the broker's. In order:
  1. Is the environment's copy there? `kubectl --context kind-andara -n andara-<env> get secret andara-server-<env>`. If not, `make kafka-creds ENV=<env>`.
  2. Was it rotated since the copy? Run `make kafka-creds ENV=<env>` again. It's idempotent and prints `unchanged` when the copy is current.
  3. Does the username name the right principal? The pod's `ANDARA_KAFKA_SASL_USERNAME` must be `<workload>-<env>`. `make helm-test` holds that for every workload.
- **`reason="topic_unauthorized"`**, or an `rpk` error `TOPIC_AUTHORIZATION_FAILED` or `GROUP_AUTHORIZATION_FAILED`: authentication worked, and the ACL refused the operation.
  - If the operation is one `topics.yaml` grants, `make topics-diff ENV=<env>` names the missing ACL. The User Operator puts back a removed ACL of its own `KafkaUser` on its next reconcile (about two minutes; seen on 2026-10-25), and `make shared-up` re-applies everything. `make kafka-acl-probe` asks the broker, as every principal, what each may and may not do.
  - If `topics.yaml` doesn't grant it, the client is doing something its principal may not (`ADR-0011`: least privilege by topic). The fix is a reviewed change to `topics.yaml`, not a hand-added ACL.
- **A `make` target exits 3 naming `andara-shared/andara-operator`**: the operator's Secret is missing, so nothing can authenticate as the operator. `make shared-up` applies the KafkaUsers and waits for it.
- **A connection that times out instead of being refused**: that's the listener's NetworkPolicy, which refuses before SASL. Only pods labelled `app.kubernetes.io/name: andara` in a namespace labelled `andara.valesor/env` (`local`, `dev`) are admitted, plus the toolbox, the brokers and the registry in `andara-shared`. `make shared-netcheck` shows the whole matrix.

## Undo SASL

Restore the previous `deploy/k8s/kafka/kafka.yaml`: the `plain` listener with no `authentication` and no `authorization` block. Then `make shared-up`, and point the clients back at no SASL (`server.kafka.sasl_username: ""`, and the registry without its `KARAPACE_SASL_*` settings). The brokers roll, and no topic, offset or record changes.
