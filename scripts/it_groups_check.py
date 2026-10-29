#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
"""Every consumer-group ID in a `*_integration_test.go` starts with `it-` (AW-INF-060 AC-12).

    scripts/it_groups_check.py            (part of `make kafka-users-check`, so of `make check`)

`make test-integration` runs as the principal `andara-it` on the shared brokers, whose only group
ACL is PREFIXED `it-` (ADR-0013 amendment 17). A test group without the prefix is refused there,
and one named like a real environment's (`andara-sim-dev`) would be a test joining that group.

A group ID is the first string literal of the expression in a group position:

    Group: <expr>                       a struct field (KafkaSourceOptions and the like)
    group := <expr>, group = <expr>     a variable named group
    ConsumerGroup(<expr>)               kgo's option
    DeleteGroups(ctx, <expr>, ...)      kadm's cleanup, every argument after the context

An expression with no string literal (a variable passed on) is skipped: the variable's own
assignment is what gets checked. A group handed to a test helper as a plain argument
(`newBrokerNamed(t, …, group)`) sits in no position, so one more rule covers the groups that
matter most: any string literal starting `andara-sim-` or `andara-projector-`, the real
environments' group names, is a group wherever it appears. scripts/it_groups_exceptions.txt names the sites that may keep
another prefix and the story that removes each; a line that matches nothing fails the check.

Exit codes: 0 clean; 1 a group without the prefix, or a stale exception; 2 usage.
"""
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXCEPTIONS = os.path.join(REPO, "scripts", "it_groups_exceptions.txt")
PREFIX = "it-"
SKIP_DIRS = {".git", ".claude", "bin", "node_modules", "vendor", ".local"}

ENV_GROUP = re.compile(r"andara-(sim|projector)-")
LITERAL = re.compile(r'"((?:[^"\\]|\\.)*)"|`([^`]*)`')
POSITIONS = (
    re.compile(r"\bGroup:\s*(?P<expr>[^,}]+)"),
    re.compile(r"\bgroup\s*:?=(?!=)\s*(?P<expr>.+)$"),
    re.compile(r"\bConsumerGroup\((?P<expr>[^)]*)\)"),
    re.compile(r"\bDeleteGroups\(\s*[A-Za-z_][A-Za-z0-9_]*\s*,(?P<expr>[^)]*)\)"),
)


def files(root=REPO):
    for dirpath, dirnames, names in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for n in sorted(names):
            if n.endswith("_integration_test.go"):
                yield os.path.join(dirpath, n)


def first_literal(expr):
    m = LITERAL.search(expr)
    if not m:
        return None
    return m.group(1) if m.group(1) is not None else m.group(2)


def groups_in(text):
    """[(line number, group ID literal, whole expression)] for every group position in Go source."""
    found = []
    for n, line in enumerate(text.splitlines(), 1):
        code = line.split("//", 1)[0] if not line.lstrip().startswith("//") else ""
        for pat in POSITIONS:
            for m in pat.finditer(code):
                expr = m.group("expr")
                # DeleteGroups(ctx, a, b): each argument is a group.
                parts = expr.split(",") if pat is POSITIONS[3] else [expr]
                for part in parts:
                    lit = first_literal(part)
                    if lit is not None:
                        found.append((n, lit, part.strip()))
        for m in LITERAL.finditer(code):
            lit = m.group(1) if m.group(1) is not None else m.group(2)
            if ENV_GROUP.match(lit) and not any(f[0] == n and f[1] == lit for f in found):
                found.append((n, lit, m.group(0)))
    return found


def load_exceptions(path=EXCEPTIONS):
    """{(relative path, group literal): story}."""
    out = {}
    if not os.path.exists(path):
        return out
    with open(path) as f:
        lines = f.readlines()
    for line in lines:
        line = line.split("#", 1)[0].strip()
        if not line:
            continue
        fields = line.split()
        if len(fields) != 3:
            raise SystemExit("make: it-groups: %s: want `<path> <group literal> <story>`, got %r"
                             % (os.path.relpath(path, REPO), line))
        out[(fields[0], fields[1])] = fields[2]
    return out


def check(root=REPO, exceptions=None):
    exceptions = load_exceptions() if exceptions is None else exceptions
    errors, used = [], set()
    for path in files(root):
        rel = os.path.relpath(path, root)
        with open(path) as f:
            text = f.read()
        for n, lit, expr in groups_in(text):
            if lit.startswith(PREFIX):
                continue
            if (rel, lit) in exceptions:
                used.add((rel, lit))
                continue
            errors.append("%s:%d: consumer group %r (%s) lacks the %r prefix andara-it's group ACL "
                          "allows (ADR-0013 amendment 17)" % (rel, n, lit, expr, PREFIX))
    for key in sorted(set(exceptions) - used):
        errors.append("scripts/it_groups_exceptions.txt: %s %r matches no group site; delete the line (%s)"
                      % (key[0], key[1], exceptions[key]))
    return errors


def main(argv):
    if argv:
        print("usage: scripts/it_groups_check.py", file=sys.stderr)
        return 2
    errors = check()
    for e in errors:
        print("make: kafka-users-check: " + e, file=sys.stderr)
    if errors:
        return 1
    print("it-groups: every consumer group in the integration tests starts with %r" % PREFIX)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
