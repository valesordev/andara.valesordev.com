#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The shared services in andara-shared (ADR-0013 decisions 3–5, AW-INF-055).

    scripts/shared.py check            make shared-check
    scripts/shared.py down <confirm>   make shared-down CONFIRM=andara-shared

`check` is the residual control for what sharing costs: one environment can fill the brokers'
disks for both. It prints every PVC in andara-shared with its use against its size, and exits 1
when one is over 80%. It then reads the brokers' log directories and prints each broker's bytes
per environment prefix (`local.`, `dev.`), so a filling disk is attributed to the environment
filling it, and every topic outside both prefixes on a line of its own (Kafka's and Strimzi's
internal topics, the registry's, the integration suite's `andara.test.` and `it<n>.`). An
unprefixed `andara.` topic outside `andara.test.` is one no environment declares on this
broker, and fails the check.

A PVC's use is `du` of its mount in the pod that mounts it, against the PVC's capacity: on kind,
a volume's own filesystem is the host's, so the kubelet's numbers are the host disk's, not the
claim's. A PVC that no running pod mounts is printed as unmeasured, and doesn't fail the check.

`down` removes the Kafka `andara-log`, its toolbox and its PVCs, which are the environments'
Command history, with no way back. It refuses with exit 2, removing nothing, unless CONFIRM
is the namespace. The namespace itself stays: `make platform-up` creates it for Strimzi.
Resetting or destroying andara-local or andara-dev never reaches it.

Every line is `shared: …`. Exit codes: 0 ok; 1 a PVC over the threshold, a stray topic, or a
failed step; 2 usage or refusal; 3 the cluster context or kubectl missing.
"""

import json
import os
import re
import shutil
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import kube  # noqa: E402
import topics  # noqa: E402

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
KAFKA = "andara-log"
KAFKA_MANIFESTS = os.path.join(REPO, "deploy", "k8s", "kafka")
THRESHOLD = 80
# The integration suite's own topics (ADR-0013 amendment 17): reported, never a stray.
SUITE = re.compile(r"^(andara\.test\.|it[0-9]+\.)")
UNITS = {"": 1, "k": 1000, "M": 1000 ** 2, "G": 1000 ** 3, "T": 1000 ** 4,
         "Ki": 1024, "Mi": 1024 ** 2, "Gi": 1024 ** 3, "Ti": 1024 ** 4}


class Failed(Exception):
    code = 1


class Usage(Exception):
    code = 2


class Missing(Exception):
    code = 3


def say(msg):
    print("shared: " + msg, flush=True)


def quantity(q):
    """A Kubernetes storage quantity in bytes: `20Gi` → 21474836480."""
    m = re.fullmatch(r"([0-9.]+)([kMGT]i?)?", str(q).strip())
    if not m:
        raise Failed("cannot read the quantity %r" % q)
    return int(float(m.group(1)) * UNITS[m.group(2) or ""])


def human(n):
    for unit in ("B", "KiB", "MiB", "GiB"):
        if abs(n) < 1024 or unit == "GiB":
            return ("%d %s" % (n, unit)) if unit == "B" else ("%.1f %s" % (n, unit))
        n /= 1024.0


def kubectl(ns, *args, run=subprocess.run):
    p = run(kube.kubectl("-n", ns, *args), capture_output=True, text=True)
    if p.returncode != 0:
        raise Failed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p.stdout


def kubectl_json(ns, *args, run=subprocess.run):
    out = kubectl(ns, *args, "-o", "json", run=run)
    try:
        return json.loads(out)
    except ValueError:
        raise Failed("kubectl %s: not JSON: %r" % (" ".join(args[:2]), out[:200]))


# --- PVCs -------------------------------------------------------------------------------------

def mounts(pods):
    """{claim: (pod, container, mountPath)} for every claim a running pod mounts."""
    out = {}
    for pod in pods.get("items", []):
        if pod.get("status", {}).get("phase") != "Running":
            continue
        name = pod["metadata"]["name"]
        vols = {v["name"]: v["persistentVolumeClaim"]["claimName"]
                for v in pod["spec"].get("volumes", []) if v.get("persistentVolumeClaim")}
        for c in pod["spec"].get("containers", []):
            for m in c.get("volumeMounts", []):
                if m["name"] in vols:
                    out.setdefault(vols[m["name"]], (name, c["name"], m["mountPath"]))
    return out


def pvc_rows(pvcs, where, used):
    """[(claim, used_bytes or None, capacity_bytes)]. `used(pod, container, path)` measures."""
    rows = []
    for pvc in pvcs.get("items", []):
        name = pvc["metadata"]["name"]
        cap = (pvc.get("status", {}).get("capacity") or {}).get("storage") \
            or pvc["spec"]["resources"]["requests"]["storage"]
        rows.append((name, used(*where[name]) if name in where else None, quantity(cap)))
    return rows


def pvc_report(rows, threshold=THRESHOLD):
    """Print each PVC; the claims over the threshold."""
    over = []
    for name, used, cap in sorted(rows):
        if used is None:
            say("pvc %s unmeasured: no running pod mounts it (capacity %s)" % (name, human(cap)))
            continue
        pct = 100.0 * used / cap
        flag = " OVER %d%%" % threshold if pct > threshold else ""
        say("pvc %s %s of %s (%.1f%%)%s" % (name, human(used), human(cap), pct, flag))
        if pct > threshold:
            over.append((name, pct))
    return over


def du(ns, run=subprocess.run):
    def used(pod, container, path):
        out = kubectl(ns, "exec", pod, "-c", container, "--", "du", "-sk", path, run=run)
        return int(out.split()[0]) * 1024
    return used


# --- Log directories ---------------------------------------------------------------------------

def log_dirs(text):
    """kafka-log-dirs.sh --describe's JSON line → [(broker, topic, bytes)] per partition."""
    for line in text.splitlines():
        if line.startswith("{"):
            doc = json.loads(line)
            break
    else:
        raise Failed("kafka-log-dirs printed no JSON")
    rows = []
    for b in doc.get("brokers", []):
        for d in b.get("logDirs", []):
            for p in d.get("partitions", []):
                rows.append((b["broker"], p["partition"].rsplit("-", 1)[0], int(p["size"])))
    return rows


