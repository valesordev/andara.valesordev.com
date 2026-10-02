#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make env-destroy ENV=<env> CONFIRM=andara-<env> — delete an environment's namespace (AW-INF-021).

The first step of AW-INF-021 AC-7's rebuild from nothing. It deletes andara-<env> and waits until
it's gone: the server, the projector, Kafka (Strimzi's resources, and so every topic), the object
store and its bucket, every Secret and PVC. The cluster-wide pieces stay: Argo CD, Strimzi's
operator, cert-manager and Traefik. So the environment comes back with its install targets.

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

ARGO_NS = "argocd"
DELETE_DEADLINE = 600


def say(msg):
    print("env-destroy: " + msg, flush=True)


def refuse(msg):
    print("env-destroy: " + msg, file=sys.stderr)
    sys.exit(2)


class StepFailed(Exception):
    pass


def kubectl(*args, check=True):
    p = subprocess.run(["kubectl", *args], capture_output=True, text=True)
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
    kubectl("-n", ARGO_NS, "delete", "imageupdater", ns, "--ignore-not-found", check=False)
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
            raise StepFailed("%s still exists after %ds; see `kubectl get namespace %s -o yaml`"
                             % (ns, deadline, ns))
        sleep(2)


def main(argv):
    env, ns = check_args(argv)
    try:
        remove_application(ns)
        delete_namespace(ns)
    except StepFailed as e:
        print("env-destroy: %s" % e, file=sys.stderr)
        return 1
    say("%s deleted" % ns)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
