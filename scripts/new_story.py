#!/usr/bin/env python3
"""Scaffold a new story from the template, allocating the next ID for a component.

    scripts/new_story.py SRV "Load zone definitions at boot"

IDs are monotonic per component and never reused. Existing files are never overwritten.
"""

import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from andara_docs import COMPONENTS, STORIES_DIR, TEMPLATE_DIR, rel  # noqa: E402


def slugify(title):
    s = re.sub(r"[^a-z0-9]+", "-", title.lower()).strip("-")
    return re.sub(r"-{2,}", "-", s)[:60]


def main():
    if len(sys.argv) < 3:
        sys.stderr.write(
            "story: usage: make story COMP=<SRV|CLI|INF|CLT> TITLE=\"<title>\"\n"
        )
        return 2
    comp = sys.argv[1].strip().upper()
    title = " ".join(sys.argv[2:]).strip()
    if comp not in COMPONENTS:
        sys.stderr.write(
            "story: COMP '%s' is not one of %s\n" % (comp, ", ".join(sorted(COMPONENTS)))
        )
        return 2
    if not title:
        sys.stderr.write("story: TITLE must not be empty\n")
        return 2

    highest = 0
    pat = re.compile(r"^AW-%s-(\d{3})-" % comp)
    for name in os.listdir(STORIES_DIR):
        m = pat.match(name)
        if m:
            highest = max(highest, int(m.group(1)))
    sid = "AW-%s-%03d" % (comp, highest + 1)
    slug = slugify(title)
    path = os.path.join(STORIES_DIR, "%s-%s.md" % (sid, slug))
    if os.path.exists(path):
        sys.stderr.write("story: %s already exists; refusing to overwrite\n" % rel(path))
        return 1

    with open(os.path.join(TEMPLATE_DIR, "story.md"), "r", encoding="utf-8") as fh:
        text = fh.read()
    text = text.replace("id: AW-XXX-NNN", "id: %s" % sid)
    text = text.replace(
        "title: <imperative phrase, what the system will do>", "title: %s" % title
    )
    text = text.replace(
        "component: server          #", "component: %s          #" % COMPONENTS[comp]
    )
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)
    print(rel(path))
    return 0


if __name__ == "__main__":
    sys.exit(main())
