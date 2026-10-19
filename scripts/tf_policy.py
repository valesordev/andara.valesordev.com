#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/tf_policy.py — may a pull request's Terraform be planned at all? (AW-INF-068, ADR-0012 decision 5)

`terraform plan` runs provider code and evaluates every HCL function, so a branch's Terraform could read
the runner's environment into a plan output. The pull-request `plan` job therefore runs this script, from
the BASE branch, over the pull request's files read as data, and plans only when it exits 0.

    scripts/tf_policy.py extract --repo <dir> --rev <sha> --out <dir>
        Write the pull request's policed paths at <sha> into <out> as regular files. Refuses a symlink
        (git mode 120000) or a submodule (160000) under them, so no link can reach Terraform.
    scripts/tf_policy.py check --base <dir> --head <dir>
        Check <head>'s deploy/terraform against the allow-list, comparing with <base> (the base branch's
        checkout). Both are repository roots.

The allow-list (ADR-0012 decision 5, with its 2026-10-17 Amendments):

- blocks: in a configuration, `terraform` (holding `required_version`, `required_providers`, `backend`),
  `provider "grafana"`, `resource` of type `grafana_*` or `terraform_data`, `data "grafana_*"`, `variable`,
  `locals`, `output`, and `module` whose `source` is a local path under deploy/terraform/grafana/modules/.
  No provisioner, `connection`, `import`, `check` or other block anywhere. In stacks/*/tests/*.tftest.hcl,
  `mock_provider "grafana"`, `variables` and `run` (holding `command`, `variables`, `assert`,
  `expect_failures` and `module` whose `source` is "../../tests/route"). tests/route/ has no provider and no
  resource. test-alert/ has a `local` backend; no job plans it.
- functions: `file()` only with a string literal or the single template "${path.module}/<literal>" or
  "${path.root}/<literal>", normalised and resolved as a real path inside deploy/terraform/grafana/,
  deploy/helm/andara/files/alerts.yaml or deploy/grafana/dashboards/. Refused: every other function that
  reads a file or a path (`templatefile`, `fileexists`, `fileset`, `filebase64`, the file hash functions,
  `abspath`, `pathexpand`), `templatestring`, `nonsensitive`, and provider-defined functions.
  `terraform_remote_state` and `external` are refused as data sources.
- symlinks: refused, in git and on disk.
- byte-identical to the base branch: every `variable` and `output` block (modules too), every `backend`
  and `provider` block and `required_providers`. A change to any of them, or a base with no
  deploy/terraform/grafana, takes the **separate-merge path**: the plan is refused with the diff, and the
  change merges on its own.
- endpoints: in each stacks/<root>/terraform.tfvars, every `*_url` is https with no user, port, query or
  fragment, its host one of that file's hosts on the base branch, matched whole; `grafana_url` and
  `fleet_url` have an empty path, and each sender URL's path equals the base value or its kind's
  constant below. Every `*_user` is digits. A changed endpoint takes the separate-merge path.
- files: no `*.auto.tfvars`, no variable file but stacks/<root>/terraform.tfvars, no JSON or override
  configuration, no state, no `.terraform/`, no CLI configuration. `.terraform.lock.hcl` holds only the
  grafana/grafana provider and changes only with its version pin, which takes the separate-merge path.

Every refusal prints `tf-policy: refused: <rule>: <file>:<line>: <detail>`.

Exit: 0 the pull request may be planned · 1 refused (a rule failed, or the separate-merge path) · 2 usage.
"""

import argparse
import difflib
import os
import posixpath
import re
import subprocess
import sys
import urllib.parse

TF = "deploy/terraform"
GRAFANA = TF + "/grafana"
# What extract writes and what a file() may read, besides deploy/terraform/grafana/.
ALERTS = "deploy/helm/andara/files/alerts.yaml"
DASHBOARDS = "deploy/grafana/dashboards"
POLICED = (TF, ALERTS, DASHBOARDS)
ROOTS = ("solo7dev", "solo7", "solo7-irm")

# Every function that reads a file or resolves a path, except file() itself, and the ones that can
# hide what they print or render a template that could call them.
REFUSED_FUNCTIONS = {
    "templatefile", "templatestring", "fileexists", "fileset", "filebase64", "filemd5", "filesha1",
    "filesha256", "filesha512", "filebase64sha256", "filebase64sha512", "abspath", "pathexpand",
    "nonsensitive",
}
# The path each sender URL carries, by kind (each stack's Details record; AW-INF-046 committed them).
SENDER_PATHS = {
    "prom_url": "/api/prom/push",
    "loki_url": "/loki/api/v1/push",
    "tempo_url": "/otlp",
    "otlp_url": "/otlp",
}
EMPTY_PATH = ("grafana_url", "fleet_url")
GRAFANA_PROVIDER = "registry.terraform.io/grafana/grafana"


class Refused(Exception):
    pass


class Findings:
    def __init__(self):
        self.items = []

    def refuse(self, rule, path, line, detail):
        self.items.append((rule, path, line, detail))


# --- HCL tokenizer ---------------------------------------------------------------------------------
#
# Enough of HCL native syntax to find blocks, attributes, function calls and their arguments with line
# numbers, and to cut a block's exact source text. Strings and heredocs are tokenized with their
# interpolations (`${ }`, `%{ }`), so a call inside a template is seen like any other.

class Tok:
    __slots__ = ("type", "value", "line", "start", "end")

    def __init__(self, type_, value, line, start, end):
        self.type, self.value, self.line, self.start, self.end = type_, value, line, start, end

    def __repr__(self):
        return "Tok(%s,%r,%d)" % (self.type, self.value, self.line)


class ParseError(Exception):
    def __init__(self, line, msg):
        super().__init__(msg)
        self.line = line


IDENT = re.compile(r"[A-Za-z_][A-Za-z0-9_-]*")
NUMBER = re.compile(r"[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?")
OPS = ("...", "==", "!=", "<=", ">=", "=>", "&&", "||")


def tokenize(text):
    toks = []
    i, line, n = 0, 1, len(text)
    # Frames: ["code", depth, interp?] or ["string"] or ["heredoc", marker, at_line_start].
    stack = [["code", 0, False]]

    def emit(type_, value, start, end, at_line=None):
        toks.append(Tok(type_, value, line if at_line is None else at_line, start, end))

    while i < n:
        frame = stack[-1]
        c = text[i]
        if frame[0] == "code":
            if c == "\n":
                emit("nl", "\n", i, i + 1)
                line += 1
                i += 1
            elif c in " \t\r":
                i += 1
            elif c == "#" or text.startswith("//", i):
                while i < n and text[i] != "\n":
                    i += 1
            elif text.startswith("/*", i):
                j = text.find("*/", i + 2)
                if j < 0:
                    raise ParseError(line, "unterminated comment")
                line += text.count("\n", i, j)
                i = j + 2
            elif c == '"':
                emit("qopen", '"', i, i + 1)
                stack.append(["string"])
                i += 1
            elif text.startswith("<<", i):
                m = re.compile(r"<<(-?)([A-Za-z_][A-Za-z0-9_-]*)[ \t]*\r?\n").match(text, i)
                if not m:
                    raise ParseError(line, "malformed heredoc")
                emit("hopen", m.group(2), i, m.end())
                line += 1
                i = m.end()
                stack.append(["heredoc", m.group(2), True])
            elif c == "{":
                frame[1] += 1
                emit("punct", "{", i, i + 1)
                i += 1
            elif c == "}":
                if frame[2] and frame[1] == 0:
                    emit("iclose", "}", i, i + 1)
                    stack.pop()
                else:
                    frame[1] -= 1
                    emit("punct", "}", i, i + 1)
                i += 1
            elif IDENT.match(text, i):
                m = IDENT.match(text, i)
                emit("ident", m.group(0), i, m.end())
                i = m.end()
            elif NUMBER.match(text, i):
                m = NUMBER.match(text, i)
                emit("num", m.group(0), i, m.end())
                i = m.end()
            else:
                op = next((o for o in OPS if text.startswith(o, i)), c)
                emit("punct", op, i, i + len(op))
                i += len(op)
        elif frame[0] == "string":
            seg_start, buf = i, []
            while i < n:
                c = text[i]
                if c == "\\" and i + 1 < n:
                    buf.append(text[i:i + 2])
                    i += 2
                elif text.startswith("$${", i) or text.startswith("%%{", i):
                    buf.append(text[i + 1:i + 3])
                    i += 3
                elif text.startswith("${", i) or text.startswith("%{", i) or c == '"' or c == "\n":
                    break
                else:
                    buf.append(c)
                    i += 1
            if buf:
                emit("str", "".join(buf), seg_start, i)
            if i >= n or text[i] == "\n":
                raise ParseError(line, "unterminated string")
            if text[i] == '"':
                emit("qclose", '"', i, i + 1)
                stack.pop()
                i += 1
            else:
                emit("iopen", text[i:i + 2], i, i + 2)
                stack.append(["code", 0, True])
                i += 2
        else:  # heredoc
            marker = frame[1]
            if frame[2]:
                eol = text.find("\n", i)
                eol = n if eol < 0 else eol
                if text[i:eol].strip() == marker:
                    emit("hclose", marker, i, eol)
                    stack.pop()
                    i = eol
                    continue
                frame[2] = False
            seg_start, buf = i, []
            while i < n:
                c = text[i]
                if text.startswith("$${", i) or text.startswith("%%{", i):
                    buf.append(text[i + 1:i + 3])
                    i += 3
                elif text.startswith("${", i) or text.startswith("%{", i):
                    break
                elif c == "\n":
                    buf.append(c)
                    i += 1
                    break
                else:
                    buf.append(c)
                    i += 1
            if buf:
                emit("str", "".join(buf), seg_start, i)
            if buf and buf[-1] == "\n":
                line += 1
                frame[2] = True
            elif i < n:
                emit("iopen", text[i:i + 2], i, i + 2)
                stack.append(["code", 0, True])
                i += 2
    if len(stack) != 1:
        raise ParseError(line, "unterminated %s" % stack[-1][0])
    return toks


