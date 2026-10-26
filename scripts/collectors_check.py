#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The k8s-monitoring render's invariants (AW-INF-065 AC-5, AC-8 and AC-9, ADR-0014 §4, §5 and §7).

render_problems() reads `helm template`'s output for both profiles (scripts/collectors.py) and returns
one message per broken invariant: the environment stamp and its guard on all three destinations, the
same table as environments.yaml and byte-identical between box and ci; the kube_ namespace rule (A1);
the keep rule; the stream-label allow-list; the receiver's Service; the autodiscovery remap and its
include-lists; the OTLP-log drop; cluster.name; the keys a profile may set; every feature's default
allow-list off; and no prod (solo7) credential anywhere in the render. keep_problems() is the keep-list
against the rules, the dashboards and the Fleet pipelines; runbook_problems() is the stream-label
allow-list against the runbooks' LogQL.

scripts/tests/test_collectors.py runs each against the committed files (no problems) and against a
fixture that breaks it (that problem, named). `make check` runs them through scripts-test.
"""
import glob
import json
import os
import re

from urllib.parse import urlparse

import yaml

import collectors
import observe_keep_list

REPO = collectors.REPO
RUNBOOKS = os.path.join(REPO, "docs", "runbooks")
DASHBOARDS = os.path.join(REPO, "deploy", "grafana", "dashboards")
PIPELINES = os.path.join(REPO, "deploy", "terraform", "grafana", "pipelines")
KIND_WORKFLOW = os.path.join(REPO, ".github", "workflows", "kind.yaml")
PROD_TFVARS = os.path.join(REPO, "deploy", "terraform", "grafana", "stacks", "solo7", "terraform.tfvars")
# Which pipeline owns a Fleet prefix (ADR-0014 §5).
PIPELINE_FOR = {"kafka_": "shared_kafka", "pg_": "shared_postgres"}
RECEIVER_SERVICE = "k8s-monitoring-alloy-receiver"
RECEIVER_PORTS = (4317, 9090)
# The include-lists (ADR-0014 §1). CI's andara-ci is the drills job's (AW-INF-061), allowed before
# kind.yaml names it.
AUTODISCOVERY = {"box": ["andara-local", "andara-dev", "traefik"], "ci": ["andara-local", "andara-ci", "traefik"]}
POD_LOGS = {"box": ["andara-local", "andara-dev", "andara-shared"], "ci": ["andara-local", "andara-ci"]}
CI_DRILLS_NAMESPACE = "andara-ci"
CLUSTER = {"box": "andara", "ci": "ci"}
PROD_NAMES = ("GRAFANA_SOLO7_",)


# --- reading the rendered Alloy -------------------------------------------------------------------------

def _skip_string(text, i):
    """The index after the string literal starting at text[i] (a `"` or a backtick)."""
    q = text[i]
    i += 1
    while i < len(text):
        if q == '"' and text[i] == "\\":
            i += 2
            continue
        if text[i] == q:
            return i + 1
        i += 1
    return i


def _body(text, open_brace):
    """The text between the `{` at open_brace and its matching `}`."""
    depth, i = 0, open_brace
    while i < len(text):
        c = text[i]
        if c in "\"`":
            i = _skip_string(text, i)
            continue
        if c == "/" and text[i:i + 2] == "//":
            nl = text.find("\n", i)
            i = len(text) if nl < 0 else nl
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
            if depth == 0:
                return text[open_brace + 1:i]
        i += 1
    return None


def component(text, kind, label):
    """The body of `kind "label" { … }` in text, or None."""
    m = re.search(r'(?m)^\s*%s\s+"%s"\s*\{' % (re.escape(kind), re.escape(label)), text)
    return _body(text, m.end() - 1) if m else None


def blocks(body, name):
    """The bodies of the `name { … }` blocks directly inside body, in order."""
    out, depth, i = [], 0, 0
    while i < len(body):
        c = body[i]
        if c in "\"`":
            i = _skip_string(body, i)
            continue
        if c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
        elif depth == 0 and body.startswith(name, i) and (i == 0 or not (body[i - 1].isalnum() or body[i - 1] in "._")):
            m = re.match(r"%s\s*\{" % re.escape(name), body[i:])
            if m:
                start = i + m.end() - 1
                inner = _body(body, start)
                out.append(inner)
                i = start + len(inner) + 2
                continue
        i += 1
    return out


def string(tok):
    tok = tok.strip()
    if tok.startswith("`"):
        return tok[1:-1]
    return json.loads(tok)


STRING = r'"(?:[^"\\]|\\.)*"|`[^`]*`'


def attr(body, key):
    """An attribute's value in a block body: a string, a list of strings, or None."""
    m = re.search(r"(?m)^\s*%s\s*=\s*(%s|\[(?:\s*(?:%s)\s*,?)*\s*\])" % (re.escape(key), STRING, STRING), body)
    if not m:
        return None
    v = m.group(1)
    if v.startswith("["):
        return [string(s) for s in re.findall(STRING, v)]
    return string(v)


