# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The runbooks AW-INF-076 writes, checked against the repo: every credential name the workflows, the
Makefile and the scripts use has an entry in grafana-credentials.md (AC-1, AC-10); alert-routing.md's
routing table names the committed trees' receivers, with no step that makes routing in the UI (AC-6, AC-8);
the failed-run entry names who looks, what first, and the dev-box fallback (AC-7)."""
import datetime
import os
import re
import subprocess
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
RUNBOOKS = os.path.join(REPO, "docs", "runbooks")
GRAFANA = os.path.join(REPO, "deploy", "terraform", "grafana")

# AC-1's patterns: a credential's (or a credential-adjacent variable's) name.
CREDENTIAL = re.compile(r"\b(GRAFANA_[A-Z0-9_]+_TOKEN|SLACK_WEBHOOK_[A-Z]+|TF_PLAN_KEY_[A-Z]+|TFSTATE_[A-Z0-9_]+_SA"
                        r"|GCP_(?:WIF_PROVIDER|DEVBOX_IDENTITY))\b")
# ADR-0012 decision 4's table, which needs an entry whether or not anything reads the name yet.
ADR_NAMES = {
    "GRAFANA_SOLO7DEV_READ_TOKEN", "GRAFANA_SOLO7_READ_TOKEN",
    "GRAFANA_SOLO7DEV_APPLY_TOKEN", "GRAFANA_SOLO7_APPLY_TOKEN",
    "GRAFANA_SOLO7DEV_TELEMETRY_TOKEN", "GRAFANA_SOLO7_TELEMETRY_TOKEN",
    "GRAFANA_SOLO7DEV_FLEET_READ_TOKEN", "GRAFANA_SOLO7_FLEET_READ_TOKEN",
    "GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN", "GRAFANA_SOLO7_FLEET_APPLY_TOKEN",
    "GRAFANA_SOLO7_IRM_TOKEN", "SLACK_WEBHOOK_DEV", "SLACK_WEBHOOK_PROD", "TF_PLAN_KEY_PROD",
    "TFSTATE_SOLO7DEV_PLAN_SA", "TFSTATE_SOLO7_PLAN_SA", "TFSTATE_SOLO7DEV_APPLY_SA", "TFSTATE_SOLO7_APPLY_SA",
    "TFSTATE_SOLO7_IRM_APPLY_SA", "TFSTATE_PRODPLAN_SA", "GCP_WIF_PROVIDER", "GCP_DEVBOX_IDENTITY",
}
FIELDS = ("**Click path:**", "**Name:**", "**Role or scopes:**", "**Expiry:**", "**Stored:**", "**Read by:**")


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def sections(text, level):
    """{heading: body} for every heading of exactly `level` hashes."""
    out, head, body = {}, None, []
    for line in text.splitlines():
        m = re.match(r"^(#+) (.*)$", line)
        if m and len(m.group(1)) <= level:
            if head is not None:
                out[head] = "\n".join(body)
            head, body = (m.group(2), []) if len(m.group(1)) == level else (None, [])
        elif head is not None:
            body.append(line)
    if head is not None:
        out[head] = "\n".join(body)
    return out


def used_names():
    """{name: [file, ...]} for every AC-1 name in the tracked workflows, Makefile and scripts (not their tests)."""
    files = subprocess.run(["git", "ls-files", ".github/workflows", "Makefile", "scripts"], cwd=REPO,
                           capture_output=True, text=True, check=True).stdout.split()
    out = {}
    for f in files:
        if f.startswith("scripts/tests/"):
            continue
        try:
            text = read(os.path.join(REPO, f))
        except (UnicodeDecodeError, OSError):
            continue
        for name in set(CREDENTIAL.findall(text)):
            out.setdefault(name, []).append(f)
    return out


def uses(name):
    """The tracked workflows, Makefile and scripts (not their tests) that name `name`."""
    files = subprocess.run(["git", "grep", "-l", "-w", name, "--", ".github/workflows", "Makefile", "scripts",
                            ":!scripts/tests"], cwd=REPO, capture_output=True, text=True).stdout.split()
    return files


class Credentials(unittest.TestCase):
    """AC-1 and AC-10: grafana-credentials.md."""

    @classmethod
    def setUpClass(cls):
        cls.text = read(os.path.join(RUNBOOKS, "grafana-credentials.md"))
        top = sections(cls.text, 2)
        cls.entries = {}
        for head, body in sections(top["The credentials"], 3).items():
            for name in re.findall(r"`([A-Z][A-Z0-9_]+)`", head):
                cls.entries[name] = body
        cls.retiring = re.findall(r"^- `([A-Z][A-Z0-9_]+)`:", top["Retiring"], re.M)

    def test_every_used_name_has_an_entry_or_is_retiring(self):
        missing = {n: fs for n, fs in used_names().items() if n not in self.entries and n not in self.retiring}
        self.assertEqual(missing, {}, "names with no entry in grafana-credentials.md (name: files)")

    def test_every_adr_credential_has_an_entry(self):
        self.assertEqual(sorted(ADR_NAMES - set(self.entries)), [])

    def test_each_entry_has_every_field(self):
        for name, body in self.entries.items():
            for field in FIELDS:
                with self.subTest(name=name, field=field):
                    self.assertIn(field, body)

    def test_retiring_is_exactly_the_ruler_era_names_and_each_is_still_used(self):
        self.assertIn("GRAFANA_CLOUD_READ_TOKEN", self.retiring)
        for name in self.retiring:
            with self.subTest(name=name):
                self.assertNotIn(name, self.entries, "%s is both retiring and an entry" % name)
                self.assertTrue(uses(name), "%s is on the Retiring list and nothing uses it any more: delete its "
                                            "line" % name)
                line = re.search(r"^- `%s`:.*$" % name, self.text, re.M).group(0)
                self.assertRegex(line, r"AW-INF-\d{3}", "%s names no removing story" % name)

    def test_service_accounts_state_the_observed_role_and_date(self):
        """AC-10."""
        for name in ("GRAFANA_SOLO7DEV_READ_TOKEN", "GRAFANA_SOLO7DEV_APPLY_TOKEN"):
            with self.subTest(name=name):
                self.assertRegex(self.entries[name], r"\*\*Observed role:\*\* (Viewer|Admin)[^\n]*read \d{4}-\d{2}-\d{2}")
        self.assertIn("issues/546#issuecomment-", self.entries["GRAFANA_SOLO7DEV_READ_TOKEN"],
                      "andara-read's entry links AW-INF-075's record of its reach")

    def test_credentials_yaml_names_only_documented_credentials_within_90_days(self):
        """A typo'd name would never be rotated by anyone, and a far-off date silences the reminder."""
        import tf_credentials
        for name, expires, _ in tf_credentials.load(tf_credentials.FILE):
            with self.subTest(name=name):
                self.assertTrue(name in self.entries or name in self.retiring,
                                "%s is in credentials.yaml but has no entry in grafana-credentials.md" % name)
                self.assertLessEqual((expires - datetime.date.today()).days, 90,
                                     "%s expires %s, more than 90 days away (ADR-0012 decision 4)" % (name, expires))

    def test_credentials_yaml_holds_exactly_what_the_runbook_says_is_recorded(self):
        """Both ways: a dropped yaml entry, or a runbook still saying "not made yet" after one is made, fails.
        The runbook gives no dates itself, so a rotation changes only credentials.yaml. Whether the runbook
        lists every live token is §8's check, against the stacks."""
        import tf_credentials
        recorded = set()
        for name_set, body in self._by_entry().items():
            block = re.search(r"^- \*\*Expiry:\*\*.*?(?=^- \*\*)", body, re.M | re.S)
            self.assertIsNotNone(block, name_set)
            for stack, state in re.findall(r"^  - `(solo7dev|solo7)`: (.*)$", block.group(0), re.M):
                with self.subTest(entry=name_set, stack=stack):
                    self.assertRegex(state, r"^(recorded in `credentials\.yaml`|not made yet\.)")
                    infix = "_SOLO7DEV_" if stack == "solo7dev" else "_SOLO7_"
                    names = [n for n in name_set if infix in n]
                    self.assertEqual(len(names), 1, "one %s name in the heading" % stack)
                    if state.startswith("recorded"):
                        recorded.add(names[0])
        self.assertEqual(recorded, {n for n, _, _ in tf_credentials.load(tf_credentials.FILE)})

    def _by_entry(self):
        top = sections(self.text, 2)
        return {tuple(re.findall(r"`([A-Z][A-Z0-9_]+)`", head)): body
                for head, body in sections(top["The credentials"], 3).items()}

    def test_the_states_click_paths_are_dated(self):
        self.assertRegex(self.text.split("## The credentials")[0], r"as of \*\*\d{4}-\d{2}-\d{2}\*\*")


