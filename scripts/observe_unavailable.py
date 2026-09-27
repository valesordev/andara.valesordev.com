#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-unavailable ENV=dev — does AndaraServerUnavailable follow the server? (AW-INF-008 AC-6)

Takes andara-<env>'s server away for real and watches the rule's expression in Grafana Cloud:

  1. Evaluates AndaraServerUnavailable's expr (files/alerts.yaml) as an instant query. The env's
     namespace must be absent from the result; every other namespace in it is recorded.
  2. Scales StatefulSet `andara` to 0 and polls the expr until a result carries the namespace,
     to DEADLINE (default 300 s). A missing series takes about 80 s to register.
  3. Scales it back to 1 whatever happened, and waits for andara-0 to be Ready.
  4. Polls until the namespace's result clears, to the same deadline.

Every poll also asserts that no namespace other than this one appeared or disappeared: the rule
answers for this environment and no other.

Why a scale, not a pod delete: the StatefulSet replaces a deleted pod faster than an absent series
registers, so a delete never shows a result (the box session, 2026-09-26). The rule's `for: 2m`
is the same property, from the alert's side.

Only ENV=dev. Taking prod's server away is an outage, not a check.

Credentials as for `make observe-check`: GRAFANA_CLOUD_READ_TOKEN and GRAFANA_CLOUD_PROM_URL /
_PROM_USER, from the environment, never printed.

Exit: 0 the result appeared and cleared, and nothing else moved · 1 it didn't, or another
namespace moved · 2 a backend or kubectl failed · 3 a credential unset or ENV not dev.
"""
import importlib.util
import os
import subprocess
import sys
import time

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("observe_check", os.path.join(HERE, "observe_check.py"))
oc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(oc)

RULE = "AndaraServerUnavailable"
POLL = 10


def say(msg):
    print("observe-unavailable: " + msg, flush=True)


def rule_expr():
    for g in yaml.safe_load(open(oc.ALERTS))["groups"]:
        for rule in g["rules"]:
            if rule["alert"] == RULE:
                return rule["expr"]
    say("no %s in %s" % (RULE, oc.ALERTS))
    sys.exit(2)


def namespaces(prom, expr):
    return {r["metric"].get("namespace", "") for r in oc.instant(prom, expr)}


def kubectl(*args):
    p = subprocess.run(["kubectl", *args], capture_output=True, text=True)
    if p.returncode != 0:
        say("kubectl %s: %s" % (" ".join(args), p.stderr.strip()))
        sys.exit(2)
    return p.stdout


def wait_for(prom, expr, ns, others, want, deadline, what):
    """Poll until ns is (want=True) or isn't in the result; fail if another namespace moves."""
    end = time.monotonic() + deadline
    while True:
        got = namespaces(prom, expr)
        moved = (got - {ns}) ^ others
        if moved:
            say("FAIL %s: namespace(s) %s moved while only %s should" % (what, sorted(moved), ns))
            return False
        if (ns in got) == want:
            say("ok     %s at %s: %s" % (what, time.strftime("%H:%M:%SZ", time.gmtime()), sorted(got) or "—"))
            return True
        if time.monotonic() > end:
            say("FAIL %s: not within %ds (result %s)" % (what, deadline, sorted(got) or "—"))
            return False
        time.sleep(POLL)


def main():
    env = sys.argv[1] if len(sys.argv) > 1 else ""
    if env != "dev":
        say("ENV=%s: only dev; taking prod's server away is an outage, not a check" % (env or "<unset>"))
        sys.exit(3)
    ns = "andara-" + env
    deadline = int(os.environ.get("DEADLINE", "300"))
    prom = oc.Backend("PROM", oc.need("GRAFANA_CLOUD_READ_TOKEN"))
    expr = rule_expr()

    before = namespaces(prom, expr)
    if ns in before:
        say("FAIL %s already names %s before the server is taken away; is andara-0 Ready?" % (RULE, ns))
        sys.exit(1)
    say("before: %s returns %s" % (RULE, sorted(before) or "—"))

    say("scaling statefulset/andara in %s to 0 at %s" % (ns, time.strftime("%H:%M:%SZ", time.gmtime())))
    kubectl("-n", ns, "scale", "statefulset", "andara", "--replicas=0")
    try:
        appeared = wait_for(prom, expr, ns, before, True, deadline, "%s names %s" % (RULE, ns))
    finally:
        say("scaling statefulset/andara in %s back to 1" % ns)
        kubectl("-n", ns, "scale", "statefulset", "andara", "--replicas=1")
        # The pod object may not exist yet; wait for it, then for Ready.
        end = time.monotonic() + deadline
        while subprocess.run(["kubectl", "-n", ns, "get", "pod", "andara-0"], capture_output=True).returncode != 0:
            if time.monotonic() > end:
                say("andara-0 was not recreated within %ds" % deadline)
                sys.exit(2)
            time.sleep(2)
        kubectl("-n", ns, "wait", "--for=condition=Ready", "pod/andara-0", "--timeout=%ds" % deadline)
    if not appeared:
        sys.exit(1)
    if not wait_for(prom, expr, ns, before, False, deadline, "%s clears %s" % (RULE, ns)):
        sys.exit(1)
    say("ok — %s followed %s's server down and back, and no other namespace moved" % (RULE, ns))


if __name__ == "__main__":
    try:
        main()
    except oc.BackendError as e:
        say("backend error: %s" % e)
        sys.exit(2)