def prefix_report(rows, prefixes):
    """Print each broker's bytes per prefix and each other topic; return the stray topics."""
    per = {}
    other = {}
    for broker, topic, size in rows:
        owner = next((p for p in prefixes if topic.startswith(p + ".")), None)
        if owner:
            per[(broker, owner)] = per.get((broker, owner), 0) + size
        else:
            other[topic] = other.get(topic, 0) + size
    for broker in sorted({b for b, _, _ in rows}):
        say("broker %d %s" % (broker, " ".join(
            "%s.=%s" % (p, human(per.get((broker, p), 0))) for p in prefixes)))
    stray = []
    for topic in sorted(other):
        if topic.startswith("andara.") and not SUITE.match(topic):
            say("stray %s %s: unprefixed, and no environment on this broker declares it" % (topic, human(other[topic])))
            stray.append(topic)
        else:
            say("other %s %s" % (topic, human(other[topic])))
    return stray


def broker_pod(ns, run=subprocess.run):
    pods = kubectl_json(ns, "get", "pods", "-l", "strimzi.io/cluster=%s,strimzi.io/pool-name=broker" % KAFKA,
                        run=run)
    ready = [p["metadata"]["name"] for p in pods.get("items", [])
             if any(c.get("ready") for c in p.get("status", {}).get("containerStatuses", []))]
    if not ready:
        raise Failed("no Ready broker of kafka/%s in %s to read the log directories from" % (KAFKA, ns))
    return sorted(ready)[0]


def check(ns=None, run=subprocess.run, decl=topics.DECL):
    ns = ns or topics.kafka_namespace()
    pvcs = kubectl_json(ns, "get", "pvc", run=run)
    pods = kubectl_json(ns, "get", "pods", run=run)
    over = pvc_report(pvc_rows(pvcs, mounts(pods), du(ns, run)))

    prefixes = sorted(e["prefix"] for e in topics.parse_environments(decl).values() if e["prefix"])
    pod = broker_pod(ns, run)
    text = kubectl(ns, "exec", pod, "-c", "kafka", "--", "/opt/kafka/bin/kafka-log-dirs.sh",
                   "--bootstrap-server", "localhost:9092", "--describe", run=run)
    stray = prefix_report(log_dirs(text), prefixes)

    if over:
        raise Failed("over %d%%: %s" % (THRESHOLD, ", ".join("%s (%.1f%%)" % o for o in over)))
    if stray:
        raise Failed("unprefixed topics on the shared broker: %s" % ", ".join(stray))
    say("ok: every PVC in %s is at or under %d%%" % (ns, THRESHOLD))


def down(confirm, ns=None, run=subprocess.run):
    ns = ns or topics.kafka_namespace()
    if confirm != ns:
        raise Usage("CONFIRM=%s is not %s; nothing was removed. This deletes the shared Kafka and "
                    "every environment's Command history on it" % (confirm or "(missing)", ns))
    kube.require()
    kubectl(ns, "delete", "-f", KAFKA_MANIFESTS, "--ignore-not-found", "--wait=true", "--timeout=5m", run=run)
    say("deleted deploy/k8s/kafka/ from %s" % ns)
    kubectl(ns, "delete", "pvc", "-l", "strimzi.io/cluster=%s" % KAFKA, "--ignore-not-found",
            "--wait=true", "--timeout=5m", run=run)
    say("deleted kafka/%s's PVCs in %s; the namespace stays (platform-up's)" % (KAFKA, ns))


def main(argv):
    cmd, args = (argv[0], argv[1:]) if argv else ("", [])
    try:
        if cmd == "check" and not args:
            if shutil.which("kubectl") is None:
                raise Missing("kubectl not found")
            kube.require()
            check()
        elif cmd == "down" and len(args) <= 1:
            down(args[0] if args else "")
        else:
            raise Usage("usage: scripts/shared.py check | down CONFIRM")
    except (Failed, Usage, Missing) as e:
        print("shared: %s" % e, file=sys.stderr)
        return e.code
    except kube.Missing as e:
        print("shared: %s" % e, file=sys.stderr)
        return 3
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
