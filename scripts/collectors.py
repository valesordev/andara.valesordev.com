#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The k8s-monitoring release's values, rendered (AW-INF-065, ADR-0014).

`make platform-up` installs grafana/k8s-monitoring from deploy/platform/collectors/: values.yaml, then the
profile's file (values-box.yaml or values-ci.yaml), then what this module renders from the committed
sources. The rendered part is computed at install, never committed, and is the same on every run, so
platform-up's values comparison reports a matching release `unchanged`:

  - each destination's url, and fleet's Fleet Management url, from solo7dev's terraform.tfvars;
  - the environment stamp from environments.yaml (ADR-0012 decision 9's table), with its `unknown` guard
    last: write_relabel_config blocks on solo7dev-metrics, loki.process stages on solo7dev-logs, and OTTL
    resource statements on solo7dev-traces (ADR-0014 §4);
  - the stream-label allow-list on solo7dev-logs (environment, namespace, cluster, job, container);
  - the keep rule from keep-list.yaml, last on solo7dev-metrics, with `kube_` names kept only from
    environments.yaml's namespaces (ADR-0014 §5, amendment A1).

The render tests (scripts/tests/test_collectors.py, in `make check`) run `helm template` on the pinned
chart with each profile, through chart() and template() here.

  values [box|ci]   print the merged values for a profile, as platform-up installs them
  template [box|ci] print `helm template`'s output for a profile (the chart is fetched into
                    .local/charts/ on first use)

Exit: 0 ok · 1 a source is invalid or helm failed · 2 usage.
"""
import copy
import os
import re
import subprocess
import sys

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DIR = os.path.join(REPO, "deploy", "platform", "collectors")
VALUES = os.path.join(DIR, "values.yaml")
PROFILE_VALUES = {"box": os.path.join(DIR, "values-box.yaml"), "ci": os.path.join(DIR, "values-ci.yaml")}
ENVIRONMENTS = os.path.join(DIR, "environments.yaml")
KEEP_LIST = os.path.join(DIR, "keep-list.yaml")
VERSIONS = os.path.join(REPO, "deploy", "platform", "versions.yaml")
TFVARS = os.path.join(REPO, "deploy", "terraform", "grafana", "stacks", "solo7dev", "terraform.tfvars")
CHART_CACHE = os.path.join(REPO, ".local", "charts")

RELEASE = "k8s-monitoring"
NAMESPACE = "observability"
SECRET = "grafana-telemetry"
TOKEN = "GRAFANA_SOLO7DEV_TELEMETRY_TOKEN"
# Destination -> (tfvars url key, tfvars user key, the Secret's username key).
DESTINATIONS = {
    "solo7dev-metrics": ("prom_url", "prom_user", "prom"),
    "solo7dev-logs": ("loki_url", "loki_user", "loki"),
    "solo7dev-traces": ("tempo_url", "tempo_user", "tempo"),
}
FLEET = ("fleet_url", "fleet_user", "fleet")
# ADR-0012 decision 9's values; `unknown` is the guard's alone.
ENV_VALUES = ("local", "dev", "staging", "prod")
GUARD = "unknown"
# The only Loki stream labels (ADR-0014 §10, from AW-INF-051).
LOG_LABELS = ("environment", "namespace", "cluster", "job", "container")
FLEET_PREFIXES = ("kafka_", "pg_")
NAMESPACE_RE = re.compile(r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$")
METRIC_RE = re.compile(r"^[a-zA-Z_:][a-zA-Z0-9_:]*$")
# The keys a profile file may set, as dotted paths (ADR-0014 §7).
PROFILE_KEYS = ("cluster.name", "annotationAutodiscovery.namespaces", "podLogsViaLoki.namespaces", "collectors.fleet")


class Invalid(Exception):
    code = 1


# --- sources --------------------------------------------------------------------------------------------

def load_yaml(path):
    with open(path) as f:
        return yaml.safe_load(f)


def environments(path=ENVIRONMENTS):
    """environments.yaml as {namespace: environment}, validated."""
    doc = load_yaml(path)
    if not isinstance(doc, dict) or not doc:
        raise Invalid("%s: not a non-empty mapping of namespace to environment" % rel(path))
    for ns, env in doc.items():
        if not isinstance(ns, str) or not NAMESPACE_RE.match(ns):
            raise Invalid("%s: %r is not a namespace name" % (rel(path), ns))
        if env == GUARD:
            raise Invalid("%s: %s maps to %s, which is reserved for the guard" % (rel(path), ns, GUARD))
        if env not in ENV_VALUES:
            raise Invalid("%s: %s maps to %r, not one of %s" % (rel(path), ns, env, ", ".join(ENV_VALUES)))
    return doc


def keep_list(path=KEEP_LIST):
    """keep-list.yaml's entries, validated: [{name, reader, owner?}]."""
    doc = load_yaml(path)
    entries = (doc or {}).get("metrics") if isinstance(doc, dict) else None
    if not isinstance(entries, list) or not entries:
        raise Invalid("%s: no `metrics` list" % rel(path))
    seen = set()
    for e in entries:
        name = (e or {}).get("name")
        if not isinstance(name, str) or not METRIC_RE.match(name):
            raise Invalid("%s: %r is not a metric name" % (rel(path), name))
        if name in seen:
            raise Invalid("%s: %s is listed twice" % (rel(path), name))
        seen.add(name)
        if not str(e.get("reader") or "").strip():
            raise Invalid("%s: %s names no reader" % (rel(path), name))
        extra = sorted(set(e) - {"name", "reader", "owner"})
        if extra:
            raise Invalid("%s: %s: unknown field(s) %s" % (rel(path), name, ", ".join(extra)))
        owner = e.get("owner")
        if owner not in (None, "fleet"):
            raise Invalid("%s: %s: owner must be fleet or absent, not %r" % (rel(path), name, owner))
        if (owner == "fleet") != name.startswith(FLEET_PREFIXES):
            raise Invalid("%s: %s: a name with a %s prefix is owner: fleet, and no other is (ADR-0014 §5)"
                          % (rel(path), name, " or ".join(FLEET_PREFIXES)))
    return entries


