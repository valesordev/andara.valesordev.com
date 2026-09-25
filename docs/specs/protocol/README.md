# Protocol specifications

The versioned gRPC contract between clients and `andara-server`, in protobuf.

ADR-0003 chose gRPC over HTTP/2 with TLS, serving Connect and gRPC-Web from the same handler so Phase 2's
browser client needs no proxy. ADR-0007 made protobuf the single schema authority — these `.proto` files
define not only the wire but also the Kafka record formats, the snapshot envelope, and content manifests.

## Layout

```
andara/game/v1/       Game service — OpenSession, Submit, Subscribe   (AW-SRV-005); the roster RPCs (AW-SRV-014)
andara/admin/v1/      Admin service — operator and builder surface    (AW-SRV-005)
andara/log/v1/        LoggedCommand, Event, TickCompleted             (AW-SRV-004, AW-SRV-005)
andara/state/v1/      snapshot envelope with state_version (AW-SRV-006); StateRecord, the andara.state.v1 value (AW-SRV-019)
andara/content/v1/    ZoneDefinition (AW-SRV-001); TemplateDefinition (AW-SRV-022); blobs, version manifests, active pointer (AW-SRV-012, AW-SRV-013)
andara/accounts/v1/   AccountRecord — the compacted account topic          (AW-SRV-008)
andara/audit/v1/      AuditRecord — the privileged-action record           (AW-SRV-008)
andara/auth/v1/       Auth service — Register, Authenticate, Refresh, Revoke (AW-SRV-008)
```

Generated Go, Python, and TypeScript live in `gen/` and are **committed**, so a clone builds without a
codegen toolchain and a schema change is visible in a diff. `make proto` regenerates; `make check` fails
if generated code is stale.

## Rules

1. **Additive only within a major version.** New fields get new numbers. Removed fields become `reserved`
   and their numbers are never reused. Changing a field's meaning is prohibited — add a new field.
   `buf breaking` enforces this in CI against the merge base.
2. **The major version is in the package path** (`andara.game.v1`). A v2 is a new package, not a mutation.
3. **Protocol version is a separate integer**, negotiated in `OpenSession`. Protobuf absorbs additive
   change; the integer exists for what it cannot.
4. **Canonical encoding where it is hashed.** Protobuf serialization is not canonical by default and map
   field ordering is unspecified. Anything feeding the State Hash uses a sorted-key encoder or avoids
   `map` fields. This is a determinism requirement (ADR-0002), not a style preference, and it is asserted
   by test.

The v1 Protocol must be **frozen** before any Phase 2 client work begins — a Phase 1 exit criterion.

`andara.content.v1.ZoneDefinition` is defined; the Game and Admin services arrive with `AW-SRV-005`.
