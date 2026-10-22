#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make pipelines-check` — the Fleet Management pipelines' safety check (AW-INF-051, ADR-0014 §3).

    scripts/pipelines_check.py [--dir <pipelines dir>] [--stacks <stacks dir>] [--alloy <alloy binary>]

A Fleet pipeline is configuration the `fleet` collector executes, and that collector's environment holds
the telemetry token. So every deploy/terraform/grafana/pipelines/*.alloy must:

- parse, validate and be formatted (`alloy validate`, `alloy fmt`, the pinned bin/alloy);
- be built only from COMPONENTS, read as Alloy reads a name (`loki . write` is `loki.write`). Anything
  else at the top level is refused: a writer (`otelcol.exporter.*`, `loki.write`, any `*.write`), a
  module import, `remote.*`, `local.file`, and every other component;
- send only to the chart's receiver: every `prometheus.remote_write` url is RECEIVER, written as one
  string literal, and no proxy setting;
- hold no credential: no secret pattern (in comments too), no credential attribute but one whose value is
  `sys.env(...)`, and no `*_file` credential;
- call no `templatefile`, and read the environment only as `sys.env("<NAME>")` with NAME listed in
  env-allow.txt, one name per line (committed empty).

And every `grafana_fleet_management_pipeline` in stacks/*/*.tf must read its contents with exactly
`file("${path.module}/../../pipelines/<name>.alloy")`, an existing source, named `<name>`, with no
for_each or count: so no pipeline reaches Fleet without passing the rules above.

Every refusal prints `pipelines-check: refused: <file>:<line>: <rule>: <detail>`.

Exit: 0 every pipeline passes · 1 a pipeline is refused, or none exists · 2 usage, or alloy not found.
"""

import argparse
import json
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GRAFANA = os.path.join(REPO, "deploy", "terraform", "grafana")
PIPELINES = os.path.join(GRAFANA, "pipelines")
STACKS = os.path.join(GRAFANA, "stacks")
ALLOY = os.path.join(REPO, "bin", "alloy")
RECEIVER = "http://k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write"

# What a pipeline may be built from: discover the pods, relabel, scrape, keep, forward to the receiver.
COMPONENTS = ("discovery.kubernetes", "discovery.relabel", "prometheus.scrape", "prometheus.relabel",
              "prometheus.remote_write")
WRITER = re.compile(r"^otelcol\.exporter\.|\.write$")

# Grafana Cloud tokens, Slack webhooks, private keys and basic auth inside a URL.
SECRET = re.compile(r"glsa_|glc_|https://hooks\.slack\.com/|-----BEGIN [A-Z ]*PRIVATE KEY-----"
                    r"|[a-z][a-z0-9+.-]*://[^/\s\"'@]+:[^/\s\"'@]+@")
CREDENTIAL = re.compile(r"\b(password|bearer_token|api_key|client_secret|authorization|credentials|headers"
                        r"|token|secret)(_file)?\s*=\s*")
PROXY = re.compile(r"\bproxy_[a-z_]+\s*=")
URL_ATTR = re.compile(r"\burl\s*=\s*")
SYS_ENV = re.compile(r"\bsys\s*\.\s*env\s*\(")
STRING = re.compile(r"\"((?:[^\"\\\n]|\\.)*)\"")
# sys.env's argument when it is one string literal and nothing else.
ENV_ARG = re.compile(r"\s*(\"(?:[^\"\\\n]|\\.)*\")\s*\)")
# A top-level block's header: a dotted name (Alloy allows space and comments around the dots) and a label.
HEADER = re.compile(r"\s*([A-Za-z_]\w*(?:\s*\.\s*[A-Za-z_]\w*)*)\s*(\"[^\"\n]*\")?\s*")

PIPELINE_RESOURCE = re.compile(r'^resource\s+"grafana_fleet_management_pipeline"\s+"([^"]+)"\s*\{', re.M)
TOP_LEVEL = re.compile(r"^[a-z]", re.M)
CONTENTS = re.compile(r'^\s*contents\s*=\s*(.*?)\s*$', re.M)
CONTENTS_FILE = re.compile(r'file\("\$\{path\.module\}/\.\./\.\./pipelines/([a-z0-9-]+)\.alloy"\)')
NAME = re.compile(r'^\s*name\s*=\s*"([^"]*)"\s*$', re.M)


class Exit(Exception):
    def __init__(self, code, msg):
        super().__init__(msg)
        self.code = code


def code_only(text):
    """text with its comments and string contents blanked, newlines kept, so a match's offset is its
    offset in text and its line is the source line. Strings become "" of the same length (quotes kept),
    so a keyword inside a string, or `//` in a URL, is not read as code."""
    out = []
    i, n = 0, len(text)
    while i < n:
        c = text[i]
        if c == '"' or c == "`":
            end = i + 1
            while end < n and text[end] != c:
                end += 2 if c == '"' and text[end] == "\\" else 1
            end = min(end + 1, n)
            body = text[i + 1:end - 1]
            out.append(c + re.sub(r"[^\n]", " ", body) + (text[end - 1] if end - 1 > i else ""))
            i = end
        elif text.startswith("//", i):
            end = text.find("\n", i)
            end = n if end < 0 else end
            out.append(" " * (end - i))
            i = end
        elif text.startswith("/*", i):
            end = text.find("*/", i + 2)
            end = n if end < 0 else end + 2
            out.append(re.sub(r"[^\n]", " ", text[i:end]))
            i = end
        else:
            out.append(c)
            i += 1
    return "".join(out)


def line_of(text, offset):
    return text.count("\n", 0, offset) + 1


def block_end(code, start):
    """The offset just past the `}` that closes the block whose `{` ends at start - 1."""
    depth = 1
    i = start
    while i < len(code) and depth:
        if code[i] == "{":
            depth += 1
        elif code[i] == "}":
            depth -= 1
        i += 1
    return i


def top_level(code):
    """([(kind, header offset, body start, body end)] for every top-level block, [offsets of anything
    else at the top level]). kind is the dotted name with its spaces removed, as Alloy reads it."""
    blocks, stray = [], []
    seg = i = 0
    while i < len(code):
        if code[i] != "{":
            i += 1
            continue
        m = HEADER.fullmatch(code, seg, i)
        if m:
            blocks.append((re.sub(r"\s+", "", m.group(1)), m.start(1), i + 1, block_end(code, i + 1)))
        else:
            stray.append(seg + len(code[seg:i]) - len(code[seg:i].lstrip()))
        i = seg = block_end(code, i + 1)
    if code[seg:].strip():
        stray.append(seg + len(code[seg:]) - len(code[seg:].lstrip()))
    return blocks, stray


def literal_at(text, code, offset):
    """The decoded string literal at offset when it is the attribute's whole value (only space or a
    comment follows it on the line), else None."""
    m = STRING.match(text, offset)
    if not m:
        return None
    eol = code.find("\n", m.end())
    if code[m.end():len(code) if eol < 0 else eol].strip() not in ("", "}"):
        return None
    return decode(m.group(0))


def decode(literal):
    """A double-quoted literal's value (Alloy's escapes are JSON's for the cases that matter here)."""
    try:
        return json.loads(literal)
    except ValueError:
        return literal[1:-1]


