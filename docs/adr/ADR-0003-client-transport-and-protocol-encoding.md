---
id: ADR-0003
title: Client transport and protocol encoding — gRPC over HTTP/2 with TLS
status: accepted
date: 2026-09-07
deciders: [brian]
gates: []
---

## Context

Phase 1 ships a playable world over a text interface. Phase 2 ships a WebGL client. `andara-cli`
speaks the same Protocol as both, because CLAUDE.md §10 forbids a privileged back door.

The draft framed this as text-protocol-versus-structured. Brian has decided: **gRPC over HTTPS**, so
that the CLI and the eventual client share one contract and the transition between them is a client
concern rather than a protocol rewrite.

## Decision

**gRPC over HTTP/2, TLS everywhere, protobuf as the message encoding** (ADR-0007 makes protobuf the
single schema authority across the wire, the log, and content).

### Service shape

Two services on one endpoint:

- `andara.game.v1.Game` — the player surface. Session establishment, Command submission, and a
  server-streaming Event subscription.
- `andara.admin.v1.Admin` — the Operator and Builder surface. Same endpoint, same TLS, same auth,
  different authorization. **Not** a separate port and **not** a separate protocol: an admin action
  is a Command with a privilege requirement, and routing it differently is how back doors get built.

### Event delivery

Server→client Events are a server-streaming RPC, one stream per Session. gRPC's HTTP/2 flow control
provides backpressure for free at the transport layer, but it must not become backpressure on the
tick: a Session whose stream will not drain is disconnected once its server-side buffer fills, per
ADR-0004's predecessor rule and `AW-SRV-004`'s subscriber policy. The sim's `EventSink` never blocks.

### Version negotiation

Protocol version is an integer in Session establishment metadata. The server declares a supported
range; a client outside it is rejected with a typed error naming both. Never silently degraded.
Protobuf field-number discipline (ADR-0007) handles additive evolution *within* a version; the
integer exists for the changes protobuf cannot absorb.

### The Text Interface is `andara-cli play`

This is the consequence of choosing gRPC that most needs stating: **a human cannot telnet into a gRPC
server.** The Phase 1 text surface is therefore a first-class gRPC client that renders Events as prose
in a terminal, shipped as a subcommand of `andara-cli`.

This is a better outcome than a second listener. It means there is exactly one protocol from the first
day, the CLI is exercised by every play session rather than only by Operators, and the Phase 2 client
is a different renderer against a contract that has been in daily use for months. It also means M1's
gate changes wording — "a human runs `andara-cli play` and moves between two rooms" — without changing
what M1 proves.

The cost is real and worth naming: we lose the ability for anyone to connect with a tool they already
have. Onboarding a playtester now means shipping them a binary. For a closed launch (ADR-0006) that is
acceptable.

### Browsers do not speak gRPC

Phase 2's WebGL client cannot open a raw gRPC stream — browsers cannot control HTTP/2 framing. The two
ways out are a gRPC-Web proxy in front of the server, or the **Connect** protocol, which is
gRPC-compatible, speaks to browsers natively over HTTP/1.1 and HTTP/2, and needs no proxy.

**Decision: serve Connect, gRPC, and gRPC-Web from the same handler.** Connect's Go implementation
does this from one service definition, so Phase 1 pays nothing for it and Phase 2 inherits a browser
path with no infrastructure. Choosing this now costs one library decision; discovering it in Phase 2
costs an Envoy deployment.

## Consequences

- **TLS is not optional, including locally.** `make up` provisions a local CA and certs, or developers
  learn to pass an insecure flag and eventually ship it. The local stack does the work.
- **Protobuf becomes the schema for everything** (ADR-0007). Kafka record values, snapshots, and
  content manifests all use it. One schema discipline instead of three.
- **Streaming reconnect is now a client concern.** A dropped stream is a dropped Session unless the
  client re-establishes and resumes. Resume semantics — replay missed Events from an ID, or resync
  from current state — is a `AW-SRV-011` question and interacts with ADR-0006's linkdead grace period.
- **We are foreclosing** telnet, MUD clients, and any third-party tooling that expects a line protocol.
  If a MUD-client-compatible gateway is ever wanted, it is a translating proxy in front of gRPC, and
  it is a separate component with its own story — never a second path into the simulation.
- Debugging is `grpcurl`, not `nc`. Worth having in `make bootstrap`.


## Resolved since acceptance

**2026-09-07 (Brian) — the Text Interface is permanent, and it is a technical client.** This ADR left
open whether `andara-cli play` was a supported client or Phase 1 scaffolding. It is permanent.
`andara-cli` is text-only for its whole life and will never render anything; rendering is `CLT`'s job
and always will be.

The part that changes engineering decisions is who it is for. `play` is an Operator and Developer
tool that a human can also play through — so seeing Intents and Events go back and forth at a
technical level is a first-class feature of it, not a debug flag. `AW-CLI-004` carries the
consequences.

This does not weaken the Phase 1 exit criteria: a human still has to play through it, and it is still
the only client for months. It means that when polish competes with protocol visibility, visibility
wins.

## Revisit when

- A closed playtest is bottlenecked on binary distribution rather than on the game.
- Connect's browser story fails to hold up under the Phase 2 client's actual streaming needs.
- Per-Session message rate makes protobuf-over-HTTP/2 framing overhead measurable, which at MUD rates
  it will not be.