def strings(body, key):
    """Every string in `key = [ … ]`, however it is laid out."""
    m = re.search(r"(?m)^\s*%s\s*=\s*\[" % re.escape(key), body)
    if not m:
        return []
    out, i = [], m.end()
    while i < len(body) and body[i] != "]":
        if body[i] in "\"`":
            end = _skip_string(body, i)
            out.append(string(body[i:end]))
            i = end
            continue
        i += 1
    return out


# --- what a destination encodes -------------------------------------------------------------------------

def metric_rules(cfg):
    """[(source_labels, regex, target_label, replacement, action, separator)] of solo7dev-metrics'
    write_relabel_config blocks, or None when cfg has no metrics destination."""
    body = component(cfg, "prometheus.remote_write", "solo7dev_metrics")
    if body is None:
        return None
    endpoint = (blocks(body, "endpoint") or [""])[0]
    return [(tuple(attr(b, "source_labels") or ()), attr(b, "regex"), attr(b, "target_label"),
             attr(b, "replacement"), attr(b, "action") or "replace", attr(b, "separator"))
            for b in blocks(endpoint, "write_relabel_config")]


def expected(envs, keep):
    """What the destinations must carry, rendered by collectors.py from the sources and read back by the
    same parsers that read the chart's output, so a comparison is like for like."""
    metrics = metric_rules('prometheus.remote_write "solo7dev_metrics" {\nendpoint {\n%s}\n}\n'
                           % collectors.metric_rules(envs, keep))
    logs = log_signature('loki.process "solo7dev_logs" {\n%s}\n' % collectors.log_stages(envs))
    return metrics, logs, collectors.trace_statements(envs)


def normalise(text):
    """Alloy text with comments dropped, whitespace collapsed and trailing commas before a close removed, so
    the chart's layout and collectors.py's compare equal and nothing else does."""
    text = re.sub(r"(?m)//[^\n]*$", "", text)
    text = re.sub(r"\s+", " ", text).strip()
    return re.sub(r",\s*([}\]])", r" \1", text).replace(" ", "")


def log_signature(cfg):
    """[(stage name, its normalised body)] of every top-level stage of solo7dev-logs' loki.process, in order;
    None when cfg has no logs destination. Every attribute counts: an action, an extra stage, a label."""
    body = component(cfg, "loki.process", "solo7dev_logs")
    if body is None:
        return None
    return [(name, normalise(inner)) for name, inner in top_blocks(body) if name.startswith("stage.")]


def top_blocks(body):
    """(name, body) of every block directly inside body, in order."""
    out, depth, i = [], 0, 0
    while i < len(body):
        c = body[i]
        if c in "\"`":
            i = _skip_string(body, i)
            continue
        if c == "{":
            if depth == 0:
                m = re.search(r"([A-Za-z_][A-Za-z0-9_.]*)\s*$", body[:i])
                inner = _body(body, i)
                if m and inner is not None:
                    out.append((m.group(1), inner))
                    i += len(inner) + 2
                    continue
            depth += 1
        elif c == "}":
            depth -= 1
        i += 1
    return out