def tfvars(path=TFVARS):
    """A terraform.tfvars file's `key = "string"` lines, as a dict. Other lines are skipped."""
    out = {}
    with open(path) as f:
        for line in f:
            m = re.match(r'^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*"([^"]*)"\s*(?:#.*)?$', line)
            if m:
                out[m.group(1)] = m.group(2)
    return out


def rel(path):
    return os.path.relpath(path, REPO)


# --- the rendered blocks --------------------------------------------------------------------------------

def by_environment(envs):
    """[(environment, [namespaces])], in a fixed order: environment values as ENV_VALUES lists them,
    namespaces sorted."""
    return [(v, sorted(ns for ns, e in envs.items() if e == v)) for v in ENV_VALUES if v in envs.values()]


def alternation(names):
    return "|".join(names)


def block(name, fields):
    """An Alloy block from [(attribute, value)]; strings are quoted, lists are string arrays."""
    lines = ["%s {" % name]
    for k, v in fields:
        if isinstance(v, list):
            v = "[" + ", ".join(quote(x) for x in v) + "]"
        else:
            v = quote(v)
        lines.append("  %s = %s" % (k, v))
    lines.append("}")
    return "\n".join(lines)


def quote(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


def metric_rules(envs, keep):
    """solo7dev-metrics' metricProcessingRules: the stamp, the guard, A1's kube_ namespace rule, keep."""
    rules = []
    for env, namespaces in by_environment(envs):
        rules.append(block("write_relabel_config", [
            ("source_labels", ["namespace"]), ("regex", alternation(namespaces)),
            ("target_label", "environment"), ("replacement", env)]))
    rules.append(block("write_relabel_config", [
        ("source_labels", ["environment"]), ("regex", ""),
        ("target_label", "environment"), ("replacement", GUARD)]))
    # A1: a kube_ series survives only from a namespace the table maps. Mark every kube_ series, clear
    # the mark where its namespace is a key, drop what is still marked. The mark is a __ label, and an
    # empty value removes it, so nothing reaches the stack.
    rules.append(block("write_relabel_config", [
        ("source_labels", ["__name__"]), ("regex", "kube_.+"),
        ("target_label", "__tmp_kube_namespace"), ("replacement", "drop")]))
    rules.append(block("write_relabel_config", [
        ("source_labels", ["__tmp_kube_namespace", "namespace"]), ("separator", ";"),
        ("regex", "drop;(%s)" % alternation(kube_namespaces(envs))),
        ("target_label", "__tmp_kube_namespace"), ("replacement", "")]))
    rules.append(block("write_relabel_config", [
        ("source_labels", ["__tmp_kube_namespace"]), ("regex", "drop"), ("action", "drop")]))
    rules.append(block("write_relabel_config", [
        ("source_labels", ["__name__"]), ("regex", alternation(sorted(e["name"] for e in keep))),
        ("action", "keep")]))
    return "\n".join(rules) + "\n"


def kube_namespaces(envs):
    return sorted(envs)


def log_stages(envs):
    """solo7dev-logs' logProcessingStages: the stamp, the guard, then the stream-label allow-list."""
    stages = []
    for env, namespaces in by_environment(envs):
        stages.append(match_stage('{namespace=~"%s"}' % alternation(namespaces), env))
    stages.append(match_stage('{environment=""}', GUARD))
    stages.append("stage.label_keep {\n  values = [%s]\n}" % ", ".join(quote(x) for x in LOG_LABELS))
    return "\n".join(stages) + "\n"


def match_stage(selector, env):
    return ("stage.match {\n  selector = %s\n  stage.static_labels {\n    values = {\n      environment = %s,\n"
            "    }\n  }\n}" % (quote(selector), quote(env)))


def trace_statements(envs):
    """solo7dev-traces' resource transform statements: the stamp on k8s.namespace.name, then the guard."""
    out = []
    for env, namespaces in by_environment(envs):
        cond = " or ".join('attributes["k8s.namespace.name"] == %s' % quote(ns) for ns in namespaces)
        out.append('set(attributes["environment"], %s) where %s' % (quote(env), cond))
    out.append('set(attributes["environment"], %s) where attributes["environment"] == nil' % quote(GUARD))
    return out


# --- the merged values ----------------------------------------------------------------------------------

def merge(base, over):
    """over deep-merged onto a copy of base: mappings merge, anything else replaces."""
    out = copy.deepcopy(base)
    for k, v in (over or {}).items():
        if isinstance(v, dict) and isinstance(out.get(k), dict):
            out[k] = merge(out[k], v)
        else:
            out[k] = copy.deepcopy(v)
    return out


def rendered(profile_doc, envs, keep, tfv):
    """The part of the values computed from the sources, for a profile whose file is profile_doc."""
    missing = [k for d in DESTINATIONS.values() for k in d[:2] if not tfv.get(k)]
    if "fleet" in ((profile_doc or {}).get("collectors") or {}):
        missing += [k for k in FLEET[:2] if not tfv.get(k)]
    if missing:
        raise Invalid("%s: no %s" % (rel(TFVARS), ", ".join(missing)))
    dest = {name: {"url": tfv[url]} for name, (url, _, _) in DESTINATIONS.items()}
    # The chart requires grpc for Grafana Cloud Traces, whose gRPC endpoint is the host on 443; the
    # tfvars url is the OTLP/HTTP one (…/otlp), so only its host is taken.
    dest["solo7dev-traces"]["url"] = tempo_grpc(tfv[DESTINATIONS["solo7dev-traces"][0]])
    dest["solo7dev-metrics"]["metricProcessingRules"] = metric_rules(envs, keep)
    dest["solo7dev-logs"]["logProcessingStages"] = log_stages(envs)
    dest["solo7dev-traces"]["processors"] = {"transform": {"traces": {"resource": trace_statements(envs)}}}
    out = {"destinations": dest}
    if "fleet" in ((profile_doc or {}).get("collectors") or {}):
        out["collectors"] = {"fleet": {"remoteConfig": {"url": tfv[FLEET[0]]}}}
    return out


def tempo_grpc(url):
    m = re.match(r"^https://([^/:]+)(?::443)?(?:/.*)?$", url)
    if not m:
        raise Invalid("%s: tempo_url %r is not an https URL" % (rel(TFVARS), url))
    return m.group(1) + ":443"


def profile_doc(profile):
    if profile not in PROFILE_VALUES:
        raise Invalid("profile %r: must be one of %s" % (profile, ", ".join(PROFILE_VALUES)))
    return load_yaml(PROFILE_VALUES[profile]) or {}


def values(profile, base=None, prof=None, envs=None, keep=None, tfv=None):
    """The values platform-up installs for profile: values.yaml, the profile, then rendered()."""
    base = load_yaml(VALUES) if base is None else base
    prof = profile_doc(profile) if prof is None else prof
    envs = environments() if envs is None else envs
    keep = keep_list() if keep is None else keep
    tfv = tfvars() if tfv is None else tfv
    return merge(merge(base, prof), rendered(prof, envs, keep, tfv))


def usernames(tfv=None, fleet=True):
    """The Secret's non-secret keys: each destination's username, and fleet's."""
    tfv = tfvars() if tfv is None else tfv
    out = {key: tfv[user] for _, user, key in DESTINATIONS.values()}
    if fleet:
        out[FLEET[2]] = tfv[FLEET[1]]
    return out


def leaf_paths(node, path=""):
    """Dotted paths of a mapping's leaves; a key in PROFILE_KEYS is a leaf whatever it holds."""
    if isinstance(node, dict) and path not in PROFILE_KEYS:
        for k, v in node.items():
            yield from leaf_paths(v, "%s.%s" % (path, k) if path else str(k))
    else:
        yield path


def profile_problems(doc):
    """The paths a profile file sets outside PROFILE_KEYS."""
    return sorted(p for p in leaf_paths(doc or {}) if p not in PROFILE_KEYS)


# --- the chart ------------------------------------------------------------------------------------------

def pin(path=VERSIONS):
    entry = (load_yaml(path) or {}).get("collectors") or {}
    if not all(entry.get(k) for k in ("repo", "chart", "version")):
        raise Invalid("%s: collectors has no repo, chart and version" % rel(path))
    return entry["repo"], entry["chart"], entry["version"]


def helm_tool():
    return os.environ.get("HELM") or os.path.join(REPO, "bin", "helm")


def chart(cache=CHART_CACHE, helm=None):
    """The pinned chart's directory, pulled into cache/<chart>-<version>/ on first use."""
    repo, name, version = pin()
    root = os.path.join(cache, "%s-%s" % (name, version))
    path = os.path.join(root, name)
    if os.path.exists(os.path.join(path, "Chart.yaml")):
        return path
    os.makedirs(root, exist_ok=True)
    p = subprocess.run([helm or helm_tool(), "pull", name, "--repo", repo, "--version", version,
                        "--untar", "--untardir", root], capture_output=True, text=True)
    if p.returncode != 0:
        raise Invalid("helm pull %s %s from %s failed: %s" % (name, version, repo, (p.stderr or p.stdout).strip()))
    return path


def template(vals, chart_path=None, helm=None):
    """helm template of the release with vals, CRDs included: the parsed documents."""
    chart_path = chart_path or chart(helm=helm)
    p = subprocess.run([helm or helm_tool(), "template", RELEASE, chart_path, "--namespace", NAMESPACE,
                        "--include-crds", "--values", "-"], input=yaml.safe_dump(vals),
                       capture_output=True, text=True)
    if p.returncode != 0:
        raise Invalid("helm template failed: %s" % (p.stderr or p.stdout).strip())
    return [d for d in yaml.safe_load_all(p.stdout) if d]


def alloy_configs(docs):
    """{collector: its config.alloy} from the rendered ConfigMaps (k8s-monitoring-<collector>)."""
    out = {}
    prefix = RELEASE + "-"
    for d in docs:
        if d.get("kind") == "ConfigMap" and "config.alloy" in (d.get("data") or {}):
            name = d["metadata"]["name"]
            out[name[len(prefix):] if name.startswith(prefix) else name] = d["data"]["config.alloy"]
    return out


def main(argv):
    if len(argv) != 2 or argv[0] not in ("values", "template") or argv[1] not in PROFILE_VALUES:
        print("usage: scripts/collectors.py values|template box|ci", file=sys.stderr)
        return 2
    try:
        vals = values(argv[1])
        if argv[0] == "values":
            sys.stdout.write(yaml.safe_dump(vals, sort_keys=True))
        else:
            sys.stdout.write(yaml.safe_dump_all(template(vals), sort_keys=False))
    except (Invalid, OSError, yaml.YAMLError) as e:
        print("collectors: %s" % e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
