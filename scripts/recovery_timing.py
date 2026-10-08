#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
"""AW-SRV-007 AC-7: the recovery-timing.json CI job's two helpers.

  recovery_timing.py summary CURRENT [PREVIOUS]
      Render CURRENT (the JSON TestRecoveryTimingAtSizingScale writes) as a markdown table, with
      PREVIOUS beside it when it exists, to $GITHUB_STEP_SUMMARY (stdout when that is unset). The
      `replay` phase is the one the story says to compare. Exits 1 when CURRENT is missing or
      isn't the shape the test writes; a missing PREVIOUS is the first run and is not an error.

  recovery_timing.py previous OUT_DIR [--workflow NAME] [--branch main]
      Download the `recovery-timing` artifact of the newest successful run of the workflow on
      the branch into OUT_DIR, with `gh`. Exits 0 and prints "no previous run" when there is none,
      since the first run has nothing to compare with.

  recovery_timing.py budget CURRENT VALUES... [--factor 3]
      AW-INF-011 AC-7: the startup budget (`probes.startup.periodSeconds × failureThreshold`, the
      later VALUES files overriding the earlier) must be at least FACTOR × CURRENT's `total`.
      Prints the comparison; exits 1 and says what failureThreshold would satisfy it when not.

The bounds themselves (`total` under 90 s, a tail of at least 600 ticks) are the test's own
assertions. It writes the JSON before it asserts them, so a run that breaks one still has its
numbers, and `previous` only reads successful runs.
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

PHASES = ("load", "seek", "replay", "verify", "total")
SHAPE = ("round_tick", "tail_ticks", "entities", "rooms", "zones", "characters", "peak_rss_bytes",
         "phases_seconds", "head_tick")


def load(path):
    """The JSON at path, or raise ValueError saying what is wrong with it."""
    try:
        data = json.loads(Path(path).read_text())
    except OSError as e:
        raise ValueError("%s: %s" % (path, e.strerror or e))
    except json.JSONDecodeError as e:
        raise ValueError("%s: not JSON: %s" % (path, e))
    if not isinstance(data, dict):
        raise ValueError("%s: want a JSON object" % path)
    missing = [k for k in SHAPE if k not in data]
    if missing:
        raise ValueError("%s: missing %s" % (path, ", ".join(missing)))
    phases = data["phases_seconds"]
    if not isinstance(phases, dict) or any(p not in phases for p in ("replay", "total")):
        raise ValueError("%s: phases_seconds needs at least replay and total" % path)
    return data


def seconds(v):
    return "%.2f" % v


def change(now, before):
    """'+1.2 s (+3%)' of now against before; '-' when before is 0."""
    d = now - before
    pct = "%+.0f%%" % (100 * d / before) if before else "-"
    return "%+.2f s (%s)" % (d, pct)


def render(cur, prev=None):
    out = ["### Recovery timing (AW-SRV-007 AC-7)", ""]
    out.append("The sizing fixture: %d Entities, %d Rooms, %d Zones, %d Characters; a round at tick %d and a "
               "tail of %d ticks; peak RSS %.0f MB." % (cur["entities"], cur["rooms"], cur["zones"],
                                                       cur["characters"], cur["round_tick"],
                                                       cur["tail_ticks"], cur["peak_rss_bytes"] / 1e6))
    out.append("")
    if prev is None:
        out += ["| phase | seconds |", "|---|---|"]
        for p in PHASES:
            if p in cur["phases_seconds"]:
                out.append("| %s | %s |" % ("**%s**" % p if p == "replay" else p, seconds(cur["phases_seconds"][p])))
        out += ["", "No previous run to compare with."]
        return "\n".join(out) + "\n"
    out += ["| phase | now (s) | previous (s) | change |", "|---|---|---|---|"]
    for p in PHASES:
        if p not in cur["phases_seconds"]:
            continue
        now, before = cur["phases_seconds"][p], prev["phases_seconds"].get(p)
        name = "**%s**" % p if p == "replay" else p
        if before is None:
            out.append("| %s | %s | - | - |" % (name, seconds(now)))
        else:
            out.append("| %s | %s | %s | %s |" % (name, seconds(now), seconds(before), change(now, before)))
    note = ""
    # The test stops at 600 ticks or a few more, so 601 against 603 is the same tail; 5% is not.
    if abs(cur["tail_ticks"] - prev["tail_ticks"]) > 0.05 * prev["tail_ticks"]:
        note = " The tails differ (%d now, %d before), so the replay times aren't like for like." % (
            cur["tail_ticks"], prev["tail_ticks"])
    shape = [k for k in ("entities", "rooms", "zones", "characters") if prev[k] != cur[k]]
    if shape:
        note += " The fixture differs (%s), so the replay times aren't like for like." % ", ".join(
            "%s %d now, %d before" % (k, cur[k], prev[k]) for k in shape)
    out += ["", "Compared with the newest successful run on main (tail %d ticks).%s" % (prev["tail_ticks"], note)]
    return "\n".join(out) + "\n"


def summary(args):
    try:
        cur = load(args.current)
    except ValueError as e:
        print("recovery_timing: %s" % e, file=sys.stderr)
        return 1
    prev = None
    if args.previous and Path(args.previous).exists():
        try:
            prev = load(args.previous)
        except ValueError as e:
            print("recovery_timing: ignoring the previous run: %s" % e, file=sys.stderr)
    text = render(cur, prev)
    target = os.environ.get("GITHUB_STEP_SUMMARY")
    if target:
        with open(target, "a") as f:
            f.write(text)
    else:
        sys.stdout.write(text)
    return 0


def startup_budget(values_files):
    """periodSeconds × failureThreshold of probes.startup, later files overriding earlier ones."""
    import yaml
    startup = {}
    for f in values_files:
        doc = yaml.safe_load(Path(f).read_text()) or {}
        startup.update(((doc.get("probes") or {}).get("startup")) or {})
    try:
        return startup["periodSeconds"] * startup["failureThreshold"]
    except KeyError as e:
        raise ValueError("%s: probes.startup has no %s" % (", ".join(values_files), e.args[0]))


def budget(args):
    try:
        cur = load(args.current)
        have = startup_budget(args.values)
    except (ValueError, OSError) as e:
        print("recovery_timing: %s" % e, file=sys.stderr)
        return 1
    total = cur["phases_seconds"]["total"]
    need = args.factor * total
    print("startup budget %d s; measured recovery %.1f s; %g x = %.1f s; headroom %.1f x"
          % (have, total, args.factor, need, have / total if total else float("inf")))
    if have >= need:
        return 0
    print("recovery_timing: the startup budget is under %g x the measured recovery (%.1f s needed): "
          "raise probes.startup.failureThreshold, or say by how much the values change"
          % (args.factor, need), file=sys.stderr)
    return 1


def gh(*argv):
    return subprocess.run(["gh", *argv], capture_output=True, text=True)


def previous(args, run=gh):
    r = run("run", "list", "--workflow", args.workflow, "--branch", args.branch, "--status", "success",
            "--limit", "1", "--json", "databaseId", "--jq", ".[0].databaseId // empty")
    if r.returncode != 0:
        # A comparison is a nicety: an API blip must not fail the job before the bound is tested.
        print("no previous run (gh run list failed: %s)" % r.stderr.strip())
        return 0
    run_id = r.stdout.strip()
    if not run_id:
        print("no previous run")
        return 0
    d = run("run", "download", run_id, "--name", "recovery-timing", "--dir", args.out_dir)
    if d.returncode != 0:
        # A run whose artifact has expired, or one from before the artifact existed.
        print("no previous run (run %s has no recovery-timing artifact: %s)" % (run_id, d.stderr.strip()))
        return 0
    print("previous run %s" % run_id)
    return 0


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("summary")
    s.add_argument("current")
    s.add_argument("previous", nargs="?")
    s.set_defaults(fn=summary)
    b = sub.add_parser("budget")
    b.add_argument("current")
    b.add_argument("values", nargs="+")
    b.add_argument("--factor", type=float, default=3)
    b.set_defaults(fn=budget)
    p = sub.add_parser("previous")
    p.add_argument("out_dir")
    p.add_argument("--workflow", default="recovery-timing.yaml")
    p.add_argument("--branch", default="main")
    p.set_defaults(fn=previous)
    args = ap.parse_args(argv)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
