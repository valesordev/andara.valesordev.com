---
id: AW-XXX-NNN
title: <imperative phrase, what the system will do>
epic: EPIC-NN
component: server          # server | cli | infra | client
type: feature              # feature | infra | spike | chore | bug
status: draft              # draft | ready | in-progress | review | done | blocked
size: M                    # S | M | L  — L means "split it"
depends_on: []
blocks: []
assignee: cursor           # cursor | claude-code
risk: medium               # low | medium | high
---

## Context
Why this exists now, and what breaks or stalls without it. Two paragraphs maximum.
Link the epic and any ADR that constrains the approach.

## User story
As a <player | builder | game master | operator | developer>, I want <capability>, so that <outcome>.

## Scope
### In scope
-
### Out of scope
- <the adjacent thing this does NOT do> — `AW-XXX-NNN`

## Acceptance criteria
1. **Given** … **when** … **then** …

## Interface contract
Function signatures, wire messages, CLI flags, config keys, env vars, exit codes, error taxonomy.

## Data / state impact
Schema changes, migrations, backward compatibility, effect on live Sessions during rollout.

## Observability requirements
### Metrics
### Logs
### Traces
### Alerts

## Test plan
- **Unit:**
- **Integration:**
- **Manual/operator:**

## Definition of done
CLAUDE.md §8, plus:
-

## Open questions
- `[ASSUMPTION]`
