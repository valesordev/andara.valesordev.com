#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Probe the shared broker's ACLs as each principal (AW-INF-056 AC-2, AC-3, AC-12, AC-13).

    scripts/kafka_acl_probe.py      make kafka-acl-probe

Every principal comes from deploy/kafka/topics.yaml, so a principal declared later is probed too.
Each probe runs rpk in the toolbox as that principal (its password from the User Operator's Secret,
on stdin) and reads the broker's answer:
  - any *_AUTHORIZATION_FAILED is `denied`;
  - success, GROUP_ID_NOT_FOUND or UNKNOWN_TOPIC is `allowed`, because the broker answers those only
    after authorization passes.
rpk's exit code is not used: some refusals exit 0.

What is probed, per environment <env> (the other is <o>):
  AC-2   andara-server-<env> produces to <env>.andara.commands.v1 and consumes it in a group under
         andara-sim-<env>; producing to <o>.andara.commands.v1 is denied, and so is a group under
         andara-sim-<o>, asked with DescribeGroups (a refused join makes rpk retry, not fail; Read
         implies Describe, so a refused Describe is a refused join).
  AC-3   every environment principal is denied CreateTopics, DeleteTopics and AlterConfigs on a name
         that doesn't exist, and DeleteGroups on another group; andara-projector-state-<env> may
         delete its own group and not <o>'s.
  AC-12  every declared principal but andara-projector-state-<env> (and the superuser) is denied
         producing to <env>.andara.state.v1; andara-projector-state-<env> is allowed.
  AC-13  andara-projector-state-<env> is denied producing to <env>.andara.content.* and
         <env>.andara.audit.v1, and reading audit; it may read content.*.

Nothing is left behind:
  - a create, delete or alter-config probe names a topic that doesn't exist;
  - a group probe uses a group under the granted prefix (`<group>-acl-probe`) and never the real
    group, and the operator deletes it afterwards;
  - the state-topic write is a tombstone for a key that never existed;
  - the commands-topic write goes to partition 0, which the operator then trims back to its end.
A probe of the projector deleting its own group is skipped, not run, when that group exists: deleting
it is what `andara-projector --rebuild` does.

