#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make local-up | local-down | local-reset` — andara-local on the box's cluster (AW-INF-060).

    scripts/local.py up <env> <profile>
    scripts/local.py down <env>
    scripts/local.py reset <env>

`<env>` and `<profile>` are what the command line gave, empty when it gave none: these targets act
on local only, and local-up installs local's own values, never CI's overlay.

up     Each step idempotent, in order (ADR-0013 decision 8): cluster-up, platform-up, shared-up,
       topics-apply ENV=local BROKER=cluster, kafka-creds ENV=local (shared-up copies no
       credential, and without the Secret andara-0 holds at FailedMount), image, kind-load,
       helm-install ENV=local. Then whether the collectors have a telemetry credential.
down   andara-local's Helm release only. The namespace, its label and its Secrets stay: shared-up
       and kafka-creds own them, and local-up puts the release back.
reset  The plan first, checked before anything changes: every live topic under `local.`, local's
       reserved consumer groups and the bucket andara-snapshots-local. A name outside local's
       prefix stops the run with nothing deleted. Then down, the topics (waited on until gone),
       the groups, and the bucket emptied. `make local-up` recreates the topics and the World.

Each line is `local: <step> run=<id> ...`, one run ID per invocation.

Exit: 0 ok · 1 a step failed or a refusal (ENV other than local, a PROFILE, a name outside local's
prefix) · 2 usage · 3 the cluster context or a credential is missing.
"""
import os
import re
import secrets
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import kube  # noqa: E402
import objectstore  # noqa: E402
import topics  # noqa: E402

ENV = "local"
NS = "andara-local"
RELEASE = "andara"
STEPS = [
    ("cluster-up", []),
    ("platform-up", []),
    ("shared-up", []),
    ("topics-apply", ["ENV=local", "BROKER=cluster"]),
    ("kafka-creds", ["ENV=local"]),
    ("image", []),
    ("kind-load", []),
    ("helm-install", ["ENV=local"]),
]
TELEMETRY = ("observability", "grafana-telemetry", "token")
TOPIC_DELETE_DEADLINE = 120
# GNU make reports a failed recipe as its own exit 2; the recipe's code is on this line.
MAKE_ERROR = re.compile(r"^make(?:\[\d+\])?: \*\*\* \[[^\]]*\] Error (\d+)\s*$")


class Failed(Exception):
    code = 1


class Missing(Exception):
    code = 3


class Log:
    def __init__(self, run_id=None, out=None):
        self.run = run_id or time.strftime("%Y%m%dT%H%M%S") + "-" + secrets.token_hex(2)
        self.out = out or sys.stdout

    def __call__(self, step, msg=""):
        print(("local: %s run=%s %s" % (step, self.run, msg)).rstrip(), file=self.out, flush=True)


def make_step(target, args, make=None):
    """Run one target as a sub-make, its output straight through. Returns the recipe's own exit code."""
    make = make or [os.environ.get("MAKE", "make")]
    code = None
    with subprocess.Popen([*make, "--no-print-directory", "-s", target, *args],
                          stderr=subprocess.PIPE, text=True) as p:
        for line in p.stderr:
            sys.stderr.write(line)
            m = MAKE_ERROR.match(line)
            if m:
                code = int(m.group(1))
    rc = p.returncode
    return 0 if rc == 0 else (code or rc)


def check_env(env, cmd):
    if env and env != ENV:
        raise Failed("local-%s acts on andara-local only, not ENV=%s; %s" % (
            cmd, env, "`make world-reset ENV=%s CONFIRM=andara-%s` starts its World over" % (env, env)
            if cmd == "reset" else "dev is installed by Argo CD (`make promote`)"))


def telemetry(kubectl):
    """Whether the collectors' Secret has a token. Its value is never read into a line."""
    ns, name, key = TELEMETRY
    p = kubectl("-n", ns, "get", "secret", name, "-o", "jsonpath={.data.%s}" % key)
    return p.returncode == 0 and bool(p.stdout.strip())


def kubectl_run(*args):
    return subprocess.run(kube.kubectl(*args), capture_output=True, text=True)


def helm_run(*args):
    return subprocess.run(kube.helm(*args), capture_output=True, text=True)


def up(log, step=make_step, kubectl=kubectl_run):
    for target, args in STEPS:
        log(target, " ".join(args))
        code = step(target, args)
        if code:
            e = (Missing if code == 3 else Failed)("`make %s` exited %d" % (" ".join([target, *args]), code))
            raise e
    if telemetry(kubectl):
        log("collectors", "the collectors have a telemetry credential (%s/%s)" % TELEMETRY[:2])
    else:
        log("collectors", "the collectors have no telemetry credential: set GRAFANA_SOLO7DEV_TELEMETRY_TOKEN in "
            ".local/box.env and run `make platform-up`; local's series reach no backend until then")
    log("up", "%s is serving" % NS)


