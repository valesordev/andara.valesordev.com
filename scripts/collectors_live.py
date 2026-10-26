#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make collectors-check | collectors-probe — the k8s-monitoring release on a cluster (AW-INF-065).

  check [box|ci]  AC-1, AC-2, AC-3 and AC-13 on ANDARA_KUBE_CONTEXT, after `make platform-up`:
                  - the release k8s-monitoring is deployed at deploy/platform/versions.yaml's pin;
                  - the Alloy CRD was created no later than the first collector ([verify 065] item 2);
                  - the collectors metrics, logs, receiver (and fleet on the box) and kube-state-metrics
                    are Ready, and no node-exporter pod exists;
                  - observability/grafana-telemetry holds its keys; whether `token` is empty is said, the
                    value never printed;
                  - every pod that mounts or env-references that Secret belongs to the release;
                  - box, with the token set: exactly one active collector from this cluster in solo7dev's
                    Fleet inventory, with cluster=andara and stack=solo7dev and no environment attribute.
  probe           AC-7's second half: through a port-forward to the receiver's remote-write port, push
                  kafka_probe_unkept (not in keep-list.yaml), then a kept control series
                  (kafka_server_replicamanager_underreplicatedpartitions{instance="probe-065"}) with the
                  pinned promtool. Poll solo7dev until the control's own sample is newer than the push
                  (the window anchor, docs/specs/testing/live-assertions.md), then assert the unkept series
                  absent once: the destination's keep rule applies to forwarded series. Box only.

  logs COLLECTOR  the collector's warnings and errors over the last hour (`level=warn|error` lines of its
                  alloy container, every pod for the logs DaemonSet): collectors aren't shipped from
                  observability, so their failures are read here (docs/runbooks/grafana-credentials.md).

Every line is `collectors: <step> run=<id>`, except logs', which are the collector's own. Reads GRAFANA_SOLO7DEV_TELEMETRY_TOKEN and the gcx read token
from the environment or .local/box.env.

