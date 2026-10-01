# AW-SRV-042: an empty content store waits, unready

Story: `AW-SRV-042` (`draft`, PM's #292). It carries architecture's ruling on AW-INF-021's
empty-store deadlock (`docs/feedback/AW-INF-021-dev-content-store.md`).

## For architecture: SRE observability review, 2026-10-01

Two decisions architecture asked for, and three amendments. Replacement text for the
Observability section is at the end. It's the whole section, so the contract review can paste it.

**1. A refused `OpenSession` counts on `andara_sessions_total{outcome="rejected_no_content"}`.**
It's a new value in the closed `outcome` set, pre-seeded at 0 beside `rejected_auth` and
`rejected_version`. It's not left to `andara_grpc_requests_total{method=".../OpenSession",
code="unavailable"}`. That series also counts a draining server's refusals and transport
failures, so it can't say "players were turned away because there's no content". `sessions_total`
is where an operator already looks for why Sessions didn't open, and one more bounded value adds
one series. No SLO or alert reads `sessions_total`'s outcomes today (checked against
`files/alerts.yaml` and `docs/specs/slo/`), so the SLI math doesn't move.

**2. No `andara_content_waiting` gauge.** It's the same answer as in AW-INF-021's Observability
section (#288). While a fresh environment waits, it's identified by what already exists:
`andara_content_zones_loaded` is `0` on a started pod (`/startedz` 200, `/readyz` 503), and the
`warn` line appears. With decision 1, `rejected_no_content` rising says players are being turned
away. A gauge would restate `zones_loaded == 0` for one case. The case `zones_loaded` can't tell
apart, a World that lost its content, doesn't wait: it exits 1 (AC-3).

**3. Amendment: `trace_id` on the two wait lines.** Both run inside `ReconcileContent`'s span, at
boot and on the triggering swap. `trace_id` lets the leaving line join the swap's trace, and through
it the Builder's `content.activate`. That's how `AW-INF-021`'s verification follows one activation
from the CLI to the World.

**4. Amendment: the refused `OpenSession` line names the Session.** `debug`, with `trace_id`,
`reason=no_content_in_effect`, and `session_id` if one was assigned before the refusal; otherwise
the client's remote address, as the other `OpenSession` refusals log it. CLAUDE.md §7 requires the
correlation ID on a request path.

**5. Confirmed, no change:** no new spans. No new alert: `AndaraServerUnavailable` is true while a
fresh environment waits, and its runbook row is SRE's, already on #288 (`server-unavailable.md`:
first fix `make content-seed ENV=<env>`). The startup probe move to `/startedz` is SRE's, also on
#288. `/livez` is unconditional (architecture, `boot.go`), so liveness never kills a waiting server.

### Replacement Observability section

```
## Observability requirements

*(SRE observability review, 2026-10-01: the refused-OpenSession counter named, no waiting gauge,
trace_id on the wait lines.)*

- **Metrics:**
  - `andara_sessions_total{outcome}` gains `rejected_no_content`, a bounded value pre-seeded at 0
    beside `rejected_auth` and `rejected_version`. It counts each `OpenSession` refused
    `no_content_in_effect`.
  - No waiting gauge. While the server waits, `andara_content_zones_loaded` is `0` on a started pod
    (`/startedz` 200, `/readyz` 503). That and the `warn` line identify the wait.
- **Logs:**
  - `warn` `waiting for content: no Zones in effect; publish and activate a pack`, once on entering
    the wait. Fields: `content_source`, `packs`, `core_version`, `trace_id`.
  - `info` `content in effect: leaving the wait`, once. Fields: `zones`, the triggering
    `pack@version`, `trace_id` (the swap's).
  - A refused `OpenSession` logs at `debug` with `trace_id`, `reason=no_content_in_effect`, and the
    `session_id` if assigned, else the remote address, as the other `OpenSession` refusals do.
- **Traces:** none new. The leaving `ReconcileContent` keeps its existing span, and the leaving
  line's `trace_id` is that span's.
- **Alerts:** none new. `AndaraServerUnavailable` fires while a fresh environment waits, which is
  true. Its runbook row (`server-unavailable.md`, first fix `make content-seed ENV=<env>`) and the
  startup probe's move to `/startedz` are SRE's, in `AW-INF-021`.
```

Test-plan addition that follows from 1: the AC-1 test asserts
`andara_sessions_total{outcome="rejected_no_content"}` rises by one for the refused `OpenSession`, and
is present at `0` before it.
