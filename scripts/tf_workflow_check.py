#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make tf-workflow-check — the credential and sink rules of .github/workflows/terraform.yaml (AW-INF-068).

ADR-0012 decisions 3 to 5 decide which job may hold which credential and where a plan may be written. This
reads the workflow and fails on any job that breaks one of these rules, naming the job and the rule:

  token-as-variable  a provider token passed as TF_VAR_* or -var (only the Slack webhooks are variables)
  prod-only          SOLO7_IRM, TFSTATE_PRODPLAN_SA, the IRM token or TF_PLAN_KEY_PROD in a job outside the
                     andara-prod-plan and andara-prod-apply environments (so in no pull-request job and in
                     no scheduled job but drift-prod-irm)
  apply-only         an apply token or apply service account outside andara-main and andara-prod-apply
  sinks              a step that runs `terraform` or `openssl` itself, prints `show -json`, or uses a
                     secret in its script instead of its env (every sink is scripts/tf_ci.py's), or an
                     artifact holding anything but encrypted plans and their manifest
  pr-plan            the pull-request `plan` job referencing anything but its own stack's READ and
                     FLEET_READ tokens, plan service account and webhook, or holding an environment
  concurrency        `apply` outside its own group, or plan-prod, apply-prod and drift-prod-irm outside one
                     shared group; any of them cancelling a run in progress; `drift` holding any group (it
                     could replace a pending apply); a drift job without `actions: read` (it lists apply runs)
  secrets-context    the whole secrets context (`toJSON(secrets)`, a bare `secrets`) anywhere
  drift-auth         a drift job that runs on anything but the schedule (the plan accounts trust no dispatch)
  failure-report     a `failure()` step that runs a repository file: it may be reporting a failed checkout
  prod-gate          a job holding andara-prod-plan or andara-prod-apply whose job-level `if` is not
                     `vars.TF_PROD_ENABLED == 'true'`, alone or as `… && (<rest>)`; or a matrix that can
                     produce a stack other than solo7dev except through the gate, built as
                     `${{ fromJSON(vars.TF_PROD_ENABLED == 'true' && '<on>' || '<off>') }}`

The credential rules cover the workflow-level and job-level `env` as well as each step's. The pull-request
job's checkout must be the base branch: no `ref`, no `repository`.

Exit: 0 clean · 1 a rule broken · 2 the workflow is missing or unreadable.
"""

import json
import os
import re
import sys

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKFLOW = os.path.join(REPO, ".github", "workflows", "terraform.yaml")

PROD_ENVS = ("andara-prod-plan", "andara-prod-apply")
APPLY_ENVS = ("andara-main", "andara-prod-apply")
PROD_ONLY = re.compile(r"SOLO7_IRM|TFSTATE_PRODPLAN_SA|TF_PLAN_KEY_PROD")
APPLY_ONLY = re.compile(r"_APPLY_TOKEN|_APPLY_SA")
REF = re.compile(r"\b(secrets|vars)(?:\.([A-Za-z0-9_]+)|\[([^\]]+)\])")
# What the pull-request plan job may reference, exactly (AC-1).
PR_PLAN_REFS = {
    ("vars", "GCP_WIF_PROVIDER"),
    ("vars", "format('TFSTATE_{0}_PLAN_SA', matrix.key)"),
    ("secrets", "format('GRAFANA_{0}_READ_TOKEN', matrix.key)"),
    ("secrets", "format('GRAFANA_{0}_FLEET_READ_TOKEN', matrix.key)"),
    ("secrets", "matrix.webhook"),
    ("vars", "TF_PROD_ENABLED"),  # the prod gate on its matrix; a flag, not a credential
}
PR_MATRIX = [{"stack": "solo7dev", "key": "SOLO7DEV", "webhook": "SLACK_WEBHOOK_DEV"},
             {"stack": "solo7", "key": "SOLO7", "webhook": "SLACK_WEBHOOK_PROD"}]
DEV_MATRIX = PR_MATRIX[:1]

# The prod gate (ADR-0012, 2026-10-19 amendment): availability only, never the security boundary.
GATE = "vars.TF_PROD_ENABLED == 'true'"
GATED_MATRIX = re.compile(r"^\$\{\{\s*fromJSON\(\s*vars\.TF_PROD_ENABLED == 'true'\s*&&\s*'(?P<on>[^']*)'"
                          r"\s*\|\|\s*'(?P<off>[^']*)'\s*\)\s*\}\}$")


def gated(cond):
    """The rest of a job-level if behind the gate ("" for the gate alone), or None when it isn't gated.
    Accepted: the gate alone, or `<gate> && (<rest>)` with <rest> one balanced group, so no operator
    precedence can lift <rest> out from under the gate."""
    cond = " ".join(str(cond or "").split())
    if cond == GATE:
        return ""
    prefix = GATE + " && ("
    if not (cond.startswith(prefix) and cond.endswith(")")):
        return None
    rest, depth = cond[len(prefix):-1], 0
    for ch in rest:
        depth += {"(": 1, ")": -1}.get(ch, 0)
        if depth < 0:
            return None
    return rest if depth == 0 else None


def matrix_legs(job):
    """(on, off): the matrix's legs with the gate on and off. A literal list is the same both ways;
    an expression that isn't the gated form is (None, None)."""
    include = ((job.get("strategy") or {}).get("matrix") or {}).get("include")
    if isinstance(include, list):
        return include, include
    m = GATED_MATRIX.match(" ".join(str(include or "").split()))
    if not m:
        return None, None
    try:
        return json.loads(m.group("on")), json.loads(m.group("off"))
    except ValueError:
        return None, None


def refs(obj):
    """{(kind, name)} for every secrets/vars reference in obj."""
    return {(m.group(1), m.group(2) or m.group(3)) for m in REF.finditer(yaml.safe_dump(obj, width=10 ** 6))}


def environment(job):
    env = job.get("environment")
    return env.get("name") if isinstance(env, dict) else env


BARE_SECRETS = re.compile(r"\bsecrets\b(?![.\[])")


def env_rules(out, name, env_name, obj, label):
    """The credential rules over one object (a job, or the workflow's own env)."""
    for kind, ref in sorted(refs(obj)):
        if PROD_ONLY.search(ref) and env_name not in PROD_ENVS:
            out.append((name, "prod-only", "%s.%s outside %s" % (kind, ref, " and ".join(PROD_ENVS))))
        if APPLY_ONLY.search(ref) and env_name not in APPLY_ENVS:
            out.append((name, "apply-only", "%s.%s outside %s" % (kind, ref, " and ".join(APPLY_ENVS))))
    if BARE_SECRETS.search(yaml.safe_dump(obj, width=10 ** 6)):
        out.append((name, "secrets-context", "%s: the whole secrets context (e.g. toJSON(secrets)) is refused" % label))


def tf_var_tokens(out, name, env, label):
    for key, value in (env or {}).items():
        if str(key).startswith("TF_VAR_") and "TOKEN" in str(value):
            out.append((name, "token-as-variable", "%s: %s passes a token" % (label, key)))


def check(workflow):
    """[(job, rule, detail)]."""
    out = []
    jobs = workflow.get("jobs") or {}
    # A workflow-level env reaches every job, so it is held to the strictest job's rules: none.
    top = {"env": workflow.get("env") or {}}
    env_rules(out, "<workflow>", None, top, "env")
    tf_var_tokens(out, "<workflow>", top["env"], "env")
    for name, job in jobs.items():
        env_name = environment(job)
        env_rules(out, name, env_name, job, name)
        tf_var_tokens(out, name, job.get("env"), "job env")
        for i, step in enumerate(job.get("steps") or []):
            label = step.get("name") or step.get("uses") or "step %d" % (i + 1)
            tf_var_tokens(out, name, step.get("env"), label)
            if "failure()" in str(step.get("if", "")) and re.search(r"scripts/|\bmake\b|\./", step.get("run") or ""):
                out.append((name, "failure-report", "%s: a failure step may run after a failed checkout, so it "
                                                    "uses gh --repo only, no repository file" % label))
            script = step.get("run") or ""
            if re.search(r"(^|\s)-var(-file)?[=\s]", script) or "TF_VAR_" in script:
                out.append((name, "token-as-variable", "%s: -var or TF_VAR_ in a script" % label))
            if re.search(r"(^|[\s;&|(])(terraform|openssl)\s", script):
                out.append((name, "sinks", "%s: runs terraform or openssl itself; use scripts/tf_ci.py" % label))
            if "show -json" in script:
                out.append((name, "sinks", "%s: prints terraform show -json" % label))
            if REF.search(script) and "secrets" in script:
                out.append((name, "sinks", "%s: uses a secret in its script; pass it through env" % label))
            if str(step.get("uses", "")).startswith("actions/upload-artifact"):
                paths = [p.strip() for p in str((step.get("with") or {}).get("path", "")).splitlines() if p.strip()]
                bad = [p for p in paths if not (p.endswith(".tfplan.enc") or p.endswith("/manifest.json"))]
                if bad or not paths:
                    out.append((name, "sinks", "%s: an artifact may hold only *.tfplan.enc and manifest.json, "
                                               "not %s" % (label, ", ".join(bad) or "nothing")))

    plan = jobs.get("plan")
    if plan is None:
        out.append(("plan", "pr-plan", "the workflow has no plan job"))
    else:
        if environment(plan):
            out.append(("plan", "pr-plan", "a pull-request job holds no environment"))
        extra = sorted(refs(plan) - PR_PLAN_REFS)
        if extra:
            out.append(("plan", "pr-plan", "references beyond its own stack's read credentials: %s"
                        % ", ".join("%s[%s]" % r for r in extra)))
        on, off = matrix_legs(plan)
        if on != PR_MATRIX or off != DEV_MATRIX:
            out.append(("plan", "pr-plan", "the matrix must be exactly %s with the prod gate on and %s with "
                                           "it off" % (PR_MATRIX, DEV_MATRIX)))
        cond = str(plan.get("if", ""))
        for needle in ("pull_request_target", "head.repo.full_name == github.repository", "base.ref == 'main'"):
            if needle not in cond:
                out.append(("plan", "pr-plan", "its if must require %s" % needle))
        for s in plan.get("steps") or []:
            if str(s.get("uses", "")).startswith("actions/checkout") and \
                    {"ref", "repository"} & set((s.get("with") or {})):
                out.append(("plan", "pr-plan", "its checkout must be the base branch: no ref or repository; "
                                               "the pull request's files are read as data by tf_policy.py extract"))

    # The plan accounts are bound to event_name `schedule` (and `pull_request_target`), never a dispatch.
    # drift-prod-irm's condition is read behind its gate (prod-gate checks the gate itself).
    for name in ("drift", "drift-prod-irm"):
        cond = (jobs.get(name) or {}).get("if", "")
        if name == "drift-prod-irm":
            cond = gated(cond)
        if " ".join(str(cond or "").split()) != "github.event_name == 'schedule'":
            out.append((name, "drift-auth", "the drift jobs run on the schedule only "
                                            "(if: github.event_name == 'schedule')"))

    # prod-gate: a prod job runs, and a non-dev matrix leg exists, only behind TF_PROD_ENABLED.
    for name, job in jobs.items():
        if environment(job) in PROD_ENVS and gated(job.get("if")) is None:
            out.append((name, "prod-gate", "a job in %s needs `if: %s`, alone or as `%s && (<rest>)`"
                        % (environment(job), GATE, GATE)))
        if "matrix" not in (job.get("strategy") or {}):
            continue
        on, off = matrix_legs(job)
        if off is None:
            out.append((name, "prod-gate", "the matrix include is a literal list or "
                                           "${{ fromJSON(%s && '<on>' || '<off>') }}" % GATE))
        elif not isinstance(off, list) or any(not isinstance(leg, dict) or leg.get("stack") != "solo7dev"
                                              for leg in off):
            out.append((name, "prod-gate", "with TF_PROD_ENABLED off the matrix may hold solo7dev only, got %s"
                        % off))
    drift_on, drift_off = matrix_legs(jobs.get("drift") or {})
    if (drift_on, drift_off) != (PR_MATRIX, DEV_MATRIX):
        out.append(("drift", "prod-gate", "the drift matrix must be exactly %s with the prod gate on and %s "
                                          "with it off" % (PR_MATRIX, DEV_MATRIX)))

    def group(job_name):
        c = (jobs.get(job_name) or {}).get("concurrency") or {}
        return (c.get("group") if isinstance(c, dict) else c), (c.get("cancel-in-progress") if isinstance(c, dict) else None)

    apply_group, apply_cancel = group("apply")
    prod = {n: group(n) for n in ("plan-prod", "apply-prod", "drift-prod-irm")}
    if not apply_group or apply_cancel is not False:
        out.append(("apply", "concurrency", "apply needs its own group with cancel-in-progress: false"))
    groups = {g for g, _ in prod.values()}
    if len(groups) != 1 or None in groups or apply_group in groups:
        out.append(("plan-prod", "concurrency", "plan-prod, apply-prod and drift-prod-irm share one group, "
                                                "not apply's: got %s" % {n: g for n, (g, _) in prod.items()}))
    for n, (_, cancel) in prod.items():
        if cancel is not False:
            out.append((n, "concurrency", "cancel-in-progress must be false"))
    # drift plans without a lock. In an apply's group it could replace that group's pending apply, so it
    # holds no group; tf_ci.py checks for an overlapping apply run instead, which needs actions: read.
    if (jobs.get("drift") or {}).get("concurrency"):
        out.append(("drift", "concurrency", "drift holds no concurrency group: queued into an apply's group "
                                            "it could replace a pending apply; tf_ci.py checks for an overlapping apply"))
    for n in ("drift", "drift-prod-irm"):
        # The drift step can fail without reporting (an exception that isn't tf.Exit), so only its own
        # `reported` output may stop the fallback, never its outcome.
        fallbacks = [s for s in (jobs.get(n) or {}).get("steps") or [] if "failure()" in str(s.get("if", ""))]
        if [str(s.get("if")) for s in fallbacks] != ["failure() && steps.drift.outputs.reported != 'true'"]:
            out.append((n, "failure-report", "%s's fallback runs on failure() unless the drift step reported: "
                                             "if: failure() && steps.drift.outputs.reported != 'true'" % n))
        if ((jobs.get(n) or {}).get("permissions") or {}).get("actions") != "read":
            out.append((n, "concurrency", "%s needs actions: read to list apply runs" % n))
    return out


def main(argv):
    path = argv[0] if argv else WORKFLOW
    try:
        with open(path) as f:
            workflow = yaml.safe_load(f)
    except (OSError, yaml.YAMLError) as e:
        print("tf-workflow-check: %s: %s" % (path, e), file=sys.stderr)
        return 2
    problems = check(workflow or {})
    for job, rule, detail in problems:
        print("tf-workflow-check: %s: %s: %s" % (job, rule, detail), file=sys.stderr)
    if problems:
        return 1
    print("tf-workflow-check: %s: clean" % os.path.relpath(path, REPO))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