# --- HCL structure ---------------------------------------------------------------------------------

class Attr:
    def __init__(self, name, line, toks, start, end):
        self.name, self.line, self.toks, self.start, self.end = name, line, toks, start, end


class Block:
    def __init__(self, type_, labels, line, body, start, end):
        self.type, self.labels, self.line, self.body, self.start, self.end = type_, labels, line, body, start, end

    def attrs(self):
        return {a.name: a for a in self.body if isinstance(a, Attr)}

    def blocks(self, type_=None):
        return [b for b in self.body if isinstance(b, Block) and (type_ is None or b.type == type_)]


OPENERS = {"(": ")", "[": "]", "{": "}"}


def parse(text):
    """The file's top-level body: a list of Attr and Block."""
    toks = tokenize(text)
    body, i = _body(toks, 0, False)
    return body


def _body(toks, i, braced):
    items = []
    while i < len(toks):
        t = toks[i]
        if t.type == "nl":
            i += 1
            continue
        if braced and t.type == "punct" and t.value == "}":
            return items, i
        if t.type != "ident":
            raise ParseError(t.line, "expected an attribute or block, got %r" % t.value)
        name, start = t.value, t.start
        i += 1
        if i < len(toks) and toks[i].type == "punct" and toks[i].value == "=":
            i += 1
            j, depth = i, 0
            while j < len(toks):
                u = toks[j]
                if u.type in ("qopen", "hopen", "iopen"):
                    depth += 1
                elif u.type in ("qclose", "hclose", "iclose"):
                    depth -= 1
                elif u.type == "punct" and u.value in OPENERS:
                    depth += 1
                elif u.type == "punct" and u.value in (")", "]", "}"):
                    if depth == 0:
                        break
                    depth -= 1
                elif u.type == "nl" and depth == 0:
                    break
                j += 1
            expr = toks[i:j]
            end = expr[-1].end if expr else toks[i - 1].end
            items.append(Attr(name, t.line, expr, start, end))
            i = j
            continue
        labels = []
        while i < len(toks) and toks[i].type in ("qopen", "ident"):
            if toks[i].type == "ident":
                labels.append(toks[i].value)
                i += 1
                continue
            parts, i = [], i + 1
            while i < len(toks) and toks[i].type == "str":
                parts.append(toks[i].value)
                i += 1
            if i >= len(toks) or toks[i].type != "qclose":
                raise ParseError(t.line, "a block label must be a plain string")
            labels.append("".join(parts))
            i += 1
        if i >= len(toks) or toks[i].type != "punct" or toks[i].value != "{":
            raise ParseError(t.line, "expected { after %s" % name)
        inner, i = _body(toks, i + 1, True)
        if i >= len(toks):
            raise ParseError(t.line, "unterminated block %s" % name)
        items.append(Block(name, labels, t.line, inner, start, toks[i].end))
        i += 1
    if braced:
        raise ParseError(toks[-1].line if toks else 1, "unterminated block")
    return items, i


