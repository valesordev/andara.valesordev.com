#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The one gcx wrapper (ADR-0012 decision 2; AW-INF-075).

    scripts/gcx.py config      make gcx-config: write $GCX_CONFIG, one context per stack whose read token is set

No script, make target or workflow calls Grafana's query or alerting HTTP APIs itself; it calls a
function here, which runs `bin/gcx --config $GCX_CONFIG --context <stack> … -o json`, maps a
non-zero exit to GcxError (exit 1 for the caller) with gcx's message, and normalises gcx's empty
shapes (`null`, an all-null `--json` object, `{"items": []}`, `result: []`, `{}`) to []. Callers get
lists and never parse gcx output. The shapes are pinned by scripts/tests/fixtures/gcx/, recorded
from the pinned tag (GCX_VERSION in scripts/bootstrap.sh); the pin moves only with a change that
re-records them.

$GCX_CONFIG defaults to .local/gcx.yaml (mode 0600, gitignored), or $RUNNER_TEMP/gcx.yaml in CI. Each context's grafana.server is
the stack's grafana_url from deploy/terraform/grafana/stacks/<stack>/terraform.tfvars, its
grafana.token GRAFANA_<STACK>_READ_TOKEN. --config bypasses gcx's layering, so ~/.config/gcx and a
.gcx.yaml in the working directory are never read. gcx's agent mode (detected from CLAUDECODE and
the like) is switched off, so a run under an agent sees what CI sees.

Logs: `gcx: <context> <command>` on stderr when GCX_DEBUG is set; a failure names the context, the
command, gcx's exit code and its message. No line carries a token: every known token value is
replaced by *** before it is printed or raised.

Exit codes (config): 0 written; 3 no read token set.
"""
import json
import os
import re
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import grafana_stack  # noqa: E402

REPO = os.path.dirname(HERE)
GCX = os.path.join(REPO, "bin", "gcx")
DEFAULT_CONFIG = os.path.join(REPO, ".local", "gcx.yaml")
TFVARS = os.path.join(REPO, "deploy", "terraform", "grafana", "stacks", "%s", "terraform.tfvars")

PROM, LOGS, TRACES = "grafanacloud-prom", "grafanacloud-logs", "grafanacloud-traces"

# gcx switches to agent mode (its own output format) when it sees one of these.
AGENT_ENV = ("CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "GITHUB_COPILOT", "AMAZON_Q", "OPENCODE",
             "PI_CODING_AGENT", "GCX_AGENT_MODE")

# Instance state, one vocabulary: Grafana-managed rules say Alerting/Normal, the ruler says firing.
FIRING, PENDING, NORMAL = "firing", "pending", "normal"
STATES = {"alerting": FIRING, "firing": FIRING, "pending": PENDING,
          "normal": NORMAL, "normal (nodata)": NORMAL, "inactive": NORMAL}


class GcxError(Exception):
    """gcx exited non-zero, or printed what is not JSON. The caller exits 1."""


def config_path(env=None):
    """$GCX_CONFIG; else $RUNNER_TEMP/gcx.yaml in CI, so the token file never lands in the checkout there;
    else .local/gcx.yaml."""
    env = env if env is not None else os.environ
    if env.get("GCX_CONFIG"):
        return env["GCX_CONFIG"]
    if env.get("RUNNER_TEMP"):
        return os.path.join(env["RUNNER_TEMP"], "gcx.yaml")
    return DEFAULT_CONFIG


def _tokens(env=None):
    """Every token value this process might hold, longest first, so a redaction never leaves a tail."""
    env = env if env is not None else os.environ
    vals = {v.strip() for k, v in env.items() if k.startswith("GRAFANA_") and k.endswith("_TOKEN") and v.strip()}
    try:
        with open(config_path(env)) as f:
            vals |= {m.group(1) for m in re.finditer(r"^\s*token:\s*\"?([^\"\s]+)\"?\s*$", f.read(), re.M)}
    except OSError:
        pass
    return sorted(vals, key=len, reverse=True)


def redact(text, env=None):
    for t in _tokens(env):
        text = text.replace(t, "***")
    return text


def _message(stderr):
    """gcx's error box, flattened to one line: `Error: <summary> | <details>`, without its suggestions."""
    out = []
    for raw in stderr.splitlines():
        line = re.sub(r"^[\s│├└─•]+", "", raw).strip()
        if line.startswith(("Suggestions:", "Learn more:")):
            break
        if line and line != "Details:":
            out.append(line)
    return " | ".join(out)


