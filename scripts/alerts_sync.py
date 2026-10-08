#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make alerts-sync` and `make alerts-diff` — deliver files/alerts.yaml to the Grafana Cloud ruler (AW-INF-009).

    scripts/alerts_sync.py sync
    scripts/alerts_sync.py diff

Reads MIMIR_ADDRESS, MIMIR_TENANT_ID and MIMIR_API_KEY from the environment and nothing else;
exit 3 names whichever are unset, before mimirtool runs. `mimirtool` (pinned by `make bootstrap`)
takes the ruler namespace from the rule file's *name*, not from `--namespaces`, so the file is
staged as `andara.yaml` in a temporary directory: the namespace is `andara` in every environment
(rules are grouped `by (namespace)`, AW-INF-008), and `--namespaces andara` scopes what a sync
may delete to that namespace and nothing else in the tenant. Run directly on alerts.yaml it
syncs namespace `alerts`, and `--namespaces andara` then matches nothing and reports "0 Groups".

sync   `mimirtool rules sync`; prints the groups created, updated and deleted. A second run
       against an unchanged file reports 0, 0, 0. Exit 0 ok; 1 the API failed.
diff   `mimirtool rules diff`, which exits 0 whatever it finds, so this reads its summary:
       exit 1 when any group would be created, updated or deleted; 0 when none would.

Exit codes: 0 ok; 1 an API error, or drift (diff); 2 usage; 3 secrets unset.
"""

import os
import re
import shutil
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
NAMESPACE = "andara"
SECRETS = ("MIMIR_ADDRESS", "MIMIR_TENANT_ID", "MIMIR_API_KEY")
SUMMARY = re.compile(r"(\d+) Groups Created, (\d+) Groups Updated, (\d+) Groups Deleted")


def say(msg):
    print("alerts: " + msg, flush=True)


def fail(code, msg):
    print("alerts: " + msg, file=sys.stderr)
    return code


def mimirtool():
    local = os.path.join(REPO, "bin", "mimirtool")
    return local if os.access(local, os.X_OK) else shutil.which("mimirtool")


def summary_counts(output):
    """(created, updated, deleted) from mimirtool's last summary line, or None."""
    if "no changes detected" in output:  # mimirtool diff prints no summary when nothing differs
        return (0, 0, 0)
    found = SUMMARY.findall(output)
    return tuple(int(n) for n in found[-1]) if found else None


def run(cmd, rules_file=ALERTS, env=None):
    env = os.environ if env is None else env
    missing = [k for k in SECRETS if not env.get(k)]
    if missing:
        return fail(3, "%s unset; they are repository secrets (docs/runbooks/alert-routing.md)"
                    % ", ".join(missing))
    tool = mimirtool()
    if not tool:
        return fail(1, "mimirtool not found; run `make bootstrap`")
    with tempfile.TemporaryDirectory() as tmp:
        staged = os.path.join(tmp, NAMESPACE + ".yaml")
        shutil.copyfile(rules_file, staged)
        p = subprocess.run([tool, "rules", cmd, "--namespaces", NAMESPACE, staged],
                           capture_output=True, text=True, env=env)
    out = p.stdout + p.stderr
    if p.returncode != 0:
        print(out, file=sys.stderr, end="")
        return fail(1, "mimirtool rules %s failed (exit %d)" % (cmd, p.returncode))
    counts = summary_counts(out)
    if counts is None:
        print(out, file=sys.stderr, end="")
        return fail(1, "mimirtool rules %s printed no summary; nothing can be said about the ruler" % cmd)
    print(out, end="")
    verb = "wrote" if cmd == "sync" else "would change"
    say("%s %d created, %d updated, %d deleted in namespace %s" % ((verb,) + counts + (NAMESPACE,)))
    if cmd == "diff" and any(counts):
        return fail(1, "the ruler differs from files/alerts.yaml; `make alerts-sync` on main delivers it")
    return 0


def main(argv):
    if len(argv) != 1 or argv[0] not in ("sync", "diff"):
        return fail(2, "usage: make alerts-sync | alerts-diff")
    return run(argv[0])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
