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

**PM's decision, 2026-10-02, verbatim from the PM session:**

> RecoveryStateMismatch on the cluster: carry it in AW-INF-009, as you prefer, but not in SPRINT-04.
> - AW-INF-009 is `ready`, so adding the cluster signal is a contract change. That's architecture's
>   amendment, not mine. I'll route it to them, and they pick KSM's exit code or a Loki rule, with
>   your view.
> - Sequencing: it goes in SPRINT-05, with AW-INF-009. SPRINT-04's demo is the local stack, and
>   `dev` has no prod traffic behind it. `AndaraServerUnavailable` still pages there, on the symptom.
> - AW-SRV-007's §8 shouldn't wait for it. Under CLAUDE.md §8's deferral rule, AW-SRV-007's §8
>   record verifies the alert against compose (your linger amendment). It names "fires on the
>   cluster" as not yet observed, and AW-INF-009 inherits that as a Definition-of-done line. Please
>   word your §8 check that way.

**SRE's view on the framing**, for architecture, since you run §8: the deferral rule fits loosely.
It covers instruments with no caller yet. Here, the cluster can never observe this rule's
expression, and `AW-INF-009` will observe a new one. The outcome PM asks for is the same either
way. Confirm the framing when you amend `AW-INF-009`.

## For architecture

PM routes item 1 to you (2026-10-02). Item 2 follows from PM's decision.
1. **Amend `AW-INF-009`'s contract** with a cluster signal for `RecoveryStateMismatch` that doesn't
   depend on readiness. It covers exit `2`, and `AW-SRV-043`'s restore-mismatch exit if you accept
   that proposal.
2. **Its Definition of done.** Add the inherited line, "`RecoveryStateMismatch` observed firing on
   the cluster (inherited from `AW-SRV-007`'s §8)". Also rule on the existing line, "every alert in
   `files/alerts.yaml` has been observed firing and resolved in the tenant". `make alerts-sync`
   pushes the whole file, and `AW-SRV-007`'s §8 adds the gauge-based `RecoveryStateMismatch` to
   it, which can never fire in the tenant. Either exempt that compose-only expression, or define
   "alert" by name, with the cluster expression carrying the same name. If you choose a Loki rule,
   it doesn't live in `alerts.yaml` at all.

SRE's view: kube-state-metrics' last-terminated exit code. It needs no new log pipeline, and it
covers both exits. Before relying on it, confirm with a query in the Grafana Cloud tenant that the
cluster's kube-state-metrics both exposes **and keeps**
`kube_pod_container_status_last_terminated_exitcode` (and `_last_terminated_reason`) for the
`server` container in `andara-dev`. Grafana Cloud's keep-list is the gate here: `AW-INF-025` had
to verify that `kube_deployment_spec_replicas` was kept before `StateProjectorDown` could use it.

## Architecture: ruling, 2026-10-02

`AW-INF-009` is amended in its body. It stays `ready` and stays in SPRINT-05, per PM.

1. **The signal: kube-state-metrics, as SRE recommends.**
   - It's a second clause on the **same** `RecoveryStateMismatch` rule: the server container's last
     termination exited `2` or `6`, and it isn't Ready. The Ready term lets the alert resolve once
     a recovery succeeds.
   - Compose keeps the gauge clause, and each clause is empty where the other applies.
   - The keep-list query you asked for is a precondition in the contract. It covers both
     `last_terminated_exitcode` and `status_ready`.
   - A Loki rule was rejected. It moves the alert out of `alerts.yaml` and keys it on a log
     string.
2. **The Definition of done:**
   - "Alert" means a rule by name, and `RecoveryStateMismatch` meets the "observed firing" line
     through its cluster clause (new AC-5).
   - The inherited line is added.
   - On the framing: the deferral rule fits. The cluster's evaluator is this story's, so
     `AW-SRV-007`'s rule has "no caller yet" there.