def normalise(doc):
    """gcx's result as a list: [] for every empty shape, the payload list otherwise."""
    if doc is None or doc == {}:
        return []
    if isinstance(doc, list):
        return [d for d in doc if not _all_null(d)]
    if isinstance(doc, dict):
        if _all_null(doc):
            return []
        if "items" in doc:
            return doc["items"] or []
        if "traces" in doc:
            return doc["traces"] or []
        if "trace" in doc:
            return [doc["trace"]] if doc["trace"] else []
        if "data" in doc:
            data = doc["data"]
            if isinstance(data, dict):
                return data.get("result") or []
            return data or []
    raise GcxError("gcx: unexpected output shape: %s" % json.dumps(doc)[:200])


def _all_null(d):
    # `--json a,b` on an empty list prints one object with every field null.
    return isinstance(d, dict) and bool(d) and all(v is None for v in d.values())


def run(context, *args, env=None, gcx=None):
    """Run gcx against context with -o json; the parsed document. GcxError on a non-zero exit."""
    env = dict(os.environ if env is None else env)
    for k in AGENT_ENV:
        env.pop(k, None)
    argv = [gcx or GCX, "--config", config_path(env), "--context", context] + list(args) + ["-o", "json"]
    command = " ".join(args)
    if env.get("GCX_DEBUG"):
        print("gcx: %s %s" % (context, command), file=sys.stderr)
    try:
        p = subprocess.run(argv, capture_output=True, text=True, env=env, timeout=120)
    except FileNotFoundError:
        raise GcxError("gcx: %s not found (run `make bootstrap`)" % argv[0])
    except subprocess.TimeoutExpired:
        raise GcxError("gcx: %s %s: timed out after 120s" % (context, command))
    # Redact the whole text before any cut, so a cut can never leave a token's prefix behind.
    if p.returncode != 0:
        detail = _message(redact(p.stderr, env)) or redact(p.stdout.strip(), env)[:300]
        raise GcxError("gcx: %s %s: exit %d: %s" % (context, command, p.returncode, detail))
    try:
        return json.loads(p.stdout) if p.stdout.strip() else None
    except ValueError:
        raise GcxError("gcx: %s %s: output is not JSON: %s" % (context, command, redact(p.stdout, env)[:200]))


# --- one function per question (ADR-0012 decision 2's table) -----------------------------------

def rule_groups(context, group=None, **kw):
    """Grafana-managed rule groups (each with its rules), optionally one group by name."""
    return normalise(run(context, "alert", "rules", "list", "--limit", "0",
                         *(["--group", group] if group else []), **kw))


def rule_defined(context, group, rule, **kw):
    """The rule by name or uid in group, paused or not; None when it is not defined."""
    for g in rule_groups(context, group, **kw):
        for r in g.get("rules") or []:
            if rule in (r.get("name"), r.get("uid")):
                return dict(r, interval=g.get("interval"))
    return None


def rule_live(context, group, rule, now, **kw):
    """True when the rule is evaluating: health ok, not paused, last evaluated within twice the group's interval.
    now is a datetime (UTC)."""
    import datetime
    r = rule_defined(context, group, rule, **kw)
    if not r or r.get("isPaused") or r.get("health") != "ok" or not r.get("interval"):
        return False
    last = r.get("lastEvaluation") or ""
    try:
        at = datetime.datetime.strptime(last.replace("Z", "+0000")[:19] + "+0000", "%Y-%m-%dT%H:%M:%S%z")
    except ValueError:
        return False
    return (now - at).total_seconds() <= 2 * float(r["interval"])


def state(raw):
    """gcx's instance state in one vocabulary: firing, pending or normal. Anything else is returned lowered."""
    return STATES.get((raw or "").strip().lower(), (raw or "").strip().lower())


def instances(context, name, labels=None, state_filter=None, datasource=None, **kw):
    """Alert instances of rules matching the name regex, as {ruleName, labels, state, activeAt}, state mapped
    by state(); filtered to those whose labels include every item of labels. datasource reads the ruler."""
    args = ["alert", "instances", "list", "--name", name, "--json", "ruleName,labels,state,activeAt"]
    if state_filter:
        args += ["--state", state_filter]
    if datasource:
        args += ["--datasource", datasource]
    out = []
    for i in normalise(run(context, *args, **kw)):
        lab = i.get("labels") or {}
        if all(lab.get(k) == v for k, v in (labels or {}).items()):
            out.append(dict(i, labels=lab, state=state(i.get("state"))))
    return out


