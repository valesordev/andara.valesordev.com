---
id: AW-INF-010
title: Local stack — the criteria that needed a server
epic: EPIC-01
component: infra
type: chore
status: ready
size: S
depends_on: [AW-INF-002, AW-CLI-002, AW-SRV-002, AW-SRV-010, AW-SRV-024]
blocks: []
lane: architecture
risk: low
---

## Context

`AW-INF-002` shipped the local stack on 2026-09-07 with four acceptance criteria that could only be
executed once there was a server to exercise them — recorded as pending rather than skipped, and
left open through two §8 passes. On 2026-09-17 Brian decided to split them out (the alternative was
marking a built, working story `blocked`), so `AW-INF-002` closes on what it did and this story
carries what it could not, each criterion named with the story it waits on.

One of the four turned out not to be waiting on what it said. AC-7 ("grep the log sink for the
Session ID") was recorded partial against a synthetic line; with a real server the log sink is empty,
because the server never exported logs (`AW-SRV-024`). This story's criteria are executed, not
reasoned, and AC-7's is the one that already found something.

## User story

As a developer, I want the local stack to prove end to end what `AW-INF-002` promised — a Session
over TLS, a trace across the log boundary, the logs behind it, and a dashboard with live tick data —
so that the stack's guarantees are observed rather than remembered.

## Scope

### In scope
The four criteria below, executed against `make up` with the server, CLI, and pipeline stories they
name, and recorded in a verification table like `AW-INF-002`'s.

### Out of scope
- Changing the stack. If a criterion needs a compose change, that change is a defect in the story
  that introduced the need and lands there.

## Acceptance criteria

Numbered as in `AW-INF-002` so the two records read together.

5. **Given** a running stack and `ANDARA_CONFIG` pointing at `.local/cli.yaml` **when**
   `andara-cli play` connects (`AW-CLI-002`) **then** it does so over TLS against the local CA with no
   insecure flag and no certificate warning.
6. **Given** a running stack **when** a player issues one command **then** a trace for it is retrievable
   from Tempo by Session correlation ID with spans `command.execute` (`AW-SRV-010`), the Kafka produce
   (`AW-SRV-010`), and the tick's apply (`AW-SRV-002`) in parent/child order.
7. **Given** that Session **when** Loki is queried `{service_name="andara-server"} | json |
   session_id="<id>"` **then** the server's structured lines for it are returned (`AW-SRV-024`) — and
   the `trace_id` on them opens the AC-6 trace.
8. **Given** the pre-provisioned dashboard **when** the server has ticked for a minute **then** tick
   duration, tick overrun count, simulation lag (`AW-SRV-002`), and per-partition consumer lag are
   plotted with live data.
12. **Given** a running stack **when** the Redpanda container is stopped **then** the server enters the
    read-only degradation mode (`AW-SRV-010`) rather than crashing, and `make up` recovers it.

## Interface contract

None new. Every command, port, and file is `AW-INF-002`'s.

## Data / state impact

None.

## Observability requirements

This story verifies `AW-INF-002`'s; it adds none.

## Test plan

- **Integration (compose, `stack` workflow):** AC-6, 7, 8, 12 as steps of the workflow once their
  stories land, so they run on every stack change rather than once.
- **Manual/operator:** AC-5, with the command from `AW-INF-002`'s test plan.

## Definition of done

CLAUDE.md §8, plus: a verification table with a dated result per criterion, and `AW-INF-002`'s
record pointing here.

## Open questions

None. Every open item is a dependency, named per criterion.
