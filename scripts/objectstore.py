#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The in-cluster snapshot object store (AW-INF-025).

    scripts/objectstore.py install <env>   make objectstore-install ENV=<dev|prod>
    scripts/objectstore.py empty <env>     delete every object in the snapshot bucket (world-reset)

`install` is idempotent, in kafka-install's pattern:
  1. The Secret andara-snapshot-s3 (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY), generated the
     first time and never rewritten, so its resourceVersion holds across runs (AC-6).
  2. deploy/k8s/objectstore/objectstore.yaml applied: versitygw, its PVC, Service and
     NetworkPolicy. Waits for Ready, bounded at 120 s.
  3. The bucket andara-snapshots-<env>, made if missing.

Bucket operations run rclone in a one-shot pod in the namespace, labelled so the store's
NetworkPolicy admits it and taking the credential from the Secret by envFrom: the key never
passes through this process or its arguments.

Exit codes: 0 done; 1 a step failed, naming it; 2 usage; 3 a tool is missing.
"""

import json
import os
import secrets
import shutil
import string
import subprocess
import sys
import time

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFEST = os.path.join(REPO, "deploy", "k8s", "objectstore", "objectstore.yaml")
SECRET = "andara-snapshot-s3"
DEPLOYMENT = "deployment/andara-objectstore"
SERVICE = "andara-objectstore"
TOOLS_LABEL = "andara-objectstore-tools"
RCLONE = "rclone/rclone:1.75.1@sha256:45401ad7410db1d67ffdb58e19059ad20b0d8e0285a60e38bbec55cc1019c7a5"
READY_TIMEOUT = "120s"
TOOL_TIMEOUT = 180


class Failed(Exception):
    pass


def say(cmd, msg):
    print("objectstore-%s: %s" % (cmd, msg), flush=True)


def env_ns(env):
    if env in ("dev", "prod"):
        return "andara-" + env
    if env == "local":
        raise SystemExit(_usage("local's object store is compose's `minio` service"))
    raise SystemExit(_usage("usage: make objectstore-install ENV=<dev|prod>"))


def _usage(msg):
    print("make: objectstore: " + msg, file=sys.stderr)
    return 2


def bucket(env):
    return "andara-snapshots-" + env


def kubectl(ns, *args, check=True, input=None):
    p = subprocess.run(["kubectl", "-n", ns, *args], capture_output=True, text=True, input=input)
    if check and p.returncode != 0:
        raise Failed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p


def ensure_secret(ns):
    if kubectl(ns, "get", "secret", SECRET, "-o", "name", check=False).returncode == 0:
        return False
    alphabet = string.ascii_letters + string.digits
    access = "".join(secrets.choice(string.ascii_uppercase + string.digits) for _ in range(20))
    secret = "".join(secrets.choice(alphabet) for _ in range(40))
    doc = {
        "apiVersion": "v1", "kind": "Secret",
        "metadata": {"name": SECRET, "labels": {"app.kubernetes.io/name": "andara-objectstore"}},
        "type": "Opaque",
        "stringData": {"AWS_ACCESS_KEY_ID": access, "AWS_SECRET_ACCESS_KEY": secret},
    }
    kubectl(ns, "create", "-f", "-", input=json.dumps(doc))
    return True


def rclone(ns, *args):
    """Run rclone against the namespace's store in a one-shot pod; return its stdout."""
    name = "%s-%d" % (TOOLS_LABEL, int(time.time() * 1000) % 10**9)
    env = [
        {"name": "RCLONE_CONFIG_S3_TYPE", "value": "s3"},
        {"name": "RCLONE_CONFIG_S3_PROVIDER", "value": "Other"},
        {"name": "RCLONE_CONFIG_S3_ENV_AUTH", "value": "true"},
        {"name": "RCLONE_CONFIG_S3_ENDPOINT", "value": "http://%s:9000" % SERVICE},
        {"name": "RCLONE_CONFIG", "value": "/tmp/rclone.conf"},
        # Errors only: `kubectl logs` interleaves stderr, and a NOTICE line would read as output.
        {"name": "RCLONE_LOG_LEVEL", "value": "ERROR"},
        {"name": "HOME", "value": "/tmp"},
    ]
    pod = {
        "apiVersion": "v1", "kind": "Pod",
        "metadata": {"name": name, "labels": {"app.kubernetes.io/name": TOOLS_LABEL}},
        "spec": {
            "restartPolicy": "Never",
            "automountServiceAccountToken": False,
            "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001,
                                "seccompProfile": {"type": "RuntimeDefault"}},
            "containers": [{
                "name": "rclone", "image": RCLONE, "args": list(args),
                "env": env, "envFrom": [{"secretRef": {"name": SECRET}}],
                "securityContext": {"allowPrivilegeEscalation": False,
                                    "capabilities": {"drop": ["ALL"]}},
                "volumeMounts": [{"name": "tmp", "mountPath": "/tmp"}],
            }],
            "volumes": [{"name": "tmp", "emptyDir": {}}],
        },
    }
    kubectl(ns, "create", "-f", "-", input=json.dumps(pod))
    try:
        deadline = time.time() + TOOL_TIMEOUT
        while True:
            phase = kubectl(ns, "get", "pod", name, "-o", "jsonpath={.status.phase}", check=False).stdout
            if phase in ("Succeeded", "Failed"):
                break
            if time.time() > deadline:
                raise Failed("rclone %s: no result in %ds" % (" ".join(args), TOOL_TIMEOUT))
            time.sleep(1)
        logs = kubectl(ns, "logs", name, check=False).stdout
        if phase != "Succeeded":
            raise Failed("rclone %s: %s" % (" ".join(args), logs.strip()[-500:]))
        return logs
    finally:
        kubectl(ns, "delete", "pod", name, "--wait=false", check=False)


def install(env):
    ns = env_ns(env)
    if kubectl(ns, "get", "namespace", ns, check=False).returncode != 0:
        kubectl(ns, "create", "namespace", ns)
    made = ensure_secret(ns)
    say("install", "Secret %s %s" % (SECRET, "created" if made else "exists; unchanged"))
    kubectl(ns, "apply", "-f", MANIFEST)
    kubectl(ns, "rollout", "status", DEPLOYMENT, "--timeout=" + READY_TIMEOUT)
    say("install", "versitygw Ready at http://%s:9000" % SERVICE)
    rclone(ns, "mkdir", "s3:" + bucket(env))
    listed = rclone(ns, "lsf", "--dirs-only", "s3:")
    if bucket(env) + "/" not in listed.split():
        raise Failed("bucket %s not listed after mkdir: %r" % (bucket(env), listed))
    say("install", "bucket %s in %s ready" % (bucket(env), ns))


def empty(env):
    ns = env_ns(env)
    rclone(ns, "delete", "s3:" + bucket(env))
    left = rclone(ns, "lsf", "-R", "--files-only", "s3:" + bucket(env)).split()
    if left:
        raise Failed("bucket %s still holds %d object(s) after delete" % (bucket(env), len(left)))
    return bucket(env)


def main(argv):
    if len(argv) != 2 or argv[0] not in ("install", "empty"):
        return _usage("usage: objectstore.py <install|empty> <env>")
    if shutil.which("kubectl") is None:
        print("make: objectstore: kubectl not found", file=sys.stderr)
        return 3
    cmd, env = argv
    try:
        if cmd == "install":
            install(env)
        else:
            say("empty", "emptied %s" % empty(env))
    except Failed as e:
        print("make: objectstore-%s: %s" % (cmd, e), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
