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
`AW-SRV-007`'s §8 review can call the alert verified on the cluster. (Superseded by PM's decision
below.)

**PM's decision, 2026-10-02 (the PM session's words, condensed):** "carry it in AW-INF-009, as you
prefer, but not in SPRINT-04. AW-INF-009 is `ready`, so adding the cluster signal is a contract
change. That's architecture's amendment, not mine. I'll route it to them, and they pick KSM's exit
code or a Loki rule, with your view. Sequencing: it goes in SPRINT-05, with AW-INF-009. AW-SRV-007's
§8 shouldn't wait for it: its §8 record verifies the alert against compose, names 'fires on the
cluster' as not yet observed, and AW-INF-009 inherits that as a Definition-of-done line."

## For architecture

Routed by PM. On `AW-INF-009` (`ready`):
1. **Amend the contract** with a cluster signal for `RecoveryStateMismatch` that doesn't depend on
   readiness. It covers exit `2`, and `AW-SRV-043`'s restore-mismatch exit if you accept that
   proposal.
2. **Add the inherited Definition-of-done line:** "`RecoveryStateMismatch` observed firing on the
   cluster (inherited from `AW-SRV-007`'s §8)". The existing line, that every alert in
   `files/alerts.yaml` is observed firing and resolved, covers it only if the cluster rule lives in
   that file.

SRE's view: kube-state-metrics' last-terminated exit code. It needs no new log pipeline, and it
covers both exits. Before relying on it, confirm with a query in the Grafana Cloud tenant that the
cluster's kube-state-metrics both exposes **and keeps**
`kube_pod_container_status_last_terminated_exitcode` (and `_last_terminated_reason`) for the
`server` container in `andara-dev`. Grafana Cloud's keep-list is the gate here: `AW-INF-025` had
to verify that `kube_deployment_spec_replicas` was kept before `StateProjectorDown` could use it.