def walk(items):
    """Every Attr and Block, depth first."""
    for item in items:
        yield item
        if isinstance(item, Block):
            yield from walk(item.body)


def literal(toks):
    """The value of an expression that is one plain string, else None."""
    if len(toks) >= 2 and toks[0].type == "qopen" and toks[-1].type == "qclose" \
            and all(t.type == "str" for t in toks[1:-1]):
        return "".join(t.value for t in toks[1:-1])
    return None


# --- the checks ------------------------------------------------------------------------------------

def rel(path, root):
    return os.path.relpath(path, root).replace(os.sep, "/")


def kind_of(relpath):
    """What a path under deploy/terraform is, for the rules that depend on where it lives."""
    parts = relpath.split("/")
    name = parts[-1]
    if ".terraform" in parts[:-1] or name == ".terraform":
        return "terraform-dir"
    if "terraform.d" in parts[:-1] or name == "terraform.d":
        return "plugin-dir"
    if name.endswith(".tfstate") or ".tfstate." in name:
        return "state"
    if name in (".terraformrc", "terraform.rc"):
        return "cli-config"
    if name.endswith(".tf.json") or name.endswith(".tftest.json") or name.endswith(".tfvars.json"):
        return "json-config"
    if name == "override.tf" or name.endswith("_override.tf"):
        return "override"
    if name.endswith(".tfvars"):
        if len(parts) == 6 and parts[2:4] == ["grafana", "stacks"] and name == "terraform.tfvars":
            return "tfvars"
        return "other-tfvars"
    if name == ".terraform.lock.hcl":
        return "lock"
    if name.endswith(".tftest.hcl"):
        if len(parts) == 7 and parts[2:4] == ["grafana", "stacks"] and parts[5] == "tests":
            return "test"
        return "misplaced-test"
    if name.endswith(".tf"):
        return "tf"
    if name.endswith(".hcl"):
        return "other-hcl"
    return "data"


