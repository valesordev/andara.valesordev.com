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
  0. Preconditions, before anything changes: the Argo CD Application is readable (only a
     confirmed NotFound counts as "none"), and the configured snapshot store is `fs` or `s3`.
  1. Argo CD's automated sync on the environment's Application is suspended, or selfHeal would
     scale the server back up mid-reset. It's restored on the way out, whatever happens.
  2. The server StatefulSet scales to 0 and its pod is waited on; the projector stops through
     `make projector-stop`'s code (pods gone, consumer group with no members).
  3. RESET_TOPICS are deleted, and recreated from deploy/kafka/topics.yaml by topics.py.
  4. The snapshot store is emptied: on `s3` (AW-INF-025) every object in
     andara-snapshots-<env>, through objectstore.py; on `fs` the snapshot PVCs, which the
     StatefulSet makes again, empty. A round that outlives its log is a wrong World.
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
import objectstore  # noqa: E402
import projector  # noqa: E402
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
STOP_DEADLINE = 120
CONFIG_MAP = "andara-config"
READY_DEADLINE = 600
WAITING_LINE = "waiting for content"


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


def argo_policy(ns):
    """The Application's syncPolicy: None when there's no Application. Fails closed otherwise."""
    p = kubectl(ARGO_NS, "get", "application", ns, "-o", "json", check=False)
    if p.returncode != 0:
        if "NotFound" in p.stderr:
            return False, None
        raise StepFailed("read Argo CD Application %s (only NotFound counts as none): %s"
                         % (ns, (p.stderr or p.stdout).strip()))
    return True, json.loads(p.stdout)["spec"].get("syncPolicy")


def snapshot_store(ns):
    p = kubectl(ns, "get", "configmap", CONFIG_MAP, "-o", "jsonpath={.data.ANDARA_SNAPSHOT_STORE}")
    return p.stdout.strip() or "fs"


def suspend_argo(ns):
    """Remove the Application's syncPolicy, returning it (or None) so it can be restored."""
    app = ns
    found, policy = argo_policy(ns)
    if not found:
        say("no Argo CD Application %s; nothing to suspend" % app)
        return app, None
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
    argo_policy(ns)
    store = snapshot_store(ns)
    if store not in ("fs", "s3"):
        raise StepFailed("snapshot.store=%s in %s isn't one world-reset can empty; nothing was changed"
                         % (store, ns))
    app, policy = suspend_argo(ns)
    try:
        server_n = replicas(ns, SERVER) or 1
        has_projector = exists(ns, PROJECTOR)
        projector_n = replicas(ns, PROJECTOR) if has_projector else 0

        scale(ns, SERVER, 0)
        if has_projector:
            try:
                say("projector-stop: " + projector.stop(env, ns, run))
            except projector.Failed as e:
                raise StepFailed(str(e))
        else:
            say("no projector in %s; skipping its steps" % ns)
        kubectl(ns, "wait", "--for=delete", "pod/" + SERVER_POD, "--timeout=%ds" % STOP_DEADLINE, check=False)
        if kubectl(ns, "get", "pod", SERVER_POD, check=False).returncode == 0:
            raise StepFailed("%s still running after scaling to 0" % SERVER_POD)
        say("scaled %s to 0" % SERVER)

        reset_topics(env, run)

        if store == "s3":
            try:
                say("emptied the snapshot store (bucket %s)" % objectstore.empty(env))
            except objectstore.Failed as e:
                raise StepFailed("empty the snapshot bucket: %s" % e)
        else:
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
        if wait_up(ns) == "waiting":
            say("%s is started and waiting for content: the store holds no pack with Zones "
                "(AW-SRV-042). Next: make content-seed ENV=%s" % (SERVER_POD, env))
    finally:
        restore_argo(app, policy)


def waiting_for_content(ns, kubectl=None):
    """True when andara-0's server container is started, not Ready, and has logged that it's
    waiting for content since it started: a store-backed server whose World has never had
    content, which stays up unready until a pack with Zones is activated (AW-SRV-042)."""
    kubectl = kubectl or globals()["kubectl"]
    pod = kubectl(ns, "get", "pod", SERVER_POD, "-o", "json", check=False)
    if pod.returncode != 0 or not pod.stdout.strip():
        return False
    st = json.loads(pod.stdout).get("status", {})
    ready = [c for c in st.get("conditions", []) if c.get("type") == "Ready"]
    if ready and ready[0].get("status") == "True":
        return False
    server = [c for c in st.get("containerStatuses", []) if c.get("name") == "server"]
    if not server or not server[0].get("started"):
        return False
    since = (server[0].get("state", {}).get("running") or {}).get("startedAt")
    logs = kubectl(ns, "logs", SERVER_POD, "-c", "server",
                   *(["--since-time=" + since] if since else []), check=False)
    return WAITING_LINE in logs.stdout


def wait_up(ns, deadline=READY_DEADLINE, sleep=time.sleep, clock=time.monotonic):
    """Wait for andara-0 to be Ready, or to be started and waiting for content: "ready" or
    "waiting". On dev's first switch, or a rebuild from nothing, the store has no pack with
    Zones, so Ready can't come before the seed that follows."""
    end = clock() + deadline
    while True:
        pod = kubectl(ns, "get", "pod", SERVER_POD, "-o", "json", check=False)
        if pod.returncode == 0 and pod.stdout.strip():
            st = json.loads(pod.stdout).get("status", {})
            ready = [c for c in st.get("conditions", []) if c.get("type") == "Ready"]
            if ready and ready[0].get("status") == "True":
                return "ready"
        if waiting_for_content(ns, kubectl):
            return "waiting"
        if clock() > end:
            raise StepFailed("%s neither Ready nor waiting for content within %ds" % (SERVER_POD, deadline))
        sleep(5)


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
