#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-check ENV=<env> — do the chart's signals reach Grafana Cloud? (AW-INF-008)

Asks the three backends the cluster's Alloy ships to, with a read token, whether what the
chart wires up actually arrived:

  metrics  up{job="andara-server"} == 1 and andara_sessions_active, both carrying
           namespace="andara-<env>" and cluster (AC-1); up == 1 for every projector
           Deployment the namespace runs, each under its own job (AC-2)
  logs     a `session opened` line from {namespace="andara-<env>", container="server"}
           (AC-4) — SESSION_ID=<id> pins which one
  traces   the andara.game.v1.Game/OpenSession span of that line's trace_id, fetched from
           Tempo by ID (AC-3) — TRACE_ID=<id> overrides

Then, informational and outside the exit status, every expression in files/alerts.yaml
evaluated as an instant query against the same series, printed with the labels each result
carries. That is AC-6's instrument: delete one environment's pod, run this, and the
AndaraServerUnavailable line names that namespace and no other.

Credentials come from the environment and nowhere else; nothing here prints them.

  GRAFANA_CLOUD_READ_TOKEN                   an access-policy token with metrics:read,
                                             logs:read, traces:read
  GRAFANA_CLOUD_PROM_URL / _PROM_USER        e.g. https://prometheus-prod-NN-….grafana.net/api/prom
  GRAFANA_CLOUD_LOKI_URL / _LOKI_USER        e.g. https://logs-prod-NNN.grafana.net
  GRAFANA_CLOUD_TEMPO_URL / _TEMPO_USER      e.g. https://tempo-prod-NN-….grafana.net/tempo

The URLs and user IDs are on the stack's details page in the Grafana Cloud portal; they are
not secrets, the token is. Optional: CLUSTER (default solo7-local), SINCE (default 1h), and
PROJECTORS — the enabled projector names, comma-separated, `none` for none — which otherwise
come from the namespace's Deployments via kubectl. If neither can say which projectors run, the
check does not guess: AC-2 unanswerable is exit 3, not a skipped check.

