#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make gcx-only-check — nothing calls Grafana's query or alerting APIs except through gcx (ADR-0012 decision 2; AW-INF-075).

Scans the tracked files of scripts/, the Makefile and .github/workflows/ (git ls-files; an untracked
__pycache__ never counts) and fails on:

  a path      /api/prom, /api/v1/query, /api/prometheus/, /prometheus/config/v1/rules, /loki/api
  a host      a *.grafana.net name (terraform.tfvars and docs, which hold the endpoints, are not scanned)
  a bearer    an `Authorization: Bearer` on a line that reads a GRAFANA_* variable (argocd.py's and
              image_digest.sh's bearer headers carry no Grafana token and are not matched)

Not scanned: scripts/tests/** (fixtures name the paths on purpose), this file (it holds the
patterns), and scripts/tf_policy.py (it names Grafana hosts and sender paths to police them and
calls no API).

EXCEPTIONS (scripts/gcx_only_exceptions.txt) lists each file still on the old path, one per line,
with the story that removes it; that story deletes its line. A line for a file that is gone, or for a
file in which no pattern matches, fails the check, so the list cannot outlive its files.

Exit: 0 clean · 1 a match outside the list, or a stale or malformed list line.
"""
import os
import re
import subprocess
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXCEPTIONS = os.path.join("scripts", "gcx_only_exceptions.txt")
SCOPE = ("scripts", "Makefile", ".github/workflows")
NOT_SCANNED = ("scripts/gcx_only_check.py", "scripts/tf_policy.py")
NOT_SCANNED_DIRS = ("scripts/tests/",)

PATTERNS = [
    ("path /api/prom", re.compile(r"/api/prom\b")),
    ("path /api/v1/query", re.compile(r"/api/v1/query")),
    ("path /api/prometheus/", re.compile(r"/api/prometheus/")),
    ("path /prometheus/config/v1/rules", re.compile(r"/prometheus/config/v1/rules")),
    ("path /loki/api", re.compile(r"/loki/api")),
    ("a *.grafana.net host", re.compile(r"[\w.…-]*\.grafana\.net\b")),
    ("a bearer header with a GRAFANA_* token",
     re.compile(r"Bearer\b.*\bGRAFANA_[A-Z0-9_]+|\bGRAFANA_[A-Z0-9_]+.*\bBearer\b")),
]
LINE = re.compile(r"^(\S+)\s+(AW-[A-Z]+-\d{3})\s*(?:#.*)?$")


def tracked(repo=REPO):
    p = subprocess.run(["git", "-C", repo, "ls-files", "--", *SCOPE], capture_output=True, text=True, check=True)
    return [f for f in p.stdout.splitlines()
            if f not in NOT_SCANNED and not f.startswith(NOT_SCANNED_DIRS)]


def matches(text):
    """[(line number, pattern name)] for every match in text."""
    out = []
    for n, line in enumerate(text.splitlines(), 1):
        for name, rx in PATTERNS:
            if rx.search(line):
                out.append((n, name))
    return out


def parse_exceptions(text):
    """({path: story}, [errors]) from the exception list."""
    allowed, errors = {}, []
    for n, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        m = LINE.match(line)
        if not m:
            errors.append("%s:%d: not `<path> <AW-story>`: %s" % (EXCEPTIONS, n, line))
            continue
        allowed[m.group(1)] = m.group(2)
    return allowed, errors


def check(files, exceptions_text):
    """files: {path: text} of the scanned files. The list of failures."""
    allowed, errors = parse_exceptions(exceptions_text)
    for path, story in sorted(allowed.items()):
        if path not in files:
            errors.append("%s: %s is listed (%s) but is not a tracked, scanned file; delete its line"
                          % (EXCEPTIONS, path, story))
        elif not matches(files[path]):
            errors.append("%s: %s is listed (%s) but no pattern matches it any more; delete its line"
                          % (EXCEPTIONS, path, story))
    for path in sorted(files):
        if path in allowed:
            continue
        for n, name in matches(files[path]):
            errors.append("%s:%d: %s — call Grafana through scripts/gcx.py (ADR-0012 decision 2)" % (path, n, name))
    return errors


def main():
    files = {}
    for f in tracked():
        try:
            # errors="replace": a non-UTF-8 file is still scanned, never skipped as clean.
            with open(os.path.join(REPO, f), encoding="utf-8", errors="replace") as fh:
                files[f] = fh.read()
        except (IsADirectoryError, FileNotFoundError):
            continue
    with open(os.path.join(REPO, EXCEPTIONS)) as fh:
        errors = check(files, fh.read())
    for e in errors:
        print("gcx-only-check: " + e, file=sys.stderr)
    if errors:
        return 1
    print("gcx-only-check: ok (%d files scanned)" % len(files))
    return 0


if __name__ == "__main__":
    sys.exit(main())