# A1's three rules, by value (source_labels, regex, target_label, replacement, action, separator); the
# middle one's regex is the namespace alternation, checked on its own.
KUBE_MARK = (("__name__",), "kube_.+", "__tmp_kube_namespace", "drop", "replace", None)
KUBE_DROP = (("__tmp_kube_namespace",), "drop", None, None, "drop", None)


def metric_table(rules):
    """(table, guarded, kube namespaces, keep names) read back from metric_rules(), every rule by value:
    a guard that does anything but replace, or an A1 rule that differs from the one that works, reads as
    absent. Nothing here comes from collectors.py, so a defect in the generator can't pass both sides."""
    table, guarded, keep = {}, False, None
    kube_mark = kube_drop = False
    kube_clear = None
    for i, rule in enumerate(rules):
        src, regex, target, repl, action, sep = rule
        if src == ("namespace",) and target == "environment":
            if guarded or action != "replace" or sep is not None:
                return {}, False, None, keep  # a stamp after the guard, or one that doesn't stamp
            for ns in (regex or "").split("|"):
                table[ns] = repl
        elif src == ("environment",) and target == "environment":
            guarded = (regex == "" and repl == collectors.GUARD and action == "replace" and sep is None)
        elif rule == KUBE_MARK:
            kube_mark = True
        elif src == ("__tmp_kube_namespace", "namespace"):
            m = re.match(r"^drop;\((.*)\)$", regex or "")
            ok = (kube_mark and target == "__tmp_kube_namespace" and repl == "" and action == "replace" and sep == ";")
            kube_clear = sorted(m.group(1).split("|")) if (m and ok) else []
        elif rule == KUBE_DROP:
            kube_drop = kube_clear is not None
        elif src == ("__name__",) and action == "keep" and target is None and sep is None:
            keep = sorted((regex or "").split("|"))
    kube = kube_clear if (kube_mark and kube_drop) else []
    return table, guarded, kube, keep


def log_stages(cfg):
    """(table, guarded, label_keep) of solo7dev-logs' loki.process, or None when cfg has none."""
    body = component(cfg, "loki.process", "solo7dev_logs")
    if body is None:
        return None
    table, guarded = {}, False
    for m in blocks(body, "stage.match"):
        sel = attr(m, "selector") or ""
        env = None
        for st in blocks(m, "stage.static_labels"):
            em = re.search(r'environment\s*=\s*(%s)' % STRING, st)
            env = string(em.group(1)) if em else None
        nm = re.fullmatch(r'\{namespace=~"([^"]*)"\}', sel)
        if nm and env and not guarded:
            for ns in nm.group(1).split("|"):
                table[ns] = env
        elif sel == '{environment=""}' and env == collectors.GUARD:
            guarded = True
    keep = [strings(b, "values") for b in blocks(body, "stage.label_keep")]
    tail = [name for name, _ in top_blocks(body)]
    after = tail[tail.index("stage.label_keep") + 1:] if "stage.label_keep" in tail else []
    return table, guarded, (keep[-1] if keep and not after else after or None)


def trace_statements(cfg):
    """solo7dev-traces' resource statements that set `environment`, or None when cfg has none."""
    body = component(cfg, "otelcol.processor.transform", "solo7dev_traces")
    if body is None:
        return None
    out = []
    for b in blocks(body, "trace_statements"):
        if attr(b, "context") == "resource":
            out += [s for s in strings(b, "statements") if "environment" in s]
    return out


def trace_table(statements):
    table, guarded = {}, False
    for s in statements:
        m = re.fullmatch(r'set\(attributes\["environment"\], "([^"]+)"\) where (.*)', s)
        if not m:
            continue
        if m.group(2) == 'attributes["environment"] == nil' and m.group(1) == collectors.GUARD:
            guarded = True
            continue
        if guarded:
            return {}, False
        for ns in re.findall(r'attributes\["k8s\.namespace\.name"\] == "([^"]+)"', m.group(2)):
            table[ns] = m.group(1)
    return table, guarded


# --- the render's invariants ----------------------------------------------------------------------------

