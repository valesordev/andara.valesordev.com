#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-unavailable ENV=dev — does AndaraServerUnavailable follow the server? (AW-INF-008 AC-6, AW-INF-069)

Takes andara-<env>'s server away for real and watches, through gcx (scripts/gcx.py), both the rule's expr
(files/alerts.yaml, `rule_expr()`) and the rule's own instance state in Grafana Cloud:

  0. Selects the backend evaluating the rule for dev (scripts/rule_backend.py) and prints it, with the stack,
     the filter, the poll interval and ALERT_SETTLE (the group's interval × 2 + the poll). It refuses, exit 1
     and before scaling anything, on the legacy ruler, and on a Grafana-managed rule whose live expression has
     no dev absence operand (`absent(up{… environment="dev", namespace="andara-dev"})`): without it the rule
     cannot fire for a stopped dev. Cutover step 4(v) adds it; the drill's live run is step 4(vi) (AW-INF-070).
  1. Before: the expr does not name the env's namespace, and the rule is normal for it.
  2. `drill: start`: scales StatefulSet `andara` to 0, and polls every 10 s until the rule is firing for the
     environment and namespace, to DEADLINE (default 300 s) + the rule's `for` + ALERT_SETTLE.
  3. Scales it back to 1 whatever happened and waits for andara-0 to be Ready (`drill: server-restored`).
  4. Polls until the expr no longer names the namespace and the rule is normal, to DEADLINE + ALERT_SETTLE.
     `drill: end`.

Each poll prints `record <time> <rule> <state> (gcx: <raw>) expr: <namespaces>`, and the drill passes on that
record alone: the rule fired, then cleared, and no other environment or namespace moved, in the expr's result
or in the rule's other instances.

Why a scale, not a pod delete: the StatefulSet replaces a deleted pod faster than an absent series
registers, so a delete never shows a result (the box session, 2026-09-26). The rule's `for: 2m`
is the same property, from the alert's side.

Only ENV=dev. Taking prod's server away is an outage, not a check.

Credentials: GRAFANA_SOLO7DEV_READ_TOKEN (dev's stack), and for the backend selection GRAFANA_SOLO7_READ_TOKEN
and the MIMIR_* ruler read (scripts/ruler_read.py), from the environment or .local/box.env, never printed.

Exit: 0 the rule fired and cleared, and nothing else moved · 1 it didn't, another environment or namespace
moved, the run was inconclusive, or it refused before cutover step 4(v) · 2 a backend or kubectl failed ·
3 a credential unset or ENV not dev.
"""
import os
import re
import subprocess
import sys
import time

import kube

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import rule_backend  # noqa: E402

RULE = "AndaraServerUnavailable"
POLL = rule_backend.POLL
DURATION = re.compile(r"^([0-9]+)([smh])$")
ABSENT = re.compile(r"absent\(\s*up\{([^}]*)\}\s*\)")


class Refused(Exception):
    """The drill cannot run yet: exit 1, before anything is scaled."""


def say(msg):
    print("observe-unavailable: " + msg, flush=True)


def rule_expr():
    _, r = rule_backend.alerts_rule(RULE)
    return r["expr"]


def rule_for():
    """The rule's `for`, in seconds."""
    _, r = rule_backend.alerts_rule(RULE)
    m = DURATION.match(str(r.get("for", "0s")))
    return int(m.group(1)) * {"s": 1, "m": 60, "h": 3600}[m.group(2)] if m else 0


def has_dev_absence(query, env="dev"):
    """True when the live expression carries the operand that fires for a stopped env (cutover step 4(v))."""
    want = ('environment="%s"' % env, 'namespace="andara-%s"' % env)
    return any(all(w in m.group(1) for w in want) for m in ABSENT.finditer(query or ""))


def ready_to_drill(b):
    """Refused unless b is the Grafana-managed backend and its live rule can fire for a stopped dev."""
    if b.kind != rule_backend.GRAFANA:
        raise Refused("runs after cutover step 4(v): %s is evaluated by the legacy %s, whose dev series cannot "
                      "satisfy the edited expression's environment matchers" % (RULE, b))
    if not has_dev_absence(b.query):
        raise Refused("runs after cutover step 4(v): the live %s in %s has no dev absence operand "
                      "(absent(up{… environment=\"dev\", namespace=\"andara-dev\"})), so it cannot fire for a "
                      "stopped dev" % (RULE, b))


class Reading:
    """One poll: the expr's namespaces, this env's rule state, and every other instance's state."""

    def __init__(self, t, namespaces, state, raw, others):
        self.t, self.namespaces, self.state, self.raw, self.others = t, set(namespaces), state, raw, dict(others)

    def line(self):
        return "%s expr: %s" % (rule_backend.record_line(RULE, rule_backend.Sample(self.t, self.state, self.raw)),
                                sorted(self.namespaces) or "—")


def read(b, expr, clock=time.time):
    """A Reading from the backend's stack: the expr as an instant query, the rule's instances split into this
    environment's (b.selectors) and the others', keyed by (environment, namespace)."""
    got = {r.get("metric", {}).get("namespace", "") for r in b.instant(expr)}
    ours, others = [], {}
    for i in gcx.instances(b.stack, "^%s$" % re.escape(RULE), env=b.env):
        lab = i["labels"]
        if all(lab.get(k) == v for k, v in b.selectors.items()):
            ours.append(i)
        else:
            others[(lab.get("environment", ""), lab.get("namespace", ""))] = i["state"]
    state, raw = rule_backend.fold(ours)
    return Reading(clock(), got, state, raw, others)


def moved(before, r, ns):
    """What moved in r, other than ns, since before: namespaces in the expr's result, other instances' states."""
    out = sorted((r.namespaces - {ns}) ^ (before.namespaces - {ns}))
    keys = set(before.others) | set(r.others)
    out += ["%s/%s %s→%s" % (k[0] or "-", k[1] or "-", before.others.get(k, "absent"), r.others.get(k, "absent"))
            for k in sorted(keys) if before.others.get(k) != r.others.get(k)]
    return out


def judge(before, record, ns):
    """The drill's verdict over its own record: [] when the rule fired for ns and then cleared (the last reading
    normal and the expr no longer naming ns), and nothing else moved; else every reason it did not."""
    out = []
    for r in record:
        m = moved(before, r, ns)
        if m:
            out.append("at %s another environment or namespace moved: %s" % (rule_backend.stamp(r.t), ", ".join(m)))
        if r.state not in (gcx.FIRING, gcx.PENDING, gcx.NORMAL):
            out.append("at %s state %r is not firing, pending or normal" % (rule_backend.stamp(r.t), r.raw))
    fired = [i for i, r in enumerate(record) if r.state == gcx.FIRING]
    if not fired:
        out.append("%s never fired for %s" % (RULE, ns))
    elif record[-1].state != gcx.NORMAL or ns in record[-1].namespaces:
        out.append("%s fired for %s but never cleared" % (RULE, ns))
    return out


def kubectl(*args):
    p = subprocess.run(kube.kubectl(*args), capture_output=True, text=True)
    if p.returncode != 0:
        say("kubectl %s: %s" % (" ".join(args), p.stderr.strip()))
        sys.exit(2)
    return p.stdout


def poll(reader, done, deadline, record, clock=None, sleep=None):
    """Read into record until done(reading) or deadline seconds pass, reads starting at most POLL apart; True when
    done."""
    clock, sleep = clock or time.monotonic, sleep or time.sleep
    end = clock() + deadline
    while True:
        t0 = clock()
        r = reader()
        record.append(r)
        say(r.line())
        if done(r):
            return True
        if clock() > end:
            return False
        sleep(max(0.0, POLL - (clock() - t0)))


def restore(ns, deadline):
    say("scaling statefulset/andara in %s back to 1" % ns)
    kubectl("-n", ns, "scale", "statefulset", "andara", "--replicas=1")
    # The pod object may not exist yet; wait for it, then for Ready.
    end = time.monotonic() + deadline
    while subprocess.run(kube.kubectl("-n", ns, "get", "pod", "andara-0"), capture_output=True).returncode != 0:
        if time.monotonic() > end:
            say("andara-0 was not recreated within %ds" % deadline)
            sys.exit(2)
        time.sleep(2)
    kubectl("-n", ns, "wait", "--for=condition=Ready", "pod/andara-0", "--timeout=%ds" % deadline)


def drill(b, ns, deadline, reader, scale_down, restore_up):
    """Steps 1 to 4; the verdict over the record. Exits 1 before scaling when the before-state is wrong."""
    before = reader()
    say("before: " + before.line())
    if ns in before.namespaces or before.state != gcx.NORMAL:
        say("FAIL %s is already %s / the expr names %s before the server is taken away; is andara-0 Ready?"
            % (RULE, before.state, sorted(before.namespaces)))
        sys.exit(1)
    record = []
    say("drill: start %s (scaling statefulset/andara in %s to 0)" % (rule_backend.stamp(time.time()), ns))
    aborted = "aborted"
    try:
        scale_down()
        try:
            poll(reader, lambda r: r.state == gcx.FIRING, deadline + rule_for() + b.settle, record)
        finally:
            restore_up()
            say("drill: server-restored %s (andara-0 Ready in %s)" % (rule_backend.stamp(time.time()), ns))
        if any(r.state == gcx.FIRING for r in record):
            poll(reader, lambda r: r.state == gcx.NORMAL and ns not in r.namespaces, deadline + b.settle, record)
        aborted = None
    except BaseException as e:
        aborted = "aborted: %s" % e
        raise
    finally:
        say("drill: end %s%s" % (rule_backend.stamp(time.time()), " (%s)" % aborted if aborted else ""))
    return judge(before, record, ns)


def main():
    main_for(sys.argv[1] if len(sys.argv) > 1 else "")


def main_for(env):
    if env != "dev":
        say("ENV=%s: only dev; taking prod's server away is an outage, not a check" % (env or "<unset>"))
        sys.exit(3)
    ns = "andara-" + env
    deadline = int(os.environ.get("DEADLINE", "300"))
    try:
        stack, ruler_env, merged = rule_backend.credentials(env)
    except grafana_stack.Exit as e:
        say(str(e))
        sys.exit(e.code)
    gcx.write_config(merged)
    try:
        b = rule_backend.select(env, RULE, stack, ruler_env, merged)
        for line in b.header():
            say(line)
        ready_to_drill(b)
    except (rule_backend.Inconclusive, Refused) as e:
        say("FAIL %s" % e)
        sys.exit(1)
    expr = rule_expr()
    problems = drill(b, ns, deadline, lambda: read(b, expr),
                     lambda: kubectl("-n", ns, "scale", "statefulset", "andara", "--replicas=0"),
                     lambda: restore(ns, deadline))
    for p in problems:
        say("FAIL %s" % p)
    if problems:
        sys.exit(1)
    say("ok — %s fired for %s and cleared, following its server down and back, and nothing else moved" % (RULE, ns))


if __name__ == "__main__":
    try:
        main()
    except gcx.GcxError as e:
        say("backend error: %s" % e)
        sys.exit(2)
