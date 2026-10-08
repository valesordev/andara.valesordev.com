#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make alerts-sync` and `make alerts-diff` — deliver files/alerts.yaml to the Grafana Cloud ruler (AW-INF-009).

    scripts/alerts_sync.py sync
    scripts/alerts_sync.py diff

Reads MIMIR_API_KEY (read), MIMIR_API_KEY_WRITE (sync only, and only when there is drift),
MIMIR_ADDRESS and MIMIR_TENANT_ID from the environment; the last two default
to what `make observe-check` and CI already have, GRAFANA_CLOUD_PROM_URL (without its /api/prom)
and GRAFANA_CLOUD_PROM_USER. ALERTS_FILE names a rule file other than the chart's for `diff` only (`sync` ignores it), which CI sets
to a pull request's copy so that the base branch's code reads it as data (the workflow says why).
Exit 3 names whichever of the three are unset, before mimirtool runs. `mimirtool` (pinned by `make bootstrap`)
takes the ruler namespace from the rule file's *name*, not from `--namespaces`, so the file is
staged as `andara.yaml` in a temporary directory: the namespace is `andara` in every environment
(rules are grouped `by (namespace)`, AW-INF-008), and `--namespaces andara` scopes what a sync
may delete to that namespace and nothing else in the tenant. Run directly on alerts.yaml it
syncs namespace `alerts`, and `--namespaces andara` then matches nothing and reports "0 Groups".

sync   plans with the read key (`mimirtool rules diff`) and writes with the write key. Grafana
       Cloud advises one scope per token, and `mimirtool rules sync`/`load` list before they
       write, so a write-only token fails on its first call. Instead: drift is found with
       MIMIR_API_KEY (rules:read); only if there is drift, every group in the file is POSTed and
       every group the ruler has and the file lacks is DELETEd with MIMIR_API_KEY_WRITE
       (rules:write), straight to the ruler API; then the diff runs again and must be clean.
       No drift means the write key is never read, so a second run reports 0, 0, 0 and writes
       nothing. Exit 0 ok; 1 the API failed.
diff   as above, read key only: exit 1 when any group would be created, updated or deleted; 0 when
       none would. (`mimirtool rules diff` itself exits 0 whatever it finds.)

Exit codes: 0 ok; 1 an API error, or drift (diff); 2 usage; 3 a key, address or tenant unset. `make` reports any
of them as its own exit 2; the script's code is the first line of the failure it prints.
"""

import base64
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
NAMESPACE = "andara"
SECRETS = ("MIMIR_ADDRESS", "MIMIR_TENANT_ID", "MIMIR_API_KEY")
PROM_SUFFIX = "/api/prom"
WRITE_KEY = "MIMIR_API_KEY_WRITE"
DELETED = re.compile(r"^\s*- Group: (.+?)\s*$", re.M)
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


def resolve_env(env):
    """env with MIMIR_ADDRESS and MIMIR_TENANT_ID filled from the GRAFANA_CLOUD_PROM_* pair."""
    env = dict(env)
    url = env.get("GRAFANA_CLOUD_PROM_URL", "").rstrip("/")
    if not env.get("MIMIR_ADDRESS") and url:
        env["MIMIR_ADDRESS"] = url[:-len(PROM_SUFFIX)] if url.endswith(PROM_SUFFIX) else url
    if not env.get("MIMIR_TENANT_ID") and env.get("GRAFANA_CLOUD_PROM_USER"):
        env["MIMIR_TENANT_ID"] = env["GRAFANA_CLOUD_PROM_USER"]
    return env


def missing(env, names):
    return [k for k in names if not env.get(k)]


def api(env, method, group=None, body=None, key=None, timeout=60):
    """One call to the ruler's namespace endpoint, authenticated as the tenant with `key`."""
    url = "%s/prometheus/config/v1/rules/%s%s" % (env["MIMIR_ADDRESS"].rstrip("/"), NAMESPACE,
                                                    "/" + urllib.parse.quote(group, safe="") if group else "")
    req = urllib.request.Request(url, data=body, method=method)
    token = base64.b64encode(("%s:%s" % (env["MIMIR_TENANT_ID"], key)).encode()).decode()
    req.add_header("Authorization", "Basic " + token)
    req.add_header("X-Scope-OrgID", env["MIMIR_TENANT_ID"])
    if body is not None:
        req.add_header("Content-Type", "application/yaml")
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.status


