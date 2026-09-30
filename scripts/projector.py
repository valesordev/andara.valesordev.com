#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Operate the state projector on a cluster environment (AW-INF-025, #80).

    scripts/projector.py stop <env>      make projector-stop ENV=<dev|prod>
    scripts/projector.py start <env>     make projector-start ENV=<dev|prod>
    scripts/projector.py rebuild <env>   make projector-rebuild ENV=<dev|prod>

stop     scales the Deployment to 0 and waits for its pods to go and its consumer group to have
         no members, bounded by PROJECTOR_STOP_TIMEOUT (120s).
start    scales it to 1 and waits for Ready, bounded by PROJECTOR_START_TIMEOUT (300s).
rebuild  refuses while a rebuild Job is still running (before scaling anything), deletes a
         finished one, runs `stop`, then the one-shot Job `andara-projector-state-rebuild`
         (`andara-projector state --rebuild`, from the Deployment's own pod template), then
         `start`. Bounded by PROJECTOR_REBUILD_TIMEOUT (30m), which is also the Job's
         activeDeadlineSeconds.

`--rebuild` doesn't exit when it has caught up: it goes on projecting, as the Deployment does.
So the rebuild is done when the Job's pod logs `state projector caught up`. The target then
deletes the Job, which stops the pod with SIGTERM (a clean stop, exit 0, with its checkpoint
committed), and `start` hands the group back to the Deployment, which resumes from that
checkpoint. A Job pod that exits on its own has failed: exit 2 is a divergence, 3 a log gap,
4 a state_version (state-projector-down.md).

The Job carries no scrape annotations (a second target under the projector's job would read
as a second writer) and not the Deployment's `app` label, so it is never mistaken for one of
the Deployment's pods. It keeps `app.kubernetes.io/name=andara`, which the broker's
NetworkPolicy admits.

Progress goes to stderr as `projector-<cmd>: <step>`; the final line names the outcome and the
elapsed time. Exit codes: 0 done; 1 a precondition, a step or a bound failed; 2 usage.
"""

import json
import os
import re
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import topics  # noqa: E402

DEPLOYMENT = "andara-projector-state"
JOB = "andara-projector-state-rebuild"
SCRAPE_PREFIXES = ("prometheus.io/", "k8s.grafana.com/")
DEFAULTS = {"PROJECTOR_STOP_TIMEOUT": "120s", "PROJECTOR_START_TIMEOUT": "300s",
            "PROJECTOR_REBUILD_TIMEOUT": "30m"}


class Failed(Exception):
    pass


def progress(cmd, msg):
    print("projector-%s: %s" % (cmd, msg), file=sys.stderr, flush=True)


def seconds(name):
    v = os.environ.get(name) or DEFAULTS[name]
    m = re.fullmatch(r"(\d+)(s|m|h)?", v.strip())
    if not m:
        raise SystemExit(usage("%s=%s is not a duration like 120s or 30m" % (name, v)))
    return int(m.group(1)) * {"s": 1, "m": 60, "h": 3600, None: 1}[m.group(2)]


def usage(msg):
    print("make: projector: " + msg, file=sys.stderr)
    return 2


def kubectl(ns, *args, check=True, input=None):
    p = subprocess.run(["kubectl", "-n", ns, *args], capture_output=True, text=True, input=input)
    if check and p.returncode != 0:
        raise Failed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p


def get_json(ns, *args):
    p = kubectl(ns, "get", *args, "-o", "json", check=False)
    if p.returncode != 0:
        if "NotFound" in p.stderr:
            return None
        raise Failed("kubectl get %s: %s" % (" ".join(args), p.stderr.strip()))
    return json.loads(p.stdout)


def require_enabled(cmd, env, ns):
    d = get_json(ns, "deployment", DEPLOYMENT)
    if d is None:
        raise Failed("projectors.state.enabled is false in %s (no Deployment %s)" % (ns, DEPLOYMENT))
    return d


def pods_of(ns, labels):
    sel = ",".join("%s=%s" % kv for kv in sorted(labels.items()))
    return kubectl(ns, "get", "pod", "-l", sel, "-o", "name").stdout.split()


def group_empty(run, group):
    res = run(["group", "describe", group])
    text = res.stdout + res.stderr
    if res.returncode != 0:
        return "not found" in text.lower() or "does not exist" in text.lower()
    state = [l.split()[-1] for l in text.splitlines() if l.split()[:1] == ["STATE"]]
    return bool(state) and state[0] in ("Empty", "Dead")


def stop(env, ns, run):
    t0 = time.time()
    d = require_enabled("stop", env, ns)
    bound = seconds("PROJECTOR_STOP_TIMEOUT")
    kubectl(ns, "scale", "deployment", DEPLOYMENT, "--replicas=0")
    progress("stop", "scaled %s to 0" % DEPLOYMENT)
    labels = d["spec"]["selector"]["matchLabels"]
    group = "andara-projector-state-" + env
    deadline = t0 + bound
    while pods_of(ns, labels):
        if time.time() > deadline:
            raise Failed("stop: projector pods still running after %ds" % bound)
        time.sleep(1)
    progress("stop", "no projector pods")
    while not group_empty(run, group):
        if time.time() > deadline:
            raise Failed("stop: consumer group %s still has members after %ds" % (group, bound))
        time.sleep(2)
    return "stopped (group %s empty) in %ds" % (group, round(time.time() - t0))


def start(env, ns):
    t0 = time.time()
    require_enabled("start", env, ns)
    bound = seconds("PROJECTOR_START_TIMEOUT")
    kubectl(ns, "scale", "deployment", DEPLOYMENT, "--replicas=1")
    progress("start", "scaled %s to 1; waiting for Ready" % DEPLOYMENT)
    p = kubectl(ns, "rollout", "status", "deployment/" + DEPLOYMENT, "--timeout=%ds" % bound, check=False)
    if p.returncode != 0:
        raise Failed("start: %s not Ready in %ds: %s" % (DEPLOYMENT, bound, (p.stderr or p.stdout).strip()))
    return "ready in %ds" % round(time.time() - t0)


def job_running(job):
    st = job.get("status", {})
    done = any(c.get("type") in ("Complete", "Failed") and c.get("status") == "True"
               for c in st.get("conditions", []))
    return not done


def job_doc(deployment, bound):
    tmpl = json.loads(json.dumps(deployment["spec"]["template"]))
    meta = tmpl.setdefault("metadata", {})
    meta["annotations"] = {k: v for k, v in meta.get("annotations", {}).items()
                           if not k.startswith(SCRAPE_PREFIXES)}
    meta["labels"] = {k: v for k, v in meta.get("labels", {}).items() if k != "app"}
    meta["labels"]["app"] = JOB
    spec = tmpl["spec"]
    spec["restartPolicy"] = "Never"
    c = spec["containers"][0]
    c["command"] = list(c["command"][:2]) + ["--rebuild"]
    for probe in ("readinessProbe", "livenessProbe", "startupProbe"):
        c.pop(probe, None)
    return {
        "apiVersion": "batch/v1", "kind": "Job",
        "metadata": {"name": JOB, "labels": {"app.kubernetes.io/name": "andara",
                                             "app.kubernetes.io/component": "projector-state-rebuild"}},
        "spec": {"backoffLimit": 0, "ttlSecondsAfterFinished": 3600,
                 "activeDeadlineSeconds": bound, "template": tmpl},
    }


def caught_up_tick(logs):
    for line in logs.splitlines():
        try:
            rec = json.loads(line)
        except ValueError:
            continue
        if rec.get("msg") == "state projector caught up":
            return rec.get("tick")
    return None


def rebuild(env, ns, run):
    t0 = time.time()
    d = require_enabled("rebuild", env, ns)
    bound = seconds("PROJECTOR_REBUILD_TIMEOUT")
    existing = get_json(ns, "job", JOB)
    if existing is not None:
        if job_running(existing):
            raise Failed("rebuild: Job %s is still running in %s; nothing was changed" % (JOB, ns))
        kubectl(ns, "delete", "job", JOB, "--wait=true")
        progress("rebuild", "deleted the finished Job %s" % JOB)
    progress("stop", stop(env, ns, run))
    kubectl(ns, "create", "-f", "-", input=json.dumps(job_doc(d, bound)))
    progress("rebuild", "Job %s created; waiting for `state projector caught up`" % JOB)
    deadline = time.time() + bound
    tick = None
    while tick is None:
        if time.time() > deadline:
            raise Failed("rebuild: Job %s not caught up in %ds" % (JOB, bound))
        job = get_json(ns, "job", JOB) or {}
        if job.get("status", {}).get("failed"):
            raise Failed("rebuild: Job %s failed; its pod's exit code is in `kubectl -n %s describe job %s` "
                         "(2 divergence, 3 log gap, 4 state_version)" % (JOB, ns, JOB))
        tick = caught_up_tick(kubectl(ns, "logs", "job/" + JOB, check=False).stdout)
        if tick is None:
            time.sleep(3)
    progress("rebuild", "caught up at tick %s; stopping the Job" % tick)
    kubectl(ns, "delete", "job", JOB, "--wait=true", "--cascade=foreground")
    progress("start", start(env, ns))
    return "rebuilt to tick %s in %ds" % (tick, round(time.time() - t0))


def main(argv):
    if len(argv) != 2 or argv[0] not in ("stop", "start", "rebuild"):
        return usage("usage: make projector-<stop|start|rebuild> ENV=<dev|prod>")
    cmd, env = argv
    if not env:
        return usage("ENV is missing: make projector-%s ENV=<dev|prod>" % cmd)
    if env not in ("dev", "prod"):
        return usage("ENV=%s: the projector runs on dev or prod only" % env)
    ns = "andara-" + env
    try:
        if cmd == "stop":
            out = stop(env, ns, topics.rpk_runner(env))
        elif cmd == "start":
            out = start(env, ns)
        else:
            out = rebuild(env, ns, topics.rpk_runner(env))
    except Failed as e:
        print("projector-%s: failed in %s: %s" % (cmd, ns, e), file=sys.stderr)
        return 1
    print("projector-%s: %s" % (cmd, out))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