def module_context(relpath):
    """(path.module, [path.root ...]) for a configuration file, as repo-relative directories."""
    d = posixpath.dirname(relpath)
    stacks = [GRAFANA + "/stacks/" + r for r in ROOTS]
    parts = d.split("/")
    if d in stacks:
        return d, [d]
    if len(parts) == 6 and parts[2:4] == ["grafana", "stacks"] and parts[5] == "tests":
        root = posixpath.dirname(d)
        return root, [root]
    if d == GRAFANA + "/test-alert":
        return d, [d]
    return d, stacks


def allowed_target(repo_path):
    return (repo_path.startswith(GRAFANA + "/") or repo_path == ALERTS
            or repo_path.startswith(DASHBOARDS + "/"))


def check_file_call(f, relpath, toks, k, head):
    """toks[k] is `file`, toks[k+1] is `(`. Refuses anything but the allowed argument forms."""
    j, depth = k + 2, 0
    while j < len(toks):
        u = toks[j]
        if u.type == "punct" and u.value in OPENERS:
            depth += 1
        elif u.type == "punct" and u.value in (")", "]", "}"):
            if depth == 0:
                break
            depth -= 1
        j += 1
    arg = [t for t in toks[k + 2:j] if t.type != "nl"]
    line = toks[k].line
    lit = literal(arg)
    base = None
    if lit is None:
        types = [t.type for t in arg]
        if types == ["qopen", "iopen", "ident", "punct", "ident", "iclose", "str", "qclose"] \
                and arg[1].value == "${" and arg[2].value == "path" and arg[3].value == "." \
                and arg[4].value in ("module", "root") and arg[6].value.startswith("/"):
            base, lit = arg[4].value, arg[6].value[1:]
        else:
            f.refuse("file() path", relpath, line,
                     "the argument must be a string literal or \"${path.module}/<literal>\" or "
                     "\"${path.root}/<literal>\"; a computed path is refused")
            return
    if "\\" in lit:
        # Terraform decodes \uNNNN, \UNNNNNNNN, \n, \\ ...: a path the policy reads one way and Terraform
        # another. No real call site needs an escape, so none is allowed.
        f.refuse("file() path", relpath, line, "escape sequences are refused in a file() path: %r" % lit)
        return
    if lit.startswith("/") or lit.startswith("~"):
        f.refuse("file() path", relpath, line, "absolute path %r" % lit)
        return
    module, roots = module_context(relpath)
    dirs = [module] if base == "module" else roots
    for d in dirs:
        target = posixpath.normpath(posixpath.join(d, lit))
        real = os.path.realpath(os.path.join(head, target))
        inside = rel(real, os.path.realpath(head))
        if inside.startswith("../") or not allowed_target(inside):
            f.refuse("file() path", relpath, line,
                     "%r resolves to %s, outside deploy/terraform/grafana/, %s and %s/"
                     % (lit, inside, ALERTS, DASHBOARDS))
            return


