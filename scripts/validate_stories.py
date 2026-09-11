#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Schema-check story frontmatter, resolve dependency IDs, and detect cycles.

Exit 0 when every story is valid. Exit 1 with one actionable line per violation.
Implements the frontmatter schema in docs/stories/AW-INF-001.
"""

import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from andara_docs import (  # noqa: E402
    ADR_DIR, ADR_ID_RE, COMPONENTS, EPICS_DIR, EPIC_ID_RE, STORY_ID_RE,
    STORY_SCHEMA, DocError, load_dir, load_stories, rel,
)

ADR_REF_RE = re.compile(r"\bADR-\d{4}\b")


def main():
    errors = []

    try:
        stories = load_stories()
        epics = load_dir(EPICS_DIR)
        adrs = load_dir(ADR_DIR)
    except DocError as exc:
        sys.stderr.write(str(exc) + "\n")
        return 1

    epic_ids = {d.get("id") for _, d, _, _ in epics}
    adr_ids = {d.get("id") for _, d, _, _ in adrs}
    by_id = {}

    for path, data, order, body in stories:
        p = rel(path)

        # Required keys, in the required order.
        required = [k for k, _, _ in STORY_SCHEMA]
        missing = [k for k in required if k not in data]
        for k in missing:
            errors.append("%s: missing required frontmatter key '%s'" % (p, k))
        present = [k for k in order if k in required]
        expected = [k for k in required if k in data]
        if present != expected:
            errors.append(
                "%s: frontmatter keys out of order; expected %s" % (p, ", ".join(required))
            )

        for key, kind, allowed in STORY_SCHEMA:
            if key not in data:
                continue
            val = data[key]
            if kind == "list" and not isinstance(val, list):
                errors.append("%s: key '%s' must be a list, e.g. []" % (p, key))
            elif kind in ("str", "enum") and isinstance(val, list):
                errors.append("%s: key '%s' must be a scalar, not a list" % (p, key))
            elif kind == "enum" and val not in allowed:
                errors.append(
                    "%s: key '%s' has value '%s'; permitted values: %s"
                    % (p, key, val, ", ".join(allowed))
                )
            elif kind == "str" and not str(val).strip():
                errors.append("%s: key '%s' must not be empty" % (p, key))

        sid = data.get("id", "")
        m = STORY_ID_RE.match(str(sid))
        if not m:
            errors.append(
                "%s: id '%s' does not match the pattern "
                "AW-<SRV|CLI|INF|CLT>-<3 digits>" % (p, sid)
            )
            continue

        if sid in by_id:
            errors.append("%s: duplicate story id '%s', also in %s" % (p, sid, rel(by_id[sid][0])))
        by_id[sid] = (path, data)

        base = os.path.basename(path)
        if not base.startswith(sid + "-"):
            errors.append("%s: filename must start with the story id '%s-'" % (p, sid))

        want_component = COMPONENTS[m.group(1)]
        if data.get("component") not in (None, want_component):
            errors.append(
                "%s: component '%s' disagrees with id prefix %s; expected '%s'"
                % (p, data.get("component"), m.group(1), want_component)
            )

        epic = data.get("epic")
        if epic is not None:
            if not EPIC_ID_RE.match(str(epic)):
                errors.append(
                    "%s: epic '%s' does not match the pattern EPIC-<2 digits>" % (p, epic)
                )
            elif epic not in epic_ids:
                errors.append("%s: epic '%s' does not resolve to a file in docs/epics/" % (p, epic))

        for ref in sorted(set(ADR_REF_RE.findall(body))):
            if ref not in adr_ids:
                errors.append("%s: references %s, which does not resolve in docs/adr/" % (p, ref))
        for ref in sorted(set(ADR_REF_RE.findall(" ".join(str(v) for v in data.values())))):
            if not ADR_ID_RE.match(ref) or ref not in adr_ids:
                errors.append("%s: frontmatter references %s, which does not resolve" % (p, ref))

    # Cross-story checks. Only run once every id is known.
    for sid, (path, data) in sorted(by_id.items()):
        p = rel(path)
        for field in ("depends_on", "blocks"):
            for ref in data.get(field, []) or []:
                if ref not in by_id:
                    errors.append(
                        "%s: %s names '%s', which does not resolve to a story in docs/stories/"
                        % (p, field, ref)
                    )
                    continue
                other = by_id[ref][1]
                mirror = "blocks" if field == "depends_on" else "depends_on"
                if sid not in (other.get(mirror, []) or []):
                    errors.append(
                        "%s: %s lists '%s' but %s does not list '%s' in %s (must be symmetric)"
                        % (p, field, ref, ref, sid, mirror)
                    )

        if data.get("status") == "ready":
            if data.get("size") == "L":
                errors.append(
                    "%s: size 'L' with status 'ready'; split it before marking ready "
                    "(CLAUDE.md §6)" % p
                )
            for ref in data.get("depends_on", []) or []:
                dep = by_id.get(ref, (None, {}))[1]
                if dep.get("status") in ("draft", "blocked"):
                    errors.append(
                        "%s: status 'ready' but depends_on '%s' has status '%s'"
                        % (p, ref, dep.get("status"))
                    )

    cycle = find_cycle(by_id)
    if cycle:
        errors.append("dependency cycle: " + " -> ".join(cycle))

    if errors:
        for e in errors:
            sys.stderr.write("validate-stories: " + e + "\n")
        sys.stderr.write(
            "validate-stories: %d violation(s); no stories were modified\n" % len(errors)
        )
        return 1

    print("validate-stories: %d stories valid" % len(by_id))
    return 0


def find_cycle(by_id):
    """Return the first depends_on cycle as a list of ids, or None."""
    WHITE, GREY, BLACK = 0, 1, 2
    color = {k: WHITE for k in by_id}
    stack = []

    def visit(node):
        color[node] = GREY
        stack.append(node)
        for dep in by_id[node][1].get("depends_on", []) or []:
            if dep not in by_id:
                continue
            if color[dep] == GREY:
                return stack[stack.index(dep):] + [dep]
            if color[dep] == WHITE:
                found = visit(dep)
                if found:
                    return found
        stack.pop()
        color[node] = BLACK
        return None

    for node in sorted(by_id):
        if color[node] == WHITE:
            found = visit(node)
            if found:
                return found
    return None


if __name__ == "__main__":
    sys.exit(main())