def env_allow(path):
    with open(path) as f:
        return {raw.strip() for raw in f if raw.strip()}


def refusals(text, allowed):
    """[(line, rule, detail)] for one pipeline source."""
    found = []
    for i, raw in enumerate(text.splitlines(), 1):
        if SECRET.search(raw):
            found.append((i, "secret", "a secret pattern (a token, a webhook, a key or user:password@)"))
    code = code_only(text)
    blocks, stray = top_level(code)
    for offset in stray:
        found.append((line_of(code, offset), "component", "only blocks of %s may be at the top level"
                      % ", ".join(COMPONENTS)))
    for kind, start, body, end in blocks:
        if kind in COMPONENTS:
            continue
        rule = "writer" if WRITER.search(kind) else "component"
        found.append((line_of(code, start), rule, "%s is not one of %s" % (kind, ", ".join(COMPONENTS))))
    for kind, start, body, end in blocks:
        if kind != "prometheus.remote_write":
            continue
        urls = list(URL_ATTR.finditer(code, body, end))
        if not urls:
            found.append((line_of(code, start), "remote_write", "no endpoint url"))
        for u in urls:
            value = literal_at(text, code, u.end())
            if value != RECEIVER:
                found.append((line_of(code, u.start()), "remote_write", "url %s is not the receiver %s"
                              % (json.dumps(value) if value is not None else "(not one string literal)",
                                 RECEIVER)))
    for m in PROXY.finditer(code):
        found.append((line_of(code, m.start()), "remote_write", "a proxy setting"))
    for m in CREDENTIAL.finditer(code):
        if m.group(2):
            found.append((line_of(code, m.start()), "credential", "%s%s reads a credential from a file"
                          % (m.group(1), m.group(2))))
        elif not SYS_ENV.match(code, m.end()):
            found.append((line_of(code, m.start()), "credential",
                          "%s must be sys.env(\"<NAME>\") from env-allow.txt, never a value" % m.group(1)))
    for m in re.finditer(r"\btemplatefile\b", code):
        found.append((line_of(code, m.start()), "templatefile", "a templatefile call"))
    for m in SYS_ENV.finditer(code):
        arg = ENV_ARG.match(text, m.end())
        value = decode(arg.group(1)) if arg else None
        if value is None:
            found.append((line_of(code, m.start()), "sys.env", "a computed variable name"))
        elif value not in allowed:
            found.append((line_of(code, m.start()), "sys.env", "%s is not in env-allow.txt" % value))
    return sorted(set(found))


