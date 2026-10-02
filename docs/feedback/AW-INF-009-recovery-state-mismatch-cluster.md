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

**PM's decision, 2026-10-02 (quoted from the PM session):** "carry it in AW-INF-009, as you prefer,
but not in SPRINT-04. AW-INF-009 is `ready`, so adding the cluster signal is a contract change.
That's architecture's amendment, not mine. I'll route it to them, and they pick KSM's exit code or
a Loki rule, with your view. AW-SRV-007's §8 shouldn't wait for it: its §8 record verifies the
alert against compose, names 'fires on the cluster' as not yet observed, and AW-INF-009 inherits
that as a Definition-of-done line."

SRE's view for architecture: kube-state-metrics' last-terminated exit code. It needs no new log
pipeline, it covers both exit `2` and the restore-mismatch exit, and the cluster already scrapes
kube-state-metrics (`StateProjectorDown` reads `kube_deployment_spec_replicas`). Confirm that the
installed version exposes `kube_pod_container_status_last_terminated_exitcode` before relying on it.