Exit: 0 every signal present · 1 a signal absent · 2 a backend refused or failed the query
· 3 a credential or URL unset — never a vacuous pass.
"""
import base64
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
OPEN_SESSION_SPAN = "andara.game.v1.Game/OpenSession"


class BackendError(Exception):
    pass


def say(msg):
    print("observe-check: " + msg)


def need(name):
    v = os.environ.get(name, "").strip()
    if not v:
        say("no %s; cannot verify" % name)
        sys.exit(3)
    return v


class Backend:
    def __init__(self, kind, token):
        self.url = need("GRAFANA_CLOUD_%s_URL" % kind).rstrip("/")
        user = need("GRAFANA_CLOUD_%s_USER" % kind)
        self.auth = "Basic " + base64.b64encode(("%s:%s" % (user, token)).encode()).decode()
        self.kind = kind.lower()

    def get(self, path, params=None, allow_404=False):
        url = self.url + path + ("?" + urllib.parse.urlencode(params) if params else "")
        req = urllib.request.Request(url, headers={"Authorization": self.auth, "Accept": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            if allow_404 and e.code == 404:
                return None
            # The body, not the request: the request carries the token.
            raise BackendError("%s %s: HTTP %d %s" % (self.kind, path, e.code, e.read()[:200].decode(errors="replace")))
        except (urllib.error.URLError, TimeoutError) as e:
            raise BackendError("%s %s: %s" % (self.kind, path, e))


def since_seconds(s):
    m = re.fullmatch(r"(\d+)([smhd])", s)
    if not m:
        say("SINCE=%s is not <n>s|m|h|d" % s)
        sys.exit(3)
    return int(m.group(1)) * {"s": 1, "m": 60, "h": 3600, "d": 86400}[m.group(2)]


def instant(prom, q):
    body = prom.get("/api/v1/query", {"query": q})
    if body.get("status") != "success":
        raise BackendError("prom query %r: %s" % (q, body.get("error")))
    return body["data"]["result"]


def labels(r):
    m = dict(r["metric"])
    name = m.pop("__name__", "")
    return name + "{" + ", ".join('%s="%s"' % kv for kv in sorted(m.items())) + "}"


def projector_jobs(ns):
    """The projector jobs AC-2 expects: PROJECTORS if set, else the projector Deployments the
    namespace actually runs — the chart renders only the enabled ones, so that is what
    `projectors.<name>.enabled` resolved to. Not knowing is exit 3: an empty list here would
    drop every projector query and let an absent projector pass."""
    names = os.environ.get("PROJECTORS", "").strip()
    if names:
        return [] if names == "none" else ["andara-projector-" + n.strip() for n in names.split(",") if n.strip()]
    if not shutil.which("kubectl"):
        say("no kubectl and no PROJECTORS; cannot verify AC-2")
        sys.exit(3)
    p = subprocess.run(["kubectl", "-n", ns, "get", "deploy", "-l", "app.kubernetes.io/name=andara",
                        "-o", "jsonpath={.items[*].metadata.name}"], capture_output=True, text=True)
    if p.returncode:
        say("kubectl could not list %s deployments (%s) and no PROJECTORS; cannot verify AC-2"
            % (ns, p.stderr.strip()))
        sys.exit(3)
    return [n for n in p.stdout.split() if n.startswith("andara-projector-")]


def main():
    env = (sys.argv[1] if len(sys.argv) > 1 else "").strip()
    if not env:
        say("usage: make observe-check ENV=<local|dev|prod>")
        sys.exit(3)
    token = need("GRAFANA_CLOUD_READ_TOKEN")
    prom, loki, tempo = (Backend(k, token) for k in ("PROM", "LOKI", "TEMPO"))
    ns = "andara-" + env
    cluster = os.environ.get("CLUSTER", "solo7-local")
    since = since_seconds(os.environ.get("SINCE", "1h"))
    absent = []

    # Metrics (AC-1, AC-2).
    sel = 'namespace="%s", cluster="%s"' % (ns, cluster)
    checks = [("up", 'up{job="andara-server", %s}' % sel, True),
              ("andara_sessions_active", 'andara_sessions_active{job="andara-server", %s}' % sel, False)]
    checks += [("up " + d, 'up{job="%s", %s}' % (d, sel), True) for d in projector_jobs(ns)]
    for what, q, must_be_one in checks:
        res = instant(prom, q)
        if not res:
            absent.append(what)
            say("ABSENT %s" % q)
            continue
        for r in res:
            v = r["value"][1]
            bad = must_be_one and v != "1"
            say("%s %s %s" % ("DOWN  " if bad else "ok    ", labels(r), v))
            if bad:
                absent.append(what)

    # Logs (AC-4). The line carries the RPC span's trace_id, which is what ties it to AC-3.
    now = int(time.time())
    logq = '{namespace="%s", container="server"} | json | msg="session opened"' % ns
    sid = os.environ.get("SESSION_ID", "").strip()
    if sid:
        logq += ' | session_id="%s"' % sid
    body = loki.get("/loki/api/v1/query_range", {"query": logq, "start": (now - since) * 10**9,
                                                 "end": now * 10**9, "limit": 1, "direction": "backward"})
    lines = [v[1] for s in body.get("data", {}).get("result", []) for v in s.get("values", [])]
    trace_id = os.environ.get("TRACE_ID", "").strip()
    if not lines:
        absent.append("session opened log line")
        say("ABSENT %s over the last %ds" % (logq, since))
    else:
        line = json.loads(lines[0])
        say("ok     loki %s -> session_id=%s trace_id=%s" % (logq, line.get("session_id"), line.get("trace_id")))
        trace_id = trace_id or line.get("trace_id", "")

    # Traces (AC-3).
    if not trace_id:
        absent.append("trace")
        say("ABSENT no trace_id to fetch (no session opened line, no TRACE_ID)")
    else:
        body = tempo.get("/api/traces/" + trace_id, allow_404=True)
        if body is None or OPEN_SESSION_SPAN not in json.dumps(body):
            absent.append("trace")
            say("ABSENT tempo trace %s with a %s span" % (trace_id, OPEN_SESSION_SPAN))
        else:
            say("ok     tempo trace %s carries %s" % (trace_id, OPEN_SESSION_SPAN))

    # The rules, evaluated where AW-INF-009 will evaluate them (AC-6's instrument).
    say("rules (informational — an empty result is a rule that would not fire):")
    for g in yaml.safe_load(open(ALERTS))["groups"]:
        for rule in g["rules"]:
            res = instant(prom, rule["expr"])
            say("  %-26s %s" % (rule["alert"], ", ".join(labels(r) for r in res) or "—"))

    if absent:
        say("%d signal(s) absent: %s" % (len(absent), ", ".join(absent)))
        sys.exit(1)
    say("every signal present for %s" % ns)


if __name__ == "__main__":
    try:
        main()
    except BackendError as e:
        say("backend error: %s" % e)
        sys.exit(2)