Exit codes: 0 every probe answered as declared; 1 one didn't; 3 a Secret, kubectl or the cluster
missing.
"""

import json
import subprocess
import sys

import kafka_auth
import kafka_users
import topics

DENIED = "AUTHORIZATION_FAILED"
# A record printed by a consume is an allowed read, even when rpk in a group then fails to exit
# before the in-pod timeout ends it.
ALLOWED_ANSWERS = ("GROUP_ID_NOT_FOUND", "UNKNOWN_TOPIC", '"offset":')
POD_TIMEOUT = 30
TIMEOUT = 60
PROBE_KEY = "andara-acl-probe"


class Probe:
    def __init__(self, ac, principal, what, args, want, stdin="", cleanup=None):
        self.ac, self.principal, self.what, self.args, self.want = ac, principal, what, args, want
        self.stdin, self.cleanup = stdin, cleanup or []


def classify(code, text):
    if DENIED in text:
        return "denied"
    if code == 0 or any(a in text for a in ALLOWED_ANSWERS):
        return "allowed"
    return "error"


def probes(doc):
    envs = sorted(kafka_users.environments(doc).items())
    prefixes = dict(envs)
    env_principals = [p["name"] for p in doc.get("principals") or []]
    platform = [p["name"] for p in doc.get("platform_principals") or [] if not p.get("superuser")]
    content = [t["name"] for t in doc.get("topics") or [] if t["name"].startswith("andara.content.")]
    audit = [t["name"] for t in doc.get("topics") or [] if t["name"].split(".")[1:2] == ["audit"]]
    out = []
    for env, prefix in envs:
        other = next((e for e, _ in envs if e != env), None)
        server, proj = "andara-server-" + env, "andara-projector-state-" + env
        commands = "%s.andara.commands.v1" % prefix
        group = "andara-sim-%s-acl-probe" % env
        # AC-2
        out.append(Probe("AC-2", server, "produce " + commands,
                         ["topic", "produce", commands, "-p", "0", "-k", PROBE_KEY], "allowed", stdin="probe\n",
                         cleanup=[("trim", commands, 0)]))
        out.append(Probe("AC-2", server, "consume %s in group %s" % (commands, group),
                         ["topic", "consume", commands, "-g", group, "-o", "start", "-n", "1"], "allowed",
                         cleanup=[("group", group)]))
        if other:
            out.append(Probe("AC-2", server, "produce %s.andara.commands.v1" % prefixes[other],
                             ["topic", "produce", "%s.andara.commands.v1" % prefixes[other], "-p", "0", "-k", PROBE_KEY],
                             "denied", stdin="probe\n"))
            # A refused JoinGroup makes rpk retry until a timeout rather than fail, so the group is
            # asked with DescribeGroups: Read implies Describe, so a refused Describe is a refused join.
            out.append(Probe("AC-2", server, "join group andara-sim-%s-acl-probe (DescribeGroups)" % other,
                             ["group", "describe", "andara-sim-%s-acl-probe" % other], "denied"))
        # AC-3
        absent = "%s.andara.acl-probe-absent" % prefix
        for name in env_principals:
            principal = "%s-%s" % (name, env)
            out.append(Probe("AC-3", principal, "CreateTopics " + absent, ["topic", "create", absent], "denied"))
            out.append(Probe("AC-3", principal, "DeleteTopics " + absent, ["topic", "delete", absent], "denied"))
            out.append(Probe("AC-3", principal, "AlterConfigs " + absent,
                             ["topic", "alter-config", absent, "--set", "retention.ms=1"], "denied"))
            out.append(Probe("AC-3", principal, "DeleteGroups andara-sim-%s-acl-probe-absent" % env,
                             ["group", "delete", "andara-sim-%s-acl-probe-absent" % env], "denied"))
        out.append(Probe("AC-3", proj, "DeleteGroups its own " + proj, ["group", "delete", proj], "allowed",
                         cleanup=[("own-group", proj)]))
        if other:
            out.append(Probe("AC-3", proj, "DeleteGroups andara-projector-state-" + other,
                             ["group", "delete", "andara-projector-state-" + other], "denied"))
        # AC-12
        state = "%s.andara.state.v1" % prefix
        for principal in ["%s-%s" % (n, e) for n in env_principals for e, _ in envs] + platform:
            out.append(Probe("AC-12", principal, "produce a tombstone to " + state,
                             ["topic", "produce", state, "-k", PROBE_KEY, "--tombstone"],
                             "allowed" if principal == proj else "denied", stdin="\n"))
        # AC-13
        for t in content + audit:
            name = "%s.%s" % (prefix, t)
            out.append(Probe("AC-13", proj, "produce " + name, ["topic", "produce", name, "-k", PROBE_KEY],
                             "denied", stdin="probe\n"))
        for t in audit:
            name = "%s.%s" % (prefix, t)
            out.append(Probe("AC-13", proj, "read " + name, ["topic", "consume", name, "-o", ":end"], "denied"))
        for t in content:
            name = "%s.%s" % (prefix, t)
            out.append(Probe("AC-13", proj, "read " + name, ["topic", "consume", name, "-o", ":end"], "allowed"))
    return out


class Broker:
    """rpk in the toolbox, as any principal; passwords read once each."""

    def __init__(self, ns, run=subprocess.run):
        self.ns, self.run_, self.passwords = ns, run, {}

    def rpk(self, principal, args, stdin=""):
        if principal not in self.passwords:
            self.passwords[principal] = kafka_auth.operator_password(self.ns, run=self.run_, principal=principal)
        argv = kafka_auth.rpk_argv(self.ns, topics.CLUSTER_TOOLBOX, topics.CLUSTER_BOOTSTRAP, principal,
                                   pod_timeout=POD_TIMEOUT)
        try:
            p = self.run_(argv + list(args), input=self.passwords[principal] + "\n" + stdin,
                          capture_output=True, text=True, timeout=TIMEOUT)
        except subprocess.TimeoutExpired:
            return 124, "timed out after %ds" % TIMEOUT
        return p.returncode, (p.stdout or "") + (p.stderr or "")


def high_watermark(broker, topic, partition):
    code, text = broker.rpk(kafka_auth.OPERATOR, ["topic", "describe", topic, "-p", "--format", "json"])
    if code != 0:
        return None
    try:
        rows = json.loads(text)
    except ValueError:
        return None
    # [{"summary": …, "configs": […], "partitions": [{"partition": 0, "high_watermark": 1, …}]}]
    parts = (rows[0] if isinstance(rows, list) and rows else {}).get("partitions") or []
    for r in parts:
        if r.get("partition") == partition and "high_watermark" in r:
            return int(r["high_watermark"])
    return None


def group_exists(broker, group):
    code, text = broker.rpk(kafka_auth.OPERATOR, ["group", "describe", group])
    return code == 0 and "GROUP_ID_NOT_FOUND" not in text and "Dead" not in text


def clean(broker, step, out):
    kind = step[0]
    if kind == "trim":
        _, topic, partition = step
        hwm = high_watermark(broker, topic, partition)
        if hwm is None:
            return "could not read %s/%d's high watermark to trim it" % (topic, partition)
        code, text = broker.rpk(kafka_auth.OPERATOR, ["topic", "trim-prefix", topic, "-p", str(partition),
                                                      "-o", str(hwm), "--no-confirm"])
        if code != 0 or DENIED in text:
            return "trimming %s/%d to %d failed: %s" % (topic, partition, hwm, text.strip()[:200])
        out.write("kafka-acl-probe: cleaned: %s partition %d trimmed to %d\n" % (topic, partition, hwm))
    elif kind == "group":
        code, text = broker.rpk(kafka_auth.OPERATOR, ["group", "delete", step[1]])
        if code != 0 and "GROUP_ID_NOT_FOUND" not in text:
            return "deleting probe group %s failed: %s" % (step[1], text.strip()[:200])
        out.write("kafka-acl-probe: cleaned: group %s deleted\n" % step[1])
    return None


def main(argv=None, broker=None, doc=None, out=sys.stdout, err=sys.stderr):
    doc = doc or kafka_users.load()
    broker = broker or Broker(topics.kafka_namespace())
    failed, leftovers, cleanups = [], [], []
    for p in probes(doc):
        if any(c[0] == "own-group" for c in p.cleanup) and group_exists(broker, p.principal):
            out.write("kafka-acl-probe: %s %s %s: skipped, the group exists and deleting it is a rebuild\n"
                      % (p.ac, p.principal, p.what))
            continue
        code, text = broker.rpk(p.principal, p.args, p.stdin)
        got = classify(code, text)
        verdict = "ok" if got == p.want else "FAIL"
        out.write("kafka-acl-probe: %s %s %s: %s (want %s) %s\n" % (p.ac, p.principal, p.what, got, p.want, verdict))
        if verdict == "FAIL":
            failed.append("%s %s %s: %s, want %s: %s" % (p.ac, p.principal, p.what, got, p.want,
                                                          " ".join(text.split())[:200]))
        cleanups += [s for s in p.cleanup if s[0] != "own-group" and s not in cleanups]
    # After every probe: the consume probe reads the record the produce probe wrote.
    for step in cleanups:
        problem = clean(broker, step, out)
        if problem:
            leftovers.append(problem)
    for f in failed:
        err.write("make: kafka-acl-probe: %s\n" % f)
    for left in leftovers:
        err.write("make: kafka-acl-probe: left behind: %s\n" % left)
    if failed or leftovers:
        err.write("make: kafka-acl-probe: %d probe(s) answered other than declared, %d cleanup(s) failed\n"
                  % (len(failed), len(leftovers)))
        return 1
    out.write("kafka-acl-probe: every probe answered as topics.yaml declares\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
