#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make kube-context-check — no kubectl, helm or kind call acts on the ambient cluster (AW-INF-053).

ADR-0013 decision 1: every call names its cluster, from ANDARA_KUBE_CONTEXT, through scripts/kube.py
or scripts/kube.sh. This scans scripts/ (outside scripts/tests/), the Makefile's recipes and the
`run:` steps of .github/workflows/*.yaml, and reports, by file and line:

  - a kubectl call without `--context` naming ANDARA_KUBE_CONTEXT, a helm call without
    `--kube-context` naming it, or any kind call but `kind version` (kind's cluster is derived by
    scripts/kube.py kind-name, so each one is named in ALLOW). A shell call counts at a command's
    start, after env, command, sudo, nohup, watch, timeout or xargs too, and in a Makefile
    `$(shell ...)`. A Python call counts when an argv list, or a call's first argument, starts with
    the tool; the helpers' kube.kubectl(...) and kube.helm(...) don't, so a converted call passes.
    Commands built as strings (shell=True, os.system, `bash -c`) aren't parsed; none exists today.
  - `kubectl config use-context`, `config set-context --current` or `config set current-context`
    anywhere, even through the helpers: they move the developer's current-context.

Calls that reach no cluster pass: helm's template, lint, show, repo, version, dependency, package,
search and pull; kubectl config get-contexts and view; kubectl version --client; kind version. Every other exception is in
ALLOW below, with its reason, and an entry nothing matches is itself a finding.

Exit: 0 clean · 1 a finding.
"""
import ast
import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TOOLS = ("kubectl", "helm", "kind")
# Names a script binds a tool's path to; a call or argv list starting with one is a call of the tool.
# `kind` alone isn't one: scripts use it for a kind of thing.
TOOL_NAMES = {"kubectl", "helm", "kubectl_path", "helm_path", "kind_path", "kubectl_bin", "helm_bin", "kind_bin",
              "KUBECTL", "HELM", "KIND"}
# Calls that take a tool's name or path without running it.
RESOLVERS = {"which", "join", "tool", "access", "basename", "relpath", "get", "getenv", "print"}
HELM_OFFLINE = {"template", "lint", "show", "repo", "version", "dependency", "dep", "package", "search", "pull"}
KUBECTL_OFFLINE = {"get-contexts", "view"}
SKIP = {os.path.join("scripts", "kube.py"), os.path.join("scripts", "kube.sh"), os.path.join("scripts", "kube_check.py")}
CONTEXT_VAR = "ANDARA_KUBE_CONTEXT"

# (path, text the logical line contains, why it may skip the helper)
ALLOW = [
    ("scripts/cluster.py", "\"config\", \"set\", \"%s.%s.%s\"",
     "cluster-up merges kind-andara into the kubeconfig with `kubectl config set`, which never moves "
     "current-context (AW-INF-052)"),
    ("scripts/cluster.py", "\"--name\", NAME",
     "cluster-up, -down and -rebuild act on the cluster `andara` only; kube.box_only() refuses any "
     "other ANDARA_KUBE_CONTEXT first, and each call names --name andara"),
    ("scripts/cluster.py", "run(kind_bin, \"get\", \"clusters\")",
     "lists kind's clusters, to find whether `andara` exists; reaches no cluster"),
    ("Makefile", "$(KIND) load docker-image",
     "--name is the cluster `scripts/kube.py kind-name` derives from ANDARA_KUBE_CONTEXT"),
]

SEG_START = re.compile(
    r"(?:^|[;&|({`!]|\$\(|\b(?:then|do|else|elif|if|while|until|time|exec|xargs|env|command|sudo|nohup|watch)\s"
    r"|\btimeout\s+\S+\s)\s*"
    r"(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*"
    r"(?P<tool>\"?\$\{?(?:KUBECTL|HELM|KIND)\}?\"?|\$\((?:KUBECTL|HELM|KIND)\)"
    r"|\"?(?:[\w./${}()-]*/)?(?:kubectl|helm|kind)\"?)(?=[\s;)|&]|$)")
SEG_END = re.compile(r"\|\||&&|[;|)`]")
MOVES = re.compile(r"\buse-context\b|\bset-context\b[^;|&]*--current|\bconfig\s+set\s+current-context\b")


