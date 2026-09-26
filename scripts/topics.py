#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Apply and diff the Kafka topic declaration in deploy/kafka/topics.yaml.

    scripts/topics.py apply [--env local] [--allow-data-loss <topic>[,<topic>...]]
    scripts/topics.py diff  [--env local]

One declaration, applied identically everywhere (AW-INF-004). The local stack calls
`apply` after `make up` so that local topics and production topics come from this file
rather than from a compose-file duplicate.

`apply` creates missing topics and aligns every COMPARED key on existing ones (AW-INF-018).
It checks everything before it changes anything: partition drift is refused, and so is a
change that lets the broker delete data (a lower retention.ms, or a cleanup policy the topic
didn't have), unless the operator names that topic in --allow-data-loss.

Talks to the broker through `rpk`, run inside the Redpanda container when one is up and
otherwise from the host. Stdlib only: this tooling validates the stack, so it must not
depend on anything the stack has to install first.
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DECL = os.path.join(REPO, "deploy", "kafka", "topics.yaml")
COMPOSE = os.path.join(REPO, "deploy", "compose", "docker-compose.yaml")

# Properties compared against the live cluster. Anything not listed here is the broker's
# business; anything listed here is ours and drift in it is an error.
# min.compaction.lag.ms is andara.state.v1's (AW-SRV-019). Redpanda accepts it and does
# not report it, so locally it is skipped like min.insync.replicas; on Kafka a topic created
# before it was declared reports the broker default, and `topics-diff` names the drift.
COMPARED = ["cleanup.policy", "retention.ms", "min.insync.replicas", "min.compaction.lag.ms"]


def die(msg, code=1):
    sys.stderr.write("make: topics: %s\n" % msg)
    sys.exit(code)


def parse_declaration(path):
    """Parse the topic declaration.

    Deliberately narrow: this reads the structure of deploy/kafka/topics.yaml and nothing
    else. If the declaration ever needs more YAML than this, the declaration is the
    problem — a topic definition that is hard to parse is a topic definition that is hard
    to review, and this file is one nobody should have to squint at.
    """
    defaults = {"replication_factor": {}, "config": {}}
    topics = []
    broker = {}
    section = None
    current = None
    skipping_block = False
    block_indent = 0
    sub = None

    with open(path, "r", encoding="utf-8") as fh:
        lines = fh.read().split("\n")

    for raw in lines:
        line = raw.split(" #")[0].rstrip() if not raw.strip().startswith("#") else ""
        if not line.strip():
            continue
        indent = len(line) - len(line.lstrip())

        if skipping_block:
            if indent > block_indent:
                continue
            skipping_block = False

        stripped = line.strip()

        if indent == 0:
            section = stripped.rstrip(":")
            sub = None
            continue

        if section == "defaults":
            if indent == 2:
                sub = stripped.rstrip(":")
                if sub == "config":
                    defaults["config"] = {}
                continue
            key, _, val = stripped.partition(":")
            key, val = key.strip(), val.strip()
            if sub == "replication_factor":
                if val:
                    defaults["replication_factor"][key] = val
            elif sub == "config":
                if indent == 4 and not val:
                    sub_key = key
                    defaults["config"].setdefault(sub_key, {})
                    current = defaults["config"][sub_key]
                elif val and isinstance(current, dict):
                    current[key] = val
            continue

        if section == "topics":
            if stripped.startswith("- "):
                current = {}
                topics.append(current)
                stripped = stripped[2:]
            key, _, val = stripped.partition(":")
            key, val = key.strip(), val.strip()
            if val == "|":
                # Prose. Kept in the file for the reviewer, not needed by the tool.
                skipping_block = True
                block_indent = indent
                continue
            if current is not None and val:
                current[key] = val.strip("'\"")
            continue

        if section == "broker":
            if indent == 2 and stripped.endswith(":"):
                sub = stripped.rstrip(":")
                broker.setdefault(sub, {})
                continue
            key, _, val = stripped.partition(":")
            if val.strip() and sub in broker:
                broker[sub][key.strip()] = val.strip().strip("'\"")
            continue

    for t in topics:
        if "name" not in t or "partitions" not in t:
            die("declaration entry is missing 'name' or 'partitions': %r" % t)
        t["partitions"] = int(t["partitions"])
    return defaults, topics, broker


def replication_factor(defaults, env):
    rf = defaults["replication_factor"].get(env)
    if rf is None:
        die("no replication_factor declared for env '%s' in deploy/kafka/topics.yaml" % env)
    return int(rf)


def declared_config(defaults, topic, env):
    """The full config this topic should carry in this environment."""
    cfg = {}
    for key, per_env in defaults["config"].items():
        if env in per_env:
            cfg[key] = str(per_env[env])
    for key, val in topic.items():
        if key in ("name", "partitions", "why"):
            continue
        cfg[key] = str(val)
    return cfg


# The broker on the box (AW-INF-014): Strimzi's bootstrap Service for Kafka `andara-log`,
# reached through the rpk toolbox in deploy/k8s/kafka/tools.yaml, because a broker's
# advertised address only resolves inside the cluster.
CLUSTER_BOOTSTRAP = "andara-log-kafka-bootstrap:9092"
CLUSTER_TOOLBOX = "deploy/andara-kafka-tools"


def rpk_prefix(env, compose_redpanda_running, host_rpk):
    """The command that runs rpk against `env`'s broker, or None when there is none.

    local is the compose Redpanda, else an rpk on the host. Every other environment is
    its namespace on the cluster, and never compose: until AW-INF-014 this function
    ignored the environment, so `topics-apply ANDARA_ENV=dev` with `make up` running
    applied dev's declaration to the laptop's broker.
    """
    if env != "local":
        return ["kubectl", "-n", "andara-" + env, "exec", "-i", CLUSTER_TOOLBOX, "--",
                "rpk", "-X", "brokers=" + CLUSTER_BOOTSTRAP]
    if compose_redpanda_running:
        return ["docker", "compose", "-f", COMPOSE, "exec", "-T", "redpanda", "rpk"]
    if host_rpk:
        return ["rpk"]
    return None


def compose_redpanda_running():
    if not os.path.isfile(COMPOSE):
        return False
    probe = subprocess.run(
        ["docker", "compose", "-f", COMPOSE, "ps", "-q", "redpanda"],
        capture_output=True, text=True,
    )
    return probe.returncode == 0 and bool(probe.stdout.strip())


def rpk_runner(env):
    """Return a function that runs an rpk command against `env`'s broker."""
    if env == "local":
        prefix = rpk_prefix(env, compose_redpanda_running(), shutil.which("rpk") is not None)
        if prefix is None:
            die("no running Redpanda container and no local rpk; run `make up` first")
    else:
        if shutil.which("kubectl") is None:
            die("env '%s' is on the cluster and kubectl is not installed" % env)
        prefix = rpk_prefix(env, False, False)
    return lambda args: subprocess.run(prefix + args, capture_output=True, text=True)


def existing_topics(run):
    """Return {name: {"partitions": int, "config": {..}}} for topics the broker has."""
    res = run(["topic", "list", "--format", "json"])
    if res.returncode != 0:
        die("could not list topics: %s" % (res.stderr or res.stdout).strip())
    out = {}
    text = res.stdout.strip()
    try:
        parsed = json.loads(text) if text.startswith(("[", "{")) else None
    except ValueError:
        parsed = None
    if parsed is not None:
        rows = parsed if isinstance(parsed, list) else parsed.get("topics", [])
        for row in rows:
            out[row.get("name") or row.get("topic")] = {
                "partitions": int(row.get("partitions", 0)), "config": {}
            }
    else:
        # Tabular fallback: NAME PARTITIONS REPLICAS
        for line in text.split("\n")[1:]:
            parts = line.split()
            if len(parts) >= 2 and not parts[0].startswith("NAME"):
                out[parts[0]] = {"partitions": int(parts[1]), "config": {}}
    return out


def describe_config(run, name):
    """Return {key: value} of the topic's live configuration."""
    res = run(["topic", "describe", name, "--print-configs"])
    if res.returncode != 0:
        die("could not describe %s: %s" % (name, (res.stderr or res.stdout).strip()))
    cfg = {}
    for line in res.stdout.split("\n"):
        parts = re.split(r"\s{2,}", line.strip())
        if len(parts) >= 2 and "." in parts[0]:
            cfg[parts[0]] = parts[1]
    return cfg


def policies(value):
    """cleanup.policy as a set: `delete,compact` and `compact,delete` are the same policy."""
    return frozenset(p.strip() for p in str(value).split(",") if p.strip())


def same(key, have, want):
    if key == "cleanup.policy":
        return policies(have) == policies(want)
    return str(have) == str(want)


def retention(value):
    """retention.ms as an order: -1 is unlimited, above every finite window."""
    v = int(value)
    return float("inf") if v < 0 else v


def destructive(key, have, want):
    """Whether setting `key` from `have` to `want` lets the broker delete data.

    A lower retention.ms expires segments sooner. A cleanup.policy gaining a policy the topic
    didn't have deletes too: `delete` added to a compacted topic expires old keys by time, and
    `compact` added to a delete topic keeps only each key's last record. Dropping a policy only
    deletes less. An unknown old value (the broker doesn't report the key) counts as
    destructive, because nothing shows it isn't.
    """
    if have is None:
        return key in ("retention.ms", "cleanup.policy")
    if key == "retention.ms":
        return retention(want) < retention(have)
    if key == "cleanup.policy":
        return bool(policies(want) - policies(have))
    return False


def run_action(action, env, allow_data_loss, run, decl=DECL, out=sys.stdout, err=sys.stderr):
    """Plan against the live broker, refuse what must be refused, then create and alter.

    Returns the exit code. Every refusal is decided across all topics before the first
    create or alter, so a refused run changes nothing anywhere.
    """
    defaults, topics, broker = parse_declaration(decl)
    rf = replication_factor(defaults, env)
    live = existing_topics(run)
    declared_names = {t["name"] for t in topics}

    missing, alters, drift, skipped = [], [], [], set()

    for topic in topics:
        name = topic["name"]
        want_parts = topic["partitions"]
        want_cfg = declared_config(defaults, topic, env)

        if name not in live:
            if action == "diff":
                drift.append("%s: does not exist (declared %d partitions)" % (name, want_parts))
            else:
                missing.append((name, want_parts, want_cfg))
            continue

        have_parts = live[name]["partitions"]
        if have_parts != want_parts:
            # Never altered by this tool: the partition is derived from the key, so changing
            # the count reorders history (ADR-0002). Refused before anything is changed.
            die("%s has %d partitions, declared %d; repartitioning is refused"
                % (name, have_parts, want_parts))

        have_cfg = describe_config(run, name)
        # The broker-level assertions are topic-level properties on Kafka too, reported
        # per topic by DescribeConfigs with the broker default as their effective value.
        # Checking them here, through the same Kafka API call the rest of this loop uses,
        # is what works against the production broker ADR-0002 names. The previous
        # implementation went through `rpk cluster config get`, which is Redpanda's admin
        # API and does not exist on Kafka — so against production it failed to read the
        # property and then treated the empty answer as agreement.
        want_all = dict(want_cfg)
        if env != "local":
            want_all.update(broker.get("assert", {}))
        for key in COMPARED + sorted(k for k in broker.get("assert", {}) if k not in COMPARED):
            if key not in want_all:
                continue
            if key not in have_cfg:
                # Redpanda does not implement min.insync.replicas or unclean leader
                # election (its Raft replication cannot elect a leader missing committed
                # records), and accepts min.compaction.lag.ms without reporting it, so a
                # local broker never reports them and there is nothing to compare. Kafka
                # reports every property with its effective value, so on any other
                # environment an absent key means the declaration is not in force — which
                # is drift, not assent (AW-INF-004 AC-5a).
                if env == "local":
                    skipped.add(key)
                    continue
                if action == "apply" and key in COMPARED:
                    alters.append((name, key, None, want_all[key]))
                else:
                    drift.append(
                        "%s: %s is not reported by the broker, declaration says '%s' — "
                        "the setting is not in force" % (name, key, want_all[key]))
                continue
            if same(key, have_cfg[key], want_all[key]):
                continue
            if action == "apply" and key in COMPARED:
                alters.append((name, key, have_cfg[key], want_all[key]))
            else:
                drift.append("%s: %s is '%s', declaration says '%s'"
                             % (name, key, have_cfg[key], want_all[key]))

    if skipped:
        out.write("topics: local broker does not report %s; skipped\n" % ", ".join(sorted(skipped)))

    if action == "apply":
        allowed = {t for t in allow_data_loss if t}
        lossy = [(n, k, h, w) for (n, k, h, w) in alters if destructive(k, h, w)]
        refused = [a for a in lossy if a[0] not in allowed]
        for name, key, have, want in refused:
            err.write("make: topics: %s %s %s -> %s can delete data the broker cannot restore; "
                      "re-run with ALLOW_DATA_LOSS=%s to apply it\n"
                      % (name, key, "(unreported)" if have is None else have, want, name))
        if refused:
            err.write("make: topics: nothing was changed\n")
            return 1
        lossy_topics = {a[0] for a in lossy}
        for name in sorted(allowed - lossy_topics):
            note = "" if name in declared_names else " (not a declared topic)"
            out.write("topics: ALLOW_DATA_LOSS names %s, which has no destructive change%s\n"
                      % (name, note))

        for name, parts, cfg in missing:
            cmd = ["topic", "create", name, "-p", str(parts), "-r", str(rf)]
            for key, val in sorted(cfg.items()):
                cmd += ["-c", "%s=%s" % (key, val)]
            res = run(cmd)
            if res.returncode != 0:
                die("could not create %s: %s" % (name, (res.stderr or res.stdout).strip()))
            out.write("topics: created %s\n" % name)

        applied = []
        for name, key, have, want in alters:
            res = run(["topic", "alter-config", name, "--set", "%s=%s" % (key, want)])
            if res.returncode != 0:
                err.write("make: topics: the broker refused %s %s=%s: %s\n"
                          % (name, key, want, (res.stderr or res.stdout).strip()))
                err.write("make: topics: already applied: %s\n"
                          % (", ".join(applied) if applied else "nothing"))
                return 1
            applied.append("%s %s" % (name, key))
            out.write("topics: altered %s %s %s -> %s%s\n"
                      % (name, key, "(unreported)" if have is None else have, want,
                         " (data loss accepted)" if destructive(key, have, want) else ""))

    # A local broker is disposable, so broker settings are applied to it. Dev and prod
    # brokers are asserted against, never rewritten by a make target.
    if env == "local" and action == "apply":
        for key, val in sorted(broker.get("local", {}).items()):
            res = run(["cluster", "config", "set", key, val])
            if res.returncode != 0:
                die("could not set broker config %s=%s: %s"
                    % (key, val, (res.stderr or res.stdout).strip()))

    if drift:
        for line in drift:
            err.write("topics-%s: %s\n" % (action, line))
        err.write("make: topics-%s: %d drift(s) from deploy/kafka/topics.yaml\n"
                  % (action, len(drift)))
        return 1

    if action == "apply":
        out.write("topics-apply: %d topic(s) match deploy/kafka/topics.yaml (env=%s)\n"
                  % (len(topics), env))
    else:
        out.write("topics-diff: no drift across %d topic(s) (env=%s)\n" % (len(topics), env))
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("action", choices=["apply", "diff"])
    ap.add_argument("--env", default=os.environ.get("ANDARA_ENV", "local"))
    ap.add_argument("--allow-data-loss", default="",
                    help="comma-separated topics whose destructive change the operator accepts")
    args = ap.parse_args()
    if args.allow_data_loss and args.action != "apply":
        die("--allow-data-loss applies only to `apply`")
    allow = [t.strip() for t in args.allow_data_loss.split(",")]
    if "*" in allow:
        die("ALLOW_DATA_LOSS names topics, never a wildcard")
    return run_action(args.action, args.env, allow, rpk_runner(args.env))


if __name__ == "__main__":
    sys.exit(main())