def first_difference(got, want):
    for i, (g, w) in enumerate(zip(got, want)):
        if g != w:
            return "at %d: %s, want %s" % (i, g, w)
    return "%d entries, want %d" % (len(got), len(want))


def render_problems(renders, envs, profiles, chart_dir, kind_text=None, prod_tfvars=None, keep=None):
    """renders: {profile: (values, docs)}; profiles: {profile: its file's parsed doc}. Each destination's
    blocks are compared in full with what collectors.py renders from environments.yaml and keep-list.yaml
    (keep, when given, stands in for keep-list.yaml); the narrower checks name the usual breakages."""
    problems = []
    say = problems.append
    blocks_by_profile = {}
    keep_entries = collectors.keep_list() if keep is None else keep
    want_metrics, want_logs, want_traces = expected(envs, keep_entries)
    for profile, (vals, docs) in sorted(renders.items()):
        cfgs = collectors.alloy_configs(docs)
        seen = {"metrics": 0, "logs": 0, "traces": 0}
        signature = []
        for name, cfg in sorted(cfgs.items()):
            rules = metric_rules(cfg)
            if rules is not None:
                seen["metrics"] += 1
                table, guarded, kube, keep = metric_table(rules)
                if table != envs:
                    say("%s/%s: solo7dev-metrics' stamp encodes %s, environments.yaml %s" % (profile, name, table, envs))
                if not guarded:
                    say("%s/%s: solo7dev-metrics has no unknown guard (a replace) after its stamp: a namespace "
                        "outside the table would carry no environment" % (profile, name))
                if kube != sorted(envs):
                    say("%s/%s: solo7dev-metrics' kube_ namespace rule keeps %s, not environments.yaml's keys %s (A1)"
                        % (profile, name, kube, sorted(envs)))
                if keep is None:
                    say("%s/%s: solo7dev-metrics has no keep rule" % (profile, name))
                elif keep != sorted(e["name"] for e in keep_entries):
                    say("%s/%s: solo7dev-metrics' keep rule keeps %d names, not keep-list.yaml's %d (missing: %s; extra: %s)"
                        % (profile, name, len(keep), len(keep_entries),
                           ", ".join(sorted({e["name"] for e in keep_entries} - set(keep))) or "none",
                           ", ".join(sorted(set(keep) - {e["name"] for e in keep_entries})) or "none"))
                elif rules[-1][4] != "keep":
                    say("%s/%s: solo7dev-metrics' keep rule is not its last rule" % (profile, name))
                if rules != want_metrics:
                    say("%s/%s: solo7dev-metrics' rules are not what environments.yaml and keep-list.yaml render, %s"
                        % (profile, name, first_difference(rules, want_metrics)))
                signature.append(("metrics", rules))
            stages = log_stages(cfg)
            if stages is not None:
                seen["logs"] += 1
                table, guarded, keep = stages
                if table != envs:
                    say("%s/%s: solo7dev-logs' stamp encodes %s, environments.yaml %s" % (profile, name, table, envs))
                if not guarded:
                    say("%s/%s: solo7dev-logs has no unknown guard after its stamp" % (profile, name))
                if keep != list(collectors.LOG_LABELS):
                    say("%s/%s: solo7dev-logs keeps stream labels %s, not the allow-list %s"
                        % (profile, name, keep, list(collectors.LOG_LABELS)))
                # The chart's own cluster stage comes first (clusterLabels); ours must be everything after it.
                sig = log_signature(cfg)
                while sig and sig[0][0] == "stage.static_labels" and re.fullmatch(r'values=\{"cluster"="[^"]*"\}', sig[0][1]):
                    sig = sig[1:]
                if sig != want_logs:
                    say("%s/%s: solo7dev-logs' stages are not what environments.yaml renders, %s"
                        % (profile, name, first_difference(sig, want_logs)))
                signature.append(("logs", sig))
            stmts = trace_statements(cfg)
            if stmts is not None:
                seen["traces"] += 1
                table, guarded = trace_table(stmts)
                if table != envs:
                    say("%s/%s: solo7dev-traces' stamp encodes %s, environments.yaml %s" % (profile, name, table, envs))
                if not guarded:
                    say("%s/%s: solo7dev-traces has no unknown guard after its stamp" % (profile, name))
                if stmts != want_traces:
                    say("%s/%s: solo7dev-traces' environment statements are not what environments.yaml renders, %s"
                        % (profile, name, first_difference(stmts, want_traces)))
                signature.append(("traces", stmts))
            for m in re.finditer(r'loki\.resource\.labels\\?"\], \\?"([^"\\]*)\\?"', cfg):
                extra = sorted({x.strip() for x in m.group(1).split(",")} - set(collectors.LOG_LABELS))
                if extra:
                    say("%s/%s: OTLP log records name stream labels outside the allow-list: %s"
                        % (profile, name, ", ".join(extra)))
        for kind, n in seen.items():
            if not n:
                say("%s: no collector renders the solo7dev-%s destination" % (profile, kind))
        blocks_by_profile[profile] = signature
        problems += collector_problems(profile, cfgs, docs)
        problems += cluster_problems(profile, cfgs)
        problems += ["%s: values-%s.yaml sets %s, outside the keys a profile may set (%s)"
                     % (profile, profile, p, ", ".join(collectors.PROFILE_KEYS))
                     for p in collectors.profile_problems(profiles.get(profile))]
        problems += allow_list_problems(profile, vals, chart_dir)
        problems += prod_problems(profile, vals, docs, prod_tfvars)
    if "box" in blocks_by_profile and "ci" in blocks_by_profile and blocks_by_profile["box"] != blocks_by_profile["ci"]:
        say("ci: the stamp, guard and keep blocks differ from box's; they must be byte-identical (ADR-0014 §7)")
    if "ci" in renders:
        problems += ci_namespace_problems(collectors.alloy_configs(renders["ci"][1]), kind_text)
    return problems


