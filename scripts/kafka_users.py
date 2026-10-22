#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Render and check the Strimzi KafkaUsers declared in deploy/kafka/topics.yaml (AW-INF-055).

    scripts/kafka_users.py render    make kafka-users: the manifests, as YAML, on stdout
    scripts/kafka_users.py check     make kafka-users-check, in `make check` (AC-9)

One KafkaUser `<principal>-<env>` for every principal in `principals` and every environment
with a prefix (ADR-0011 decision 4, as ADR-0013 decision 6 amends it): `andara-server-dev`,
`andara-projector-state-local`. Its topic ACLs are LITERAL on `<prefix>.<topic>`, its group
ACLs PREFIXED on `<group>-<env>`, and an environment's `quotas` are on each of its principals.
They are rendered and checked here, and applied by AW-INF-056 once the broker has an
authorizer: Strimzi's User Operator writes ACLs through the Admin API, which a broker without
one refuses (ADR-0013 amendment 19).

`check` renders them and fails, naming the KafkaUser and the ACL, when:
  - a principal holds Create, Alter, AlterConfigs, Delete or All on a topic, or anything but
    Describe on the cluster: topics are the operator's alone (ADR-0011). The one exception is
    the integration suite's `andara-it` (AW-INF-060), which may hold Create and Delete on the
    PREFIXED topics `andara.test.` and `it` (ADR-0013 amendment 17); the exception is by that
    exact name, so no other principal can borrow it. `andara-it` is scoped positively too: its
    topics are those two PREFIXED names only and its groups PREFIXED `it-` only (Delete
    included), so it never reaches an environment's topics or groups;
  - a principal holds Delete on a group other than andara-projector-state's own
    (`andara-projector --rebuild` deletes it);
  - a topic or group ACL names a literal `*`, or reaches outside its environment: a topic not
    under `<prefix>.`, or a group not ending `-<env>`;
  - a declared principal has no KafkaUser in some environment, or one environment's
    principals carry another's quotas (dev's have none);
  - a principal's topic is not one the declaration's `topics` names.

