#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-check ENV=<env> [KEEP_LIST=1] — do the chart's signals reach Grafana Cloud? (AW-INF-008, AW-INF-075)

Asks ENV's stack (scripts/grafana_stack.py: local, dev, staging → solo7dev; prod → solo7), through
gcx (scripts/gcx.py), whether what the chart wires up actually arrived. Every query carries ENV's
selectors, environment="<env>" and namespace="andara-<env>":

  metrics  up{job="andara-server"} == 1 and andara_sessions_active (AC-1); up == 1 for every
           projector Deployment the namespace runs, each under its own job (AC-2)
  logs     a `session opened` line from the namespace's server container (AC-4) — SESSION_ID=<id>
           pins which one
  traces   the andara.game.v1.Game/OpenSession span of that line's trace_id (AC-3) — TRACE_ID=<id>
           overrides

Then, informational and outside the exit status, every expression in files/alerts.yaml evaluated
as an instant query, printed with the labels each result carries.

--keep-list (KEEP_LIST=1) runs scripts/observe_keep_list.py instead: every series the rules and
the dashboard name, ADR-0012 decision 13.

Credentials: GRAFANA_<STACK>_READ_TOKEN for ENV's stack, from the environment or .local/box.env;
nothing here prints it. Optional: SINCE (default 1h), and PROJECTORS — the enabled projector names,
comma-separated, `none` for none — which otherwise come from the namespace's Deployments via
kubectl. If neither can say which projectors run, the check does not guess: AC-2 unanswerable is a
failed check, not a skipped one.

Exit: 0 every signal present · 1 a signal absent, AC-2 unanswerable, or a gcx error · 2 usage ·
3 the stack's read token unset — never a vacuous pass.
"""
import json
import os
import re
import shutil
import subprocess
import sys

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import kube  # noqa: E402

REPO = os.path.dirname(HERE)
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
OPEN_SESSION_SPAN = "andara.game.v1.Game/OpenSession"


class Failed(Exception):
    """A check could not be answered (exit 1)."""


def say(msg):
    print("observe-check: " + msg)


def since_ok(s):
    if not re.fullmatch(r"\d+[smhd]", s):
        raise grafana_stack.Exit(2, "SINCE=%s is not <n>s|m|h|d" % s)
    return s


def matchers(env):
    """ENV's selectors as PromQL/LogQL matchers: environment, and the namespace the drills read."""
    sel = dict(grafana_stack.selectors(env), namespace="andara-" + env)
    return ", ".join('%s="%s"' % kv for kv in sorted(sel.items()))


def labels(r):
    m = dict(r.get("metric") or {})
    name = m.pop("__name__", "")
    return name + "{" + ", ".join('%s="%s"' % kv for kv in sorted(m.items())) + "}"


def projector_jobs(ns):
    """The projector jobs AC-2 expects: PROJECTORS if set, else the projector Deployments the
    namespace actually runs — the chart renders only the enabled ones, so that is what
    `projectors.<name>.enabled` resolved to. Not knowing fails the check: an empty list here would
    drop every projector query and let an absent projector pass."""
    names = os.environ.get("PROJECTORS", "").strip()
    if names:
        return [] if names == "none" else ["andara-projector-" + n.strip() for n in names.split(",") if n.strip()]
    if not shutil.which("kubectl"):
        raise Failed("no kubectl and no PROJECTORS; cannot verify AC-2")
    p = subprocess.run(kube.kubectl("-n", ns, "get", "deploy", "-l", "app.kubernetes.io/name=andara",
                                    "-o", "jsonpath={.items[*].metadata.name}"), capture_output=True, text=True)
    if p.returncode:
        raise Failed("kubectl could not list %s deployments (%s) and no PROJECTORS; cannot verify AC-2"
                     % (ns, p.stderr.strip()))
    return [n for n in p.stdout.split() if n.startswith("andara-projector-")]


def check(env, ctx, since, q=gcx):
    """Every signal for ENV through q (scripts/gcx.py); the list of what is absent."""
    ns = "andara-" + env
    sel = matchers(env)
    absent = []

    # Metrics (AC-1, AC-2).
    checks = [("up", 'up{job="andara-server", %s}' % sel, True),
              ("andara_sessions_active", 'andara_sessions_active{job="andara-server", %s}' % sel, False)]
    checks += [("up " + d, 'up{job="%s", %s}' % (d, sel), True) for d in projector_jobs(ns)]
    for what, expr, must_be_one in checks:
        res = q.metrics_query(ctx, expr)
        if not res:
            absent.append(what)
            say("ABSENT %s" % expr)
            continue
        for r in res:
            v = r["value"][1]
            bad = must_be_one and v != "1"
            say("%s %s %s" % ("DOWN  " if bad else "ok    ", labels(r), v))
            if bad:
                absent.append(what)

    # Logs (AC-4). The line carries the RPC span's trace_id, which is what ties it to AC-3.
    logq = '{%s, container="server"} | json | msg="session opened"' % sel
    sid = os.environ.get("SESSION_ID", "").strip()
    if sid:
        logq += ' | session_id="%s"' % sid
    lines = q.logs_query(ctx, logq, since=since, limit=1)
    trace_id = os.environ.get("TRACE_ID", "").strip()
    if not lines:
        absent.append("session opened log line")
        say("ABSENT %s over the last %s" % (logq, since))
    else:
        line = json.loads(lines[0]["line"])
        say("ok     logs %s -> session_id=%s trace_id=%s" % (logq, line.get("session_id"), line.get("trace_id")))
        trace_id = trace_id or line.get("trace_id", "")

    # Traces (AC-3).
    if not trace_id:
        absent.append("trace")
        say("ABSENT no trace_id to fetch (no session opened line, no TRACE_ID)")
    else:
        found = q.trace_get(ctx, trace_id, since=since)
        if not found or OPEN_SESSION_SPAN not in json.dumps(found):
            absent.append("trace")
            say("ABSENT trace %s with a %s span" % (trace_id, OPEN_SESSION_SPAN))
        else:
            say("ok     trace %s carries %s" % (trace_id, OPEN_SESSION_SPAN))

    # The rules, evaluated against the same stack (AC-6's instrument).
    say("rules (informational — an empty result is a rule that would not fire):")
    with open(ALERTS) as f:
        groups = yaml.safe_load(f)["groups"]
    for g in groups:
        for rule in g["rules"]:
            res = q.metrics_query(ctx, rule["expr"])
            say("  %-26s %s" % (rule["alert"], ", ".join(labels(r) for r in res) or "—"))
    return absent


def main(argv):
    args = [a for a in argv if a != "--keep-list"]
    env = (args[0] if args else "").strip()
    if len(args) != 1 or not env:
        raise grafana_stack.Exit(2, "usage: make observe-check ENV=<local|dev|staging|prod> [KEEP_LIST=1]")
    ctx, _ = grafana_stack.require(env)
    gcx.write_config()
    if "--keep-list" in argv:
        import observe_keep_list
        return observe_keep_list.run(env, ctx)
    absent = check(env, ctx, since_ok(os.environ.get("SINCE", "1h")))
    if absent:
        say("%d signal(s) absent: %s" % (len(absent), ", ".join(absent)))
        return 1
    say("every signal present for andara-%s in %s" % (env, ctx))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except grafana_stack.Exit as e:
        say(str(e))
        sys.exit(e.code)
    except (gcx.GcxError, Failed) as e:
        say(str(e))
        sys.exit(1)
