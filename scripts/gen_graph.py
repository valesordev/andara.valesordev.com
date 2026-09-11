#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Emit the story dependency DAG as Mermaid on stdout."""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from andara_docs import load_stories  # noqa: E402

CLASSES = {
    "blocked": "fill:#3a1f1f,stroke:#c0392b,color:#f5d5d0",
    "review": "fill:#3a331f,stroke:#c9a227,color:#f5efd5",
    "in-progress": "fill:#1f2f3a,stroke:#2980b9,color:#d5e8f5",
    "ready": "fill:#1f3a26,stroke:#27ae60,color:#d5f5e0",
    "draft": "fill:#2a2a2a,stroke:#777777,color:#dddddd",
    "done": "fill:#232323,stroke:#444444,color:#888888",
}


def main():
    stories = [d for _, d, _, _ in load_stories()]
    print("graph LR")
    for status in CLASSES:
        print("  classDef %s %s;" % (status.replace("-", "_"), CLASSES[status]))
    for d in sorted(stories, key=lambda x: x.get("id", "")):
        sid = d.get("id")
        node = sid.replace("-", "_")
        label = "%s<br/>%s" % (sid, d.get("title", "")[:44])
        print('  %s["%s"]:::%s' % (node, label, str(d.get("status", "draft")).replace("-", "_")))
    print("")
    for d in sorted(stories, key=lambda x: x.get("id", "")):
        for dep in d.get("depends_on") or []:
            print("  %s --> %s" % (dep.replace("-", "_"), d.get("id").replace("-", "_")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
