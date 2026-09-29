#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make world-reset ENV=<env> CONFIRM=<namespace>` — start an environment's World over.

    scripts/world_reset.py <env> <confirm>

Recreates the World's log topics and the Account store, and empties the snapshot store and
the projector's state, keeping content (AW-INF-021, feedback item 2). Every Character and
Account is destroyed. The bootstrap operator comes back from its Secret on the next boot.

Refused with exit 2 before anything touches the cluster: ENV=prod, ENV=local (whose World is
`make down VOLUMES=1`), a missing CONFIRM, or a CONFIRM that isn't the target namespace.

Steps, in order, each named on failure:
  1. Argo CD's automated sync on the environment's Application is suspended, or selfHeal would
     scale the server back up mid-reset. It's restored on the way out, whatever happens.
  2. The server StatefulSet and the projector Deployment scale to 0.
  3. RESET_TOPICS are deleted, and recreated from deploy/kafka/topics.yaml by topics.py.
  4. The snapshot PVCs are deleted, and the StatefulSet makes them again, empty.
  5. The projector's consumer group is deleted.
  6. Both scale back to what they were, and the server is waited on until Ready.
Projector steps are skipped with a line saying so while the environment runs no projector.

Exit codes: 0 reset; 1 a step failed; 2 usage or refusal.
"""

import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import topics  # noqa: E402

# Named explicitly: a topic added to topics.yaml later is kept unless it's added here. Never
# andara.content.* or andara.audit.v1.
RESET_TOPICS = ["andara.commands.v1", "andara.events.v1", "andara.state.v1", "andara.accounts.v1"]
SERVER = "statefulset/andara"
SERVER_POD = "andara-0"
SNAPSHOT_PVC_PREFIX = "snapshots-andara-"
PROJECTOR = "deployment/andara-projector-state"
ARGO_NS = "argocd"
TOPIC_DELETE_DEADLINE = 120
READY_DEADLINE = "600s"


def say(msg):
    print("world-reset: " + msg, flush=True)


def refuse(msg):
    print("world-reset: " + msg, file=sys.stderr)
    sys.exit(2)


class StepFailed(Exception):
    pass


def kubectl(ns, *args, check=True):
    p = subprocess.run(["kubectl", "-n", ns, *args], capture_output=True, text=True)
    if check and p.returncode != 0:
        raise StepFailed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p


def exists(ns, ref):
    return kubectl(ns, "get", ref, "-o", "name", check=False).returncode == 0


def replicas(ns, ref):
    return int(kubectl(ns, "get", ref, "-o", "jsonpath={.spec.replicas}").stdout or 0)


def scale(ns, ref, n):
    kubectl(ns, "scale", ref, "--replicas=%d" % n)


def check_args(argv):
    if len(argv) != 2 or not argv[0]:
        refuse("usage: make world-reset ENV=<env> CONFIRM=andara-<env>")
    env, confirm = argv
    if env == "prod":
        refuse("prod is never reset")
    if env == "local":
        refuse("local's World is `make down VOLUMES=1`")
    ns = "andara-" + env
    if not confirm:
        refuse("CONFIRM is missing; it must be the namespace, %s" % ns)
    if confirm != ns:
        refuse("CONFIRM=%s is not the namespace %s" % (confirm, ns))
    return env, ns


def suspend_argo(ns):
    """Remove the Application's syncPolicy, returning it (or None) so it can be restored."""
    app = ns
    p = kubectl(ARGO_NS, "get", "application", app, "-o", "json", check=False)
    if p.returncode != 0:
        say("no Argo CD Application %s; nothing to suspend" % app)
        return app, None
    policy = json.loads(p.stdout)["spec"].get("syncPolicy")
    if policy:
        kubectl(ARGO_NS, "patch", "application", app, "--type", "json",
                "-p", json.dumps([{"op": "remove", "path": "/spec/syncPolicy"}]))
        say("suspended Argo CD's automated sync on %s" % app)
    return app, policy


def restore_argo(app, policy):
    if not policy:
        return
    p = kubectl(ARGO_NS, "patch", "application", app, "--type", "merge",
                "-p", json.dumps({"spec": {"syncPolicy": policy}}), check=False)
    if p.returncode != 0:
        print("world-reset: could not restore %s's syncPolicy; restore it with: kubectl -n %s "
              "patch application %s --type merge -p '%s'"
              % (app, ARGO_NS, app, json.dumps({"spec": {"syncPolicy": policy}})), file=sys.stderr)
        raise StepFailed("restore Argo CD sync")
    say("restored Argo CD's automated sync on %s" % app)


def reset_topics(env, run):
    live = topics.existing_topics(run)
    for name in RESET_TOPICS:
        if name not in live:
            continue
        res = run(["topic", "delete", name])
        if res.returncode != 0:
            raise StepFailed("delete %s: %s" % (name, (res.stderr or res.stdout).strip()))
    deadline = time.time() + TOPIC_DELETE_DEADLINE
    while set(RESET_TOPICS) & set(topics.existing_topics(run)):
        if time.time() > deadline:
            raise StepFailed("topics still present %ds after delete" % TOPIC_DELETE_DEADLINE)
        time.sleep(2)
    say("deleted %s" % ", ".join(RESET_TOPICS))
    if topics.run_action("apply", env, [], run) != 0:
        raise StepFailed("recreate topics from deploy/kafka/topics.yaml")


def reset(env, ns):
    run = topics.rpk_runner(env)
    app, policy = suspend_argo(ns)
    try:
        server_n = replicas(ns, SERVER) or 1
        has_projector = exists(ns, PROJECTOR)
        projector_n = replicas(ns, PROJECTOR) if has_projector else 0

        scale(ns, SERVER, 0)
        if has_projector:
            scale(ns, PROJECTOR, 0)
        else:
            say("no projector in %s; skipping its steps" % ns)
        kubectl(ns, "wait", "--for=delete", "pod/" + SERVER_POD, "--timeout=120s", check=False)
        if kubectl(ns, "get", "pod", SERVER_POD, check=False).returncode == 0:
            raise StepFailed("%s still running after scaling to 0" % SERVER_POD)
        say("scaled %s to 0" % SERVER)

        reset_topics(env, run)

        pvcs = [n.split("/", 1)[1] for n in kubectl(ns, "get", "pvc", "-o", "name").stdout.split()
                if n.split("/", 1)[1].startswith(SNAPSHOT_PVC_PREFIX)]
        for pvc in pvcs:
            kubectl(ns, "delete", "pvc", pvc, "--wait=true", "--timeout=120s")
        say("emptied the snapshot store (%s)" % (", ".join(pvcs) or "no PVC"))

        if has_projector:
            group = "andara-projector-state-" + env
            res = run(["group", "delete", group])
            if res.returncode != 0 and "not found" not in (res.stderr + res.stdout).lower():
                raise StepFailed("delete consumer group %s: %s" % (group, (res.stderr or res.stdout).strip()))
            say("deleted consumer group %s" % group)
            scale(ns, PROJECTOR, projector_n)

        scale(ns, SERVER, server_n)
        say("scaled %s to %d; waiting for Ready" % (SERVER, server_n))
        kubectl(ns, "rollout", "status", SERVER, "--timeout=" + READY_DEADLINE)
    finally:
        restore_argo(app, policy)


def main(argv):
    env, ns = check_args(argv)
    try:
        reset(env, ns)
    except StepFailed as e:
        print("world-reset: %s" % e, file=sys.stderr)
        return 1
    say("%s reset; content kept, accounts and characters gone" % ns)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
