<!--
SPDX-FileCopyrightText: 2026 Valesor Development
SPDX-License-Identifier: CC-BY-SA-4.0
-->

# ContentLoadFailing

**Alert:** `max by (namespace, pack) (andara_content_pending_seconds) > 300` for 5 m. A pack's Active Pointer
has named a version for more than five minutes that the World is not serving, and nothing the
Builder did explains it. **Severity:** ticket. **SLO:** `docs/specs/slo/content-freshness.md`.
**Ships with:** `AW-SRV-012`.

## What fired, and what the player is experiencing

**Players see the previous version of the pack, whole and working.** The retained-version rule
means a refused or stuck load never leaves the World half-changed: the version that was serving
before the pointer moved is still serving. What is missing is the *new* content. A Builder
activated it and it has not gone live: a new area not open, a fixed description still wrong.

The alert is on the **system** failing to serve published content. A version refused for a
Builder's mistake does not fire it: `validation`, `fallback_missing`, `pack_mismatch` and
`blob_too_large` are the Builder's to fix, and `AW-SRV-013` rejects them at publish. `zone_removed`
and `spawn_room_removed`, also Builder reasons under `validation`, can't be caught at publish
because they depend on what is in effect. The `error` line names them. What remains is the store,
the binary, or the activation order.

## How to confirm

```
curl -s http://<server>:8080/metrics | grep -E '^andara_content_(pending_seconds|active_version|load_failures_total)'
```

- `andara_content_pending_seconds{pack}` is the age of the unserved move, the thing that fired.
- `andara_content_active_version{pack}` is what is serving now, which is the version before the
  move.
- `andara_content_load_failures_total{reason}` shows which reason is rising. It is the first
  column of the table below.

Then the server's `error` line for the refusal, from Loki:

```
{service_name="andara-server"} |= "the previous version keeps serving"
```

There are two messages, on purpose:
- `content version refused; …` for a version the loader will not serve;
- `content could not be read from the store; …` for a store fault.

Each carries `pack`, `version`, `reason` and `error`, which names both versions for a skew and the
hash and path for a blob. A refusal for validation is followed by one `content finding` line per
finding.

## Diagnose, in this order

Stop at the first row that explains it.

| Step | `reason` rising | Means, and what to do |
|------|-----------------|-----------------------|
| 1 | `store_unavailable` | Five causes share this reason, and the `error` line's `error` field says which. The content topics are not answering; the swap's produce to the command log failed; the swap did not apply within the bounded wait (`reload_debounce` × 15); the World Partition did not catch up; or a stale swap was refused three times. The first two are a broker restart, a leader move, or the broker being down. The resolver retries with backoff (see the story's ruling), so this clears when the broker does. Check `WorldReadOnly` and `world-read-only.md` first; a broker outage is that alert's too, and this one is its echo. |
| 2 | `core_version` | The pack was compiled against an `andara.core` newer than the one active. It is **held**, not refused, and loads on its own as soon as that core is activated. Fix: activate the core version the pack names, which the `error` line gives. Activation is `andara-cli content activate` (`AW-CLI-003`); until that lands there is no command, and the Builder publishes against the active core instead. The reverse also holds: a core rollback that would strand an active pack is refused (see below), and it shows here as the *core* pack pending. |
| 3 | `format_version` | The content was compiled by a newer toolchain than this server binary reads. The server needs a rollout to a binary that supports the version (`AW-INF-007`), or the Builder recompiles with the matching toolchain. It will not clear on its own. |
| 4 | `manifest_missing`, `blob_missing` | The pointer names a version, or the manifest names a blob, that the content topics do not hold. The publisher was interrupted between writes, or retention removed something that must be kept forever. The content topics are compacted, and every blob and version key is unique, so compaction keeps every record ever published (`deploy/kafka/topics.yaml`). A missing record there is a publish bug (`AW-SRV-013`), and the fix is to republish the version. |
| 5 | `blob_corrupt` | A blob's bytes do not hash to its key. The store is content-addressed, so this is corruption on the broker or in the server's blob cache. Delete the cache directory (`content.cache_dir`) on the pod by restarting it with an empty volume, and it refetches. If the broker copy is the corrupt one, republish. |
| 6 | `store_unavailable` whose `error` says "did not apply within" or "the world partition was not consumed to its end" | The swap was produced but never applied, so Partition 0 is not being consumed. Check that the tick loop is running (`andara_ticks_total` advancing; `SimulationLagging`), and whether a Zone on Partition 0 has faulted (`ZoneFaulted`). A faulted Partition stays frozen until the process restarts, and every content change waits on it. *(Corrected 2026-09-25: this row said nothing rises. The wait is bounded now, and it counts.)* |

## How to mitigate

Nothing to mitigate for players: they have the previous version. The fix is to make the new one
load, per the row above. There is no command to force a re-evaluation. None is needed, because a
held pack is re-evaluated on every pointer move, including core's, and a `store_unavailable`
failure is retried on its own. If a fix above does not clear the gauge within a minute, escalate.

**A core rollback can be refused.** Moving `andara.core`'s pointer back past the version an active
pack was compiled against would leave the World serving a combination the loader would refuse to
assemble. So the core move is refused and `andara.core` stays where it is, naming the pack that
holds it. Roll that pack back first, to a version compiled against the older core, then core.

## When to escalate

- `blob_corrupt` on the broker's copy, or `manifest_missing`/`blob_missing` on a version that
  published cleanly: a publish-path bug, `AW-SRV-013`.
- Row 6 with the tick loop healthy: a `ContentSwap` that was produced and never applied, a
  simulation bug, `AW-SRV-012`.
