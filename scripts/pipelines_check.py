#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make pipelines-check` — the Fleet Management pipelines' safety check (AW-INF-051, ADR-0014 §3).

    scripts/pipelines_check.py [--dir <pipelines dir>] [--alloy <alloy binary>]

A Fleet pipeline is configuration the `fleet` collector executes, and that collector's environment holds
the telemetry token. So every deploy/terraform/grafana/pipelines/*.alloy must:

- parse and validate (`alloy validate`, the pinned bin/alloy);
- hold no secret pattern (in comments too);
- call no `templatefile` (Terraform reads a pipeline with one literal file(), never a template);
- write nowhere but the chart's receiver: every `prometheus.remote_write` endpoint URL is RECEIVER, and
  there is no `otelcol.exporter.*` and no `loki.write` block;
- read the environment only through `sys.env("<NAME>")` with NAME listed in env-allow.txt, one name per
  line (committed empty; a computed name is refused).

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
PIPELINES = os.path.join(REPO, "deploy", "terraform", "grafana", "pipelines")
ALLOY = os.path.join(REPO, "bin", "alloy")
RECEIVER = "http://k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write"

# Grafana Cloud tokens, Slack webhooks, private keys and basic auth inside a URL.
SECRET = re.compile(r"glsa_|glc_|https://hooks\.slack\.com/|-----BEGIN [A-Z ]*PRIVATE KEY-----"
                    r"|[a-z][a-z0-9+.-]*://[^/\s\"'@]+:[^/\s\"'@]+@")
WRITERS = re.compile(r"\b(otelcol\.exporter\.[A-Za-z0-9_.]+|loki\.write)\b")
REMOTE_WRITE = re.compile(r"\bprometheus\.remote_write\s+\"[^\"]*\"\s*\{")
URL_ATTR = re.compile(r"\burl\s*=\s*")
SYS_ENV = re.compile(r"\bsys\.env\s*\(")
STRING = re.compile(r"\"((?:[^\"\\\n]|\\.)*)\"")
# sys.env's argument when it is one string literal and nothing else.
ENV_ARG = re.compile(r"\s*(\"(?:[^\"\\\n]|\\.)*\")\s*\)")


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


def string_at(text, offset):
    """The decoded string literal starting at offset, or None when no string literal starts there."""
    m = STRING.match(text, offset)
    if not m:
        return None
    try:
        return json.loads(m.group(0))
    except ValueError:
        return m.group(1)


def env_allow(path):
    names = set()
    with open(path) as f:
        for raw in f:
            name = raw.strip()
            if name:
                names.add(name)
    return names


def refusals(path, text, allowed):
    """[(line, rule, detail)] for one pipeline source."""
    found = []
    for i, raw in enumerate(text.splitlines(), 1):
        if SECRET.search(raw):
            found.append((i, "secret", "a secret pattern (a token, a webhook, a key or user:password@)"))
    code = code_only(text)
    for m in re.finditer(r"\btemplatefile\b", code):
        found.append((line_of(code, m.start()), "templatefile", "a templatefile call"))
    for m in WRITERS.finditer(code):
        found.append((line_of(code, m.start()), "writer", "%s writes outside the receiver" % m.group(1)))
    for m in REMOTE_WRITE.finditer(code):
        end = block_end(code, m.end())
        urls = list(URL_ATTR.finditer(code, m.end(), end))
        if not urls:
            found.append((line_of(code, m.start()), "remote_write", "no endpoint url"))
        for u in urls:
            value = string_at(text, u.end())
            if value != RECEIVER:
                found.append((line_of(code, u.start()), "remote_write",
                              "url %s is not the receiver %s" % (value if value is not None else "(computed)",
                                                                  RECEIVER)))
    for m in SYS_ENV.finditer(code):
        arg = ENV_ARG.match(text, m.end())
        value = string_at(text, arg.start(1)) if arg else None
        if value is None:
            found.append((line_of(code, m.start()), "sys.env", "a computed variable name"))
        elif value not in allowed:
            found.append((line_of(code, m.start()), "sys.env",
                          "%s is not in env-allow.txt" % value))
    return sorted(found)


def validate(alloy, path):
    proc = subprocess.run([alloy, "validate", path], capture_output=True, text=True)
    if proc.returncode == 0:
        return None
    lines = [ln for ln in (proc.stderr + proc.stdout).splitlines() if ln.strip()]
    return lines[0] if lines else "alloy validate exited %d" % proc.returncode


def check(directory, alloy):
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
        rel = os.path.relpath(path, REPO)
        with open(path) as f:
            text = f.read()
        found = refusals(path, text, allowed)
        error = validate(alloy, path)
        if error:
            found.append((0, "alloy validate", error))
        for line, rule, detail in found:
            print("pipelines-check: refused: %s:%d: %s: %s" % (rel, line, rule, detail))
        bad += bool(found)
    if bad:
        raise Exit(1, "%d of %d pipeline(s) refused" % (bad, len(sources)))
    print("pipelines-check: ok (%s)" % ", ".join(sources))
    return 0


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--dir", default=PIPELINES)
    ap.add_argument("--alloy", default=ALLOY)
    a = ap.parse_args(argv)
    try:
        return check(a.dir, a.alloy)
    except Exit as e:
        print("pipelines-check: %s" % e, file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
