# cmd/andara-cli

`main` for `andara-cli`. Owned by Cursor (CLAUDE.md §2).

Wires `admin/cli.Execute`. The command tree, configuration, and output contract
live under `admin/cli`. `andara-cli` talks to the server over the same versioned
gRPC service as every other client (ADR-0003) — no privileged back door, no
direct datastore access.

```
make build
./bin/andara-cli version
```