def check_expressions(f, relpath, items, head):
    for item in walk(items):
        if not isinstance(item, Attr):
            continue
        toks = item.toks
        for k, t in enumerate(toks):
            if t.type != "ident":
                continue
            nxt = toks[k + 1] if k + 1 < len(toks) else None
            if nxt is not None and nxt.type == "punct" and nxt.value == ":" and t.value == "provider" \
                    and k + 2 < len(toks) and toks[k + 2].value == ":":
                f.refuse("functions", relpath, t.line, "provider-defined functions are refused")
                continue
            if nxt is None or nxt.type != "punct" or nxt.value != "(":
                continue
            if t.value in REFUSED_FUNCTIONS:
                f.refuse("functions", relpath, t.line, "%s() is refused" % t.value)
            elif t.value == "file":
                check_file_call(f, relpath, toks, k, head)


def local_module_source(f, relpath, block, allowed_prefix):
    src = block.attrs().get("source")
    value = literal(src.toks) if src else None
    if value is None:
        f.refuse("blocks", relpath, block.line, "module source must be a string literal")
        return
    if "\\" in value:
        f.refuse("blocks", relpath, block.line, "escape sequences are refused in a module source: %r" % value)
        return
    if not (value.startswith("./") or value.startswith("../")):
        f.refuse("blocks", relpath, block.line, "module source %r is not a local path" % value)
        return
    target = posixpath.normpath(posixpath.join(posixpath.dirname(relpath), value))
    if not (target == allowed_prefix or target.startswith(allowed_prefix + "/")):
        f.refuse("blocks", relpath, block.line, "module source %r resolves to %s, outside %s/"
                 % (value, target, allowed_prefix))


def no_provisioners(f, relpath, items):
    for item in walk(items):
        if isinstance(item, Block) and item.type in ("provisioner", "connection"):
            f.refuse("blocks", relpath, item.line, "%s blocks are refused" % item.type)


def check_config(f, relpath, items):
    d = posixpath.dirname(relpath)
    is_route = d == GRAFANA + "/tests/route"
    is_test_alert = d == GRAFANA + "/test-alert"
    no_provisioners(f, relpath, items)
    for b in items:
        if isinstance(b, Attr):
            f.refuse("blocks", relpath, b.line, "a top-level attribute %r is not a block" % b.name)
            continue
        if b.type == "terraform":
            for inner in b.body:
                name = inner.name if isinstance(inner, Attr) else inner.type
                if name not in ("required_version", "required_providers", "backend"):
                    f.refuse("blocks", relpath, inner.line, "terraform.%s is refused" % name)
                elif name == "backend":
                    if is_route:
                        f.refuse("blocks", relpath, inner.line, "tests/route has no backend")
                    elif is_test_alert and inner.labels != ["local"]:
                        f.refuse("blocks", relpath, inner.line, "test-alert's backend must be local")
                    elif not is_test_alert and inner.labels != ["gcs"]:
                        f.refuse("blocks", relpath, inner.line, "a stack root's backend must be gcs")
                elif name == "required_providers":
                    for p in inner.body:
                        pname = p.name if isinstance(p, Attr) else p.type
                        if pname != "grafana":
                            f.refuse("blocks", relpath, p.line, "provider %r is refused; only grafana/grafana" % pname)
                        elif isinstance(p, Attr):
                            src = re.search(r'source\s*=\s*"([^"]*)"', "".join(
                                t.value for t in p.toks if t.type in ("ident", "str", "punct", "qopen", "qclose")))
                            if not src or src.group(1) not in ("grafana/grafana", GRAFANA_PROVIDER):
                                f.refuse("blocks", relpath, p.line, "grafana's source must be grafana/grafana")
        elif b.type == "provider":
            if is_route:
                f.refuse("blocks", relpath, b.line, "tests/route has no provider")
            elif b.labels != ["grafana"]:
                f.refuse("blocks", relpath, b.line, "provider %r is refused; only grafana" % " ".join(b.labels))
        elif b.type == "resource":
            rtype = b.labels[0] if b.labels else ""
            if is_route:
                f.refuse("blocks", relpath, b.line, "tests/route has no resources")
            elif not (rtype.startswith("grafana_") or rtype == "terraform_data"):
                f.refuse("blocks", relpath, b.line, "resource type %r is refused" % rtype)
        elif b.type == "data":
            dtype = b.labels[0] if b.labels else ""
            if is_route:
                f.refuse("blocks", relpath, b.line, "tests/route has no data sources")
            elif not dtype.startswith("grafana_"):
                f.refuse("blocks", relpath, b.line, "data source %r is refused" % dtype)
        elif b.type == "module":
            local_module_source(f, relpath, b, GRAFANA + "/modules")
        elif b.type not in ("variable", "locals", "output"):
            f.refuse("blocks", relpath, b.line, "%s blocks are refused" % b.type)


