---
id: AW-SRV-044
title: One Kafka client constructor, with SASL/SCRAM
epic: EPIC-10
component: server
type: feature
status: draft
size: M
depends_on: [AW-SRV-019]
blocks: [AW-INF-030]
lane: implementation
risk: medium
---

## Context

ADR-0011 (accepted 2026-09-29) puts SASL/SCRAM-SHA-512 on every broker listener and denies by
default. That gives each workload its own principal, and it's how `AW-SRV-019` AC-9's "only
`andara-projector-state` writes `andara.state.v1`" gets enforced at all. A broker that requires
SASL refuses any client that can't speak it, so the clients have to move first. Today the server
and the projector build their franz-go clients at about 20 `kgo.NewClient` sites, each choosing its
own options, and none speaks SASL.

This story is ADR-0011's decision items 6 and 7, the implementation half of AC-9's carrier
(ADR-0011 *Consequences*). Every client is built by one shared constructor, which reads its
credentials from the environment and a mounted file, and reads the password again on each new
connection. `AW-INF-030` then turns SASL on in the compose stack and asserts AC-9 in CI. A broker
without SASL keeps working, because a process given no username connects as it does today.

## User story

As an operator, I want every Kafka client in the server and the projector to authenticate as its
own workload's principal, so that the broker can hold each one to its ACLs.

## Scope

### In scope
- One constructor that every `kgo.NewClient` in the repository goes through, in `server/`, `cmd/`,
  `internal/` and `admin/`, test files included. It
  supplies the seed brokers, the client ID, and the SASL mechanism.
- SASL/SCRAM-SHA-512 when a username is configured, with the password read from a file on each new
  connection.
- Moving every client site onto the constructor, test fixtures included, plus a check that keeps
  new sites from bypassing it.
- The config keys in `server/README.md`, and in the chart's values schema (`keys.yaml`), with no
  defaults that carry a secret.

### Out of scope
- Turning SASL on in any broker, the `principals:` declaration, ACLs, and the per-workload Secrets
  in the chart. Those are `AW-INF-030`, plus `AW-INF-031` for `dev`.
- TLS on the Kafka listener (ADR-0011, *Revisit when*).

## Acceptance criteria

1. **Given** the source tree **when** `make check` runs **then** a test fails if any `.go` file
   outside the constructor's package calls `kgo.NewClient` directly. That includes `*_test.go`
   files, in every directory. The only exemptions are test files on an explicit allowlist kept in
   the guard, each with a comment saying why it needs raw franz-go. The allowlist starts empty.
   Today 18 call sites in 10 test files, across 7 packages (`admin/cli`, `internal/smoke`, `server/content`,
   `server/ingress`, `server/projector`, `server/recordlog`, `server/tickloop`) move onto the
   constructor. *(Revised after review of #316: the first draft exempted test files, which is where
   an overlooked fixture would bypass SASL.)*
2. **Given** `ANDARA_KAFKA_SASL_USERNAME=u` and `ANDARA_KAFKA_SASL_PASSWORD_FILE=/f` **when** a
   client connects **then** it authenticates with SCRAM-SHA-512 as `u`, with the password read from
   `/f`.
3. **Given** a connected client **when** `/f` is rewritten with a new password and the broker drops
   the connection **then** the next connection uses the new password, with no restart.
4. **Given** brokers configured and no username **then** clients connect without SASL, as today. A
   SASL-only listener then refuses them at the first request, and the process logs it at `error`.
5. **Given** a username but a password file that's missing or unreadable **when** the process boots
   **then** it exits `1` naming the path, before any client connects.
6. **Given** a username and an empty password file **then** the outcome is the same as AC-5.
7. **Given** the compose stack with SASL on (`AW-INF-030`) **when** the server and the projector run
   with their own credentials **then** every client they build authenticates. This is asserted in
   `AW-INF-030`'s CI run, which this story's constructor makes possible.

## Interface contract

```go
// CONTRACT SKETCH — not an implementation
package kafkaclient // placement is implementation's; one package, imported everywhere

type Config struct {
    Brokers          []string // kafka.brokers
    ClientID         string   // per call site, e.g. "andara-server/tickloop"
    SASLUsername     string   // kafka.sasl_username; empty = no SASL
    SASLPasswordFile string   // kafka.sasl_password_file; read per connection
}

// New validates cfg (AC-5, AC-6) and returns a client with the shared options plus opts.
func New(cfg Config, opts ...kgo.Opt) (*kgo.Client, error)
```

| Key | Env | Default | Meaning |
|-----|-----|---------|---------|
| `kafka.sasl_username` | `ANDARA_KAFKA_SASL_USERNAME` | empty | the principal. Empty means no SASL |
| `kafka.sasl_password_file` | `ANDARA_KAFKA_SASL_PASSWORD_FILE` | `/etc/andara/secrets/kafka/password` | read on each new connection |

- The same two keys apply to `andara-projector`. Each workload mounts its own Secret at
  `/etc/andara/secrets/kafka` (ADR-0011 decision 6).
- The mechanism is SCRAM-SHA-512 only. There's no config to pick another.
- The password is never logged, never put in a span attribute, and never in an error string. A
  test asserts this over the error paths of AC-5 and AC-6.

## Data / state impact

None. No topic, offset or record changes. With no username configured, behaviour is unchanged,
so this story can roll out before any broker requires SASL.

## Observability requirements

- **Metrics:** `andara_kafka_auth_failures_total{workload}` (counter), with `workload` in
  `server|projector-state`. That's a cardinality of 2, pre-seeded at 0, incremented on a SASL
  authentication failure. *(PM's proposal, for SRE's review.)*
- **Logs:** `error` `kafka authentication failed`, with `workload`, `principal`, `broker` and
  `error`, and never the password. `info` once at boot: `kafka client config`, with `principal`
  (or `none`) and `mechanism`.
- **Traces:** none new. Connection setup isn't trace-worthy (CLAUDE.md §7).
- **Alerts:** none here. Any alert on authentication failure is `AW-INF-030`'s, tied to an SLO.

## Test plan

- **Unit:**
  - the bypass check (AC-1);
  - config validation (AC-5, AC-6);
  - the per-connection password read, with a fake dialer (AC-3);
  - no SASL when there's no username (AC-4);
  - no password in any log, span or error.
- **Integration:** a Redpanda with SASL on, in the test fixture, for ACs 2–4: authenticate, rotate
  the password, and refuse a client with no username. The full compose assertion is `AW-INF-030`'s
  (AC-7).
- **Manual/operator:** none beyond `make check`.

## Definition of done

CLAUDE.md §8.

## Open questions

- `[ASSUMPTION]` The password file's default path is ADR-0011's mount path plus `password`, the
  key Strimzi's User Operator writes.
- `[ASSUMPTION]` `depends_on: AW-SRV-019` because the projector's clients must be among those
  moved. The story can start while `AW-SRV-019` sits at `review`.