Exit: 0 every assertion held · 1 one failed (each is named) · 2 usage · 3 a missing prerequisite (the
context, kubectl, helm, gcx's config, promtool).
"""
import json
import os
import socket
import subprocess
import sys
import time
import uuid

import collectors
import gcx
import kube
import tf

REPO = collectors.REPO
NS = collectors.NAMESPACE
RELEASE = collectors.RELEASE
CRD = "alloys.collectors.grafana.com"
DEPLOYMENTS = {"box": ("metrics", "receiver", "fleet", "kube-state-metrics"),
               "ci": ("metrics", "receiver", "kube-state-metrics")}
DAEMONSETS = ("logs",)
SECRET_KEYS = {"box": ("token", "prom", "loki", "tempo", "fleet"), "ci": ("token", "prom", "loki", "tempo")}
STACK = "solo7dev"
PROBE_INSTANCE = "probe-065"
UNKEPT = "kafka_probe_unkept"
CONTROL = "kafka_server_replicamanager_underreplicatedpartitions"
DEADLINE = 300
POLL = 5
SKEW = 5
READY_TIMEOUT = 300


class Failed(Exception):
    code = 1


class Usage(Exception):
    code = 2


class Missing(Exception):
    code = 3


class Run:
    def __init__(self):
        self.id = time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:4]
        self.failures = []

    def say(self, msg):
        print("collectors: %s run=%s" % (msg, self.id), flush=True)

    def check(self, ok, msg):
        self.say(("ok   " if ok else "FAIL ") + msg)
        if not ok:
            self.failures.append(msg)


def tool(name, var):
    path = os.environ.get(var) or os.path.join(REPO, "bin", name)
    if not os.access(path, os.X_OK):
        raise Missing("%s not found; run `make bootstrap`" % os.path.relpath(path, REPO))
    return path


def kubectl(env, *args):
    try:
        argv = kube.kubectl(*args, tool=tool("kubectl", "KUBECTL"), env=env)
    except SystemExit as e:
        raise Missing("the context is not in the kubeconfig") from e
    p = subprocess.run(argv, capture_output=True, text=True)
    return p


def kjson(env, *args):
    p = kubectl(env, *args, "-o", "json")
    if p.returncode != 0:
        return None
    try:
        return json.loads(p.stdout)
    except ValueError:
        return None


def ts(s):
    return time.mktime(time.strptime(s, "%Y-%m-%dT%H:%M:%SZ"))


def references_secret(pod, name):
    spec = pod.get("spec") or {}
    for v in spec.get("volumes") or []:
        if (v.get("secret") or {}).get("secretName") == name:
            return True
        for src in (v.get("projected") or {}).get("sources") or []:
            if (src.get("secret") or {}).get("name") == name:
                return True
    for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
        for e in c.get("env") or []:
            if ((e.get("valueFrom") or {}).get("secretKeyRef") or {}).get("name") == name:
                return True
        for e in c.get("envFrom") or []:
            if (e.get("secretRef") or {}).get("name") == name:
                return True
    return False


def in_release(pod):
    labels = (pod.get("metadata") or {}).get("labels") or {}
    return pod["metadata"]["namespace"] == NS and (
        labels.get("app.kubernetes.io/instance", "").startswith(RELEASE) or labels.get("release") == RELEASE)


def ready(doc):
    """(ready?, ready count, wanted count) of a Deployment or DaemonSet document."""
    st = (doc or {}).get("status") or {}
    if (doc or {}).get("kind") == "DaemonSet":
        want, have = st.get("desiredNumberScheduled", 0), st.get("numberReady", 0)
    else:
        want, have = ((doc or {}).get("spec") or {}).get("replicas", 1), st.get("readyReplicas", 0)
    return bool(doc) and want >= 1 and have == want, have, want


def wait_ready(env, kind, name, timeout=READY_TIMEOUT, sleep=time.sleep, now=time.time):
    deadline = now() + timeout
    while True:
        ok, have, want = ready(kjson(env, "-n", NS, "get", kind, name))
        if ok or now() >= deadline:
            return ok, have, want
        sleep(POLL)


def check(r, profile, env, sleep=time.sleep, now=time.time):
    if profile not in DEPLOYMENTS:
        raise Usage("PROFILE=%s: must be box or ci" % profile)
    kube.require(env=env)
    _, chart, version = collectors.pin()
    try:
        argv = kube.helm("list", "-n", NS, "-o", "json", tool=tool("helm", "HELM"), env=env)
    except SystemExit as e:
        raise Missing("the context is not in the kubeconfig") from e
    rows = json.loads(subprocess.run(argv, capture_output=True, text=True).stdout or "[]")
    rel = [x for x in rows if x.get("name") == RELEASE]
    r.check(bool(rel) and rel[0].get("chart") == "%s-%s" % (chart, version) and rel[0].get("status") == "deployed",
            "release %s is deployed at %s-%s (%s)" % (RELEASE, chart, version,
                                                      "%s, %s" % (rel[0].get("chart"), rel[0].get("status")) if rel else "absent"))
    crd = kjson(env, "get", "crd", CRD)
    alloys = (kjson(env, "-n", NS, "get", "alloy") or {}).get("items") or []
    if crd and alloys:
        first = min((a["metadata"]["creationTimestamp"] for a in alloys), key=ts)
        r.check(ts(crd["metadata"]["creationTimestamp"]) <= ts(first),
                "CRD %s created %s, no later than the first collector (%s)" % (
                    CRD, crd["metadata"]["creationTimestamp"], first))
    else:
        r.check(False, "CRD %s and the Alloy collectors exist" % CRD)
    # The operator creates the collectors after Helm's --wait returns, so readiness is polled to a
    # deadline, never read once (docs/specs/testing/live-assertions.md rule 1).
    for kind, name in [("deployment", n) for n in DEPLOYMENTS[profile]] + [("daemonset", n) for n in DAEMONSETS]:
        ok, have, want = wait_ready(env, kind, "%s-%s" % (RELEASE, name), sleep=sleep, now=now)
        r.check(ok, "%s %s-%s Ready %s/%s" % (kind, RELEASE, name, have, want))
        if not ok:
            diagnose(r, env, name)
    pods = kjson(env, "get", "pods", "-A")
    pod_checks(r, pods)
    secret = kjson(env, "-n", NS, "get", "secret", collectors.SECRET)
    data = (secret or {}).get("data") or {}
    missing = [k for k in SECRET_KEYS[profile] if k not in data]
    r.check(bool(secret) and not missing, "secret %s/%s holds %s%s" % (
        NS, collectors.SECRET, ", ".join(SECRET_KEYS[profile]), " (missing %s)" % ", ".join(missing) if missing else ""))
    shipping = bool(data.get("token"))
    r.say("token %s" % ("set: the collectors ship to %s" % STACK if shipping else
                        "empty: the collectors ship nothing (%s unset at platform-up)" % collectors.TOKEN))
    if profile == "box" and shipping and pods is not None:
        def list_pods():
            doc = kjson(env, "-n", NS, "get", "pods", "-l", "app.kubernetes.io/instance=%s-fleet" % RELEASE)
            return None if doc is None else fleet_pods(doc)
        fleet_inventory(r, env, list_pods, sleep=sleep, now=now)
    return r


def diagnose(r, env, name):
    """Why collector `name` isn't Ready, printed where CI can show it: each of its pods' phase, Ready
    condition and container states, then the pod's events and its last log lines."""
    prefix = "%s-%s-" % (RELEASE, name)
    doc = kjson(env, "-n", NS, "get", "pods")
    pods = [p for p in (doc or {}).get("items") or [] if p["metadata"]["name"].startswith(prefix)]
    if not pods:
        r.say("%s: no pods%s" % (name, "" if doc is not None else " (the pod list could not be read)"))
        # Then the reason is on the Alloy resource (the operator's install) or the workload (FailedCreate).
        workload = RELEASE + "-" + name
        alloy = kjson(env, "-n", NS, "get", "alloy", workload)
        for c in ((alloy or {}).get("status") or {}).get("conditions") or []:
            r.say("alloy %s: %s=%s (%s: %s)" % (workload, c.get("type"), c.get("status"), c.get("reason", ""),
                                               c.get("message", "")))
        out = kubectl(env, "-n", NS, "get", "events", "--field-selector", "involvedObject.name=" + workload)
        r.say("%s: events:\n%s" % (workload, (out.stdout if out.returncode == 0 else out.stderr).rstrip()))
    for p in pods:
        pname, st = p["metadata"]["name"], p.get("status") or {}
        ready = [c for c in st.get("conditions") or [] if c.get("type") == "Ready"]
        r.say("%s: phase %s, Ready=%s%s" % (pname, st.get("phase"), ready[0].get("status") if ready else "?",
                                           " (%s)" % ready[0]["reason"] if ready and ready[0].get("reason") else ""))
        for c in st.get("containerStatuses") or []:
            state = c.get("state") or {}
            how = next(("%s %s" % (k, (v or {}).get("reason", "")) for k, v in state.items()), "unknown")
            r.say("%s: %s: %s, %d restart(s)" % (pname, c.get("name"), how.strip(), c.get("restartCount", 0)))
        for what, args in (("events", ("get", "events", "--field-selector", "involvedObject.name=" + pname)),
                           ("last log lines", ("logs", pname, "--all-containers", "--tail=20"))):
            out = kubectl(env, "-n", NS, *args)
            r.say("%s: %s:\n%s" % (pname, what, (out.stdout if out.returncode == 0 else out.stderr).rstrip()))


def pod_checks(r, doc):
    """No node-exporter, and no pod outside the release that mounts or env-references the Secret (AC-13).
    doc is `get pods -A`'s JSON, or None when it couldn't be read: that fails, rather than passing both
    checks on an empty list."""
    if doc is None:
        r.check(False, "pods in all namespaces could not be listed, so node-exporter's absence and the "
                       "readers of %s are unchecked" % collectors.SECRET)
        return
    pods = doc.get("items") or []
    exporters = [p["metadata"]["name"] for p in pods if "node-exporter" in p["metadata"]["name"]]
    r.check(not exporters, "no node-exporter pod%s" % (": " + ", ".join(exporters) if exporters else ""))
    readers = [p for p in pods if references_secret(p, collectors.SECRET)]
    outside = ["%s/%s" % (p["metadata"]["namespace"], p["metadata"]["name"]) for p in readers if not in_release(p)]
    # The collectors read the Secret through the API (remote.kubernetes.secret), so a pod that mounts or
    # env-references it is an addition; none is the expected count.
    r.check(not outside, "pods that mount or env-reference %s: %d%s; the collectors read it through the API" % (
        collectors.SECRET, len(readers), "" if not outside else ", outside the release: " + ", ".join(outside)))


def fleet_pods(doc):
    """The names of fleet's running pods in a pod list's JSON, leaving out a terminating one (still phase
    Running, with a deletionTimestamp). A collector's Fleet ID ends in its pod's name (the chart's id is
    k8s-monitoring-<cluster>-<namespace>-<hostname>)."""
    return [p["metadata"]["name"] for p in doc.get("items") or []
            if p["metadata"]["namespace"] == NS and (p.get("status") or {}).get("phase") == "Running"
            and not p["metadata"].get("deletionTimestamp")
            and ((p["metadata"].get("labels") or {}).get("app.kubernetes.io/instance") == "%s-fleet" % RELEASE)]


def fleet_inventory(r, env, list_pods, sleep=time.sleep, now=time.time):
    """AC-3: exactly one active collector from this cluster, counted among the running fleet pod's own.
    A replaced pod's collector unregisters at shutdown, but one that couldn't authenticate (a revoked or
    empty token) stays active until Fleet times it out, up to ~3h, so it is reported, not counted. A
    restarted pod is Ready before it registers, so the inventory is polled to a deadline."""
    deadline = now() + DEADLINE
    while True:
        pods = list_pods()
        try:
            doc = gcx.run(STACK, "fleet", "collectors", "list", env=env)
        except gcx.GcxError as e:
            raise Missing(str(e))
        items = doc if isinstance(doc, list) else (doc or {}).get("items") or []
        mine = []
        for c in items:
            spec = c.get("spec") or {}
            attrs = dict(spec.get("local_attributes") or {}, **(spec.get("remote_attributes") or {}))
            if attrs.get("cluster") == "andara" and not spec.get("marked_inactive_at"):
                mine.append(attrs)
        running = [a for a in mine if any(str(a.get("collector.ID", "")).endswith("-" + p) for p in pods or [])]
        if (pods is not None and len(running) == 1) or now() >= deadline:
            break
        sleep(POLL)
    if pods is None:
        r.check(False, "fleet's pods could not be listed, so the Fleet inventory is unchecked")
        return
    stale = len(mine) - len(running)
    if stale:
        r.say("Fleet inventory: %d active collector(s) from pods that are gone, not counted" % stale)
    mine = running
    r.check(len(mine) == 1, "Fleet inventory: %d active collector(s) from cluster andara's running fleet pod(s), "
                            "want 1" % len(mine))
    for attrs in mine:
        r.check(attrs.get("stack") == STACK and attrs.get("workloadName") == "fleet" and "environment" not in attrs,
                "collector %s: workload %s, stack=%s, %s" % (attrs.get("collector.ID"), attrs.get("workloadName"),
                                                           attrs.get("stack"),
                                                           "no environment attribute" if "environment" not in attrs
                                                           else "environment=%s" % attrs["environment"]))


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def push(promtool, url, text):
    p = subprocess.run([promtool, "push", "metrics", url], input=text, capture_output=True, text=True)
    if p.returncode != 0:
        raise Failed("promtool push metrics %s: %s" % (url, (p.stderr or p.stdout).strip()))


def probe(r, env, q=gcx, now=time.time, sleep=time.sleep):
    kube.require(env=env)
    promtool = tool("promtool", "PROMTOOL")
    port = free_port()
    try:
        argv = kube.kubectl("-n", NS, "port-forward", "svc/%s" % "k8s-monitoring-alloy-receiver",
                            "%d:9090" % port, tool=tool("kubectl", "KUBECTL"), env=env)
    except SystemExit as e:
        raise Missing("the context is not in the kubeconfig") from e
    pf = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        url = "http://127.0.0.1:%d/api/v1/metrics/write" % port
        for _ in range(20):
            try:
                socket.create_connection(("127.0.0.1", port), timeout=1).close()
                break
            except OSError:
                sleep(0.5)
        labels = 'job="andara-kafka",namespace="andara-shared",instance="%s"' % PROBE_INSTANCE
        # The unkept series first, the control after it: the control's arrival is the anchor that the
        # receiver has forwarded what came before it through the same destination.
        push(promtool, url, "%s{%s} 1\n" % (UNKEPT, labels))
        cause = now()
        push(promtool, url, "%s{%s} 0\n" % (CONTROL, labels))
        r.say("pushed %s, then %s{instance=\"%s\"} to the receiver" % (UNKEPT, CONTROL, PROBE_INSTANCE))
    finally:
        pf.terminate()
        pf.wait(timeout=10)
    selector = '%s{instance="%s", namespace="andara-shared"}' % (CONTROL, PROBE_INSTANCE)
    deadline = now() + DEADLINE
    seen = None
    while now() < deadline:
        try:
            rows = q.metrics_query(STACK, "timestamp(%s)" % selector, env=env)
        except q.GcxError as e:
            raise Missing(str(e))
        stamps = [float((x.get("value") or [0, 0])[1]) for x in rows]
        if stamps and min(stamps) >= cause - SKEW:
            seen = rows
            break
        sleep(POLL)
    r.check(seen is not None, "control %s arrived in %s within %ds" % (selector, STACK, DEADLINE))
    if seen is None:
        return r
    labelled = q.metrics_query(STACK, selector, env=env)
    got = (labelled[0].get("metric") or {}) if labelled else {}
    r.check(got.get("environment") == "unknown" and got.get("cluster") == "andara",
            "control carries environment=%s cluster=%s (want unknown, andara)" % (got.get("environment"), got.get("cluster")))
    absent = q.metrics_series(STACK, '%s{instance="%s"}' % (UNKEPT, PROBE_INSTANCE), since="15m", env=env)
    r.check(not absent, "%s is absent from %s: the destination's keep rule drops a forwarded series it doesn't list"
            % (UNKEPT, STACK))
    return r


COLLECTORS = ("metrics", "logs", "receiver", "fleet")


def logs(collector, env):
    if collector not in COLLECTORS:
        raise Usage("COLLECTOR=%s: must be one of %s" % (collector, ", ".join(COLLECTORS)))
    kube.require(env=env)
    kind = "daemonset" if collector in DAEMONSETS else "deployment"
    p = kubectl(env, "-n", NS, "logs", "%s/%s-%s" % (kind, RELEASE, collector), "-c", "alloy", "--since=1h",
                "--all-pods=true", "--prefix")
    if p.returncode != 0:
        raise Failed("logs %s/%s-%s: %s" % (kind, RELEASE, collector, (p.stderr or p.stdout).strip()))
    lines = [l for l in p.stdout.splitlines() if "level=warn" in l or "level=error" in l]
    sys.stdout.write("\n".join(lines) + ("\n" if lines else ""))
    print("collectors: %s: %d warning or error line(s) in the last hour" % (collector, len(lines)), file=sys.stderr)
    return 0


def main(argv):
    r = Run()
    env = dict(os.environ)
    try:
        env = tf.load_box_env(env)
    except tf.Exit as e:
        print("collectors: %s run=%s" % (e.msg, r.id), file=sys.stderr)
        return 1
    try:
        if argv[:1] == ["check"] and len(argv) <= 2:
            check(r, argv[1] if len(argv) > 1 and argv[1] else "box", env)
        elif argv == ["probe"]:
            probe(r, env)
        elif argv[:1] == ["logs"] and len(argv) == 2:
            return logs(argv[1], env)
        else:
            raise Usage("usage: scripts/collectors_live.py check [box|ci] | probe | logs <%s>" % "|".join(COLLECTORS))
    except (Failed, Usage, Missing, kube.Missing) as e:
        print("collectors: %s run=%s" % (e, r.id), file=sys.stderr)
        return e.code
    if r.failures:
        r.say("%d failed: %s" % (len(r.failures), "; ".join(r.failures)))
        return 1
    r.say("done")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
