#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The shared services in andara-shared (ADR-0013 decisions 3–5, AW-INF-055).

    scripts/shared.py check            make shared-check
    scripts/shared.py netcheck         make shared-netcheck
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
claim's. A PVC that no running pod mounts is printed as unmeasured, and doesn't fail the check. A
mounted one that can't be measured (a crash-looping broker, which is what a full disk looks like)
is printed with the reason, the rest are still reported, and the check fails naming it.

`netcheck` proves the listener's NetworkPolicy (AC-5) with four throwaway pods running the toolbox's
image: an `app.kubernetes.io/name: andara` pod in andara-local and one in andara-dev must reach the
bootstrap, and one in a fresh unlabelled namespace, and a pod without that label in andara-dev,
must not. A refusal counts only from a probe that resolved the bootstrap and reached kube-dns, so
a pod with broken DNS or egress can't pass for a policy. The pods carry no `app: andara`, so no
Service or controller of the chart selects them.
Everything it creates is deleted on the way out, pass or fail. Exit 1 names each case that
didn't hold.

`down` removes the Kafka `andara-log`, its toolbox and its PVCs, which are the environments'
Command history, with no way back. It refuses with exit 2, removing nothing, unless CONFIRM
is the namespace. The namespace itself stays: `make platform-up` creates it for Strimzi.
Resetting or destroying andara-local or andara-dev never reaches it.

Every line is `shared: …`. Exit codes: 0 ok; 1 a PVC over the threshold, a stray topic, or a
failed step, or a netcheck case that didn't hold; 2 usage or refusal; 3 the cluster context or
kubectl missing.
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
    """kubectl in namespace ns; an empty ns is a cluster-scoped call."""
    p = run(kube.kubectl(*(("-n", ns) if ns else ()), *args), capture_output=True, text=True)
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
    """[(claim, used_bytes or None, capacity_bytes, why_unmeasured)]. `used(pod, container, path)`
    measures; a measurement that fails (a crash-looping broker can't be exec'd into, which is
    the disk-full case) is a reason, not an abort, so every other claim is still reported."""
    rows = []
    for pvc in pvcs.get("items", []):
        name = pvc["metadata"]["name"]
        cap = (pvc.get("status", {}).get("capacity") or {}).get("storage") \
            or pvc["spec"]["resources"]["requests"]["storage"]
        n, why = None, None
        if name not in where:
            why = "no running pod mounts it"
        else:
            try:
                n = used(*where[name])
            except (Failed, ValueError, IndexError) as e:
                why = "measuring it in %s failed: %s" % (where[name][0], str(e)[:200])
        rows.append((name, n, quantity(cap), why))
    return rows


def pvc_report(rows, threshold=THRESHOLD):
    """Print each PVC; return (claims over the threshold, mounted claims that couldn't be measured).
    An unmounted claim is reported and left: nothing is writing to it."""
    over, unmeasured = [], []
    for name, used, cap, why in sorted(rows):
        if used is None:
            say("pvc %s unmeasured: %s (capacity %s)" % (name, why, human(cap)))
            if not why.startswith("no running pod"):
                unmeasured.append(name)
            continue
        pct = 100.0 * used / cap
        flag = " OVER %d%%" % threshold if pct > threshold else ""
        say("pvc %s %s of %s (%.1f%%)%s" % (name, human(used), human(cap), pct, flag))
        if pct > threshold:
            over.append((name, pct))
    return over, unmeasured


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
    over, unmeasured = pvc_report(pvc_rows(pvcs, mounts(pods), du(ns, run)))

    # The prefixes are read even when a claim is over, so the operator sees whose bytes they are.
    prefixes = sorted(e["prefix"] for e in topics.parse_environments(decl).values() if e["prefix"])
    pod = broker_pod(ns, run)
    text = kubectl(ns, "exec", pod, "-c", "kafka", "--", "/opt/kafka/bin/kafka-log-dirs.sh",
                   "--bootstrap-server", "localhost:9092", "--describe", run=run)
    stray = prefix_report(log_dirs(text), prefixes)

    if over:
        raise Failed("over %d%%: %s" % (THRESHOLD, ", ".join("%s (%.1f%%)" % o for o in over)))
    if unmeasured:
        raise Failed("mounted but not measured, so not known to be under %d%%: %s"
                     % (THRESHOLD, ", ".join(unmeasured)))
    if stray:
        raise Failed("unprefixed topics on the shared broker: %s" % ", ".join(stray))
    say("ok: every PVC in %s is at or under %d%%" % (ns, THRESHOLD))