def autodiscovery_namespaces(cfgs):
    body = component(cfgs.get("metrics", ""), "declare", "annotation_autodiscovery") or ""
    disc = component(body, "discovery.kubernetes", "pods") or ""
    ns = blocks(disc, "namespaces")
    return strings(ns[0], "names") if ns else None


def collector_problems(profile, cfgs, docs):
    out = []
    say = out.append
    want = ["fleet", "logs", "metrics", "receiver"] if profile == "box" else ["logs", "metrics", "receiver"]
    if sorted(cfgs) != want:
        say("%s: collectors %s, not %s" % (profile, sorted(cfgs), want))
    names = autodiscovery_namespaces(cfgs)
    if names is None:
        say("%s: annotation autodiscovery has no namespace include-list" % profile)
    else:
        if "andara-shared" in names:
            say("%s: annotation autodiscovery includes andara-shared, whose brokers are Fleet's (ADR-0014 §3)" % profile)
        if sorted(names) != sorted(AUTODISCOVERY[profile]):
            say("%s: annotation autodiscovery includes %s, not %s" % (profile, names, AUTODISCOVERY[profile]))
    pods = component(component(cfgs.get("logs", ""), "declare", "pod_logs_via_loki") or "", "discovery.kubernetes", "pods") or ""
    ns = blocks(pods, "namespaces")
    if not ns or sorted(strings(ns[0], "names")) != sorted(POD_LOGS[profile]):
        say("%s: pod logs read %s, not %s" % (profile, strings(ns[0], "names") if ns else None, POD_LOGS[profile]))
    ad = component(cfgs.get("metrics", ""), "declare", "annotation_autodiscovery") or ""
    for annotation in ("prometheus_io_scrape", "prometheus_io_port", "prometheus_io_path"):
        if "__meta_kubernetes_pod_annotation_%s" % annotation not in ad:
            say("%s: annotation autodiscovery does not read prometheus.io/%s" % (profile, annotation.split("_")[-1]))
    jobs = set(re.findall(r'"(__meta_kubernetes_pod_annotation_[a-z0-9_]*job)"', ad))
    if jobs != {"__meta_kubernetes_pod_annotation_k8s_grafana_com_job"}:
        say("%s: annotation autodiscovery reads job from %s, not k8s.grafana.com/job" % (profile, sorted(jobs) or "nothing"))
    fleet = cfgs.get("fleet")
    if fleet is not None:
        writers = sorted(set(re.findall(r'(?m)^\s*(prometheus\.remote_write|loki\.write|otelcol\.exporter\.[a-z_]+)\s+"', fleet)))
        if writers:
            say("%s: fleet renders its own writers (%s); its only output is the receiver, through Fleet's pipelines "
                "(ADR-0014 §3)" % (profile, ", ".join(writers)))
    rcv = cfgs.get("receiver", "")
    flt = component(component(rcv, "declare", "application_observability") or "", "otelcol.processor.filter", "default")
    logs = blocks(flt or "", "logs")
    if not logs or strings(logs[0], "log_record") != ["true"]:
        say("%s: the receiver does not drop every OTLP log record (ADR-0014 §1)" % profile)
    recv = component(component(rcv, "declare", "prometheus_metrics_receiver") or "", "prometheus.receive_http", "default")
    if not recv or '"%d"' % RECEIVER_PORTS[1] not in recv:
        say("%s: the receiver's remote-write listener is not on %d" % (profile, RECEIVER_PORTS[1]))
    svcs = [d for d in docs if d.get("kind") == "Service" and d["metadata"]["name"] == RECEIVER_SERVICE]
    if not svcs:
        say("%s: no Service %s" % (profile, RECEIVER_SERVICE))
    else:
        ports = sorted(p["port"] for p in svcs[0]["spec"].get("ports") or [])
        for port in RECEIVER_PORTS:
            if port not in ports:
                say("%s: Service %s has no port %d (ports %s)" % (profile, RECEIVER_SERVICE, port, ports))
    if "node-exporter" in " ".join(d["metadata"]["name"] for d in docs if d.get("kind") in ("DaemonSet", "Deployment")):
        say("%s: node-exporter is deployed; no rule or panel reads node_* (ADR-0014 §1)" % profile)
    return out


