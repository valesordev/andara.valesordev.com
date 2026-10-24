#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make slo-report: render the 28-day SLIs into the SLO documents' measurement tables (AW-INF-039).

    scripts/slo_report.py            reads solo7dev (local and dev) through gcx, appends one row per
                                     namespace to each document's ## Measurement table

Each document in DOCS carries its SLI expression and target here and, verbatim (whitespace aside), in
its own ## SLI section; scripts/tests/test_slo_report.py holds the two equal, so the number this writes
is the number the document defines. The query is an instant query of the 28-day expression, per
namespace, with CI's cluster excluded (ADR-0014 A3: CI ships `namespace="andara-local"` on cluster
`ci`, and its short runs are not local's availability).

No drill window is excluded yet (ruling 5): every table carries NOTE, and the "Drill windows named"
column is left for the operator, who names the windows from the drill's own `drill: start/end` output
until AW-INF-081's sink exists.

Exit codes: 0 rendered; 1 a query failed (gcx's message); 2 usage; 3 GRAFANA_SOLO7DEV_READ_TOKEN unset
in the environment and in .local/box.env, before any query.
"""
import datetime
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gcx  # noqa: E402
import grafana_stack  # noqa: E402

REPO = os.path.dirname(HERE)
SLO_DIR = os.path.join(REPO, "docs", "specs", "slo")
NOTE = "_Includes drill minutes; drill exclusion not yet applied (AW-INF-081)._"
HEADER = "| Measured (UTC) | Environment | SLI (28 d) | Budget left | Drill windows named |"
SEPARATOR = "|---|---|---|---|---|"

DOCS = [
    {
        "file": "world-write-availability.md",
        "target": 0.995,  # the pre-launch target in force; the document's table says which
        "expr": '1 - avg by (namespace) ('
                'avg_over_time(max_over_time(andara_ingress_degraded{cluster!="ci"}[1m])[28d:1m]))',
    },
    {
        "file": "kafka-availability.md",
        "target": 0.999,
        "expr": '1 - avg by (namespace) ('
                'avg_over_time((max_over_time(andara_consumer_lag{cluster!="ci"}[1m]) > bool 400)[28d:1m]))',
    },
]


def say(msg):
    print("slo-report: " + msg, flush=True)


def squash(s):
    return re.sub(r"\s+", "", s)


def budget_left(sli, target):
    """The fraction of the error budget not yet spent: 1 at a perfect SLI, 0 at the target, negative past it."""
    return 1 - (1 - sli) / (1 - target)


def rows(results, target, when):
    out = []
    for r in sorted(results, key=lambda r: r.get("metric", {}).get("namespace", "")):
        ns = r.get("metric", {}).get("namespace") or "(no namespace)"
        sli = float(r["value"][1])
        out.append("| %s | %s | %.4f%% | %.1f%% | — |" % (when, ns, sli * 100, budget_left(sli, target) * 100))
    return out


def render(text, new_rows):
    """text with NOTE and the table under ## Measurement, and new_rows appended to the table."""
    m = re.search(r"^## Measurement\n", text, re.M)
    if not m:
        raise ValueError("no ## Measurement section")
    start = m.end()
    nxt = re.search(r"^## ", text[start:], re.M)
    end = start + nxt.start() if nxt else len(text)
    section = text[start:end].split("\n")
    if HEADER not in section:
        raise ValueError("no measurement table (%s) under ## Measurement" % HEADER)
    h = section.index(HEADER)
    if NOTE not in section:
        section[h:h] = [NOTE, ""]
        h += 2
    last = h + 1
    while last + 1 < len(section) and section[last + 1].startswith("|"):
        last += 1
    section[last + 1:last + 1] = new_rows
    return text[:start] + "\n".join(section) + text[end:]


def main(argv, q=gcx, now=None):
    if argv:
        raise grafana_stack.Exit(2, "usage: make slo-report")
    ctx, _ = grafana_stack.require("dev")  # dev and local share solo7dev
    q.write_config()
    when = (now or datetime.datetime.now(datetime.timezone.utc)).strftime("%Y-%m-%d %H:%M")
    # Every query, then every write: a failed query leaves no document half-updated.
    rendered = []
    for d in DOCS:
        path = os.path.join(SLO_DIR, d["file"])
        new = rows(q.metrics_query(ctx, d["expr"]), d["target"], when)
        if not new:
            new = ["| %s | (no series) | — | — | — |" % when]
        with open(path) as f:
            rendered.append((path, d["file"], render(f.read(), new), len(new)))
    for path, name, text, n in rendered:
        with open(path, "w") as f:
            f.write(text)
        say("%s: %d row(s) from %s" % (name, n, ctx))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except grafana_stack.Exit as e:
        say(str(e))
        sys.exit(e.code)
    except (gcx.GcxError, ValueError) as e:
        say(str(e))
        sys.exit(1)