def groups_of(path):
    import yaml
    with open(path) as f:
        return (yaml.safe_load(f) or {}).get("groups") or []


def mimirtool_rules(cmd, tool, rules_file, env, verbose=False):
    """Run `mimirtool rules <cmd>` over the rule file staged as andara.yaml. Returns (rc, output)."""
    with tempfile.TemporaryDirectory() as tmp:
        staged = os.path.join(tmp, NAMESPACE + ".yaml")
        shutil.copyfile(rules_file, staged)
        args = [tool, "rules", cmd, "--namespaces", NAMESPACE] + (["--verbose"] if verbose else []) + [staged]
        p = subprocess.run(args, capture_output=True, text=True, env=env)
    return p.returncode, p.stdout + p.stderr


def plan(tool, rules_file, env):
    """(counts, deleted group names, output) from a read-only diff, or an exit code."""
    rc, out = mimirtool_rules("diff", tool, rules_file, env)
    if rc != 0:
        print(out, file=sys.stderr, end="")
        return fail(1, "mimirtool rules diff failed (exit %d)" % rc)
    counts = summary_counts(out)
    if counts is None:
        print(out, file=sys.stderr, end="")
        return fail(1, "mimirtool rules diff printed no summary; nothing can be said about the ruler")
    return counts, DELETED.findall(out), out


def apply(rules_file, deleted, env):
    """POST every group in the file and DELETE the ones the file lacks, with the write key."""
    key = env[WRITE_KEY]
    import yaml
    try:
        for g in groups_of(rules_file):
            api(env, "POST", body=yaml.safe_dump(g).encode(), key=key)
        for name in deleted:
            api(env, "DELETE", group=name, key=key)
    except urllib.error.HTTPError as e:
        return fail(1, "the ruler refused the write: HTTP %d %s (the write key needs rules:write)" % (e.code, e.reason))
    except (urllib.error.URLError, OSError) as e:
        return fail(1, "could not reach the ruler to write: %s" % e)
    return 0


def run(cmd, rules_file=None, env=None, find_tool=mimirtool):
    env = resolve_env(os.environ if env is None else env)
    # Only a diff reads another file; a sync always writes the chart's, whatever the environment says.
    rules_file = rules_file or (env.get("ALERTS_FILE") if cmd == "diff" else None) or ALERTS
    absent = missing(env, SECRETS)
    if absent:
        return fail(3, "%s unset (docs/runbooks/alert-routing.md)" % ", ".join(absent))
    tool = find_tool()
    if not tool:
        return fail(1, "mimirtool not found; run `make bootstrap`")
    planned = plan(tool, rules_file, env)
    if isinstance(planned, int):
        return planned
    counts, deleted, out = planned
    print(out, end="")
    if cmd == "diff":
        say("would change %d created, %d updated, %d deleted in namespace %s" % (counts + (NAMESPACE,)))
        if any(counts):
            return fail(1, "the ruler differs from files/alerts.yaml; `make alerts-sync` on main delivers it")
        return 0
    if any(counts):
        if missing(env, [WRITE_KEY]):
            return fail(3, "%s unset: the ruler differs from files/alerts.yaml and there is no key to write it "
                        "(docs/runbooks/alert-routing.md)" % WRITE_KEY)
        rc = apply(rules_file, deleted, env)
        if rc:
            return rc
        again = plan(tool, rules_file, env)
        if isinstance(again, int):
            return again
        if any(again[0]):
            return fail(1, "the ruler still differs after the write (%d created, %d updated, %d deleted)" % again[0])
    say("wrote %d created, %d updated, %d deleted in namespace %s" % (counts + (NAMESPACE,)))
    return 0


def main(argv):
    if len(argv) != 1 or argv[0] not in ("sync", "diff"):
        return fail(2, "usage: make alerts-sync | alerts-diff")
    return run(argv[0])


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
