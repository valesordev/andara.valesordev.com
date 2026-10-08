#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Take the server down and back up on a cluster environment (AW-INF-037).

    scripts/server_ctl.py stop <env>     make server-stop ENV=<dev|prod>
    scripts/server_ctl.py start <env>    make server-start ENV=<dev|prod>

stop   suspends the Argo CD Application's automated sync (selfHeal would scale the StatefulSet
       back up within seconds), scales statefulset/andara to 0, and waits for the pod to go,
       bounded by SERVER_STOP_TIMEOUT (120s). An environment with no Application is scaled
       without suspending anything, and says so.
start  scales to the chart's replica count, restores the sync policy `stop` suspended, and
       waits for Ready (or for the server to be started and waiting for content, as
       world-reset does), bounded by SERVER_START_TIMEOUT (300s).

The suspended `automated` block is recorded as an annotation on the Application, not a local
file: the stop may run on one machine and the start on another. `stop` writes it only when the
Application has an automated policy and none is recorded, so a second `stop` (whose Application
no longer has one) keeps the original. `start` removes the annotation after restoring.

Only `syncPolicy.automated` is suspended; `syncOptions` (RespectIgnoreDifferences) stay.

Exit codes: 0 done; 1 a step or a bound failed; 2 usage or an unknown environment.
"""

import json
import os
import re
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import world_reset  # noqa: E402

ANNOTATION = "andara.valesordev.com/server-stop-sync-policy"
DEFAULTS = {"SERVER_STOP_TIMEOUT": "120s", "SERVER_START_TIMEOUT": "300s"}
ENVS = ("dev", "prod")
SUSPENDED_LINE = "server-stop: automated sync is suspended; make server-start restores it"
REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

Failed = world_reset.StepFailed


def kubectl(*args, **kw):
    # Looked up at call time so one patch of world_reset.kubectl covers scale() and wait_up() too.
    return world_reset.kubectl(*args, **kw)


def say(cmd, msg):
    print("server-%s: %s" % (cmd, msg), flush=True)


def usage(msg):
    print("make: server: " + msg, file=sys.stderr)
    return 2


def seconds(name):
    v = os.environ.get(name) or DEFAULTS[name]
    m = re.fullmatch(r"(\d+)(s|m|h)?", v.strip())
    if not m:
        raise SystemExit(usage("%s=%s is not a duration like 120s or 5m" % (name, v)))
    return int(m.group(1)) * {"s": 1, "m": 60, "h": 3600, None: 1}[m.group(2)]


def chart_replicas(env, values_dir=None):
    """replicaCount as the chart renders it for env: the base values, then the env's file, from
    this checkout (run it from main: Argo CD deploys main on dev)."""
    import yaml
    n = None
    base = values_dir or os.path.join(REPO, "deploy", "helm")
    for path in (os.path.join(base, "andara", "values.yaml"), os.path.join(base, "values", env + ".yaml")):
        with open(path) as f:
            doc = yaml.safe_load(f) or {}
        n = doc.get("replicaCount", n)
    if isinstance(n, bool) or not isinstance(n, int) or n < 1:
        raise Failed("replicaCount in the chart's values is %r, not a positive integer" % (n,))
    return n


def application(ns):
    """The Argo CD Application document, or None when there is none. Fails closed otherwise."""
    p = kubectl(world_reset.ARGO_NS, "get", "application", ns, "-o", "json", check=False)
    if p.returncode != 0:
        if "NotFound" in p.stderr:
            return None
        raise Failed("read Argo CD Application %s (only NotFound counts as none): %s"
                     % (ns, (p.stderr or p.stdout).strip()))
    return json.loads(p.stdout)


def patch(ns, kind, body):
    kubectl(world_reset.ARGO_NS, "patch", "application", ns, "--type", kind, "-p", json.dumps(body))


def suspend(ns):
    """Suspend automated sync, recording it first. Returns True if the sync is now suspended
    by a stop (this one or an earlier one), False when there was nothing to suspend."""
    app = application(ns)
    if app is None:
        say("stop", "no Argo CD Application %s; nothing to suspend" % ns)
        return False
    automated = (app["spec"].get("syncPolicy") or {}).get("automated")
    recorded = (app["metadata"].get("annotations") or {}).get(ANNOTATION)
    if automated is not None and recorded is None:
        patch(ns, "merge", {"metadata": {"annotations": {ANNOTATION: json.dumps(automated)}}})
    if automated is not None:
        patch(ns, "json", [{"op": "remove", "path": "/spec/syncPolicy/automated"}])
        say("stop", "suspended automated sync on %s" % ns)
        return True
    if recorded is not None:
        say("stop", "automated sync on %s is already suspended; the recorded policy stands" % ns)
        return True
    say("stop", "%s has no automated sync; nothing to suspend" % ns)
    return False


def restore(ns):
    app = application(ns)
    if app is None:
        say("start", "no Argo CD Application %s; nothing to restore" % ns)
        return
    recorded = (app["metadata"].get("annotations") or {}).get(ANNOTATION)
    if recorded is None:
        say("start", "no suspended sync recorded on %s; nothing to restore" % ns)
        return
    try:
        automated = json.loads(recorded)
    except ValueError:
        raise Failed("the annotation %s on %s isn't JSON: %r" % (ANNOTATION, ns, recorded))
    # add replaces a member, where a merge patch would union a hand-set block with the record.
    patch(ns, "json", [{"op": "add", "path": "/spec/syncPolicy/automated", "value": automated}])
    patch(ns, "merge", {"metadata": {"annotations": {ANNOTATION: None}}})
    say("start", "restored automated sync on %s" % ns)


def stop(env, ns):
    t0 = time.time()
    bound = seconds("SERVER_STOP_TIMEOUT")
    suspended = suspend(ns)
    try:
        world_reset.scale(ns, world_reset.SERVER, 0)
        say("stop", "scaled %s to 0" % world_reset.SERVER)
        kubectl(ns, "wait", "--for=delete", "pod/" + world_reset.SERVER_POD, "--timeout=%ds" % bound,
                check=False)
        if kubectl(ns, "get", "pod", world_reset.SERVER_POD, check=False).returncode == 0:
            raise Failed("%s still running %ds after scaling to 0" % (world_reset.SERVER_POD, bound))
    except Failed:
        if suspended:
            print(SUSPENDED_LINE, file=sys.stderr, flush=True)
        raise
    say("stop", "pods gone (%ds)" % round(time.time() - t0))
    if suspended:
        print(SUSPENDED_LINE, flush=True)


def start(env, ns):
    t0 = time.time()
    bound = seconds("SERVER_START_TIMEOUT")
    n = chart_replicas(env)
    world_reset.scale(ns, world_reset.SERVER, n)
    say("start", "scaled %s to %d" % (world_reset.SERVER, n))
    restore(ns)
    state = world_reset.wait_up(ns, deadline=bound)
    if state == "waiting":
        say("start", "%s is started and waiting for content (AW-SRV-042). Next: make content-seed ENV=%s"
            % (world_reset.SERVER_POD, env))
    else:
        say("start", "%s Ready in %ds" % (world_reset.SERVER_POD, round(time.time() - t0)))


def main(argv):
    if len(argv) != 2 or argv[0] not in ("stop", "start"):
        return usage("usage: make server-<stop|start> ENV=<dev|prod>")
    cmd, env = argv
    if not env:
        return usage("ENV is missing: make server-%s ENV=<dev|prod>" % cmd)
    if env == "local":
        return usage("ENV=local: the local server is `make down` and `make up`")
    if env not in ENVS:
        return usage("ENV=%s is unknown; expected dev or prod" % env)
    ns = "andara-" + env
    try:
        (stop if cmd == "stop" else start)(env, ns)
    except Failed as e:
        print("server-%s: failed in %s: %s" % (cmd, ns, e), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
