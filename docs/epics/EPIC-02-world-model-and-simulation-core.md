---
id: EPIC-02
title: World model and simulation core
phase: 1
component: server
milestone: M1
status: ready
adr_gates: []
adr_refs: [ADR-0001, ADR-0002, ADR-0007]
---

## Goal
A transport-agnostic, dependency-free simulation package that holds a Room graph, advances on a
deterministic Tick Loop driven by its Kafka Partitions, and emits Events. Given the same snapshot and
the same offset range it produces byte-identical output, every time.

## Why now
This is the load-bearing architectural claim of the whole project (CLAUDE.md §10). It has no ADR
gate. Every persistence, transport, and content decision downstream is shaped by the interfaces this
epic defines, so building it first means those decisions are made against something real.

## In scope
- Room, Exit, Zone, Entity types and the Zone-scoped state boundary from ADR-0001.
- Zone Definition loading and referential validation at boot.
- Deterministic Tick Loop consuming assigned Partitions, with tick SLIs from the first commit.
- Tick Boundary Records (`TickCompleted`) so replay reads tick boundaries rather than re-deriving them.
- Event emission through an interface the sim owns.
- The determinism test harness: replay equality, no clock, no unseeded randomness, no map-order
  dependence.

## Out of scope
- Any network transport — `EPIC-03`.
- Snapshots and recovery — `EPIC-04`. Recovery works from M1 by replaying the whole log; snapshots
  are an RTO optimization, not a correctness mechanism.
- The Kafka cluster, topics, and schema registry themselves — `EPIC-10`.
- Combat, skills, economy, or any specific game system. This epic delivers the substrate.

## Done when
A test can construct a World from a Content Pack, replay a recorded offset range, and assert the
resulting State Hash matches a golden value across runs, processes, and platforms.

## Stories
`AW-SRV-001`, `AW-SRV-002`, `AW-SRV-004`