def tool_of(token):
    t = token.strip("\"'${}()").rsplit("/", 1)[-1].lower()
    return t if t in TOOLS else None


def strip_comment(line):
    """line without a shell comment: a # at the start or after whitespace, outside quotes."""
    quote = None
    for i, ch in enumerate(line):
        if quote:
            if ch == quote:
                quote = None
        elif ch in "'\"":
            quote = ch
        elif ch == "#" and (i == 0 or line[i - 1].isspace()):
            return line[:i]
    return line


def logical(lines, start=1):
    """(first line number, text) per logical shell line: backslash continuations joined."""
    out, buf, first = [], [], None
    for n, line in enumerate(lines, start):
        text = line.rstrip("\n")
        if first is None:
            first = n
        if text.rstrip().endswith("\\"):
            buf.append(text.rstrip()[:-1])
            continue
        buf.append(text)
        out.append((first, " ".join(buf)))
        buf, first = [], None
    if buf:
        out.append((first, " ".join(buf)))
    return out


def offline_kubectl(words):
    return (words[:1] == ["config"] and len(words) > 1 and words[1] in KUBECTL_OFFLINE) \
        or (words[:1] == ["version"] and "--client" in words)


def shell_call_ok(tool, args):
    words = args.split()
    if tool == "kubectl":
        if re.search(r"--context[= ]\S*" + CONTEXT_VAR, args):
            return True
        return offline_kubectl(words)
    if tool == "helm":
        if re.search(r"--kube-context[= ]\S*" + CONTEXT_VAR, args):
            return True
        first = next((w for w in words if not w.startswith("-")), "")
        return first in HELM_OFFLINE
    return words[:1] == ["version"]


def shell_findings(path, lines, start=1, makefile=False):
    found = []
    for n, text in logical(lines, start):
        code = strip_comment(text.replace("$$", "$") if makefile else text)
        if MOVES.search(code):
            found.append((path, n, text, "moves the kubeconfig's current-context"))
        for m in SEG_START.finditer(code):
            tool = tool_of(m.group("tool"))
            if not tool:
                continue
            rest = code[m.end():]
            end = SEG_END.search(rest)
            if not shell_call_ok(tool, rest[:end.start()] if end else rest):
                found.append((path, n, text, message(tool)))
    return found


def message(tool):
    return {"kubectl": "kubectl without --context \"$%s\" (use kube_kubectl or kube.kubectl)" % CONTEXT_VAR,
            "helm": "helm without --kube-context \"$%s\" (use kube_helm or kube.helm)" % CONTEXT_VAR,
            "kind": "kind without --name from %s (scripts/kube.py kind-name)" % CONTEXT_VAR}[tool]


def tool_ref(node):
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return tool_of(node.value) if "/" in node.value or node.value in TOOLS else None
    name = node.id if isinstance(node, ast.Name) else node.attr if isinstance(node, ast.Attribute) else None
    if name in TOOL_NAMES:
        return name.lower().split("_")[0]
    if isinstance(node, ast.Call):
        # kubectl_path() and the like: a call that returns the tool's path.
        return tool_ref(node.func)
    return None


def const(node):
    return node.value if isinstance(node, ast.Constant) and isinstance(node.value, str) else None


def python_call_ok(tool, rest):
    words = [const(x) for x in rest]
    if tool == "helm":
        first = next((w for w in words if w and not w.startswith("-")), None)
        return first in HELM_OFFLINE
    if tool == "kubectl":
        return offline_kubectl(words)
    return words[:1] == ["version"]


