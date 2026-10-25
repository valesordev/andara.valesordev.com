#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Probe the shared broker's ACLs as each principal (AW-INF-056 AC-2, AC-3, AC-12, AC-13).

    scripts/kafka_acl_probe.py      make kafka-acl-probe

Every principal comes from deploy/kafka/topics.yaml, so a principal declared later is probed too.
Each probe runs rpk in the toolbox as that principal (its password from the User Operator's Secret,
on stdin) and reads the broker's answer:
  - any *_AUTHORIZATION_FAILED is `denied`;
  - success, MESSAGE_TOO_LARGE, GROUP_ID_NOT_FOUND or UNKNOWN_TOPIC is `allowed`, because the broker
    answers those only after authorization passes.
rpk's exit code is not used: some refusals exit 0.

**It never writes a record.** The topics it probes are the World's: the command log is its WAL,
kept forever, and any record a reader can't decode stalls a partition. So every produce probe sends
one record larger than its topic's max.message.bytes (from topics.yaml; the broker's default when
none is declared). The broker authorizes a Produce before it sizes it: an authorized principal gets
MESSAGE_TOO_LARGE and nothing is appended, a refused one TOPIC_AUTHORIZATION_FAILED.

What is probed, per environment <env> (the other is <o>):
  AC-2   andara-server-<env> may produce to <env>.andara.commands.v1 and commit offsets on it in a
         group under andara-sim-<env> (`rpk group seek`: OffsetCommit needs Read on the group and the
         topic, as a join does); producing to <o>.andara.commands.v1 and a group under
         andara-sim-<o> are denied.
  AC-3   every environment principal is denied CreateTopics, DeleteTopics and AlterConfigs on a name
         that doesn't exist, and DeleteGroups on another group; andara-projector-state-<env> may
         delete its own group and not <o>'s.
  AC-12  every declared principal but andara-projector-state-<env> (and the superuser) is denied
         producing to <env>.andara.state.v1; andara-projector-state-<env> is allowed.
  AC-13  andara-projector-state-<env> is denied producing to <env>.andara.content.* and
         <env>.andara.audit.v1, and reading audit; it may read content.*.

Nothing is left behind:
  - a create, delete or alter-config probe names a topic that doesn't exist;
  - a group probe uses a group under the granted prefix (`<group>-acl-probe`), never the real one,
    and the operator deletes it afterwards;
  - the projector deleting its own group runs only when the operator has seen that group absent;
    when it exists (deleting it is `andara-projector --rebuild`) the probe is skipped, and when the
    operator can't tell, the run fails without deleting it.

Exit codes: 0 every probe answered as declared; 1 one didn't, or a cleanup failed; 3 a Secret,
kubectl or the cluster missing.
"""

import base64
import os
import subprocess
import sys

import kafka_auth
import kafka_users
import topics

DENIED = "AUTHORIZATION_FAILED"
ALLOWED_ANSWERS = ("MESSAGE_TOO_LARGE", "GROUP_ID_NOT_FOUND", "UNKNOWN_TOPIC")
POD_TIMEOUT = 60
TIMEOUT = 90
PROBE_KEY = "andara-acl-probe"
# Kafka's message.max.bytes default, which applies to a topic that declares no max.message.bytes.
BROKER_MAX_MESSAGE_BYTES = 1048588
OVERSIZE = 1 << 20


class Probe:
    def __init__(self, ac, principal, what, args, want, size=0, cleanup=None):
        self.ac, self.principal, self.what, self.args, self.want = ac, principal, what, args, want
        self.size, self.cleanup = size, cleanup or []


def classify(code, text):
    if DENIED in text:
        return "denied"
    if code == 0 or any(a in text for a in ALLOWED_ANSWERS):
        return "allowed"
    return "error"


def max_message_bytes(doc):
    """{topic name (unprefixed): its max.message.bytes} from the declaration."""
    out = {}
    for t in doc.get("topics") or []:
        v = t.get("max.message.bytes")  # a topic's settings are its own keys in topics.yaml
        out[t["name"]] = int(v) if v is not None else BROKER_MAX_MESSAGE_BYTES
    return out


def produce(ac, principal, prefix, topic, limits, want):
    """A produce probe: one record OVERSIZE past the topic's limit, so nothing is ever appended."""
    size = limits.get(topic, BROKER_MAX_MESSAGE_BYTES) + OVERSIZE
    name = "%s.%s" % (prefix, topic)
    return Probe(ac, principal, "produce " + name,
                 ["topic", "produce", name, "-k", PROBE_KEY, "--compression", "none",
                  "--max-message-bytes", str(2 * size)], want, size=size)