Exit codes: 0 ok; 1 a check failed; 2 usage.
"""

import os
import sys

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DECL = os.path.join(REPO, "deploy", "kafka", "topics.yaml")
CLUSTER = "andara-log"
API = "kafka.strimzi.io/v1"
ENV_LABEL = "andara.valesor/env"

TOPIC_FORBIDDEN = {"Create", "Alter", "AlterConfigs", "Delete", "All"}
CLUSTER_ALLOWED = {"Describe"}
IT_PRINCIPAL = "andara-it"
IT_PREFIXES = {"andara.test.", "it"}
IT_ALLOWED = {"Create", "Delete"}
IT_GROUP_PREFIX = "it-"
GROUP_DELETER = "andara-projector-state"


def load(path=DECL):
    with open(path) as f:
        return yaml.safe_load(f)


def environments(doc):
    """{env: prefix} for every environment with a prefix: the ones the shared broker serves."""
    return {env: str(e.get("prefix") or "") for env, e in (doc.get("environments") or {}).items()
            if e.get("prefix")}


def render(doc):
    users = []
    quotas = doc.get("quotas") or {}
    for env, prefix in environments(doc).items():
        for p in doc.get("principals") or []:
            acls = [{"resource": {"type": "topic", "name": "%s.%s" % (prefix, topic), "patternType": "literal"},
                     "operations": list(ops)}
                    for topic, ops in (p.get("topics") or {}).items()]
            acls += [{"resource": {"type": "group", "name": "%s-%s" % (group, env), "patternType": "prefix"},
                      "operations": list(ops)}
                     for group, ops in (p.get("groups") or {}).items()]
            if p.get("cluster"):
                acls.append({"resource": {"type": "cluster"}, "operations": list(p["cluster"])})
            spec = {"authentication": {"type": "scram-sha-512"},
                    "authorization": {"type": "simple", "acls": acls}}
            if env in quotas:
                spec["quotas"] = dict(quotas[env])
            users.append({
                "apiVersion": API,
                "kind": "KafkaUser",
                "metadata": {"name": "%s-%s" % (p["name"], env),
                             "labels": {"strimzi.io/cluster": CLUSTER, ENV_LABEL: env}},
                "spec": spec,
            })
    return users


def principal_of(user):
    """(principal, env) of a rendered KafkaUser, from its name and label."""
    env = user["metadata"].get("labels", {}).get(ENV_LABEL, "")
    name = user["metadata"]["name"]
    if env and name.endswith("-" + env):
        return name[:-len(env) - 1], env
    return name, env


def check(users, doc):
    errors = []
    envs = environments(doc)
    quotas = doc.get("quotas") or {}
    declared = {p["name"] for p in doc.get("principals") or []}
    topic_names = {t["name"] for t in doc.get("topics") or []}
    for p in doc.get("principals") or []:
        for topic in sorted(set(p.get("topics") or {}) - topic_names):
            errors.append("%s: an ACL on %s, which the declaration's topics don't name" % (p["name"], topic))
    seen = set()
    for u in users:
        name = u["metadata"]["name"]
        principal, env = principal_of(u)
        seen.add((principal, env))
        prefix = envs.get(env)
        for acl in (u.get("spec", {}).get("authorization") or {}).get("acls") or []:
            r = acl.get("resource", {})
            rtype, rname, pattern = r.get("type"), str(r.get("name", "")), r.get("patternType", "literal")
            ops = set(acl.get("operations") or [])
            what = "%s: %s %s %s %s" % (name, rtype, pattern, rname or "-", ",".join(sorted(ops)))
            if rtype in ("topic", "group") and (rname == "*" or "*" in rname):
                errors.append("%s: a wildcard name" % what)
                continue
            it = name == IT_PRINCIPAL
            if rtype == "topic":
                bad = ops & TOPIC_FORBIDDEN
                in_suite = pattern == "prefix" and rname in IT_PREFIXES
                if it and in_suite:
                    bad -= IT_ALLOWED
                if bad:
                    errors.append("%s: %s on a topic is the operator's alone" % (what, ",".join(sorted(bad))))
                if it and not in_suite:
                    # The suite never touches an environment's data (ADR-0013 amendment 17).
                    errors.append("%s: %s's topics are PREFIXED %s only" % (what, IT_PRINCIPAL,
                                                                           " and ".join(sorted(IT_PREFIXES))))
                if not it and (prefix is None or not rname.startswith(prefix + ".")):
                    errors.append("%s: outside environment %s's prefix %s." % (what, env or "(none)", prefix))
            elif rtype == "group":
                own = (principal == GROUP_DELETER and rname == "%s-%s" % (GROUP_DELETER, env)) \
                    or (it and pattern == "prefix" and rname.startswith(IT_GROUP_PREFIX))
                if "Delete" in ops and not own:
                    errors.append("%s: Delete on a group other than %s-<env>'s own" % (what, GROUP_DELETER))
                if ops & {"All", "Alter", "AlterConfigs", "Create"}:
                    errors.append("%s: %s on a group" % (what, ",".join(sorted(ops & {"All", "Alter", "AlterConfigs", "Create"}))))
                if it and not (pattern == "prefix" and rname.startswith(IT_GROUP_PREFIX)):
                    # A principal that can join andara-sim-dev can move dev's offsets.
                    errors.append("%s: %s's groups are PREFIXED %s only" % (what, IT_PRINCIPAL, IT_GROUP_PREFIX))
                if not it and not rname.endswith("-" + env):
                    errors.append("%s: a group outside environment %s" % (what, env or "(none)"))
            elif rtype == "cluster":
                if ops - CLUSTER_ALLOWED:
                    errors.append("%s: %s on the cluster" % (what, ",".join(sorted(ops - CLUSTER_ALLOWED))))
            else:
                errors.append("%s: an ACL on a %s" % (what, rtype))
        want = quotas.get(env)
        have = u.get("spec", {}).get("quotas")
        if name != IT_PRINCIPAL and have != want:
            errors.append("%s: quotas %r, environment %s declares %r" % (name, have, env, want))
    for env in envs:
        for principal in sorted(declared):
            if (principal, env) not in seen:
                errors.append("%s-%s: no KafkaUser for principal %s in environment %s"
                              % (principal, env, principal, env))
    return errors


def main(argv):
    if argv not in (["render"], ["check"]):
        print("usage: scripts/kafka_users.py render | check", file=sys.stderr)
        return 2
    doc = load()
    users = render(doc)
    if argv == ["render"]:
        sys.stdout.write(yaml.safe_dump_all(users, sort_keys=False))
        return 0
    errors = check(users, doc)
    for e in errors:
        print("kafka-users-check: %s" % e, file=sys.stderr)
    if errors:
        print("make: kafka-users-check: %d finding(s) in the KafkaUsers rendered from deploy/kafka/topics.yaml"
              % len(errors), file=sys.stderr)
        return 1
    print("kafka-users-check: %d KafkaUsers across %s, each scoped to its prefix"
          % (len(users), ", ".join(sorted(environments(doc)))))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
