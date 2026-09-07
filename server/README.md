# andara-server

The world simulation, session gateway, and persistence adapters. Go, deployed to Kubernetes.

Not yet implemented. First stories: `AW-SRV-001` (world model and zone loading), `AW-SRV-002`
(deterministic tick loop), `AW-SRV-003` (command pipeline), `AW-SRV-004` (event emission).

## Structure

```
server/sim/     the simulation core — transport-agnostic and dependency-free
server/...      everything else: gateway, persistence adapters, telemetry wiring
```

`server/sim` is the load-bearing constraint of this codebase. It imports no network, no datastore,
no filesystem, no wall clock, and no global randomness. This is enforced by `depguard` in
`.golangci.yml`, not by convention — see `ADR-0001` for why the seam exists and `ADR-0002` for why
determinism is a production dependency rather than a preference.

## Configuration

Populated by `AW-SRV-001` and `AW-SRV-002` as those stories land. Every key is settable by config
file and by environment variable, and every key appears in the Helm values schema (`CLAUDE.md` §8).
