---
id: AW-SRV-022
title: Template definitions — schema, flattened loading, and Entity instantiation
epic: EPIC-02
component: server
type: feature
status: ready
size: M
depends_on: [AW-SRV-020, AW-SRV-021]
blocks: [AW-SRV-009, AW-SRV-012, AW-SRV-014, AW-CLI-005, AW-CLI-006]
lane: implementation
risk: medium
---

## Context

ADR-0010 decided the type system: Templates in single inheritance containing Components, resolved at
compile time so the sim receives flat Templates and carries no resolver (§9). `AW-SRV-021` attached
Components to Rooms and Zones and built the server-defined component registry; it explicitly left
Templates to a later story. Four `ready` stories — the Content Language (`AW-CLI-005`), its compiler
(`AW-CLI-006`), content resolution (`AW-SRV-012`), and Character creation (`AW-SRV-014`) — all need a
`TemplateDefinition` message and a way to make an Entity from one. This is that story, split out on
2026-09-11 so the schema has one owner.

## User story

As a builder, I want the Templates I compile to be what the server instantiates, with the provenance of
every field retained, so that "why does this NPC have that value" has an answer.

## Scope

### In scope
- `andara.content.v1.TemplateDefinition`: flattened Components, the inheritance chain, per-field
  provenance — the compiler's output and the loader's input.
- Loader: Templates from a Content Pack into a `TemplateRegistry` keyed `<pack>.<Name>`; validation
  that every Component type is registered (`AW-SRV-021`), that `extends` names resolve within the pack
  or its declared core version, and that the chain is acyclic and within depth.
- Entity instantiation: `sim.Instantiate(template, id) EntityState` copies the flattened Components
  onto a new Entity, sorted by type, with `EntityState.template` recorded so an Entity always names its
  Template and pack.
- The `andara.core` seed Templates needed by stories already `ready`: `andara.core.Entity`,
  `andara.core.Character`, `andara.core.Npc`, `andara.core.Item`, and the `andara.core.Behavior` and
  `andara.core.Memory` Components they reference — data only, no behaviour.
- `format_version` bump on `zone.proto`'s pack-level container to carry Templates.

### Out of scope
- Resolution and merge — compile-time, `AW-CLI-006`. The loader rejects an unflattened Template.
- Systems that read Components — each arrives with its mechanic.
- Items as placeable instances in Rooms; `AW-SRV-021` covers Room and Zone instances, Items come with
  the inventory story.

## Acceptance criteria

1. **Given** a pack with `town.Merchant extends andara.core.Npc` flattened by the compiler **when** the
   server loads it **then** `TemplateRegistry.Get("town.Merchant")` returns the flattened Component set
   and `chain = [andara.core.Entity, andara.core.Npc, town.Merchant]`.
2. **Given** a Template naming a Component type not in the registry **when** loaded **then** boot (or
   reload) fails with `ValidationError{code: unknown_component}` naming pack, Template, type, and that
   types are server-defined.
3. **Given** a Template whose `extends` names a Template absent from the pack and the declared core
   **when** loaded **then** `unresolved_extends` names both, and the loader never attempts to resolve
   across packs other than core.
4. **Given** a `TemplateDefinition` with `resolved=false` **when** loaded **then** it is rejected with
   `unflattened_template` — the sim carries no resolver (ADR-0010 §9).
5. **Given** `sim.Instantiate(t, id)` **when** called twice with the same inputs **then** the two
   `EntityState` values encode byte-identically, and `components` is sorted by type.
6. **Given** a Template with `andara.core.Behavior{name: "town.merchant"}` **when** loaded **then** the
   name is recorded and not validated here; `AW-CLI-006` validates it at compile, `AW-SRV-009` at claim.
7. **Given** two Templates in one pack with the same name **when** loaded **then** `duplicate_template`
   names the file of each.
8. **Given** an `EntityState` **when** inspected **then** `template` is `<pack>.<Name>` and the
   `content_version` it was instantiated from is retained, so `AW-SRV-019`'s records carry provenance.

## Interface contract

```protobuf
// CONTRACT SKETCH — not an implementation; andara/content/v1/template.proto
message TemplateDefinition {
  string name = 1;                          // "<pack>.<Name>", unique per pack
  TemplateKind kind = 2;                    // ENTITY, ITEM, BEHAVIOR — open enum, ADR-0010 §1
  repeated string chain = 3;                // root first, self last; compiler-emitted
  repeated ComponentValue components = 4;   // flattened, sorted by type (AW-SRV-021's message)
  repeated FieldProvenance provenance = 5;  // (component, field) → ancestor that set it; sorted
  bool resolved = 6;                        // must be true to load (ADR-0010 §9)
  SourceRef source = 7;                     // file:line for errors
}
```

```go
// CONTRACT SKETCH — not an implementation
package sim
type TemplateRef string   // "<pack>.<Name>"
type TemplateRegistry interface { Get(TemplateRef) (*Template, bool); Pack(string) []TemplateRef }
func Instantiate(t *Template, id EntityID, contentVersion string) EntityState
// EntityState (AW-SRV-006) gains: string template = 8; string content_version = 9;
```

### Error taxonomy (extends `AW-SRV-001`'s `ValidationError`)

`unknown_component`, `unresolved_extends`, `unflattened_template`, `duplicate_template`,
`chain_mismatch` (the chain names an ancestor whose Components are not a subset of the flattened set —
a compiler bug surfaced as a load error rather than a silent Template).

### Configuration

None new. `content.core_pack` (`AW-SRV-013`) names the core pack.

## Data / state impact

`EntityState.template` and `content_version` are Zone state, hashed and snapshotted; `state_version`
bumps by one with a migration that back-fills `andara.core.Entity` and the active version. `zone.proto`
gains no fields; Templates are their own blobs in the pack, one per declaration, named by
`TemplateDefinition.name`.

## Observability requirements

- **Metrics:** `andara_content_templates_loaded{pack}` — gauge; `andara_content_validation_errors_total`
  gains the codes above.
- **Logs:** `info` per load with template count per pack; `error` per finding with `file:line`.
- **Traces:** inside `content.validate` (`AW-SRV-012`).
- **Alerts:** none; `ContentLoadFailing` (`AW-SRV-012`) covers a rejected pack.

## Test plan

- **Unit:** registry load from fixtures for every error code; `Instantiate` determinism (AC-5); sorted
  invariants.
- **Integration:** the `AW-CLI-005` corpus `town` pack loaded end to end once `AW-CLI-006` exists;
  until then, hand-flattened fixtures under `testdata/templates/`.
- **Manual/operator:**
  ```
  andara-server --content.source dir --content.path testdata/templates   # expect: "7 templates loaded"
  andara-cli content inspect template town.Merchant                       # AW-CLI-002: provenance shown
  ```

## Definition of done

CLAUDE.md §8, plus: `testdata/templates/` fixtures shared with `AW-CLI-005`'s corpus; the glossary's
Template entry names this story.

## Open questions

- `[NEEDS BRIAN]` The `andara.core` Template vocabulary beyond the four seeded here — the same question
  `AW-SRV-021` carries for Components; a data change, not a contract change.
- `[ASSUMPTION]` `TemplateKind` is an open enum with three values now; the kind set is incomplete by
  Brian's own statement (ADR-0010 §1).