def external_labels(cfg, kind, label):
    """{label: value} of a component's external_labels, or None when it has none."""
    body = component(cfg, kind, label)
    m = re.search(r"external_labels\s*=\s*\{([^}]*)\}", body or "")
    if not m:
        return None
    return {k.strip('"'): string(v)
            for k, v in re.findall(r'("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s*=\s*(%s)' % STRING, m.group(1))}


def cluster_problems(profile, cfgs):
    """Every metric, log and trace destination stamps cluster with the profile's name (A3), and the logs
    destination adds no stream label outside the allow-list after its label_keep."""
    out = []
    want = CLUSTER[profile]
    for name, cfg in sorted(cfgs.items()):
        for kind, label, what in (("prometheus.remote_write", "solo7dev_metrics", "metrics"),
                                  ("loki.write", "solo7dev_logs", "logs")):
            if component(cfg, kind, label) is None:
                continue
            labels = external_labels(cfg, kind, label) or {}
            if labels.get("cluster") != want:
                out.append('%s/%s: solo7dev-%s carries cluster=%s, not "%s"'
                           % (profile, name, what, labels.get("cluster"), want))
            if what == "logs":
                extra = sorted(set(labels) - set(collectors.LOG_LABELS))
                if extra:
                    out.append("%s/%s: solo7dev-logs adds stream labels outside the allow-list after label_keep: %s"
                               % (profile, name, ", ".join(extra)))
        body = component(cfg, "otelcol.processor.transform", "solo7dev_traces")
        if body is not None:
            stmts = [x for b in blocks(body, "trace_statements") for x in strings(b, "statements")]
            if 'set(attributes["cluster"], "%s")' % want not in stmts:
                out.append('%s/%s: solo7dev-traces does not set cluster="%s"' % (profile, name, want))
    return out


