#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make observe-check ENV=<env> KEEP_LIST=1 — do the series the rules and the dashboard need exist? (AW-INF-075)

ADR-0012 decision 13's keep-list, computed rather than copied: every metric name in
deploy/helm/andara/files/alerts.yaml and the dashboard source (DASHBOARD) is extracted from the
PromQL itself, so the list cannot go stale. Each name is one of two kinds:

  always-present   ROWS: the selector the name is checked by for ENV. Each row must return at least
                   one series through gcx (`metrics series`). These rows gate adding ENV to the
                   stack's required_environments.
  fault-only       FAULT_ONLY (‡ in the ADR): absent on a clean rebuild until the event they describe.
                   Not queried; the `keep` pipeline's allow-list (KEEP_PIPELINE, AW-INF-051) must name
                   them, which is checked offline (scripts/tests) once that source exists, and the
                   first drill that produces the fault must then see them.

The compose dashboard also reads broker series (NOT_SHIPPED, redpanda_*), which the `keep` pipeline
drops by design; they are reported and not queried, and no rule may name one. A name in no set
fails a unit test, so a new rule decides its row when it is written.

Then decision 9's label check: `gcx metrics labels --label environment` lists every value in the stack, with
no window; a value outside local, dev, staging, prod and unknown fails while it still has series in SINCE
(`metrics series`), naming up to five senders (metric, job, namespace), and is reported as stale otherwise,
since it ages out with the stack's retention. Then the `unknown` guard: no keep-list series in an andara-* namespace may carry
environment="unknown", except PLATFORM_SERIES (certmanager_*, traefik_*; the rules derive their
environment) and namespace="andara-shared" (ADR-0013's platform namespace). It runs only after
every always-present row returned series: an empty answer is a pass only when series are known to
exist.

One line per row (its set, the series count, pass or fail) and a summary per set, so a failed run
names every missing series at once.

Exit: 0 every row has series, no unknown series and no live stray environment value · 1 otherwise, or a
gcx error.
"""
import json
import os
import re
import sys

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gcx  # noqa: E402

REPO = os.path.dirname(HERE)
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
# AW-INF-049 moves the dashboard to deploy/grafana/dashboards/ and changes this one constant.
DASHBOARD = os.path.join(REPO, "deploy", "compose", "grafana", "dashboards", "tick-health.json")
# AW-INF-051's Fleet `keep` pipeline: the allow-list the fault-only names must appear in.
KEEP_PIPELINE = os.path.join(REPO, "deploy", "terraform", "grafana", "pipelines", "keep.alloy")

FAULT_ONLY = ("kube_pod_container_status_last_terminated_exitcode", "andara_recovery_state_hash_match")
PLATFORM_SERIES = ("certmanager_", "traefik_")
# ADR-0012 decision 9: the only environment values, and unknown, which only the stamp pipeline sets.
ENVIRONMENTS = ("local", "dev", "staging", "prod", "unknown")
# Broker series the compose dashboard reads. AW-INF-051's `keep` allow-list drops them by design (drills
# read them from the broker), and AW-INF-049's rewrite of the dashboard removes them. Reported, not
# queried; a rule may never name one (a unit test).
NOT_SHIPPED = ("redpanda_",)
SINCE = "10m"

# metric -> [(row name, selector for ENV)]. {env}, {ns} are filled in; ns is andara-<env>.
ANDARA = '{{__name__="{m}", environment="{env}", namespace="{ns}"}}'
POD = '{{__name__="{m}", environment="{env}", namespace="{ns}", container="server"}}'
ROWS = {
    "up": [("up andara-server", '{{__name__="up", job="andara-server", environment="{env}", namespace="{ns}"}}'),
           # A separate row: the server's series must not stand in for the projector's.
           ("up andara-projector-state",
            '{{__name__="up", job="andara-projector-state", environment="{env}", namespace="{ns}"}}')],
    "kube_pod_container_status_ready": [(None, POD)],
    "kube_pod_container_status_restarts_total": [(None, POD)],
    "kube_deployment_spec_replicas": [
        (None, '{{__name__="{m}", environment="{env}", namespace="{ns}", deployment="andara-projector-state"}}')],
    # The Certificate's namespace, so another workload's certificate cannot satisfy it.
    "certmanager_certificate_expiration_timestamp_seconds": [
        (None, '{{__name__="{m}", exported_namespace="{ns}", name=~"andara-.*"}}')],
    # Traefik names an Ingress router <namespace>-<ingress>-<host>…; the prefix pins ENV.
    "traefik_router_requests_total": [(None, '{{__name__="{m}", router=~"{ns}-andara.*"}}')],
}
# The server's own series: every andara_* name the sources use, checked under ENV's selectors.
ANDARA_PREFIX = "andara_"

KEYWORDS = {"and", "or", "unless", "by", "without", "on", "ignoring", "group_left", "group_right",
            "bool", "offset", "inf", "nan"}


def names_in(expr):
    """The metric names a PromQL expression reads."""
    s = re.sub(r'"(?:[^"\\]|\\.)*"', '""', expr)          # string literals (label values, label_replace args)
    s = re.sub(r"#[^\n]*", "", s)                          # comments
    s = re.sub(r"\{[^}]*\}", "", s)                        # matchers
    s = re.sub(r"\[[^\]]*\]", "", s)                       # ranges and subqueries
    s = re.sub(r"\b(by|without|on|ignoring|group_left|group_right)\s*\([^)]*\)", " ", s)
    out = set()
    for m in re.finditer(r"[A-Za-z_:][A-Za-z0-9_:]*", s):
        tok = m.group(0)
        rest = s[m.end():].lstrip()
        if tok.lower() in KEYWORDS or rest.startswith("("):
            continue
        if re.fullmatch(r"\d+[smhdwy]", tok) or s[max(0, m.start() - 1)].isdigit():
            continue
        out.add(tok)
    return out


def _exprs(node):
    if isinstance(node, dict):
        for k, v in node.items():
            if k == "expr" and isinstance(v, str):
                yield v
            else:
                yield from _exprs(v)
    elif isinstance(node, list):
        for v in node:
            yield from _exprs(v)


def source_names(alerts=ALERTS, dashboard=DASHBOARD):
    """Every metric name in the rules and the dashboard."""
    with open(alerts) as f:
        rules = yaml.safe_load(f)
    names = set()
    for e in _exprs(rules):
        names |= names_in(e)
    if os.path.exists(dashboard):
        with open(dashboard) as f:
            for e in _exprs(json.load(f)):
                names |= names_in(e.replace("$__rate_interval", "5m").replace("$__interval", "1m"))
    return names


def rows_for(name):
    """[(row name, selector template)] for an always-present name; None when it has no row."""
    if name in ROWS:
        return [(r or name, sel) for r, sel in ROWS[name]]
    if name.startswith(ANDARA_PREFIX):
        return [(name, ANDARA)]
    return None


def not_shipped(name):
    return name.startswith(NOT_SHIPPED)


def unclassified(names):
    """Names in no set: each needs a row (always-present) or a place in FAULT_ONLY."""
    return sorted(n for n in names if n not in FAULT_ONLY and not not_shipped(n) and rows_for(n) is None)


def platform(name):
    return name.startswith(PLATFORM_SERIES)


def unknown_selector(name):
    return '{__name__="%s", environment="unknown", namespace=~"andara-.*", namespace!="andara-shared"}' % name


def run(env, ctx, q=gcx, names=None):
    """The keep-list check against ctx for ENV; the exit code."""
    names = source_names() if names is None else names
    loose = unclassified(names)
    ns = "andara-" + env
    counts = {"always-present": [0, 0], "environment": [0, 0]}  # [pass, fail]
    say = lambda m: print("observe-check: keep-list: " + m)  # noqa: E731
    if loose:
        say("FAIL %s: in neither the always-present rows nor FAULT_ONLY" % ", ".join(loose))
        counts["always-present"][1] += len(loose)
    present = True
    for name in sorted(names):
        if name in FAULT_ONLY:
            continue
        if not_shipped(name):
            say("--   not-shipped    %-56s not queried: the keep pipeline drops broker series" % name)
            continue
        for row, tmpl in rows_for(name) or []:
            series = q.metrics_series(ctx, tmpl.format(m=name, env=env, ns=ns), since=SINCE)
            ok = len(series) > 0
            present &= ok
            counts["always-present"][0 if ok else 1] += 1
            say("%-4s always-present %-56s %d series" % ("ok" if ok else "FAIL", row, len(series)))
    for name in FAULT_ONLY:
        say("--   fault-only     %-56s not queried: the keep pipeline must name it; the first drill "
            "that produces the fault must see it" % name)
    # Decision 9: a sender that sets environment outside the four values (and the reserved unknown) is a defect.
    # `metrics labels` has no window, so a value is a failure only while it still has series in SINCE;
    # an older one is reported and ages out with the stack's retention.
    for value in q.metrics_labels(ctx, "environment"):
        if value in ENVIRONMENTS:
            continue
        # json.dumps quotes and escapes " and \ the way a PromQL string literal needs.
        live = q.metrics_series(ctx, "{environment=%s}" % json.dumps(value), since=SINCE)
        if not live:
            say('--   environment    value "%s" has no series in the last %s (stale; ages out)' % (value, SINCE))
            continue
        senders = sorted({'%s{job="%s", namespace="%s"}' % (s.get("__name__", ""), s.get("job", ""), s.get("namespace", ""))
                          for s in live})
        say('FAIL environment     value "%s" is not one of %s: %s'
            % (value, ", ".join(ENVIRONMENTS), ", ".join(senders[:5]) + (" …" if len(senders) > 5 else "")))
        counts["environment"][1] += 1
    if present and not loose:
        for name in sorted(n for n in names if not platform(n) and not not_shipped(n)):
            bad = q.metrics_series(ctx, unknown_selector(name), since=SINCE)
            for s in bad:
                say("FAIL unknown         %s in namespace %s carries environment=\"unknown\""
                    % (name, s.get("namespace")))
            if bad:
                counts["always-present"][1] += 1
    else:
        say("--   unknown guard skipped: it is a pass only when the always-present series exist")
    p, f = counts["always-present"]
    ef = counts["environment"][1]
    say("summary: always-present %d pass, %d fail; environment %d fail; fault-only %d named, not queried"
        % (p, f, ef, len(FAULT_ONLY)))
    return 0 if f == 0 and ef == 0 and present else 1
