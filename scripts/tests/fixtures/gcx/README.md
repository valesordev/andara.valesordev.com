# gcx output fixtures

What `gcx` prints, recorded from the tag `scripts/bootstrap.sh` pins (`GCX_VERSION`, v1.5.0) against
`solo7dev` and `solo7` on 2026-10-19, with `--config`, `--context` and `-o json`, outside agent mode.
`scripts/tests/test_gcx.py` reads every file through `scripts/gcx.py`, so a field gcx renames or a shape it
changes fails a unit test rather than a drill (ADR-0012 decision 2).

Every key, nesting level and value type is as recorded. The values are replaced with Andara-shaped ones
(rule names, namespaces, trace IDs), because the stacks held other products' data (Synthetic Monitoring
checks, other services' traces, Slack permalinks). `error_*.stderr` are gcx's error output verbatim.
The `Pending` instance state is the vocabulary ADR-0012 names; no pending instance existed to record.

| File | Command (all `-o json`) |
|---|---|
| `alert_rules_list.json` | `alert rules list --limit 0 [--group <g>]`: a list of groups, each with its `rules` |
| `alert_rules_list_empty.json` | the same with no Grafana-managed rule, or an unknown `--group`: `null` |
| `alert_instances_json.json` | `alert instances list --name <re> --json ruleName,labels,state,activeAt` |
| `alert_instances_json_empty.json` | the same with no `--name` match and no `--name`: one object, every field null |
| `alert_instances_name_empty.json` | the same with `--name` matching nothing: `[]` |
| `alert_instances_legacy_empty.json` | `alert instances list --datasource grafanacloud-prom` (the ruler) with nothing active: `null` |
| `metrics_query_vector.json`, `_vector_empty.json` | `metrics query -d grafanacloud-prom <expr>` (no `--since`: instant) |
| `metrics_query_matrix.json` | the same with `--since`: a range query |
| `metrics_series.json`, `_empty.json` | `metrics series -d grafanacloud-prom --since <d> <selector>` |
| `metrics_labels.json` | `metrics labels -d grafanacloud-prom --label environment` |
| `logs_query.json`, `_empty.json` | `logs query -d grafanacloud-logs <expr> --since <d> --limit <n>`: `values` are objects, not pairs |
| `traces_query.json`, `_empty.json` | `traces query -d grafanacloud-traces <traceql> --since <d>` |
| `traces_get.json` | `traces get -d grafanacloud-traces <id>`: `{"trace": {"resourceSpans": …}, "metrics": …}` |
| `traces_get_missing.json` | the same for an ID that does not resolve: `{}`, exit 0 |
| `irm_alert_groups.json`, `_empty.json` | `irm oncall alert-groups list` against `solo7` |
| `error_invalid_promql.stderr` | `metrics query 'up{'`: exit 1 |
| `error_unknown_context.stderr` | `--context nosuch`: exit 1 |

A pin bump re-records each file from the new tag's output and keeps the replaced values, in the same change.