def check_test_file(f, relpath, items):
    no_provisioners(f, relpath, items)
    for b in items:
        if isinstance(b, Attr):
            f.refuse("test files", relpath, b.line, "a top-level attribute %r is refused" % b.name)
        elif b.type == "mock_provider":
            if b.labels != ["grafana"]:
                f.refuse("test files", relpath, b.line, "mock_provider %r is refused; only grafana" % " ".join(b.labels))
        elif b.type == "variables":
            for inner in b.body:
                if isinstance(inner, Block):
                    f.refuse("test files", relpath, inner.line, "variables holds attributes only")
        elif b.type == "run":
            for inner in b.body:
                if isinstance(inner, Attr):
                    if inner.name not in ("command", "expect_failures"):
                        f.refuse("test files", relpath, inner.line, "run.%s is refused" % inner.name)
                elif inner.type == "module":
                    src = inner.attrs().get("source")
                    if not src or literal(src.toks) != "../../tests/route" or len(inner.body) != 1:
                        f.refuse("test files", relpath, inner.line,
                                 'run.module must be exactly { source = "../../tests/route" }')
                elif inner.type not in ("variables", "assert"):
                    f.refuse("test files", relpath, inner.line, "run.%s is refused" % inner.type)
        else:
            f.refuse("test files", relpath, b.line, "%s blocks are refused in a test file" % b.type)


def protected_blocks(tree, parsed):
    """{key: source text} for every block the separate-merge path guards, from {relpath: (text, items)}."""
    out = {}
    for relpath, (text, items) in parsed.items():
        if kind_of(relpath) != "tf":
            continue
        d = posixpath.dirname(relpath)
        counts = {}
        for b in items:
            if not isinstance(b, Block):
                continue
            if b.type in ("variable", "output"):
                out[(d, b.type, " ".join(b.labels))] = text[b.start:b.end]
            elif b.type == "provider":
                n = counts.setdefault(("provider",) + tuple(b.labels), 0)
                counts[("provider",) + tuple(b.labels)] = n + 1
                out[(d, "provider", " ".join(b.labels), n)] = text[b.start:b.end]
            elif b.type == "terraform":
                for inner in b.blocks():
                    if inner.type in ("backend", "required_providers"):
                        out[(d, inner.type)] = text[inner.start:inner.end]
    return out


def describe(key):
    return "%s: %s" % (key[0], " ".join(str(k) for k in key[1:]))


def tfvars_values(items):
    """{name: (value or None, line)} for a tfvars file's attributes."""
    return {a.name: (literal(a.toks), a.line) for a in items if isinstance(a, Attr)}


def check_endpoints(f, relpath, head_items, base_items):
    head_vals = tfvars_values(head_items)
    base_vals = tfvars_values(base_items) if base_items is not None else {}
    base_hosts = set()
    for name, (value, _) in base_vals.items():
        if name.endswith("_url") and value:
            base_hosts.add(urllib.parse.urlsplit(value).hostname or "")
    changed = []
    for name, (value, line) in sorted(head_vals.items()):
        if name.endswith("_user"):
            if value is None or not re.fullmatch(r"[0-9]+", value):
                f.refuse("endpoints", relpath, line, "%s must be digits" % name)
        if not name.endswith("_url"):
            continue
        if value is None:
            f.refuse("endpoints", relpath, line, "%s must be a string literal" % name)
            continue
        u = urllib.parse.urlsplit(value)
        netloc_host = u.netloc
        if u.scheme != "https":
            f.refuse("endpoints", relpath, line, "%s must be https" % name)
        elif "@" in netloc_host or ":" in netloc_host:
            f.refuse("endpoints", relpath, line, "%s carries a user or a port: %r" % (name, netloc_host))
        elif u.query or u.fragment or "?" in value or "#" in value:
            f.refuse("endpoints", relpath, line, "%s carries a query or fragment" % name)
        elif netloc_host not in base_hosts:
            f.refuse("endpoints", relpath, line, "%s's host %r is not one of this stack's hosts on the base branch (%s)"
                     % (name, netloc_host, ", ".join(sorted(base_hosts)) or "none"))
        else:
            base_path = urllib.parse.urlsplit(base_vals[name][0]).path if base_vals.get(name, (None,))[0] else None
            if name in EMPTY_PATH and u.path:
                f.refuse("endpoints", relpath, line, "%s must have an empty path" % name)
            elif name in SENDER_PATHS and u.path not in (base_path, SENDER_PATHS[name]):
                f.refuse("endpoints", relpath, line, "%s's path %r is neither the base value nor %r"
                         % (name, u.path, SENDER_PATHS[name]))
            elif name not in EMPTY_PATH and name not in SENDER_PATHS and u.path != base_path:
                f.refuse("endpoints", relpath, line, "%s is not a known endpoint and its path changed" % name)
        if base_vals.get(name, (None,))[0] != value:
            changed.append((name, base_vals.get(name, (None,))[0], value, line))
    for name, (value, line) in base_vals.items():
        if name.endswith("_url") and name not in head_vals:
            changed.append((name, value, None, 1))
    for name, old, new, line in changed:
        f.refuse("separate-merge", relpath, line, "endpoint %s changed: %r -> %r; merge it on its own" % (name, old, new))


