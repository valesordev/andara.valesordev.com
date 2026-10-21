#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make dashboard-check ENV=<env> — does every tick-health panel show data for ENV? (AW-INF-049 AC-3, AC-4)

Reads deploy/grafana/dashboards/tick-health.json, the source Terraform deploys to both stacks
(ADR-0012 decision 11), and evaluates every panel query that reads a series against ENV's stack
(scripts/grafana_stack.py: local, dev, staging → solo7dev; prod → solo7) through gcx
(scripts/gcx.py), with the dashboard's variables filled in: $environment=ENV and
$namespace=andara-<env>, plus cluster="andara" for ENV=local (ADR-0014 A3: CI also ships
andara-local, on cluster ci). Each query must return a series. The queries are polled together until
each has, or until DEADLINE (seconds, default 300) passes (docs/specs/testing/live-assertions.md).
A query that reads no series, such as the Tick Budget line `vector(0.05)`, is listed and not
queried.

Then the environment variable's options, as the dashboard lists them (AC-4): its label_values
query through `gcx metrics labels`. They must include ENV and hold only the environments ENV's
stack carries (solo7dev: local, dev, staging; solo7: prod), polled to the same DEADLINE.

Credentials: GRAFANA_<STACK>_READ_TOKEN for ENV's stack, from the environment or .local/box.env.

Exit: 0 every panel query returned a series and the options hold · 1 a query returned none before
the deadline (named by panel), the options are wrong, or a gcx error · 2 usage · 3 the stack's read
token unset.
"""
import json
import os
import re
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
from observe_keep_list import names_in  # noqa: E402

REPO = os.path.dirname(HERE)
DASHBOARD = os.path.join(REPO, "deploy", "grafana", "dashboards", "tick-health.json")
# The environments each stack carries (ADR-0012 decision 9).
STACK_ENVIRONMENTS = {"solo7dev": {"local", "dev", "staging"}, "solo7": {"prod"}}
POLL = 15


def say(msg):
    print("dashboard-check: " + msg, flush=True)


def load(path=DASHBOARD):
    with open(path) as f:
        return json.load(f)


def variables(d):
    return {v["name"]: v for v in d.get("templating", {}).get("list", [])}


def variable_query(v):
    q = v.get("query")
    return q.get("query", "") if isinstance(q, dict) else (q or "")


def targets(d):
    """[(panel title, refId, expr)] for every panel target, in panel order."""
    return [(p["title"], t.get("refId", ""), t["expr"])
            for p in d["panels"] for t in p.get("targets", []) if "expr" in t]


def fill(expr, env, ns, extra=""):
    """expr with the dashboard's variables as Grafana would substitute them for ENV; extra follows the namespace."""
    return (expr.replace('namespace="$namespace"', 'namespace="%s"%s' % (ns, extra))
            .replace("$environment", env).replace("$namespace", ns)
            .replace("$__rate_interval", "5m").replace("$__interval", "1m"))


def box_matcher(env):
    """ADR-0014 A3: CI's lifecycle job also ships andara-local into solo7dev, on cluster ci. A box ENV=local
    check selects the box's cluster, so CI's series can't stand in for it."""
    return ', cluster="andara"' if env == "local" else ""


def label_values(query):
    """(label, series selector) from label_values(<selector>, <label>)."""
    m = re.fullmatch(r"\s*label_values\((.*),\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s*", query)
    if not m:
        raise grafana_stack.Exit(1, "the variable query %r is not label_values(<selector>, <label>)" % query)
    return m.group(2), m.group(1)


def check_queries(env, ctx, deadline, q=gcx):
    """The panels with a query that returned no series before the deadline."""
    ns = "andara-" + env
    pending = []
    for title, ref, expr in targets(load()):
        if not names_in(expr):
            say("--     %s [%s] %s: reads no series, not queried" % (title, ref, expr))
            continue
        pending.append((title, ref, fill(expr, env, ns, box_matcher(env))))
    end = time.monotonic() + deadline
    while True:
        left = []
        for title, ref, expr in pending:
            res = q.metrics_query(ctx, expr)
            if res:
                say("ok     %s [%s] %d series" % (title, ref, len(res)))
            else:
                left.append((title, ref, expr))
        pending = left
        if not pending or time.monotonic() >= end:
            break
        time.sleep(POLL)
    for title, ref, expr in pending:
        say("ABSENT %s [%s] %s: no series within %ds" % (title, ref, expr, deadline))
    return sorted({title for title, _, _ in pending})


def check_options(env, ctx, stack, deadline, q=gcx):
    """AC-4: the environment variable's options in ENV's stack, polled to the deadline; the problems left."""
    label, match = label_values(variable_query(variables(load())["environment"]))
    end = time.monotonic() + deadline
    while True:
        options = sorted(q.metrics_labels(ctx, label, match=match))
        problems = []
        if env not in options:
            problems.append("%s is not an option" % env)
        foreign = [o for o in options if o not in STACK_ENVIRONMENTS[stack]]
        if foreign:
            problems.append("%s carries %s, which is not one of its environments" % (stack, ", ".join(foreign)))
        if not problems or time.monotonic() >= end:
            break
        time.sleep(POLL)
    say("environment options in %s: %s" % (stack, ", ".join(options) or "—"))
    return problems


def main(argv, q=gcx):
    env = (argv[0] if argv else "").strip()
    if len(argv) != 1 or not env:
        raise grafana_stack.Exit(2, "usage: make dashboard-check ENV=<local|dev|staging|prod>")
    stack, _ = grafana_stack.require(env)
    try:
        deadline = int(os.environ.get("DEADLINE", "300"))
    except ValueError:
        raise grafana_stack.Exit(2, "DEADLINE=%s is not a whole number of seconds" % os.environ["DEADLINE"])
    q.write_config()
    absent = check_queries(env, stack, deadline, q=q)
    problems = check_options(env, stack, stack, deadline, q=q)
    for p in problems:
        say("FAIL options: " + p)
    if absent:
        say("%d panel(s) without data for environment=%s: %s" % (len(absent), env, "; ".join(absent)))
    if absent or problems:
        return 1
    say("every panel query returned a series for environment=%s in %s" % (env, stack))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except grafana_stack.Exit as e:
        say(str(e))
        sys.exit(e.code)
    except gcx.GcxError as e:
        say(str(e))
        sys.exit(1)