def tfvars(stack):
    text = read(os.path.join(GRAFANA, "stacks", stack, "terraform.tfvars"))
    envs = re.findall(r'"([a-z]+)"', re.search(r"^environments\s*=\s*\[([^\]]*)\]", text, re.M).group(1))
    pcp = re.search(r"^page_contact_point\s*=\s*(\S+)", text, re.M).group(1)
    return envs, None if pcp == "null" else pcp.strip('"')


def committed_receivers(stack):
    """The receivers stack's committed tree routes to, as modules/stack/main.tf builds it from the tfvars."""
    envs, pcp = tfvars(stack)
    out = {"slack-prod" if stack == "solo7" else "slack-dev"}
    if "local" in envs:
        out.add("blackhole")
    if "prod" in envs and pcp:
        out.add(pcp)
    return out, pcp


class Routing(unittest.TestCase):
    """AC-6 and AC-8: alert-routing.md."""

    @classmethod
    def setUpClass(cls):
        cls.text = read(os.path.join(RUNBOOKS, "alert-routing.md"))

    def test_the_module_routes_as_committed_receivers_assumes(self):
        """The names, and which child routes where: committed_receivers re-models main.tf, so a rewired route
        must fail here rather than pass against the model."""
        main = read(os.path.join(GRAFANA, "modules", "stack", "main.tf"))
        self.assertIn('name = "slack-${local.channel}"', main)
        self.assertIn('channel = var.stack == "solo7" ? "prod" : "dev"', main)
        self.assertRegex(main, r'name\s+=\s+"blackhole"')
        children = re.findall(r"(local\.\w+(?: && [^?]+)?) \? \[\{\s*labels\s*=\s*(\{[^}]*\})\s*contact_point\s*=\s*(\S+)",
                              main)
        self.assertEqual([(c.strip(), " ".join(l.split()), cp) for c, l, cp in children], [
            ("local.local", '{ environment = "local" }', "grafana_contact_point.blackhole[0].name"),
            ("local.prod && var.page_contact_point != null", '{ environment = "prod", severity = "page" }',
             "var.page_contact_point"),
            ("local.prod", '{ environment = "prod", severity = "page" }', "grafana_contact_point.slack.name"),
        ])
        root = re.search(r'resource "grafana_notification_policy" "this" \{\s*contact_point\s*=\s*(\S+)', main)
        self.assertEqual(root.group(1), "grafana_contact_point.slack.name")

    def test_the_table_names_the_committed_receivers(self):
        table = sections(self.text, 2)["The routing table"]
        rows = [[c.strip() for c in l.strip().strip("|").split("|")] for l in table.splitlines()
                if re.match(r"^\| `solo7", l)]
        self.assertTrue(rows)
        for stack in ("solo7dev", "solo7"):
            want, pcp = committed_receivers(stack)
            got = set()
            for row in rows:
                if row[0] != "`%s`" % stack:
                    continue
                if pcp is None and "From AW-INF-078" in row[4]:
                    continue
                got |= set(re.findall(r"`([a-z0-9-]+)`", row[3]))
            with self.subTest(stack=stack):
                self.assertEqual(got, want)

    def test_no_step_makes_routing_in_the_ui(self):
        bad = re.compile(r"(?i)\b(create|add|click|new)\b[^.\n]*\b(contact points?|notification polic(y|ies))\b"
                         r"|Notification policies")
        for line in self.text.splitlines():
            with self.subTest(line=line[:60]):
                self.assertIsNone(bad.search(line))

    def test_the_ruler_section_is_marked_for_removal(self):
        self.assertRegex(self.text, r"(?m)^## .*removed by AW-INF-077")


class FailedRun(unittest.TestCase):
    """AC-7: terraform-run-failed.md."""

    def test_names_who_what_first_and_the_fallback(self):
        text = read(os.path.join(RUNBOOKS, "terraform-run-failed.md"))
        self.assertIn("**Who looks:**", text)
        self.assertIn("## First look", text)
        self.assertIn("make tf-apply STACK=solo7dev", text)
        self.assertIn("(../../.github/workflows/terraform.yaml)", text)
        for job in ("`apply`", "`apply-prod`", "`drift`", "`drift-prod-irm`"):
            self.assertIn("| %s |" % job, text)


if __name__ == "__main__":
    unittest.main()