def allow_list_problems(profile, vals, chart_dir):
    """Every useDefaultAllowList the merged values reach is false, and every source the chart defaults
    true is set false explicitly, so a chart upgrade that adds one can't turn it on unnoticed."""
    out = []

    def walk(node, path):
        if isinstance(node, dict):
            for k, v in node.items():
                p = "%s.%s" % (path, k) if path else k
                if k == "useDefaultAllowList" and v is not False:
                    out.append("%s: %s is %r; the destination's keep rule is the one filter (ADR-0014 §5)" % (profile, p, v))
                walk(v, p)
    walk(vals, "")
    for feature, key in (("feature-cluster-metrics", "clusterMetrics"),):
        with open(os.path.join(chart_dir, "charts", feature, "values.yaml")) as f:
            defaults = yaml.safe_load(f) or {}
        for source, sv in defaults.items():
            if isinstance(sv, dict) and (sv.get("metricsTuning") or {}).get("useDefaultAllowList") is True:
                got = (((vals.get(key) or {}).get(source) or {}).get("metricsTuning") or {}).get("useDefaultAllowList")
                if got is not False:
                    out.append("%s: %s.%s.metricsTuning.useDefaultAllowList is not set false (the chart defaults it true)"
                               % (profile, key, source))
    return out


def prod_values(path=None):
    """solo7's (prod's) endpoint values, which must never be rendered for this cluster."""
    path = path or PROD_TFVARS
    return {k: v for k, v in collectors.tfvars(path).items() if k.endswith(("_url", "_user")) and v}


def prod_problems(profile, vals, docs, prod_tfvars=None):
    """Prod's endpoint values, and each prod url's host however it is spelled (Tempo renders host:443)."""
    text = yaml.safe_dump(vals) + yaml.safe_dump_all(docs)
    out = []
    for k, v in sorted(prod_values(prod_tfvars).items()):
        host = urlparse(v).hostname if k.endswith("_url") else None
        if v in text or (host and host in text):
            out.append("%s: the render names prod's %s (%s); prod's credentials never reach this cluster (AW-INF-078)"
                       % (profile, k, v))
    out += ["%s: the render names %s, a prod credential" % (profile, n) for n in PROD_NAMES if n in text]
    return out


def kind_namespaces(text):
    return sorted(set(re.findall(r"\bNS=([a-z0-9-]+)", text)))


def ci_namespace_problems(cfgs, kind_text=None):
    if kind_text is None:
        with open(KIND_WORKFLOW) as f:
            kind_text = f.read()
    names = autodiscovery_namespaces(cfgs) or []
    required = set(kind_namespaces(kind_text)) | {"traefik"}
    allowed = required | {CI_DRILLS_NAMESPACE}
    out = ["ci: annotation autodiscovery misses %s, where kind.yaml deploys the server" % ns
           for ns in sorted(required - set(names))]
    out += ["ci: annotation autodiscovery includes %s, which kind.yaml never deploys into" % ns
            for ns in sorted(set(names) - allowed)]
    return out


# --- the keep-list against its readers ------------------------------------------------------------------

def source_names(alerts=None, dashboards=None):
    """Every metric name the rules and every dashboard under deploy/grafana/dashboards/ read."""
    alerts = alerts or observe_keep_list.ALERTS
    paths = dashboards if dashboards is not None else sorted(glob.glob(os.path.join(DASHBOARDS, "*.json")))
    names = observe_keep_list.source_names(alerts=alerts, dashboard=os.path.join(DASHBOARDS, "absent"))
    for p in paths:
        names |= _dashboard_names(p)
    return names


def _dashboard_names(path):
    with open(path) as f:
        doc = json.load(f)
    names = set()
    for e in observe_keep_list._exprs(doc):
        names |= observe_keep_list.names_in(e.replace("$__rate_interval", "5m").replace("$__interval", "1m"))
    return names


def pipeline_keeps(text):
    """The names a Fleet pipeline's prometheus.relabel keep rule keeps."""
    out = set()
    for m in re.finditer(r'(?m)^prometheus\.relabel\s+"[^"]+"\s*\{', text):
        body = _body(text, m.end() - 1) or ""
        for rule in blocks(body, "rule"):
            if attr(rule, "action") == "keep" and attr(rule, "source_labels") == ["__name__"]:
                out |= set((attr(rule, "regex") or "").split("|"))
    return out


