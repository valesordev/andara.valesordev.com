# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Shared frontmatter parsing and repo-layout constants for Andara's docs tooling (ADRs).

Stories, epics, and sprints are GitHub issues since the SPRINT-05 cutover; nothing here reads them.

Deliberately stdlib-only. This tooling validates the repo; it must not depend on
anything the repo has to install first.
"""

import os
import re
import sys

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ADR_DIR = os.path.join(REPO, "docs", "adr")
TEMPLATE_DIR = os.path.join(REPO, "docs", ".templates")

ADR_ID_RE = re.compile(r"^ADR-(\d{4})$")


class DocError(Exception):
    pass


def parse_frontmatter(path):
    """Return (dict, body). Supports the scalar/list subset this repo uses.

    Not a YAML parser. If a doc needs more YAML than this, the doc is the problem.
    """
    with open(path, "r", encoding="utf-8") as fh:
        text = fh.read()
    if not text.startswith("---\n"):
        raise DocError("%s: missing YAML frontmatter (file must start with '---')" % path)
    end = text.find("\n---\n", 3)
    if end == -1:
        raise DocError("%s: frontmatter is not terminated by a '---' line" % path)
    block = text[4:end]
    body = text[end + 5:]

    data = {}
    order = []
    for lineno, line in enumerate(block.split("\n"), start=2):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if ":" not in line:
            raise DocError("%s:%d: frontmatter line is not 'key: value'" % (path, lineno))
        key, _, raw = line.partition(":")
        key = key.strip()
        raw = raw.split("  #")[0].strip()
        if raw.startswith("[") and raw.endswith("]"):
            inner = raw[1:-1].strip()
            value = [v.strip() for v in inner.split(",") if v.strip()] if inner else []
        else:
            value = raw.strip("'\"")
        data[key] = value
        order.append(key)
    return data, order, body


def load_dir(dirname):
    """Load every .md in dirname (skipping dotfiles) as (path, data, order, body)."""
    out = []
    if not os.path.isdir(dirname):
        return out
    for name in sorted(os.listdir(dirname)):
        if not name.endswith(".md") or name.startswith("."):
            continue
        path = os.path.join(dirname, name)
        data, order, body = parse_frontmatter(path)
        out.append((path, data, order, body))
    return out


def rel(path):
    return os.path.relpath(path, REPO)


def die(msg, code=1):
    sys.stderr.write(msg.rstrip("\n") + "\n")
    sys.exit(code)