def lock_version(items):
    for b in items:
        if isinstance(b, Block) and b.type == "provider":
            v = b.attrs().get("version")
            return literal(v.toks) if v else None
    return None


def check_lock(f, relpath, text, items, base_text, base_items):
    for b in items:
        if not (isinstance(b, Block) and b.type == "provider" and b.labels == [GRAFANA_PROVIDER]):
            f.refuse("files", relpath, b.line, "the lock file holds only %s" % GRAFANA_PROVIDER)
    if base_text is None or text == base_text:
        return
    if lock_version(items) != lock_version(base_items):
        f.refuse("separate-merge", relpath, 1, "the provider version changed (%s -> %s); a version pin "
                 "and its hashes merge on their own" % (lock_version(base_items), lock_version(items)))
    else:
        f.refuse("files", relpath, 1, "the grafana/grafana hashes changed without a version change")


def tree_files(repo):
    """Repo-relative paths of every file under deploy/terraform in repo, and the symlinks there."""
    files, links = [], []
    top = os.path.join(repo, TF)
    for dirpath, dirnames, filenames in os.walk(top):
        for name in dirnames + filenames:
            full = os.path.join(dirpath, name)
            if os.path.islink(full):
                links.append(rel(full, repo))
        dirnames[:] = [d for d in dirnames if not os.path.islink(os.path.join(dirpath, d))]
        for name in filenames:
            full = os.path.join(dirpath, name)
            if not os.path.islink(full):
                files.append(rel(full, repo))
    return sorted(files), sorted(links)


def read(repo, relpath):
    try:
        with open(os.path.join(repo, relpath), encoding="utf-8") as fh:
            return fh.read()
    except FileNotFoundError:
        return None


def check(base, head):
    """The findings for head's deploy/terraform against base. An empty list means plan it."""
    f = Findings()
    files, links = tree_files(head)
    for link in links:
        f.refuse("symlinks", link, 1, "symlinks are refused")
    for extra in (ALERTS, DASHBOARDS):
        p = os.path.join(head, extra)
        if os.path.islink(p):
            f.refuse("symlinks", extra, 1, "symlinks are refused")
        for dirpath, dirnames, filenames in (os.walk(p) if os.path.isdir(p) else []):
            for name in dirnames + filenames:
                if os.path.islink(os.path.join(dirpath, name)):
                    f.refuse("symlinks", rel(os.path.join(dirpath, name), head), 1, "symlinks are refused")
            for name in dirnames + filenames:
                # Only deploy/terraform is policed as configuration; a module or plugin anywhere else
                # would be configuration nothing checked.
                if name == "terraform.d" or kind_of(TF + "/x/" + name) not in ("data",):
                    f.refuse("files", rel(os.path.join(dirpath, name), head), 1,
                             "Terraform files outside deploy/terraform are refused")
    if not os.path.isdir(os.path.join(base, GRAFANA)):
        f.refuse("separate-merge", GRAFANA, 1, "the base branch has no %s to compare to; this change "
                 "merges on its own" % GRAFANA)
        return f.items

    parsed = {}
    for relpath in files:
        kind = kind_of(relpath)
        if kind in ("terraform-dir", "plugin-dir", "state", "cli-config", "json-config", "override", "other-tfvars",
                    "misplaced-test", "other-hcl"):
            f.refuse("files", relpath, 1, {
                "terraform-dir": "a .terraform directory is refused",
                "plugin-dir": "a terraform.d directory is refused: Terraform installs providers from it",
                "state": "state files are refused",
                "cli-config": "Terraform CLI configuration is refused",
                "json-config": "JSON configuration is refused",
                "override": "override files are refused",
                "other-tfvars": "the only variable file is stacks/<root>/terraform.tfvars",
                "misplaced-test": "test files live in stacks/<root>/tests/ only",
                "other-hcl": "only .tf, .tftest.hcl, terraform.tfvars and the lock file are HCL here",
            }[kind])
            continue
        if kind == "data":
            continue
        text = read(head, relpath)
        try:
            parsed[relpath] = (text, parse(text))
        except ParseError as e:
            f.refuse("syntax", relpath, e.line, str(e))
        except UnicodeDecodeError:
            f.refuse("syntax", relpath, 1, "not UTF-8")

    for relpath, (text, items) in parsed.items():
        kind = kind_of(relpath)
        if kind == "tf":
            check_config(f, relpath, items)
        elif kind == "test":
            check_test_file(f, relpath, items)
        elif kind == "tfvars":
            for a in items:
                if isinstance(a, Block):
                    f.refuse("endpoints", relpath, a.line, "a variable file holds attributes only")
            base_text = read(base, relpath)
            base_items = None
            if base_text is not None:
                try:
                    base_items = parse(base_text)
                except ParseError:
                    base_items = None
            check_endpoints(f, relpath, items, base_items)
        elif kind == "lock":
            base_text = read(base, relpath)
            check_lock(f, relpath, text, items, base_text, parse(base_text) if base_text is not None else [])
        check_expressions(f, relpath, items, head)

    base_files, _ = tree_files(base)
    head_set = set(files)
    for relpath in base_files:
        if kind_of(relpath) == "lock" and relpath not in head_set:
            # Without it, `init -lockfile=readonly` has no pins to hold the provider to.
            f.refuse("files", relpath, 1, "the lock file was deleted; it pins grafana/grafana's hashes")
    base_parsed = {}
    for relpath in base_files:
        if kind_of(relpath) == "tf":
            text = read(base, relpath)
            try:
                base_parsed[relpath] = (text, parse(text))
            except (ParseError, UnicodeDecodeError):
                pass
    before, after = protected_blocks(base, base_parsed), protected_blocks(head, parsed)
    for key in sorted(set(before) | set(after), key=str):
        if before.get(key) != after.get(key):
            diff = "".join(difflib.unified_diff(
                (before.get(key) or "").splitlines(True), (after.get(key) or "").splitlines(True),
                "base", "head"))
            f.refuse("separate-merge", key[0], 1, "%s changed; it merges on its own:\n%s"
                     % (describe(key), diff.rstrip()))
    return f.items


