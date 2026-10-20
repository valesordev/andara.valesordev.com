#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make tf-credentials-check — which hand-made Grafana Cloud credentials expire soon (AW-INF-076).

    scripts/tf_credentials.py [--file PATH] [--today YYYY-MM-DD] [--issue]

ADR-0012 decision 4: every token Brian makes by hand carries an expiry of at most 90 days, recorded in
deploy/terraform/grafana/credentials.yaml as names and dates, never values:

    credentials:
      - name: GRAFANA_SOLO7DEV_READ_TOKEN
        expires: 2027-01-15
        warn_days: 30            # optional; WARN_DAYS when absent

One line per credential (its name, expiry date and days remaining, or `expired`), then the count within
its window. An entry is within its window from warn_days before its expiry; on its expiry date and after,
it is expired. Reads the file only: no network, no credential, no environment approval.

--issue (the credentials-expiry job, .github/workflows/credentials.yaml) also opens the one open issue
labelled credentials-expiry, or rewrites its body, while any entry is within its window, and does the same
when the file can't be read, so a broken check never looks like nothing expiring. A clean check closes it.
--today replaces the date, for the tests and for a look ahead.

Exit: 0 nothing within its window · 1 one or more within it or expired, or an unreadable file (naming the
line) · 2 usage.
"""
import argparse
import datetime
import json
import os
import re
import subprocess
import sys

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILE = os.path.join(REPO, "deploy", "terraform", "grafana", "credentials.yaml")
WARN_DAYS = 14
FIELDS = ("name", "expires", "warn_days")
NAME = re.compile(r"^[A-Z][A-Z0-9_]*$")
DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
# A value pasted by mistake: a stack service-account token, a Cloud access-policy token, a Slack webhook.
SECRET = re.compile(r"glsa_|glc_|https://hooks\.slack\.com/")
LABEL = "credentials-expiry"


class Exit(Exception):
    def __init__(self, code, msg):
        super().__init__(msg)
        self.code = code


def say(msg):
    print("tf-credentials: " + msg)


def _fail(node, msg):
    raise Exit(1, "line %d: %s" % (node.start_mark.line + 1, msg))


def parse(text):
    """[(name, expires date, warn_days)] in file order; Exit 1 naming the line of the first problem."""
    for i, line in enumerate(text.splitlines(), 1):
        if SECRET.search(line):
            raise Exit(1, "line %d: holds a credential value; the file names credentials, never their values" % i)
    try:
        root = yaml.compose(text, Loader=yaml.SafeLoader)
    except yaml.YAMLError as e:
        mark = getattr(e, "problem_mark", None)
        raise Exit(1, "line %s: not YAML: %s" % (mark.line + 1 if mark else "?", getattr(e, "problem", e)))
    if not isinstance(root, yaml.MappingNode) or [k.value for k, _ in root.value] != ["credentials"]:
        raise Exit(1, "line %d: the file is one key, credentials, holding a list"
                   % ((root.start_mark.line + 1) if root is not None else 1))
    seq = root.value[0][1]
    if not isinstance(seq, yaml.SequenceNode):
        _fail(seq, "credentials is not a list")
    out, seen = [], set()
    for item in seq.value:
        if not isinstance(item, yaml.MappingNode):
            _fail(item, "an entry is a mapping of name, expires and an optional warn_days")
        fields = {}
        for k, v in item.value:
            if k.value not in FIELDS:
                _fail(k, "unknown field %r; an entry has only name, expires and warn_days" % k.value)
            if k.value in fields:
                _fail(k, "field %s given twice" % k.value)
            if not isinstance(v, yaml.ScalarNode):
                _fail(v, "%s is not a single value" % k.value)
            fields[k.value] = v
        for need in ("name", "expires"):
            if need not in fields:
                _fail(item, "entry has no %s" % need)
        name = fields["name"].value
        if not NAME.match(name):
            _fail(fields["name"], "name %r is not a credential's variable name" % name)
        if name in seen:
            _fail(fields["name"], "%s is listed twice" % name)
        seen.add(name)
        raw = fields["expires"].value
        try:
            if not DATE.match(raw):
                raise ValueError
            expires = datetime.date.fromisoformat(raw)
        except ValueError:
            _fail(fields["expires"], "%s: expires %r is not an ISO date (YYYY-MM-DD)" % (name, raw))
        warn = WARN_DAYS
        if "warn_days" in fields:
            w = fields["warn_days"]
            # An unquoted whole number: "14" in quotes is a string, and 1.5 or -3 are not a count of days.
            if w.style is not None or not re.match(r"^\d+$", w.value):
                _fail(w, "%s: warn_days %r is not a whole number of days" % (name, w.value))
            warn = int(w.value)
        out.append((name, expires, warn))
    return out


def load(path):
    try:
        with open(path, encoding="utf-8") as f:
            return parse(f.read())
    except OSError as e:
        raise Exit(1, "%s: %s" % (os.path.relpath(path, REPO), e.strerror))
    except UnicodeDecodeError:
        raise Exit(1, "%s: not UTF-8" % os.path.relpath(path, REPO))


def due(entries, today):
    """[(name, expires, days left or None when expired)] for every entry within its window or expired."""
    out = []
    for name, expires, warn in entries:
        left = (expires - today).days
        if left <= 0:
            out.append((name, expires, None))
        elif left <= warn:
            out.append((name, expires, left))
    return out


def report(entries, today):
    """Print one line per credential and the count; the entries within their window."""
    found = due(entries, today)
    hot = {n for n, _, _ in found}
    for name, expires, warn in entries:
        left = (expires - today).days
        state = "expired" if left <= 0 else "%d days left" % left
        mark = "  WITHIN %d-day window" % warn if name in hot and left > 0 else ""
        say("%-40s expires %s  %s%s" % (name, expires.isoformat(), state, mark))
    say("%d of %d within its window or expired" % (len(found), len(entries)))
    return found


# --- the issue (--issue) -----------------------------------------------------------------------------

def gh(*args, input_text=None):
    p = subprocess.run(["gh", *args], input=input_text, capture_output=True, text=True)
    if p.returncode:
        raise Exit(1, "gh %s failed: %s" % (" ".join(args[:2]), p.stderr.strip()))
    return p.stdout


def run_url(env):
    if env.get("GITHUB_RUN_ID"):
        return "%s/%s/actions/runs/%s" % (env.get("GITHUB_SERVER_URL", "https://github.com"),
                                          env.get("GITHUB_REPOSITORY", ""), env["GITHUB_RUN_ID"])
    return "(not a GitHub Actions run)"


def output(env, key, value):
    path = env.get("GITHUB_OUTPUT")
    if path:
        with open(path, "a") as f:
            f.write("%s=%s\n" % (key, value))


def open_issue():
    found = json.loads(gh("issue", "list", "--label", LABEL, "--state", "open", "--json", "number",
                          "--limit", "1") or "[]")
    return found[0]["number"] if found else None


def raise_issue(title, body, env):
    """Open the one credentials-expiry issue, or rewrite the open one's body to body."""
    gh("label", "create", LABEL, "--color", "D93F0B", "--force", "--description",
       "A hand-made Grafana Cloud credential near or past its expiry, or a failed check (AW-INF-076)")
    number = open_issue()
    if number:
        gh("issue", "edit", str(number), "--title", title, "--body-file", "-", input_text=body)
        say("updated issue #%s (%s)" % (number, LABEL))
    else:
        gh("issue", "create", "--title", title, "--label", LABEL, "--body-file", "-", input_text=body)
        say("opened an issue labelled %s" % LABEL)
    output(env, "reported", "true")