def python_findings(path, source):
    found = []
    lines = source.splitlines()
    for node in ast.walk(ast.parse(source)):
        if isinstance(node, ast.Call):
            func = node.func
            fname = func.id if isinstance(func, ast.Name) else func.attr if isinstance(func, ast.Attribute) else ""
            if fname in RESOLVERS or not node.args:
                continue
            elts = node.args
        elif isinstance(node, (ast.List, ast.Tuple)):
            elts = node.elts
        else:
            continue
        words = [const(x) for x in elts]
        if "use-context" in words or ("set-context" in words and "--current" in words) \
                or ("set" in words and "current-context" in words):
            found.append((path, node.lineno, lines[node.lineno - 1], "moves the kubeconfig's current-context"))
        # A tool with no arguments isn't a call: ("kind",) is a tuple of names.
        if len(elts) < 2 or isinstance(getattr(node, "ctx", None), ast.Store):
            continue
        # A path variable passed along with other variables (merge(kubectl_bin, path), `return
        # kind_bin, kubectl_bin`) is handed over, not run: an argv carries a literal or *args.
        if not isinstance(elts[0], ast.Constant) \
                and not any(isinstance(x, (ast.Constant, ast.Starred)) for x in elts[1:]):
            continue
        tool = tool_ref(elts[0])
        if tool and not python_call_ok(tool, elts[1:]):
            text = " ".join(lines[node.lineno - 1:getattr(node, "end_lineno", node.lineno)])
            found.append((path, node.lineno, text, message(tool)))
    # ast.walk is breadth-first; report in line order.
    return sorted(found, key=lambda f: f[1])


def workflow_runs(lines):
    """(first line number, lines) of each `run:` step's script."""
    out, i = [], 0
    while i < len(lines):
        m = re.match(r"^(\s*)(?:-\s+)?run:\s*(.*)$", lines[i])
        if not m:
            i += 1
            continue
        key_indent, value = len(m.group(1)), m.group(2).strip()
        if value and value[0] not in "|>":
            out.append((i + 1, [value]))
            i += 1
            continue
        j = i + 1
        while j < len(lines) and (not lines[j].strip() or len(lines[j]) - len(lines[j].lstrip()) > key_indent):
            j += 1
        out.append((i + 2, lines[i + 1:j]))
        i = j
    return out


def scan(repo):
    found = []
    sdir = os.path.join(repo, "scripts")
    for name in sorted(os.listdir(sdir)) if os.path.isdir(sdir) else []:
        rel = os.path.join("scripts", name)
        full = os.path.join(sdir, name)
        if rel in SKIP or not os.path.isfile(full):
            continue
        with open(full) as f:
            text = f.read()
        if name.endswith(".py"):
            found += python_findings(rel, text)
        elif name.endswith(".sh"):
            found += shell_findings(rel, text.splitlines())
    mk = os.path.join(repo, "Makefile")
    if os.path.exists(mk):
        with open(mk) as f:
            lines = f.read().splitlines()
        # A recipe line's own prefixes (@ silent, - ignore errors, + always run) aren't the command;
        # outside recipes, only a $(shell ...) runs anything.
        recipe = [re.sub(r"^\t[@+-]*", "\t", line) if line.startswith("\t")
                  else line[line.index("$(shell ") + 8:] if "$(shell " in line else "" for line in lines]
        found += shell_findings("Makefile", recipe, makefile=True)
    wdir = os.path.join(repo, ".github", "workflows")
    if os.path.isdir(wdir):
        for name in sorted(os.listdir(wdir)):
            if not name.endswith((".yaml", ".yml")):
                continue
            rel = os.path.join(".github", "workflows", name)
            with open(os.path.join(wdir, name)) as f:
                lines = f.read().splitlines()
            for start, block in workflow_runs(lines):
                found += shell_findings(rel, block, start)
    return found


def check(repo=REPO, allow=None):
    allow = ALLOW if allow is None else allow
    used, out = set(), []
    for path, n, text, why in scan(repo):
        hit = next((i for i, (p, needle, _) in enumerate(allow) if p == path and needle in text), None)
        if hit is None:
            out.append("%s:%d: %s" % (path, n, why))
        else:
            used.add(hit)
    for i, (path, needle, _) in enumerate(allow):
        if i not in used:
            out.append("%s: allow-list entry %r matches no call; remove it" % (path, needle))
    return out


def main(argv):
    if argv:
        print("usage: scripts/kube_check.py", file=sys.stderr)
        return 2
    out = check()
    for line in out:
        print("kube-context-check: " + line, file=sys.stderr)
    if out:
        return 1
    print("kube-context-check: every kubectl, helm and kind call names its cluster")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