# --- extract ---------------------------------------------------------------------------------------

def git(repo, *args, binary=False):
    p = subprocess.run(["git", "-C", repo, *args], capture_output=True, text=not binary)
    if p.returncode != 0:
        raise Refused("git %s: %s" % (" ".join(args[:3]), (p.stderr if not binary else p.stderr.decode()).strip()))
    return p.stdout


def extract(repo, rev, out):
    """Write rev's policed paths into out as regular files. Returns findings for links and submodules."""
    f = Findings()
    listing = git(repo, "ls-tree", "-r", "-z", "--full-tree", rev, "--", *POLICED)
    for entry in listing.split("\0"):
        if not entry:
            continue
        meta, path = entry.split("\t", 1)
        mode, otype, sha = meta.split()
        if mode == "120000":
            f.refuse("symlinks", path, 1, "a symlink in git (mode 120000) is refused")
            continue
        if mode == "160000" or otype != "blob":
            f.refuse("files", path, 1, "a submodule or non-file entry is refused")
            continue
        target = os.path.join(out, path)
        os.makedirs(os.path.dirname(target), exist_ok=True)
        with open(target, "wb") as fh:
            fh.write(git(repo, "cat-file", "blob", sha, binary=True))
    return f.items


def report(items):
    for rule, path, line, detail in items:
        print("tf-policy: refused: %s: %s:%d: %s" % (rule, path, line, detail), file=sys.stderr)


def main(argv):
    p = argparse.ArgumentParser(prog="tf_policy.py")
    sub = p.add_subparsers(dest="cmd")
    e = sub.add_parser("extract")
    e.add_argument("--repo", required=True)
    e.add_argument("--rev", required=True)
    e.add_argument("--out", required=True)
    c = sub.add_parser("check")
    c.add_argument("--base", required=True)
    c.add_argument("--head", required=True)
    try:
        a = p.parse_args(argv)
    except SystemExit as ex:
        return 2 if ex.code else 0
    if not a.cmd:
        p.print_usage(sys.stderr)
        return 2
    try:
        items = extract(a.repo, a.rev, a.out) if a.cmd == "extract" else check(a.base, a.head)
    except Refused as ex:
        print("tf-policy: %s" % ex, file=sys.stderr)
        return 1
    if items:
        report(items)
        print("tf-policy: %d refusal(s); the pull request's Terraform is not planned" % len(items), file=sys.stderr)
        return 1
    print("tf-policy: %s: allowed" % ("extracted" if a.cmd == "extract" else "deploy/terraform"))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
