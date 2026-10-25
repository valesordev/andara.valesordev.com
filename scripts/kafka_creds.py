#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The credential path on the shared broker (AW-INF-056; ADR-0011 decision 6 as ADR-0013 decision
6 amends it).

The Strimzi User Operator writes each principal's Secret in the broker's namespace, andara-shared,
beside its KafkaUser. A workload mounts its own Secret in its own namespace, so the operator copies
each environment's principals there, and only there: `andara-server-dev` into andara-dev, never into
andara-local. The workloads' ServiceAccounts hold no RBAC on andara-shared's Secrets. This is the
only writer of those copies.

    scripts/kafka_creds.py copy <env>               make kafka-creds ENV=<env>: copy <env>'s
                                                    principals' passwords into andara-<env>; remove
                                                    a copy whose principal topics.yaml no longer
                                                    declares
    scripts/kafka_creds.py rotate <env> <principal> make kafka-creds-rotate ENV=<env>
                                                    PRINCIPAL=<principal>: a new password for
                                                    <principal>-<env>, then `copy <env>`. A platform
                                                    principal (andara-operator,
                                                    andara-schema-registry) takes no ENV and isn't
                                                    copied; the registry is restarted, since
                                                    Karapace reads its password at start.

A copy holds the password under `password` only, the file ANDARA_KAFKA_SASL_PASSWORD_FILE names;
the username is the principal's name, which the chart sets. Clients read the file per connection,
so a re-copy rotates a running workload without a restart (the kubelet refreshes the mounted
Secret within its sync period, about a minute). Rotation deletes the User Operator's Secret and
asks it to reconcile the KafkaUser, which writes a new password to the broker and a new Secret;
the old password is refused from then on.

Logs: `kafka-creds: <principal> -> <namespace> created|updated|unchanged|removed`, never a value.

Exit codes: 0 ok; 1 a Secret missing in andara-shared, or a kubectl step failed; 2 usage, an
unknown environment or principal; 3 the cluster or context missing.
"""

import datetime
import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import kafka_users  # noqa: E402
import kube  # noqa: E402

LABEL = "andara.valesor/kafka-creds"
REGISTRY = "andara-schema-registry"
ROTATED = "andara.valesor/rotated-at"
ROTATE_TIMEOUT = 180


class Failed(Exception):
    def __init__(self, msg, code=1):
        super().__init__(msg)
        self.code = code


def say(msg):
    print("kafka-creds: %s" % msg, flush=True)


def shared_ns():
    return os.environ.get("ANDARA_KAFKA_NAMESPACE", "andara-shared")


def env_ns(env):
    return "andara-" + env


def principals(env, doc=None):
    """The KafkaUser names of env's principals, from topics.yaml."""
    doc = doc or kafka_users.load()
    if env not in kafka_users.environments(doc):
        raise Failed("ENV=%s: not an environment on the shared broker (%s)"
                     % (env, ", ".join(sorted(kafka_users.environments(doc)))), 2)
    return ["%s-%s" % (p["name"], env) for p in doc.get("principals") or []]


def kubectl(ns, *args, check=True, input=None, run=subprocess.run):
    p = run(kube.kubectl("-n", ns, *args), capture_output=True, text=True, input=input)
    if check and p.returncode != 0:
        raise Failed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p


def password_of(name, run=subprocess.run):
    p = kubectl(shared_ns(), "get", "secret", name, "-o", "jsonpath={.data.password}", check=False, run=run)
    if p.returncode != 0 or not p.stdout.strip():
        raise Failed("no Secret %s/%s with a password: the User Operator writes it for the KafkaUser "
                     "`make shared-up` applies" % (shared_ns(), name))
    return p.stdout.strip()


def copy(env, run=subprocess.run, doc=None):
    names = principals(env, doc)
    ns = env_ns(env)
    for name in names:
        data = {"password": password_of(name, run)}
        cur = kubectl(ns, "get", "secret", name, "-o", "json", check=False, run=run)
        if cur.returncode == 0 and json.loads(cur.stdout).get("data") == data:
            say("%s -> %s unchanged" % (name, ns))
            continue
        doc_ = {"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
                "metadata": {"name": name, "namespace": ns, "labels": {LABEL: "true"}},
                "data": data}
        kubectl(ns, "apply", "-f", "-", input=json.dumps(doc_), run=run)
        say("%s -> %s %s" % (name, ns, "updated" if cur.returncode == 0 else "created"))
    live = kubectl(ns, "get", "secret", "-l", LABEL + "=true", "-o", "name", run=run).stdout.split()
    for res in sorted(live):
        name = res.split("/", 1)[1]
        if name not in names:
            kubectl(ns, "delete", res, run=run)
            say("%s -> %s removed: topics.yaml no longer declares it" % (name, ns))


def rotate(env, principal, run=subprocess.run, doc=None, sleep=time.sleep, now=time.monotonic):
    doc = doc or kafka_users.load()
    platform = [p["name"] for p in doc.get("platform_principals") or []]
    if principal in platform:
        if env:
            raise Failed("PRINCIPAL=%s is platform-wide; give no ENV" % principal, 2)
        name = principal
    else:
        names = principals(env, doc)
        name = principal if principal.endswith("-" + env) else "%s-%s" % (principal, env)
        if name not in names:
            raise Failed("PRINCIPAL=%s: %s has no principal %s (%s)" % (principal, env, name, ", ".join(names)), 2)
    old = password_of(name, run)
    kubectl(shared_ns(), "delete", "secret", name, run=run)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H%M%SZ")
    kubectl(shared_ns(), "annotate", "--overwrite", "kafkauser/" + name, "%s=%s" % (ROTATED, stamp), run=run)
    deadline = now() + ROTATE_TIMEOUT
    while True:
        p = kubectl(shared_ns(), "get", "secret", name, "-o", "jsonpath={.data.password}", check=False, run=run)
        if p.returncode == 0 and p.stdout.strip() and p.stdout.strip() != old:
            break
        if now() > deadline:
            raise Failed("the User Operator wrote no new Secret %s/%s within %ds"
                         % (shared_ns(), name, ROTATE_TIMEOUT))
        sleep(2)
    say("%s: new password written by the User Operator" % name)
    if principal not in platform:
        copy(env, run, doc)
    elif name == REGISTRY:
        kubectl(shared_ns(), "rollout", "restart", "deploy/" + REGISTRY, run=run)
        kubectl(shared_ns(), "rollout", "status", "deploy/" + REGISTRY, "--timeout=180s", run=run)
        say("%s: restarted on the new password" % REGISTRY)


def main(argv):
    try:
        if len(argv) == 2 and argv[0] == "copy":
            copy(argv[1])
        elif len(argv) == 3 and argv[0] == "rotate":
            rotate(argv[1], argv[2])
        else:
            sys.stderr.write("usage: scripts/kafka_creds.py copy <env> | rotate <env> <principal>\n")
            return 2
    except Failed as e:
        sys.stderr.write("make: kafka-creds: %s\n" % e)
        return e.code
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