def legacy_instances(context, **kw):
    """Every instance the legacy (Mimir) ruler holds, unfiltered (cutover only, until AW-INF-077)."""
    return [dict(i, state=state(i.get("state")))
            for i in normalise(run(context, "alert", "instances", "list", "--datasource", PROM, **kw))]


def metrics_query(context, expr, since=None, **kw):
    """PromQL; an instant query unless since (a range over it). The result list: {metric, value|values}."""
    return normalise(run(context, "metrics", "query", "-d", PROM, expr, *(["--since", since] if since else []), **kw))


def metrics_series(context, selector, since="10m", **kw):
    """The label sets of the series matching selector over since."""
    return normalise(run(context, "metrics", "series", "-d", PROM, "--since", since, selector, **kw))


def metrics_labels(context, label, match=None, **kw):
    """The values label takes (scoped by a series selector when match is given)."""
    return normalise(run(context, "metrics", "labels", "-d", PROM, "--label", label,
                         *(["--match", match] if match else []), **kw))


def logs_query(context, expr, since="1h", limit=5, **kw):
    """LogQL; one entry per line: {stream, timestamp, line}, newest first as Loki returns them."""
    out = []
    for s in normalise(run(context, "logs", "query", "-d", LOGS, expr, "--since", since, "--limit", str(limit), **kw)):
        for v in s.get("values") or []:
            out.append({"stream": s.get("stream") or {}, "timestamp": v.get("timestamp"), "line": v.get("line")})
    return out


def traces_query(context, traceql, since="1h", limit=20, **kw):
    """TraceQL search; the trace summaries ({traceID, rootServiceName, rootTraceName, …})."""
    return normalise(run(context, "traces", "query", "-d", TRACES, traceql, "--since", since,
                         "--limit", str(limit), **kw))


def trace_get(context, trace_id, since=None, **kw):
    """[the trace] when trace_id resolves, [] when it does not (gcx prints {} and exits 0)."""
    return normalise(run(context, "traces", "get", "-d", TRACES, trace_id,
                         *(["--since", since] if since else []), **kw))


def irm_alert_groups(context, *extra, **kw):
    """IRM alert groups (firing, acknowledged, silenced unless extra says otherwise)."""
    return normalise(run(context, "irm", "oncall", "alert-groups", "list", *extra, **kw))


# --- make gcx-config --------------------------------------------------------------------------

def grafana_url(stack):
    with open(TFVARS % stack) as f:
        m = re.search(r'^\s*grafana_url\s*=\s*"([^"]+)"', f.read(), re.M)
    if not m:
        raise grafana_stack.Exit(1, "no grafana_url in %s" % (TFVARS % stack))
    return m.group(1)


def render_config(tokens):
    """The config YAML for {stack: token}; the first stack is the current context."""
    lines = ["version: 1", "stacks:"]
    for s, t in tokens.items():
        lines += ["  %s:" % s, "    grafana:", "      server: %s" % grafana_url(s), "      token: %s" % json.dumps(t)]
    lines.append("contexts:")
    for s in tokens:
        lines += ["  %s:" % s, "    stack: %s" % s]
    lines.append("current-context: %s" % next(iter(tokens)))
    return "\n".join(lines) + "\n"


def write_config(env=None):
    """Write $GCX_CONFIG (mode 0600) with one context per stack whose read token is set; the stacks written.
    Exit 3 when none is."""
    env = grafana_stack.environ(env)
    tokens = {s: env[grafana_stack.token_var(s)].strip() for s in grafana_stack.STACKS
              if (env.get(grafana_stack.token_var(s)) or "").strip()}
    if not tokens:
        raise grafana_stack.Exit(3, "no read token set: %s (the environment or .local/box.env)"
                                 % ", ".join(grafana_stack.token_var(s) for s in grafana_stack.STACKS))
    path = config_path(env)
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(os.path.abspath(path)), prefix=".gcx-")
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as f:
            f.write(render_config(tokens))
        os.replace(tmp, path)
    except BaseException:
        if os.path.exists(tmp):
            os.unlink(tmp)
        raise
    return list(tokens)


def main(argv):
    if argv != ["config"]:
        print("usage: gcx.py config", file=sys.stderr)
        return 2
    try:
        stacks = write_config()
    except grafana_stack.Exit as e:
        print("gcx-config: %s" % e, file=sys.stderr)
        return e.code
    print("gcx-config: wrote %s (contexts: %s)" % (config_path(), ", ".join(stacks)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
