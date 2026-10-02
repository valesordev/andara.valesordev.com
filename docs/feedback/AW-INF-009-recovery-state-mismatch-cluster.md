# AW-INF-009: RecoveryStateMismatch on the cluster

Story: `AW-INF-009` (alert rule delivery, `ready`, not in SPRINT-04).
Raised: 2026-10-02, SRE, from the observability review of `AW-SRV-007`.

## For PM

`RecoveryStateMismatch`, the alert `docs/specs/slo/recovery.md` says must page, can't fire on the
cluster as `AW-SRV-007` specifies it:
- it reads `andara_recovery_state_hash_match == 0`;
- the annotation scrape keeps only Ready pods;
- a server whose recovery refuses is never Ready.

`AW-SRV-007`'s §7 amendment fixes the compose path with a linger. On `dev` and `prod`, only
`AndaraServerUnavailable` pages, on the symptom, and the cause alert stays dead.

The work: a cluster signal for `RecoveryStateMismatch` that outlives the process and doesn't depend
on readiness. Two candidates:
- kube-state-metrics' last-terminated exit code for the `server` container, `2` or the restore
  exit;
- a Loki rule on recovery's `error` line.

SRE's view: carry it in `AW-INF-009`, because that story already delivers `alerts.yaml` to the
cluster's ruler. A new `lane: sre` story would also work. Either way, schedule it before
`AW-SRV-007`'s §8 review can call the alert verified on the cluster.
