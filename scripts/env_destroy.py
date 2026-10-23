#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make env-destroy ENV=<env> CONFIRM=andara-<env> — delete an environment's namespace (AW-INF-021).

The first step of AW-INF-021 AC-7's rebuild from nothing. It deletes andara-<env> and waits until
it's gone: the server, the projector, the object store and its bucket, every Secret and PVC. Then,
on the shared Kafka in andara-shared, the environment's own topics (every declared topic under its
prefix, `dev.andara.…`) and its reserved consumer groups (`andara-sim-dev`, …), so the rebuild
starts from nothing, as it did when the Kafka lived in the namespace (AW-INF-055). The other
environment's topics are never named. The cluster-wide pieces stay: Argo CD, Strimzi's operator,
cert-manager, Traefik and the shared Kafka itself. So the environment comes back with its install
targets, `make kafka-install ENV=<env>` recreating its topics.

Before the namespace, it removes the Argo CD Application without cascading. With automated sync
and selfHeal, Argo CD would otherwise recreate the namespace's objects while it's being deleted.
`make argocd-install ENV=<env>` creates the Application again.

Steps print as `env-destroy: <step>`. The last line is `env-destroy: andara-<env> deleted`.
Idempotent: a namespace that's already gone exits 0 with that line.

Exit: 0 deleted (or already gone) · 1 a step failed, naming it · 2 usage or refusal (prod, local,
a missing CONFIRM, or a CONFIRM that isn't the namespace).
"""
import json
import subprocess
import sys
import time

import kube
import topics

ARGO_NS = "argocd"
DELETE_DEADLINE = 600
TOPIC_DELETE_DEADLINE = 120


def say(msg):
    print("env-destroy: " + msg, flush=True)


def refuse(msg):
    print("env-destroy: " + msg, file=sys.stderr)
    sys.exit(2)


class StepFailed(Exception):
    pass


def kubectl(*args, check=True):
    p = subprocess.run(kube.kubectl(*args), capture_output=True, text=True)
    if check and p.returncode != 0:
        raise StepFailed("kubectl %s: %s" % (" ".join(args[:4]), (p.stderr or p.stdout).strip()))
    return p


def check_args(argv):
    if len(argv) != 2 or not argv[0]:
        refuse("usage: make env-destroy ENV=<env> CONFIRM=andara-<env>")
    env, confirm = argv
    if env == "prod":
        refuse("prod is never destroyed")
    if env == "local":
        refuse("local has no namespace to destroy; its stack is `make down VOLUMES=1`")
    ns = "andara-" + env
    if not confirm:
        refuse("CONFIRM is missing; it must be the namespace, %s" % ns)
    if confirm != ns:
        refuse("CONFIRM=%s is not the namespace %s" % (confirm, ns))
    return env, ns


def remove_application(ns):
    """Remove the Application without cascading. Only a confirmed NotFound counts as none: an
    unreadable Application (RBAC, a timeout) stops here, before anything is deleted."""
    p = kubectl("-n", ARGO_NS, "get", "application", ns, "-o", "json", check=False)
    if p.returncode != 0:
        if "NotFound" in p.stderr or "not found" in p.stderr:
            say("no Argo CD application %s" % ns)
            return
        raise StepFailed("read the Argo CD application %s (only NotFound counts as none): %s"
                         % (ns, p.stderr.strip()))
    kubectl("-n", ARGO_NS, "patch", "application", ns, "--type", "json", "-p",
            json.dumps([{"op": "remove", "path": "/spec/syncPolicy"}]), check=False)
    kubectl("-n", ARGO_NS, "patch", "application", ns, "--type", "merge", "-p",
            json.dumps({"metadata": {"finalizers": None}}))
    kubectl("-n", ARGO_NS, "delete", "application", ns, "--wait=true")
    say("removed the Argo CD application %s without cascading" % ns)


def delete_namespace(ns, deadline=DELETE_DEADLINE, sleep=time.sleep, clock=time.monotonic):
    if kubectl("get", "namespace", ns, check=False).returncode != 0:
        say("%s doesn't exist; nothing to delete" % ns)
        return
    kubectl("delete", "namespace", ns, "--wait=false")
    say("deleting %s; waiting until it's gone" % ns)
    end = clock() + deadline
    while kubectl("get", "namespace", ns, check=False).returncode == 0:
        if clock() > end:
            raise StepFailed("%s still exists after %ds; see `kubectl --context %s get namespace %s -o yaml`"
                             % (ns, deadline, kube.context(), ns))
        sleep(2)


def delete_topics(env, run=None, decl=None, sleep=time.sleep, clock=time.monotonic):
    """The environment's declared topics and reserved groups on the shared broker. Idempotent:
    what's already gone is skipped. Never an unprefixed name, which could be another's."""
    decl = decl or topics.DECL
    prefix = topics.environment(decl, env)["prefix"]
    if not prefix:
        raise StepFailed("environment %s has no topic prefix; refusing to delete unprefixed topics" % env)
    run = run or topics.rpk_runner(env, "cluster")
    names = [topics.topic_name(prefix, t["name"]) for t in topics.parse_declaration(decl)[1]]
    live = topics.existing_topics(run)
    gone = [n for n in names if n in live]
    for name in gone:
        res = run(["topic", "delete", name])
        if res.returncode != 0:
            raise StepFailed("delete %s on the shared broker: %s" % (name, (res.stderr or res.stdout).strip()))
    # Deletion is asynchronous: wait until they're gone, so a `kafka-install` that follows
    # creates them afresh rather than finding them pending deletion.
    end = clock() + TOPIC_DELETE_DEADLINE
    while set(gone) & set(topics.existing_topics(run)):
        if clock() > end:
            raise StepFailed("%s's topics still listed %ds after delete" % (env, TOPIC_DELETE_DEADLINE))
        sleep(2)
    for group in topics.consumer_groups(env, decl):
        res = run(["group", "delete", group])
        out = (res.stderr + res.stdout).lower()
        if res.returncode != 0 and "not found" not in out and "not_found" not in out \
                and "does not exist" not in out:
            raise StepFailed("delete consumer group %s: %s" % (group, (res.stderr or res.stdout).strip()))
    say("deleted %s's %d topic(s) and its consumer groups on the shared broker (%s.*)"
        % (env, len(gone), prefix))


def main(argv):
    env, ns = check_args(argv)
    try:
        remove_application(ns)
        delete_namespace(ns)
        delete_topics(env)
    except StepFailed as e:
        print("env-destroy: %s" % e, file=sys.stderr)
        return 1
    say("%s deleted" % ns)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