def due_body(found, today, env):
    lines = ["These hand-made Grafana Cloud credentials are within their warning window or expired, as of %s:"
             % today.isoformat(), "", "| Credential | Expires | Left |", "|---|---|---|"]
    for name, expires, left in found:
        lines.append("| `%s` | %s | %s |" % (name, expires.isoformat(), "expired" if left is None else "%d days" % left))
    lines += ["", "Rotate each one as `docs/runbooks/grafana-credentials.md` says, then change its date in "
              "`deploy/terraform/grafana/credentials.yaml`. This issue closes on the first check with nothing "
              "in its window.", "", "Run: %s" % run_url(env)]
    return "\n".join(lines) + "\n"


def close_issue(env):
    """A clean check closes the open issue. The check itself succeeded, so a gh failure is only reported."""
    try:
        number = open_issue()
        if number:
            gh("issue", "close", str(number), "--comment",
               "Nothing within its window as of %s; closing." % run_url(env))
            say("closed issue #%s (%s)" % (number, LABEL))
    except Exit as e:
        say("nothing within its window, but closing the issue failed: %s" % e)


# --- main --------------------------------------------------------------------------------------------

def main(argv, env=None):
    env = dict(os.environ if env is None else env)
    p = argparse.ArgumentParser(prog="tf_credentials.py")
    p.add_argument("--file", default=FILE)
    p.add_argument("--today", default="")
    p.add_argument("--issue", action="store_true")
    try:
        a = p.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    try:
        today = datetime.date.fromisoformat(a.today) if a.today else datetime.datetime.now(datetime.timezone.utc).date()
    except ValueError:
        print("tf-credentials: --today %r is not an ISO date (YYYY-MM-DD)" % a.today, file=sys.stderr)
        return 2
    try:
        try:
            entries = load(a.file)
        except Exit as e:
            # Printed first: if the issue can't be written, the log still names the line.
            print("tf-credentials: %s" % e, file=sys.stderr)
            if a.issue:
                raise_issue("Credentials expiry check failed",
                            "The credentials-expiry check could not read `%s`: %s\n\nUntil it can, no expiry is "
                            "being watched. Run: %s\n" % (os.path.relpath(a.file, REPO), e, run_url(env)), env)
            return e.code
        found = report(entries, today)
        if a.issue:
            if found:
                # A count, not the names: tokens made together expire together, and a title is 256 characters.
                raise_issue("%d Grafana Cloud credential%s expiring or expired" % (len(found), "" if len(found) == 1 else "s"),
                            due_body(found, today, env), env)
            else:
                close_issue(env)
                output(env, "reported", "true")
        return 1 if found else 0
    except Exit as e:
        print("tf-credentials: %s" % e, file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