def keep_problems(keep, names, pipelines):
    """keep: keep-list.yaml's entries; names: source_names(); pipelines: {pipeline name: its text}."""
    out = []
    kept = {e["name"] for e in keep}
    fleet = {e["name"] for e in keep if e.get("owner") == "fleet"}
    out += ["%s: read by a rule or dashboard panel, missing from keep-list.yaml" % n for n in sorted(names - kept)]
    for n in sorted(fleet):
        prefix = next(p for p in PIPELINE_FOR if n.startswith(p))
        pipeline = PIPELINE_FOR[prefix]
        if pipeline not in pipelines:
            out.append("%s: owner fleet, but no pipeline %s.alloy keeps it" % (n, pipeline))
        elif n not in pipeline_keeps(pipelines[pipeline]):
            out.append("%s: owner fleet, but %s.alloy does not keep it" % (n, pipeline))
    for pipeline, text in sorted(pipelines.items()):
        for n in sorted(pipeline_keeps(text) - {"up"}):
            if n not in fleet:
                out.append("%s: %s.alloy keeps it, but keep-list.yaml does not list it with owner: fleet, so the "
                           "destination drops it" % (n, pipeline))
    return out


def pipelines(directory=PIPELINES):
    out = {}
    for p in sorted(glob.glob(os.path.join(directory, "*.alloy"))):
        with open(p) as f:
            out[os.path.splitext(os.path.basename(p))[0]] = f.read()
    return out


# --- the runbooks' LogQL --------------------------------------------------------------------------------

# A stream selector is LogQL, not PromQL, when a line filter or parser follows it (on its line or the
# next), when it is the argument of a log range function (count_over_time({…}[5m])), or when it sits in a
# ```logql block. service_name is Loki's alone (no kept series carries it), so a selector naming it is
# always LogQL.
SELECTOR = re.compile(r"\{([^{}]*)\}")
# A line filter takes a string: `!= 0` is PromQL's comparison, `!= "x"` is LogQL's filter.
PIPELINE = re.compile(r"\s*(?:(?:\|=|\|~|!=|!~|\|>|!>)\s*[\"`]"
                      r"|\|\s*(?:json|logfmt|pattern|regexp|unpack|unwrap|decolorize|line_format|label_format|drop|keep)\b)")
LOG_RANGE = re.compile(r"\b(?:count_over_time|bytes_over_time|bytes_rate|absent_over_time|rate|rate_counter|"
                       r"sum_over_time|avg_over_time|max_over_time|min_over_time|first_over_time|last_over_time|"
                       r"stdvar_over_time|stddev_over_time|quantile_over_time)\((?:[^(){}]*,)?\s*$")
FENCE = re.compile(r"(?ms)^```logql[ \t]*$(.*?)^```")
MATCHER = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)\s*(?:=~|!~|!=|=)\s*"')
LOKI_ONLY = ("service_name",)


def logql_selectors(text):
    """(offset, labels) of every selector in text that reads as LogQL."""
    fenced = [(m.start(1), m.end(1)) for m in FENCE.finditer(text)]
    for m in SELECTOR.finditer(text):
        labels = MATCHER.findall(m.group(1))
        if not labels:
            continue
        piped = PIPELINE.match(text, m.end())
        ranged = LOG_RANGE.search(text, max(0, m.start() - 60), m.start()) and re.match(r"\s*(?:\||\[)", text[m.end():])
        fence = any(a <= m.start() < b for a, b in fenced)
        if piped or ranged or fence or set(labels) & set(LOKI_ONLY):
            yield m.start(), labels


def runbook_problems(directory=RUNBOOKS):
    out = []
    for path in sorted(glob.glob(os.path.join(directory, "*.md"))):
        with open(path) as f:
            text = f.read()
        for offset, labels in logql_selectors(text):
            n = text.count("\n", 0, offset) + 1
            for label in labels:
                if label not in collectors.LOG_LABELS:
                    out.append("%s:%d: a LogQL selector names %s, outside the stream-label allow-list %s"
                               % (os.path.relpath(path, REPO), n, label, ", ".join(collectors.LOG_LABELS)))
    return out
