#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Check the Content Language grammar against the conformance corpus (AW-CLI-005 AC-1).

Builds docs/specs/content-language/v1/grammar.ebnf with lark and parses every corpus
file, asserting three things the grammar alone cannot assert about itself:

  corpus/valid/**, corpus/pending/**   every .aw parses
  corpus/invalid/semantic/**           every .aw parses — a semantic case that fails to
                                       parse is testing the wrong thing, and would pass
                                       a conforming compiler for the wrong reason
  corpus/invalid/syntax/**             every .aw fails to parse, at the file:line:col its
                                       .errors sidecar names
  corpus/roundtrip/**                  every .aw parses

corpus/invalid/encoding/ is excluded from the parse check on purpose: those cases are
wrong in their bytes before a grammar is reached, which is what they are testing.

and two things about the expected output beside them:

  expected/**.json                     is canonical per semantics.md section 7 — formatVersion
                                       first, then field-number order, two-space indent, LF
  TemplateDefinition.source            names a line that really does open that Template in
                                       that source file

and the Definition of done's coverage rule: every error code errors.md declares appears in
at least one .errors sidecar, and no sidecar uses a code errors.md does not declare.

This is the one acceptance criterion of AW-CLI-005 that is mechanically checkable before
the compiler (AW-CLI-006) exists, which is the whole point of specifying the language
before implementing it (CLAUDE.md §2). AC-2, AC-3 and AC-9 need a compiler and are
`make content-conformance`, which AW-CLI-006 wires up.

Exit 0 when the corpus agrees with the grammar. Exit 1 with one line per disagreement.
"""

import json
import os
import re
import sys

SPEC = "docs/specs/content-language/v1"
GRAMMAR = os.path.join(SPEC, "grammar.ebnf")
ERRORS_DOC = os.path.join(SPEC, "errors.md")
CORPUS = os.path.join(SPEC, "corpus")

# `file:line:col: code`, the first line of a finding in a .errors sidecar. Chain lines are
# indented beneath it and are not this script's business — they are semantics, and a
# conforming compiler checks them under `make content-conformance`.
FINDING_RE = re.compile(r"^(?P<file>[^:]+):(?P<line>\d+):(?P<col>\d+): (?P<code>[a-z_]+)$")

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# Key order per message, formatVersion first and then field-number order (semantics.md
# section 7). Held here rather than read from the .proto because this script's job is to
# disagree with the corpus when one of them is wrong, and a table it derived from the same
# place the corpus was written from could not.
KEY_ORDER = {
    # fallbackRoom is field 6, pending AW-SRV-012 (semantics.md section 9).
    "ZoneDefinition": ["formatVersion", "id", "name", "rooms", "components", "fallbackRoom"],
    "RoomDefinition": ["id", "title", "description", "exits", "components"],
    "ExitDefinition": ["direction", "toZone", "toRoom", "perceives"],
    "ComponentValue": ["type", "fields"],
    "ComponentField": ["name", "stringValue", "intValue", "boolValue"],
    "TemplateDefinition": ["formatVersion", "name", "kind", "chain", "components",
                           "provenance", "resolved", "source"],
    "FieldProvenance": ["component", "field", "from"],
    "SourceRef": ["file", "line"],
}

# Which message a nested key holds, so the order check can recurse.
NESTED = {
    "rooms": "RoomDefinition", "exits": "ExitDefinition", "components": "ComponentValue",
    "fields": "ComponentField", "provenance": "FieldProvenance", "source": "SourceRef",
}


def rel(path):
    return os.path.relpath(path, REPO)


def aw_files(*parts):
    """Every .aw under a corpus subtree, sorted, repo-relative."""
    root = os.path.join(REPO, CORPUS, *parts)
    out = []
    for dirpath, _, names in os.walk(root):
        for name in sorted(names):
            if name.endswith(".aw"):
                out.append(os.path.join(dirpath, name))
    return sorted(out)


def eof_position(text):
    """Where a parser reports a file that ended too early: after the last character.

    lark raises UnexpectedEOF without a position. A Builder whose brace is missing needs
    to be told where the file ran out, so the contract (errors.md) is that end-of-input is
    reported at the position one past the final character, and the sidecars say that.
    """
    lines = text.split("\n")
    return len(lines), len(lines[-1]) + 1


def declared_codes():
    """Every code errors.md declares as raisable, from the leading cell of its 3.x tables.

    Section 3.5 is excluded: those are loader-only codes the compiler cannot raise, so a
    corpus case for one would be a case no compiler can satisfy. errors.md says which and
    why, and this is the one place that distinction has to be mechanical.
    """
    codes = set()
    in_three = False
    with open(os.path.join(REPO, ERRORS_DOC), encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("## "):
                in_three = line.startswith("## 3.")
            elif line.startswith("### "):
                in_three = line.startswith("### 3.") and not line.startswith("### 3.5")
            if not in_three or not line.startswith("| `"):
                continue
            code = line.split("`")[1]
            if re.fullmatch(r"[a-z_]+", code):
                codes.add(code)
    return codes


def sidecar_codes():
    """Every code the corpus actually uses, with one example path each."""
    used = {}
    for dirpath, _, names in os.walk(os.path.join(REPO, CORPUS)):
        for name in sorted(names):
            if not name.endswith(".errors"):
                continue
            path = os.path.join(dirpath, name)
            with open(path, encoding="utf-8") as fh:
                for line in fh:
                    m = FINDING_RE.match(line.rstrip("\n"))
                    if m:
                        used.setdefault(m["code"], path)
    return used


def expected_files():
    """Every expected/**.json in the corpus, sorted."""
    out = []
    for dirpath, _, names in os.walk(os.path.join(REPO, CORPUS)):
        if "expected" not in dirpath.split(os.sep):
            continue
        for name in sorted(names):
            if name.endswith(".json"):
                out.append(os.path.join(dirpath, name))
    return sorted(out)


def check_key_order(path, message, obj, errors):
    want = KEY_ORDER[message]
    got = [k for k in obj]
    unknown = [k for k in got if k not in want]
    if unknown:
        errors.append(f"{rel(path)}: {message} carries {unknown}, which {message} has no field for")
        return
    index = [want.index(k) for k in got]
    if index != sorted(index):
        errors.append(
            f"{rel(path)}: {message} keys are {got}; canonical order is "
            f"{[k for k in want if k in got]} (semantics.md section 7)"
        )
    for key, value in obj.items():
        nested = NESTED.get(key)
        if not nested:
            continue
        for item in value if isinstance(value, list) else [value]:
            check_key_order(path, nested, item, errors)


def check_expected(path, errors):
    """Canonical form, and that a Template's source line opens that Template."""
    raw = open(path, "rb").read()
    try:
        obj = json.loads(raw)
    except ValueError as exc:
        errors.append(f"{rel(path)}: not JSON: {exc}")
        return
    message = "TemplateDefinition" if "chain" in obj or "resolved" in obj else "ZoneDefinition"
    check_key_order(path, message, obj, errors)

    canonical = (json.dumps(obj, indent=2, ensure_ascii=False) + "\n").encode("utf-8")
    if raw != canonical:
        errors.append(
            f"{rel(path)}: not canonical bytes — two-space indent, one key per line, "
            f"trailing LF (semantics.md section 7)"
        )

    if message != "TemplateDefinition":
        return
    source, name = obj.get("source"), obj.get("name", "")
    if not source:
        errors.append(f"{rel(path)}: a TemplateDefinition with no source")
        return
    # source.file is <packdir>/<path>: the pack directory is the case directory, which is
    # the one holding expected/, however deep the blob sits inside it.
    parts = path.split(os.sep)
    case = os.sep.join(parts[: parts.index("expected")])
    inside = source["file"].split("/", 1)
    if len(inside) != 2:
        errors.append(f"{rel(path)}: source.file {source['file']!r} is not <packdir>/<path>")
        return
    packdir, within = inside
    if packdir != os.path.basename(case):
        errors.append(
            f"{rel(path)}: source.file names pack directory {packdir!r}, but the case "
            f"directory is {os.path.basename(case)!r} (semantics.md section 6)"
        )
        return
    src = os.path.join(case, within)
    if not os.path.exists(src):
        errors.append(f"{rel(path)}: source.file names {source['file']!r}, which does not exist")
        return
    lines = open(src, encoding="utf-8").read().split("\n")
    n = source["line"]
    short = name.rsplit(".", 1)[-1]
    if not 1 <= n <= len(lines):
        errors.append(f"{rel(path)}: source.line {n} is outside {source['file']!r}")
    elif not lines[n - 1].startswith(f"template {short} "):
        errors.append(
            f"{rel(path)}: source says {source['file']}:{n}, but that line is "
            f"{lines[n - 1].strip()!r}, not the declaration of {short}"
        )


def load_parser():
    try:
        from lark import Lark
    except ImportError:
        sys.stderr.write(
            "lark is not installed; it is pinned in scripts/requirements.txt.\n"
            "Run `make bootstrap`, or: python3 -m pip install -r scripts/requirements.txt\n"
        )
        raise SystemExit(2)

    with open(os.path.join(REPO, GRAMMAR), encoding="utf-8") as fh:
        grammar = fh.read()

    # Earley with the dynamic lexer, because the language has no reserved words: `entity`
    # is a Template kind in one position and a legal Room id in another, and a context-free
    # lexer would have to pick one. AW-CLI-006's recursive-descent parser is contextual for
    # the same reason (semantics.md §1).
    return Lark(grammar, start="file", parser="earley", propagate_positions=True)


def parse(parser, path):
    """Parse one file. Returns None on success, or (line, col) of the syntax error."""
    from lark import UnexpectedEOF, UnexpectedInput

    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    try:
        parser.parse(text)
        return None
    except UnexpectedEOF:
        return eof_position(text)
    except UnexpectedInput as exc:
        if exc.line is None or exc.line < 0:
            return eof_position(text)
        return exc.line, exc.column


def sidecar_positions(path):
    """The (line, col, code) findings a .errors sidecar names for its .aw, in order."""
    errors_path = path[: -len(".aw")] + ".errors"
    if not os.path.exists(errors_path):
        return None, f"{rel(path)}: no .errors sidecar beside it"
    out = []
    with open(errors_path, encoding="utf-8") as fh:
        for n, line in enumerate(fh, 1):
            line = line.rstrip("\n")
            if not line or line.startswith("  ") or line.startswith("#"):
                continue
            m = FINDING_RE.match(line)
            if not m:
                return None, f"{rel(errors_path)}:{n}: not `file:line:col: code`: {line!r}"
            out.append((int(m["line"]), int(m["col"]), m["code"], m["file"]))
    if not out:
        return None, f"{rel(errors_path)}: no findings"
    return out, None


def main():
    if not os.path.isdir(os.path.join(REPO, CORPUS)):
        sys.stderr.write(f"{CORPUS}: no corpus directory\n")
        return 1

    parser = load_parser()
    errors = []
    counts = {"parses": 0, "rejects": 0}

    # Must parse.
    for group in (("valid",), ("pending",), ("roundtrip",), ("invalid", "semantic")):
        for path in aw_files(*group):
            pos = parse(parser, path)
            if pos is not None:
                errors.append(
                    f"{rel(path)}:{pos[0]}:{pos[1]}: expected to parse, but the grammar "
                    f"rejected it"
                )
            else:
                counts["parses"] += 1

    # Must not parse, at the position the sidecar names.
    for path in aw_files("invalid", "syntax"):
        findings, problem = sidecar_positions(path)
        if problem:
            errors.append(problem)
            continue
        if len(findings) != 1:
            errors.append(
                f"{rel(path)}: a syntax case names {len(findings)} findings; parsing stops "
                f"at the first, so the sidecar carries exactly one"
            )
            continue
        line, col, code, named_file = findings[0]
        if code != "syntax_error":
            errors.append(
                f"{rel(path)}: a case under invalid/syntax/ carries code {code!r}; only "
                f"syntax_error stops the parse, everything else belongs in invalid/semantic/"
            )
            continue
        if os.path.basename(named_file) != os.path.basename(path):
            errors.append(
                f"{rel(path)}: sidecar names {named_file!r}, which is not this file"
            )
            continue
        pos = parse(parser, path)
        if pos is None:
            errors.append(f"{rel(path)}: expected a syntax error, but the grammar accepted it")
        elif pos != (line, col):
            errors.append(
                f"{rel(path)}: sidecar says {line}:{col}, the grammar reports "
                f"{pos[0]}:{pos[1]}"
            )
        else:
            counts["rejects"] += 1

    # Coverage: errors.md and the corpus name the same set of codes.
    declared, used = declared_codes(), sidecar_codes()
    if not declared:
        errors.append(f"{ERRORS_DOC}: no codes found; has section 3 been restructured?")
    for code in sorted(declared - set(used)):
        errors.append(f"{ERRORS_DOC}: code {code!r} is declared but no corpus case raises it")
    for code in sorted(set(used) - declared):
        errors.append(f"{rel(used[code])}: code {code!r} is not declared in errors.md section 3")

    # Every pending case names the story that unblocks it, or it is not pending, it is
    # forgotten (semantics.md §9).
    pending_root = os.path.join(REPO, CORPUS, "pending")
    if os.path.isdir(pending_root):
        for name in sorted(os.listdir(pending_root)):
            case = os.path.join(pending_root, name)
            if not os.path.isdir(case):
                continue
            marker = os.path.join(case, "PENDING")
            if not os.path.exists(marker):
                errors.append(f"{rel(case)}: a pending case with no PENDING marker naming its story")
            elif not re.match(r"^AW-[A-Z]{3}-\d{3} ", open(marker, encoding="utf-8").readline()):
                errors.append(
                    f"{rel(marker)}: first line must start with the gating story id, "
                    f"e.g. `AW-SRV-012 — ZoneDefinition.fallback_room (field 6)`"
                )

    # Expected output: canonical bytes, and a source line that opens the Template.
    expected = expected_files()
    for path in expected:
        check_expected(path, errors)

    for e in errors:
        sys.stderr.write(e + "\n")
    if errors:
        sys.stderr.write(f"\ncontent-grammar-check: {len(errors)} disagreement(s)\n")
        return 1

    print(
        f"content-grammar-check: {counts['parses']} files parse, "
        f"{counts['rejects']} rejected at the position their sidecar names, "
        f"{len(expected)} expected blobs canonical, "
        f"{len(declared)} error codes each covered"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