def alloy_errors(alloy, path, text):
    """[(line, rule, detail)] from alloy validate and alloy fmt."""
    found = []
    proc = subprocess.run([alloy, "validate", path], capture_output=True, text=True)
    if proc.returncode:
        lines = [ln for ln in (proc.stderr + proc.stdout).splitlines() if ln.strip()]
        found.append((0, "alloy validate", lines[0] if lines else "exit %d" % proc.returncode))
    proc = subprocess.run([alloy, "fmt", path], capture_output=True, text=True)
    if proc.returncode == 0 and proc.stdout != text:
        found.append((0, "alloy fmt", "not formatted; run bin/alloy fmt -w %s" % os.path.basename(path)))
    return found


def resource_refusals(stacks, sources):
    """[(path, line, rule, detail)]: each pipeline resource reads one checked source with one literal file()."""
    found = []
    for root in sorted(os.listdir(stacks)) if os.path.isdir(stacks) else []:
        rdir = os.path.join(stacks, root)
        for name in sorted(n for n in os.listdir(rdir) if n.endswith(".tf")):
            path = os.path.join(rdir, name)
            with open(path) as f:
                text = f.read()
            for m in PIPELINE_RESOURCE.finditer(text):
                nxt = TOP_LEVEL.search(text, m.end())
                body = text[m.end():nxt.start() if nxt else len(text)]
                line = line_of(text, m.start())
                where = "resource %s" % m.group(1)
                contents = CONTENTS.findall(body)
                src = CONTENTS_FILE.fullmatch(contents[0]) if len(contents) == 1 else None
                if not src:
                    found.append((path, line, "terraform", "%s: contents must be exactly "
                                  'file("${path.module}/../../pipelines/<name>.alloy")' % where))
                elif src.group(1) + ".alloy" not in sources:
                    found.append((path, line, "terraform", "%s: pipelines/%s.alloy does not exist"
                                  % (where, src.group(1))))
                else:
                    names = NAME.findall(body)
                    if names != [src.group(1)]:
                        found.append((path, line, "terraform", "%s: name must be %r, its source's name"
                                      % (where, src.group(1))))
                if re.search(r"^\s*(for_each|count|dynamic)\b", body, re.M):
                    found.append((path, line, "terraform", "%s: one resource per source, no for_each, count "
                                  "or dynamic" % where))
    return found


def check(directory, stacks, alloy):
    if not (os.path.isfile(alloy) and os.access(alloy, os.X_OK)):
        raise Exit(2, "%s not found; run make bootstrap" % alloy)
    allow_path = os.path.join(directory, "env-allow.txt")
    if not os.path.isfile(allow_path):
        raise Exit(1, "%s is missing; it is committed, empty until a pipeline needs a variable" % allow_path)
    allowed = env_allow(allow_path)
    sources = sorted(n for n in os.listdir(directory) if n.endswith(".alloy"))
    if not sources:
        raise Exit(1, "no pipeline in %s" % directory)
    bad = 0
    for name in sources:
        path = os.path.join(directory, name)
        with open(path) as f:
            text = f.read()
        found = sorted(refusals(text, allowed) + alloy_errors(alloy, path, text))
        for line, rule, detail in found:
            print("pipelines-check: refused: %s:%d: %s: %s" % (os.path.relpath(path, REPO), line, rule, detail))
        bad += bool(found)
    resources = resource_refusals(stacks, sources)
    for path, line, rule, detail in resources:
        print("pipelines-check: refused: %s:%d: %s: %s" % (os.path.relpath(path, REPO), line, rule, detail))
    if bad or resources:
        raise Exit(1, "%d of %d pipeline(s) refused, %d resource refusal(s)" % (bad, len(sources), len(resources)))
    print("pipelines-check: ok (%s)" % ", ".join(sources))
    return 0


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--dir", default=PIPELINES)
    ap.add_argument("--stacks", default=STACKS)
    ap.add_argument("--alloy", default=ALLOY)
    a = ap.parse_args(argv)
    try:
        return check(a.dir, a.stacks, a.alloy)
    except Exit as e:
        print("pipelines-check: %s" % e, file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
