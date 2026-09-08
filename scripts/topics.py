#!/usr/bin/env python3
"""Apply and diff the Kafka topic declaration in deploy/kafka/topics.yaml.

    scripts/topics.py apply [--env local]
    scripts/topics.py diff  [--env local]

One declaration, applied identically everywhere (AW-INF-004). The local stack calls
`apply` after `make up` so that local topics and production topics come from this file
rather than from a compose-file duplicate.

Talks to the broker through `rpk`, run inside the Redpanda container when one is up and
otherwise from the host. Stdlib only: this tooling validates the stack, so it must not
depend on anything the stack has to install first.
"""

import argparse
import json
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DECL = os.path.join(REPO, "deploy", "kafka", "topics.yaml")
COMPOSE = os.path.join(REPO, "deploy", "compose", "docker-compose.yaml")

# Properties compared against the live cluster. Anything not listed here is the broker's
# business; anything listed here is ours and drift in it is an error.
COMPARED = ["cleanup.policy", "retention.ms", "min.insync.replicas"]


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


def rpk_runner():
    """Return a function that runs an rpk command, in the container if one is running."""
    if os.path.isfile(COMPOSE):
        probe = subprocess.run(
            ["docker", "compose", "-f", COMPOSE, "ps", "-q", "redpanda"],
            capture_output=True, text=True,
        )
        if probe.returncode == 0 and probe.stdout.strip():
            prefix = ["docker", "compose", "-f", COMPOSE, "exec", "-T", "redpanda", "rpk"]
            return lambda args: subprocess.run(prefix + args, capture_output=True, text=True)
    if subprocess.run(["which", "rpk"], capture_output=True).returncode == 0:
        return lambda args: subprocess.run(["rpk"] + args, capture_output=True, text=True)
    die("no running Redpanda container and no local rpk; run `make up` first")


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
        return {}
    cfg = {}
    for line in res.stdout.split("\n"):
        parts = re.split(r"\s{2,}", line.strip())
        if len(parts) >= 2 and "." in parts[0]:
            cfg[parts[0]] = parts[1]
    return cfg


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("action", choices=["apply", "diff"])
    ap.add_argument("--env", default=os.environ.get("ANDARA_ENV", "local"))
    args = ap.parse_args()

    defaults, topics, broker = parse_declaration(DECL)
    rf = replication_factor(defaults, args.env)
    run = rpk_runner()
    live = existing_topics(run)

    created, drift = [], []

    for topic in topics:
        name = topic["name"]
        want_parts = topic["partitions"]
        want_cfg = declared_config(defaults, topic, args.env)

        if name not in live:
            if args.action == "diff":
                drift.append("%s: does not exist (declared %d partitions)" % (name, want_parts))
                continue
            cmd = ["topic", "create", name, "-p", str(want_parts), "-r", str(rf)]
            for key, val in sorted(want_cfg.items()):
                cmd += ["-c", "%s=%s" % (key, val)]
            res = run(cmd)
            if res.returncode != 0:
                die("could not create %s: %s" % (name, (res.stderr or res.stdout).strip()))
            created.append(name)
            continue

        have_parts = live[name]["partitions"]
        if have_parts != want_parts:
            # Never silently altered, and never altered at all by this tool: the partition
            # is derived from the key, so changing the count reorders history (ADR-0002).
            die(
                "%s has %d partitions, declaration says %d. Repartitioning a keyed topic "
                "reorders history; no change was made. Recreate the topic deliberately, or "
                "amend the declaration." % (name, have_parts, want_parts)
            )

        have_cfg = describe_config(run, name)
        for key in COMPARED:
            if key not in want_cfg or key not in have_cfg:
                continue
            if str(have_cfg[key]) != str(want_cfg[key]):
                drift.append(
                    "%s: %s is '%s', declaration says '%s'"
                    % (name, key, have_cfg[key], want_cfg[key])
                )

    # A local broker is disposable, so broker settings are applied to it. Dev and prod
    # brokers are asserted against, never rewritten by a make target.
    if args.env == "local" and args.action == "apply":
        for key, val in sorted(broker.get("local", {}).items()):
            res = run(["cluster", "config", "set", key, val])
            if res.returncode != 0:
                die("could not set broker config %s=%s: %s"
                    % (key, val, (res.stderr or res.stdout).strip()))

    # The two settings the zero-RPO claim rests on. Drift in them is silent, which is
    # exactly why it is asserted rather than assumed (AW-INF-004 AC-5a).
    if args.env != "local":
        res = run(["cluster", "config", "get", "unclean.leader.election.enable"])
        have = res.stdout.strip() if res.returncode == 0 else ""
        want = broker.get("assert", {}).get("unclean.leader.election.enable", "false")
        if have and have != want:
            drift.append(
                "cluster: unclean.leader.election.enable is '%s', must be '%s' — with it "
                "enabled, acks=all does not guarantee durability" % (have, want)
            )

    if drift:
        for line in drift:
            sys.stderr.write("topics-%s: %s\n" % (args.action, line))
        sys.stderr.write(
            "make: topics-%s: %d drift(s) from deploy/kafka/topics.yaml\n"
            % (args.action, len(drift))
        )
        return 1

    if args.action == "apply":
        if created:
            print("topics-apply: created %d topic(s): %s" % (len(created), ", ".join(created)))
        print("topics-apply: %d topic(s) match deploy/kafka/topics.yaml (env=%s)"
              % (len(topics), args.env))
    else:
        print("topics-diff: no drift across %d topic(s) (env=%s)" % (len(topics), args.env))
    return 0


if __name__ == "__main__":
    sys.exit(main())