def probes(doc):
    envs = sorted(kafka_users.environments(doc).items())
    prefixes = dict(envs)
    limits = max_message_bytes(doc)
    env_principals = [p["name"] for p in doc.get("principals") or []]
    platform = [p["name"] for p in doc.get("platform_principals") or [] if not p.get("superuser")]
    content = [t["name"] for t in doc.get("topics") or [] if t["name"].startswith("andara.content.")]
    audit = [t["name"] for t in doc.get("topics") or [] if t["name"].split(".")[1:2] == ["audit"]]
    commands = next(t["name"] for t in doc.get("topics") or [] if t["name"].split(".")[1:2] == ["commands"])
    state = next(t["name"] for t in doc.get("topics") or [] if t["name"].split(".")[1:2] == ["state"])
    out = []
    for env, prefix in envs:
        other = next((e for e, _ in envs if e != env), None)
        server, proj = "andara-server-" + env, "andara-projector-state-" + env
        # AC-2
        out.append(produce("AC-2", server, prefix, commands, limits, "allowed"))
        group = "andara-sim-%s-acl-probe" % env
        out.append(Probe("AC-2", server, "commit offsets on %s.%s in group %s" % (prefix, commands, group),
                         ["group", "seek", group, "--to", "start", "--topics", "%s.%s" % (prefix, commands),
                          "--allow-new-topics"], "allowed", cleanup=[("group", group)]))
        if other:
            out.append(produce("AC-2", server, prefixes[other], commands, limits, "denied"))
            ogroup = "andara-sim-%s-acl-probe" % other
            out.append(Probe("AC-2", server, "commit offsets on %s.%s in group %s" % (prefix, commands, ogroup),
                             ["group", "seek", ogroup, "--to", "start", "--topics", "%s.%s" % (prefix, commands),
                              "--allow-new-topics"], "denied", cleanup=[("group", ogroup)]))
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
        for principal in ["%s-%s" % (n, e) for n in env_principals for e, _ in envs] + platform:
            p = produce("AC-12", principal, prefix, state, limits, "allowed" if principal == proj else "denied")
            out.append(p)
        # AC-13
        for t in content + audit:
            out.append(produce("AC-13", proj, prefix, t, limits, "denied"))
        for t in audit:
            name = "%s.%s" % (prefix, t)
            out.append(Probe("AC-13", proj, "read " + name, ["topic", "consume", name, "-o", ":end"], "denied"))
        for t in content:
            name = "%s.%s" % (prefix, t)
            out.append(Probe("AC-13", proj, "read " + name, ["topic", "consume", name, "-o", ":end"], "allowed"))
    return out


def payload(size):
    """size bytes of base64 text from random bytes: incompressible, so the broker sees all of it."""
    return base64.b64encode(os.urandom(size * 3 // 4 + 3)).decode()[:size]


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


def group_state(broker, group):
    """'absent', 'exists' or 'unknown', as the operator sees it. Only 'absent' lets the projector's
    own-group probe run: a group with no members loses its offsets to a DeleteGroups."""
    code, text = broker.rpk(kafka_auth.OPERATOR, ["group", "describe", group])
    if DENIED in text:
        return "unknown"
    if "GROUP_ID_NOT_FOUND" in text:
        return "absent"
    states = [line.split()[1] for line in text.splitlines() if line.startswith("STATE") and len(line.split()) > 1]
    if code == 0 and states and states[0] != "Dead":
        return "exists"
    return "unknown"


def clean(broker, step, out):
    if step[0] == "group":
        code, text = broker.rpk(kafka_auth.OPERATOR, ["group", "delete", step[1]])
        if code != 0 and "GROUP_ID_NOT_FOUND" not in text or DENIED in text:
            return "deleting probe group %s failed: %s" % (step[1], " ".join(text.split())[:200])
        out.write("kafka-acl-probe: cleaned: group %s deleted\n" % step[1])
    return None


def main(argv=None, broker=None, doc=None, out=sys.stdout, err=sys.stderr):
    doc = doc or kafka_users.load()
    broker = broker or Broker(topics.kafka_namespace())
    failed, leftovers, cleanups = [], [], []
    for p in probes(doc):
        if any(c[0] == "own-group" for c in p.cleanup):
            state = group_state(broker, p.principal)
            if state == "exists":
                out.write("kafka-acl-probe: %s %s %s: skipped, the group exists and deleting it is a rebuild\n"
                          % (p.ac, p.principal, p.what))
                continue
            if state == "unknown":
                out.write("kafka-acl-probe: %s %s %s: not run, the operator couldn't tell whether the group "
                          "exists FAIL\n" % (p.ac, p.principal, p.what))
                failed.append("%s %s %s: not run: the operator couldn't tell whether %s exists"
                              % (p.ac, p.principal, p.what, p.principal))
                continue
        code, text = broker.rpk(p.principal, p.args, payload(p.size) + "\n" if p.size else "")
        got = classify(code, text)
        verdict = "ok" if got == p.want else "FAIL"
        out.write("kafka-acl-probe: %s %s %s: %s (want %s) %s\n" % (p.ac, p.principal, p.what, got, p.want, verdict))
        if verdict == "FAIL":
            failed.append("%s %s %s: %s, want %s: %s" % (p.ac, p.principal, p.what, got, p.want,
                                                          " ".join(text.split())[:200]))
        cleanups += [s for s in p.cleanup if s[0] != "own-group" and s not in cleanups]
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
    out.write("kafka-acl-probe: every probe answered as topics.yaml declares, and wrote no record\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