# --- NetworkPolicy probe ------------------------------------------------------------------------

NET_ENV_LABEL = "andara.valesor/env"
NET_PROBE = "andara-netcheck"
NET_TOOLS = os.path.join(KAFKA_MANIFESTS, "tools.yaml")
BOOTSTRAP = "%s-kafka-bootstrap.%s.svc"
# Something every pod may reach whatever the listener's policy says: a refusal counts only from a
# probe that resolved the bootstrap and reached this (live-assertions rule 3: anchor the absence).
NET_ANCHOR = "kube-dns.kube-system.svc.cluster.local/53"
NET_PORT = "9092"
# What runs in each probe pod, as `bash -c NET_SCRIPT <host> <anchor host/port> <port>`: a marker
# first, so a probe that never ran can't read as refused; then the anchor; then the verdict.
NET_SCRIPT = ('echo probe-ran; getent hosts "$0" >/dev/null && echo resolved; '
              'timeout 3 bash -c "exec 3<>/dev/tcp/$1" && echo anchored; '
              'if timeout 3 bash -c "exec 3<>/dev/tcp/$0/$2"; '
              'then echo connected; else echo blocked; fi')


def tools_image(path=NET_TOOLS):
    """The rpk toolbox's image, so the probe adds no pin of its own (deploy/platform/versions.yaml)."""
    with open(path) as f:
        for line in f:
            m = re.match(r"\s*image:\s*(\S+)", line)
            if m:
                return m.group(1)
    raise Failed("no image in %s" % os.path.relpath(path, REPO))


def probe_manifest(ns, name, app, image):
    """A pod that idles until exec'd. `app` is its app.kubernetes.io/name; without `app: andara`
    it matches the chart's listener peer but none of its Services or controllers."""
    labels = {"andara.valesor/netcheck": "true"}
    if app:
        labels["app.kubernetes.io/name"] = app
    return {"apiVersion": "v1", "kind": "Pod",
            "metadata": {"name": name, "namespace": ns, "labels": labels},
            "spec": {"restartPolicy": "Never", "terminationGracePeriodSeconds": 0,
                     "containers": [{"name": "probe", "image": image, "command": ["sleep", "600"]}]}}


def probe_once(ns, name, host, run=subprocess.run, anchor=NET_ANCHOR, port=NET_PORT):
    """"connected", "blocked", or "unanchored" (it didn't connect, but it also couldn't resolve the
    bootstrap or reach NET_ANCHOR, so the refusal proves nothing); Failed when the probe didn't run."""
    out = kubectl(ns, "exec", name, "-c", "probe", "--", "bash", "-c", NET_SCRIPT, host, anchor, port, run=run)
    words = out.split()
    if "probe-ran" not in words or not ({"connected", "blocked"} & set(words)):
        raise Failed("probe %s/%s gave no verdict: %r" % (ns, name, out[:200]))
    if "connected" in words:
        return "connected"
    return "blocked" if {"resolved", "anchored"} <= set(words) else "unanchored"


