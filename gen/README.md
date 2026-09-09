# gen — generated protobuf code, committed

ADR-0007 makes protobuf the single schema authority for the wire, the log, snapshots, and content.
Generated code is committed so that a clone builds without a codegen toolchain, and so that a schema
change and its blast radius appear in the same diff.

```
gen/go/      # Go, imported as github.com/valesordev/andara/gen/go/...
gen/python/  # Behavior Agent SDK (ADR-0005)
gen/ts/      # Phase 2 client (ADR-0003, Connect)
```

Do not hand-edit anything under this directory. `make proto` regenerates it from
`docs/specs/protocol/`; `make proto-check` fails `make check` when what is committed no longer matches
the `.proto` sources.