def down(log, helm=helm_run):
    log("down", "removing the release %s from %s; the namespace and its Secrets stay" % (RELEASE, NS))
    p = helm("status", RELEASE, "-n", NS)
    if p.returncode != 0:
        if "not found" in (p.stderr + p.stdout).lower():
            log("down", "%s has no release %s; nothing to remove" % (NS, RELEASE))
            return
        raise Failed("helm status %s -n %s: %s" % (RELEASE, NS, (p.stderr or p.stdout).strip()))
    p = helm("uninstall", RELEASE, "-n", NS, "--wait", "--timeout", "5m")
    if p.returncode != 0:
        raise Failed("helm uninstall %s -n %s: %s" % (RELEASE, NS, (p.stderr or p.stdout).strip()))
    log("down", "removed %s from %s; `make local-up` brings it back" % (RELEASE, NS))


def plan(run, decl=topics.DECL, bucket=None):
    """(topics, groups, bucket) reset deletes, each checked against local's prefix before anything is."""
    # An empty or other prefix would select another environment's topics (prod's are unprefixed).
    prefix = topics.environment(decl, ENV)["prefix"]
    if prefix != ENV:
        raise Failed("topics.yaml gives local the prefix %r, not %r; nothing deleted" % (prefix, ENV))
    names = sorted(n for n in topics.existing_topics(run) if n.startswith(prefix + "."))
    groups = topics.consumer_groups(ENV, decl)
    bucket = bucket or objectstore.bucket(ENV)
    for group in groups:
        if not group.endswith("-" + ENV):
            raise Failed("consumer group %s is not local's (-%s); nothing deleted" % (group, ENV))
    if bucket != "andara-snapshots-" + ENV:
        raise Failed("bucket %s is not andara-snapshots-%s; nothing deleted" % (bucket, ENV))
    return names, groups, bucket


def delete_topics(log, run, names, sleep=time.sleep, clock=time.monotonic):
    for name in names:
        res = run(["topic", "delete", name])
        if res.returncode != 0:
            raise Failed("delete topic %s: %s" % (name, (res.stderr or res.stdout).strip()))
        log("reset", "deleted topic %s" % name)
    end = clock() + TOPIC_DELETE_DEADLINE
    while set(names) & set(topics.existing_topics(run)):
        if clock() > end:
            raise Failed("local's topics still listed %ds after delete" % TOPIC_DELETE_DEADLINE)
        sleep(2)


def delete_groups(log, run, groups):
    for group in groups:
        res = run(["group", "delete", group])
        out = (res.stderr + res.stdout).lower()
        if res.returncode != 0 and not any(s in out for s in ("not found", "not_found", "does not exist")):
            raise Failed("delete consumer group %s: %s" % (group, (res.stderr or res.stdout).strip()))
        log("reset", "deleted consumer group %s" % group)


def reset(log, run=None, helm=helm_run, empty=objectstore.empty, decl=topics.DECL, bucket=None):
    run = run or topics.rpk_runner(ENV, "cluster")
    names, groups, bucket = plan(run, decl, bucket)
    log("reset", "plan: %d topic(s) under local., %d consumer group(s), bucket %s" % (len(names), len(groups), bucket))
    down(log, helm)
    delete_topics(log, run, names)
    delete_groups(log, run, groups)
    empty(ENV)
    log("reset", "emptied bucket %s" % bucket)
    log("reset", "andara-local is reset; `make local-up` recreates its topics and World")


def main(argv, log=None):
    cmds = {"up": 3, "down": 2, "reset": 2}
    if not argv or argv[0] not in cmds or len(argv) != cmds[argv[0]]:
        print("usage: local.py up <env> <profile> | down <env> | reset <env>", file=sys.stderr)
        return 2
    cmd, env = argv[0], argv[1]
    log = log or Log()
    try:
        check_env(env, cmd)
        if cmd == "up":
            if argv[2]:
                raise Failed("local-up installs local's own values and takes no PROFILE; CI's overlay is "
                             "`make helm-install ENV=local PROFILE=ci`")
            up(log)
        elif cmd == "down":
            down(log)
        else:
            reset(log)
    except (Failed, Missing) as e:
        print("make: local-%s: run=%s %s" % (cmd, log.run, e), file=sys.stderr)
        return e.code
    except (kube.Missing, objectstore.Missing) as e:
        print("make: local-%s: run=%s %s" % (cmd, log.run, e), file=sys.stderr)
        return 3
    except objectstore.Failed as e:
        print("make: local-%s: run=%s %s" % (cmd, log.run, e), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
