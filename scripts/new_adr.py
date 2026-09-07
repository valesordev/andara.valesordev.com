#!/usr/bin/env python3
"""Scaffold a new ADR from the template, allocating the next four-digit ID.

    scripts/new_adr.py "Sharding model"

ADR IDs are monotonic and never reused, including for rejected or superseded ADRs.
"""

import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from andara_docs import ADR_DIR, TEMPLATE_DIR, rel  # noqa: E402


def slugify(title):
    s = re.sub(r"[^a-z0-9]+", "-", title.lower()).strip("-")
    return re.sub(r"-{2,}", "-", s)[:60]


def main():
    title = " ".join(sys.argv[1:]).strip()
    if not title:
        sys.stderr.write('adr: usage: make adr TITLE="<title>"\n')
        return 2

    highest = 0
    pat = re.compile(r"^ADR-(\d{4})-")
    for name in os.listdir(ADR_DIR):
        m = pat.match(name)
        if m:
            highest = max(highest, int(m.group(1)))
    aid = "ADR-%04d" % (highest + 1)
    path = os.path.join(ADR_DIR, "%s-%s.md" % (aid, slugify(title)))
    if os.path.exists(path):
        sys.stderr.write("adr: %s already exists; refusing to overwrite\n" % rel(path))
        return 1

    with open(os.path.join(TEMPLATE_DIR, "adr.md"), "r", encoding="utf-8") as fh:
        text = fh.read()
    text = text.replace("id: ADR-NNNN", "id: %s" % aid)
    text = text.replace(
        "title: <decision subject, stated as a noun phrase>", "title: %s" % title
    )
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)
    print(rel(path))
    return 0


if __name__ == "__main__":
    sys.exit(main())