def netcheck(ns=None, run=subprocess.run, sleep=None, clock=None, suffix=None, image=None,
             deadline=60, refusals=3):
    """AW-INF-055 AC-5: an andara pod in andara-local and in andara-dev reaches the bootstrap; an
    andara pod in an unlabelled namespace, and an unlabelled pod in andara-dev, do not."""
    import time
    sleep, clock = sleep or time.sleep, clock or time.monotonic
    ns = ns or topics.kafka_namespace()
    host = BOOTSTRAP % (KAFKA, ns)
    suffix = suffix or os.urandom(3).hex()
    image = image or tools_image()
    for env in ("local", "dev"):
        try:
            got = ((kubectl_json("", "get", "namespace", "andara-" + env, run=run).get("metadata") or {})
                   .get("labels") or {}).get(NET_ENV_LABEL)
        except Failed:
            got = None
        if got != env:
            raise Failed("namespace andara-%s is missing or isn't labelled %s=%s; run `make shared-up` first"
                         % (env, NET_ENV_LABEL, env))
    scratch = "%s-%s" % (NET_PROBE, suffix)
    name = "%s-%s" % (NET_PROBE, suffix)
    # (namespace, pod, app label, expected, what it proves)
    cases = [("andara-local", name, "andara", "connected", "an andara pod in andara-local"),
             ("andara-dev", name, "andara", "connected", "an andara pod in andara-dev"),
             (scratch, name, "andara", "blocked", "an andara pod in an unlabelled namespace"),
             ("andara-dev", name + "-other", None, "blocked", "a pod not labelled andara in andara-dev")]
    made_ns = False
    try:
        kubectl("", "create", "namespace", scratch, run=run)
        made_ns = True
        for pns, pod, app, _, _ in cases:
            p = run(kube.kubectl("apply", "-f", "-"), input=json.dumps(probe_manifest(pns, pod, app, image)),
                    capture_output=True, text=True)
            if p.returncode != 0:
                raise Failed("creating probe %s/%s: %s" % (pns, pod, (p.stderr or p.stdout).strip()))
        for pns, pod, _, _, _ in cases:
            try:
                kubectl(pns, "wait", "--for=condition=Ready", "pod/" + pod, "--timeout=120s", run=run)
            except Failed:
                try:
                    st = (kubectl_json(pns, "get", "pod", pod, run=run).get("status") or {})
                    why = next((c.get("state", {}).get("waiting", {}).get("reason")
                                for c in st.get("containerStatuses") or []), None) or st.get("phase")
                except Failed:
                    why = None
                raise Failed("probe %s/%s not Ready within 120s (%s)" % (pns, pod, why or "reason unknown"))
        say("netcheck: probes Ready; bootstrap %s:9092" % host)
        failed, unproven = [], []
        # Rule 1 of docs/specs/testing/live-assertions.md: the admitted probes are polled to a
        # deadline (a new pod's policy can lag its readiness); the refused ones are tried
        # `refusals` times after, and a single connection is a failure, as is a refusal from a
        # probe that couldn't resolve the bootstrap or reach NET_ANCHOR (rule 3).
        for pns, pod, _, want, what in cases:
            if want == "connected":
                end = clock() + deadline
                verdict = probe_once(pns, pod, host, run)
                while verdict != "connected" and clock() < end:
                    sleep(2)
                    verdict = probe_once(pns, pod, host, run)
            else:
                verdicts = []
                for i in range(refusals):
                    if i:
                        sleep(2)
                    verdicts.append(probe_once(pns, pod, host, run))
                verdict = next((v for v in ("connected", "unanchored") if v in verdicts), "blocked")
            ok = verdict == want
            say("netcheck: %s %s: %s (want %s)" % ("ok" if ok else "FAIL", what, verdict, want))
            if not ok:
                # Only an absence needs its anchor: an admitted pod that never connected fails AC-5
                # whatever the reason, while a refusal without one proves nothing about the policy.
                (unproven if want == "blocked" and verdict == "unanchored" else failed).append(what)
        msgs = []
        if failed:
            msgs.append("the listener's NetworkPolicy is not as kafka.yaml declares: %s" % "; ".join(failed))
        if unproven:
            msgs.append("not proven, because the probe couldn't resolve the bootstrap or reach %s "
                        "(its DNS or egress, not the listener's policy): %s" % (NET_ANCHOR, "; ".join(unproven)))
        if msgs:
            raise Failed(". ".join(msgs))
        say("netcheck: ok — admitted andara pods in andara-local and andara-dev only")
    finally:
        for pns, pod, _, _, _ in cases:
            if pns != scratch:
                run(kube.kubectl("-n", pns, "delete", "pod", pod, "--ignore-not-found", "--wait=false"),
                    capture_output=True, text=True)
        if made_ns:
            run(kube.kubectl("delete", "namespace", scratch, "--ignore-not-found", "--wait=false"),
                capture_output=True, text=True)


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
        elif cmd == "netcheck" and not args:
            if shutil.which("kubectl") is None:
                raise Missing("kubectl not found")
            kube.require()
            netcheck()
        elif cmd == "down" and len(args) <= 1:
            down(args[0] if args else "")
        else:
            raise Usage("usage: scripts/shared.py check | netcheck | down CONFIRM")
    except (Failed, Usage, Missing) as e:
        print("shared: %s" % e, file=sys.stderr)
        return e.code
    except kube.Missing as e:
        print("shared: %s" % e, file=sys.stderr)
        return 3
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
