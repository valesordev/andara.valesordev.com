# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/tf_ci.py (AW-INF-068): the superseded-plan refusal (AC-5), the plan's sha256 (AC-6), the IRM root's
sinks (AC-12), the drift issue (AC-7), tf-plan-show's exits (AC-8), the OIDC claims (AC-10). Terraform,
gh and the OIDC endpoint are stubbed; openssl is real."""
import base64
import contextlib
import io
import json
import os
import shutil
import sys
import tempfile
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import tf  # noqa: E402
import tf_ci  # noqa: E402

IRM_URL = "https://oncall-prod-us-central-0.grafana.net/oncall/integrations/v1/grafana_alerting/SECRETabc123/"
KEY = "test-plan-key-0123456789"


def irm_plan_json():
    """A plan that replaces irm-prod, the URL in its before and after values."""
    return {"resource_changes": [
        {"address": "module.irm.grafana_contact_point.irm_prod",
         "change": {"actions": ["delete", "create"],
                    "before": {"oncall": [{"url": IRM_URL}]}, "after": {"oncall": [{"url": IRM_URL + "new"}]}}},
        {"address": "module.irm.grafana_oncall_integration.page",
         "change": {"actions": ["no-op"], "before": {"link": IRM_URL}, "after": {"link": IRM_URL}}},
    ]}


class FakeTerraform:
    """tf.run's stand-in: every call recorded; `show -json` returns plan_json. In the solo7-irm root, plan, show
    and apply print the IRM URL, as the real provider would (it does not mark it sensitive)."""

    def __init__(self, plan_json=None, plan_rc=0, write_plan=b"PLANBYTES"):
        self.calls, self.plan_json, self.plan_rc, self.write_plan = [], plan_json or {"resource_changes": []}, plan_rc, write_plan

    def run(self, args, cwd, env, capture=False, pass_fds=()):
        self.calls.append((tuple(args), capture))
        leak = IRM_URL if cwd.endswith("solo7-irm") else "(sensitive value)"
        if args[0] == "plan":
            out = next((a for a in args if a.startswith("-out=")), None)
            if out:
                with open(out[5:], "wb") as f:
                    f.write(self.write_plan)
            text = "  ~ url = %s\n" % leak
            if not capture:
                sys.stdout.write(text)
            return self.plan_rc, text
        if args[:2] == ["show", "-json"]:
            return 0, json.dumps(self.plan_json)
        if args[0] == "show":
            text = "Terraform will perform the following actions:\n  ~ url = %s\n" % leak
            if not capture:
                sys.stdout.write(text)
            return 0, text
        out = "%s ok\n" % args[0]
        if args[0] == "apply":
            out = "module.irm.grafana_contact_point.irm_prod: Creating... [url=%s]\n" % leak
        if not capture:
            sys.stdout.write(out)
        return 0, out


class Base(unittest.TestCase):

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        self.summary = os.path.join(self.dir, "summary.md")
        self.outputs = os.path.join(self.dir, "outputs")
        p = mock.patch.dict(os.environ, {"GITHUB_STEP_SUMMARY": self.summary, "GITHUB_OUTPUT": self.outputs})
        p.start()
        self.addCleanup(p.stop)
        # Never the box's real credentials, and never the real gh: a test that needs gh patches it.
        b = mock.patch.object(tf, "box_env_path", lambda env: None)
        b.start()
        self.addCleanup(b.stop)

        def no_gh(*args, input_text=None):
            raise AssertionError("a test reached the real gh: gh %s" % " ".join(args))

        g = mock.patch.object(tf_ci, "gh", no_gh)
        g.start()
        self.addCleanup(g.stop)
        self.env = {"PATH": os.environ["PATH"], "TF_PLAN_KEY_PROD": KEY, "GITHUB_ACTIONS": "true",
                    "GRAFANA_SOLO7_READ_TOKEN": "read", "GRAFANA_SOLO7_APPLY_TOKEN": "apply",
                    "GRAFANA_SOLO7_IRM_TOKEN": "irm", "SLACK_WEBHOOK_PROD": "https://hooks.slack.invalid/x",
                    "GRAFANA_SOLO7DEV_READ_TOKEN": "read", "SLACK_WEBHOOK_DEV": "https://hooks.slack.invalid/d"}

    def fake(self, **kw):
        t = FakeTerraform(**kw)
        p = mock.patch.object(tf, "run", t.run)
        p.start()
        self.addCleanup(p.stop)
        return t

    def sinks(self, fn):
        """Run fn; return (result or exception, everything written to stdout, stderr and the summary)."""
        out, err = io.StringIO(), io.StringIO()
        result = None
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                result = fn()
            except tf.Exit as e:
                result = e
                err.write(e.msg or "")
        text = out.getvalue() + err.getvalue()
        if os.path.exists(self.summary):
            with open(self.summary) as f:
                text += f.read()
        return result, text

    def plan_prod(self, plan_json=None):
        self.fake(plan_json=plan_json)
        out = os.path.join(self.dir, "prod")
        result, text = self.sinks(lambda: tf_ci.cmd_plan_prod(out, "c0ffee", self.env))
        return out, result, text


class PlanProd(Base):

    def test_encrypted_manifest_and_sha256(self):
        out, result, text = self.plan_prod()
        self.assertEqual(result, 0)
        self.assertEqual(sorted(os.listdir(out)), ["manifest.json", "solo7-irm.tfplan.enc", "solo7.tfplan.enc"])
        with open(os.path.join(out, "solo7.tfplan.enc"), "rb") as f:
            self.assertNotIn(b"PLANBYTES", f.read())
        manifest = tf_ci.read_manifest(out)
        self.assertEqual(manifest["commit"], "c0ffee")
        import hashlib
        self.assertEqual(manifest["plans"]["solo7"], hashlib.sha256(b"PLANBYTES").hexdigest())
        self.assertIn("sha256 of the plan file: `%s`" % manifest["plans"]["solo7"], text)
        self.assertEqual(tf_ci.decrypt(os.path.join(out, "solo7.tfplan.enc"), self.env), b"PLANBYTES")

    def test_irm_first_and_no_changes_means_no_apply(self):
        t = self.fake()
        out = os.path.join(self.dir, "prod")
        self.sinks(lambda: tf_ci.cmd_plan_prod(out, "c0ffee", self.env))
        plans = [c for c in t.calls if c[0][0] == "plan"]
        self.assertEqual(len(plans), 2)
        self.assertTrue(all("-lock=false" in c[0] for c in plans), "prod plans take no lock")
        with open(self.outputs) as f:
            self.assertIn("changes=false", f.read())

    def test_changes_are_flagged_for_apply_prod(self):
        self.plan_prod(plan_json=irm_plan_json())
        with open(self.outputs) as f:
            self.assertIn("changes=true", f.read())

    def test_without_the_key_it_is_exit_3(self):
        self.env.pop("TF_PLAN_KEY_PROD")
        self.assertEqual(tf_ci.main(["plan-prod", "--out", self.dir, "--commit", "x"], self.env), 3)


class Irm(Base):
    """AC-12: a plan of solo7-irm that replaces irm-prod puts no IRM URL in any sink."""

    def test_plan_prod(self):
        _, result, text = self.plan_prod(plan_json=irm_plan_json())
        self.assertEqual(result, 0)
        self.assertNotIn("SECRETabc123", text, "an IRM URL reached the log or the summary")
        self.assertIn("module.irm.grafana_contact_point.irm_prod: delete,create", text)
        self.assertNotIn("module.irm.grafana_oncall_integration.page", text, "no-ops are not listed")

    def test_apply_prod(self):
        out, _, _ = self.plan_prod(plan_json=irm_plan_json())
        os.remove(self.summary)
        t = self.fake(plan_json=irm_plan_json())
        result, text = self.sinks(lambda: tf_ci.cmd_apply_prod(out, "solo7-irm", "c0ffee", self.env))
        self.assertEqual(result, 0)
        self.assertNotIn("SECRETabc123", text)
        self.assertTrue(all(capture for args, capture in t.calls), "every IRM terraform call is captured")

    def test_drift_issue_body(self):
        self.fake(plan_json=irm_plan_json(), plan_rc=2)
        bodies = []
        with mock.patch.object(tf_ci, "drift_issue", lambda root, title, body: bodies.append(body)), \
                mock.patch.object(tf_ci, "gh", lambda *a, **k: "[]"):
            result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7-irm", True, self.env))
        self.assertIsInstance(result, tf.Exit)
        self.assertEqual(result.code, 1)
        self.assertEqual(len(bodies), 1)
        self.assertNotIn("SECRETabc123", bodies[0] + text)
        self.assertIn("module.irm.grafana_contact_point.irm_prod: delete,create", bodies[0])

    def test_a_failed_irm_plan_withholds_its_output(self):
        self.fake(plan_rc=1)
        result, text = self.sinks(lambda: tf_ci.cmd_plan_prod(os.path.join(self.dir, "p"), "c", self.env))
        self.assertIsInstance(result, tf.Exit)
        self.assertIn("output withheld", result.msg)
        self.assertNotIn("SECRETabc123", text)


class ApplyProd(Base):

    def test_a_superseded_plan_is_refused(self):
        out, _, _ = self.plan_prod()
        t = self.fake()
        result, _ = self.sinks(lambda: tf_ci.cmd_apply_prod(out, "solo7", "beef", self.env))
        self.assertIsInstance(result, tf.Exit)
        self.assertEqual(result.code, 1)
        self.assertIn("superseded plan", result.msg)
        self.assertEqual(t.calls, [], "nothing ran")

    def test_a_tampered_plan_is_refused(self):
        out, _, _ = self.plan_prod()
        manifest = tf_ci.read_manifest(out)
        manifest["plans"]["solo7"] = "0" * 64
        with open(os.path.join(out, "manifest.json"), "w") as f:
            json.dump(manifest, f)
        t = self.fake()
        result, _ = self.sinks(lambda: tf_ci.cmd_apply_prod(out, "solo7", "c0ffee", self.env))
        self.assertIsInstance(result, tf.Exit)
        self.assertIn("is not the one plan-prod printed", result.msg)
        self.assertEqual([c for c in t.calls if c[0][0] == "apply"], [])

    def test_applies_that_plan_then_a_second_plan_must_be_empty(self):
        out, _, _ = self.plan_prod()
        t = self.fake()
        result, text = self.sinks(lambda: tf_ci.cmd_apply_prod(out, "solo7", "c0ffee", self.env))
        self.assertEqual(result, 0)
        verbs = [c[0][0] for c in t.calls]
        self.assertEqual(verbs, ["init", "apply", "plan"])
        self.assertIn("-detailed-exitcode", t.calls[-1][0])
        manifest = tf_ci.read_manifest(out)
        self.assertIn(manifest["plans"]["solo7"], text, "apply-prod prints the same sha256")

    def test_a_non_empty_second_plan_fails(self):
        out, _, _ = self.plan_prod()
        self.fake(plan_rc=2)
        result, _ = self.sinks(lambda: tf_ci.cmd_apply_prod(out, "solo7", "c0ffee", self.env))
        self.assertIsInstance(result, tf.Exit)
        self.assertIn("did not converge", result.msg)


class Drift(Base):

    def test_no_drift(self):
        self.fake(plan_rc=0)
        self.assertEqual(tf_ci.main(["drift", "--stack", "solo7dev"], self.env), 0)

    def runs_gh(self, runs=(), issues="[]", close_fails=False, list_fails=False, runs_fail=False):
        """A gh whose `run list --event <e>` answers with runs of that event."""
        calls = []

        def gh(*args, input_text=None):
            calls.append(args)
            if args[:2] == ("run", "list"):
                if runs_fail:
                    raise tf.Exit(1, "gh run list failed: HTTP 403")
                event = args[args.index("--event") + 1]
                return json.dumps([r for r in runs if r["event"] == event])
            if args[:2] == ("issue", "list"):
                if list_fails:
                    raise tf.Exit(1, "gh issue list failed: HTTP 502")
                return issues
            if args[:2] == ("issue", "close") and close_fails:
                raise tf.Exit(1, "gh issue close failed: HTTP 403")
            return ""

        p = mock.patch.object(tf_ci, "gh", gh)
        p.start()
        self.addCleanup(p.stop)
        return calls

    def run_(self, event="push", status="completed", started="2000-01-01T00:00:00Z", updated=None, run_id=77):
        return {"databaseId": run_id, "event": event, "status": status, "createdAt": "2000-01-01T00:00:00Z",
                "startedAt": started, "updatedAt": updated or started}

    def issue_calls(self, calls):
        return [c for c in calls if c[0] == "issue" and c[1] != "list"]

    def test_without_issue_no_gh_at_all(self):
        """make tf-drift from the dev box: no overlap check, no issue."""
        self.fake(plan_rc=0)
        self.assertEqual(tf_ci.main(["drift", "--stack", "solo7dev"], self.env), 0)

    def test_an_apply_in_progress_makes_it_inconclusive(self):
        for status in ("queued", "in_progress", "requested", "pending"):
            with self.subTest(status=status):
                if os.path.exists(self.summary):
                    os.remove(self.summary)  # each case reads its own summary
                self.fake(plan_rc=2)
                calls = self.runs_gh(runs=[self.run_(status=status)])
                result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
                self.assertEqual(result, 0)
                self.assertIn("inconclusive", text)
                self.assertIn("### drift solo7: inconclusive", text, "the job summary says so")
                self.assertEqual(self.issue_calls(calls), [])

    def test_a_clean_check_during_an_apply_does_not_close_the_issue(self):
        self.fake(plan_rc=0)
        calls = self.runs_gh(runs=[self.run_(status="in_progress")], issues='[{"number": 9}]')
        result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result, 0)
        self.assertIn("inconclusive", text)
        self.assertEqual(self.issue_calls(calls), [])

    def test_a_dispatch_counts_like_a_push(self):
        self.fake(plan_rc=2)
        self.runs_gh(runs=[self.run_(event="workflow_dispatch", status="in_progress")])
        result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertIn("inconclusive", text)

    def test_a_rerun_that_started_during_the_plan(self):
        """A re-run keeps its old createdAt; its startedAt or updatedAt moves. Each on its own."""
        for started, updated in (("2999-01-01T00:00:00Z", "2000-01-01T00:00:00Z"),
                                 ("2000-01-01T00:00:00Z", "2999-01-01T00:00:00Z")):
            with self.subTest(started=started, updated=updated):
                self.fake(plan_rc=2)
                calls = self.runs_gh(runs=[self.run_(started=started, updated=updated)])
                result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
                self.assertIn("inconclusive", text)
                self.assertEqual(self.issue_calls(calls), [])

    def test_the_time_window(self):
        """started is taken before the plan; a minute of slack before it counts; the boundary is inclusive."""
        T = 1800000000
        for offset, overlaps in ((300, True), (-30, True), (-60, True), (-61, False), (-120, False)):
            with self.subTest(offset=offset):
                if os.path.exists(self.summary):
                    os.remove(self.summary)  # the summary accumulates; each case reads its own
                clock = [float(T)]
                t = self.fake(plan_rc=2)
                real = t.run

                def run(args, cwd, env, capture=False, pass_fds=(), real=real, clock=clock):
                    if args[0] == "plan":
                        clock[0] += 600  # the plan takes ten minutes
                    return real(args, cwd, env, capture, pass_fds)

                p = mock.patch.object(tf, "run", run)
                p.start()
                c = mock.patch.object(tf_ci.time, "time", lambda clock=clock: clock[0])
                c.start()
                when = tf_ci.iso(T + offset)
                self.runs_gh(runs=[self.run_(started=when, updated=when)])
                result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
                c.stop()
                p.stop()
                self.assertEqual("inconclusive" in text, overlaps, text)

    def test_a_run_awaiting_approval_does_not_block_the_check(self):
        """An apply-prod awaiting Brian changes nothing; drift is still reported (AC-7)."""
        self.fake(plan_rc=2)
        calls = self.runs_gh(runs=[self.run_(status="waiting")])
        result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result.code, 1)
        self.assertNotIn("inconclusive", text)
        self.assertTrue(any(c[:2] == ("issue", "create") for c in calls))

    def test_an_older_finished_run_does_not_count(self):
        self.fake(plan_rc=0)
        calls = self.runs_gh(runs=[self.run_()], issues='[{"number": 9}]')
        result, _ = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result, 0)
        self.assertTrue(any(c[:3] == ("issue", "close", "9") for c in calls))

    def test_each_event_is_listed_on_its_own(self):
        """--limit applies per event, so pull_request_target and schedule runs can't push an apply out of view."""
        self.fake(plan_rc=0)
        calls = self.runs_gh()
        self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        events = sorted(c[c.index("--event") + 1] for c in calls if c[:2] == ("run", "list"))
        self.assertEqual(events, ["push", "workflow_dispatch"])

    def test_a_failed_run_list_opens_the_issue(self):
        for plan_rc in (0, 2):
            with self.subTest(plan_rc=plan_rc):
                self.fake(plan_rc=plan_rc)
                bodies = []
                self.runs_gh(runs_fail=True)
                with mock.patch.object(tf_ci, "drift_issue", lambda root, title, body: bodies.append((title, body))):
                    result, _ = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
                self.assertIsInstance(result, tf.Exit)
                self.assertEqual(len(bodies), 1)
                self.assertIn("gh run list failed", bodies[0][1])

    def test_an_opened_issue_is_recorded_for_the_fallback(self):
        """The workflow's fallback runs unless the drift step says it reported (steps.drift.outputs.reported)."""
        self.fake(plan_rc=2)
        self.runs_gh()
        self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        with open(self.outputs) as f:
            self.assertIn("reported=true", f.read())

    def test_a_clean_check_records_nothing(self):
        self.fake(plan_rc=0)
        self.runs_gh()
        self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertFalse(os.path.exists(self.outputs) and "reported" in open(self.outputs).read())

    def test_a_failed_close_is_a_warning(self):
        self.fake(plan_rc=0)
        calls = self.runs_gh(issues='[{"number": 9}]', close_fails=True)
        result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result, 0)
        self.assertIn("closing issue #9 failed", text)
        self.assertFalse(any(c[:2] == ("issue", "comment") for c in calls))
        self.assertNotIn("failed before it could report", text)

    def test_a_failed_issue_list_on_a_clean_check_is_a_warning(self):
        self.fake(plan_rc=0)
        self.runs_gh(list_fails=True)
        result, text = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result, 0)
        self.assertIn("listing its drift issue failed", text)

    def test_no_drift_closes_the_open_issue(self):
        self.fake(plan_rc=0)
        calls = []

        def gh(*args, input_text=None):
            calls.append(args)
            return '[{"number": 9}]' if args[:2] == ("issue", "list") else ""

        with mock.patch.object(tf_ci, "gh", gh):
            result, _ = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result, 0)
        self.assertIn(("issue", "list", "--label", "drift:solo7", "--state", "open", "--json", "number", "--limit", "1"),
                      calls)
        self.assertTrue(any(c[:3] == ("issue", "close", "9") for c in calls), calls)

    def test_no_drift_and_no_issue_touches_nothing(self):
        self.fake(plan_rc=0)
        calls = []

        def gh(*args, input_text=None):
            calls.append(args)
            return "[]"

        with mock.patch.object(tf_ci, "gh", gh):
            self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertFalse(any(c[:2] in (("issue", "close"), ("issue", "create"), ("issue", "comment")) for c in calls))

    def test_drift_opens_the_issue_and_fails(self):
        self.fake(plan_json={"resource_changes": [{"address": "module.stack.grafana_folder.x",
                                                   "change": {"actions": ["update"]}}]}, plan_rc=2)
        calls = []

        def gh(*args, input_text=None):
            calls.append(args)
            return "[]" if args[:2] == ("issue", "list") else ""

        with mock.patch.object(tf_ci, "gh", gh):
            result, _ = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result.code, 1)
        self.assertIn(("issue", "list", "--label", "drift:solo7", "--state", "open", "--json", "number", "--limit", "1"),
                      calls)
        self.assertTrue(any(c[:2] == ("issue", "create") and "drift:solo7" in c for c in calls), calls)

    def test_an_open_issue_is_updated_not_duplicated(self):
        self.fake(plan_rc=2)
        calls = []

        def gh(*args, input_text=None):
            calls.append(args)
            return '[{"number": 7}]' if args[:2] == ("issue", "list") else ""

        with mock.patch.object(tf_ci, "gh", gh):
            self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertTrue(any(c[:3] == ("issue", "comment", "7") for c in calls), calls)
        self.assertFalse(any(c[:2] == ("issue", "create") for c in calls))

    def test_a_failed_job_opens_the_issue(self):
        self.env.pop("GRAFANA_SOLO7_READ_TOKEN")
        bodies = []
        with mock.patch.object(tf_ci, "drift_issue", lambda root, title, body: bodies.append((title, body))), \
                mock.patch.object(tf_ci, "gh", lambda *a, **k: "[]"):
            result, _ = self.sinks(lambda: tf_ci.cmd_drift("solo7", True, self.env))
        self.assertEqual(result.code, 3)
        self.assertEqual(len(bodies), 1)
        self.assertIn("failed", bodies[0][0])

    def test_outside_actions_only_solo7dev(self):
        self.env.pop("GITHUB_ACTIONS")
        self.assertEqual(tf_ci.main(["drift", "--stack", "solo7"], self.env), 2)


class PlanShow(Base):
    """AC-8."""

    def test_usage(self):
        self.assertEqual(tf_ci.main(["plan-show", "--stack", "solo7"], self.env), 2)
        self.assertEqual(tf_ci.main(["plan-show", "--run", "1", "--stack", "solo7dev"], self.env), 2)

    def test_without_the_key_it_is_exit_3(self):
        self.env.pop("TF_PLAN_KEY_PROD")
        with mock.patch.object(tf, "box_env_path", lambda env: None):
            self.assertEqual(tf_ci.main(["plan-show", "--run", "1", "--stack", "solo7"], self.env), 3)

    @unittest.skipUnless(hasattr(os, "memfd_create"), "Linux only")
    def test_decrypts_in_memory_and_prints_the_sha256(self):
        out, _, _ = self.plan_prod()
        t = self.fake()

        def gh(*args, input_text=None):
            dest = args[args.index("--dir") + 1]
            for name in os.listdir(out):
                shutil.copy(os.path.join(out, name), dest)
            return ""

        written = []
        real_open = open

        def spy_open(path, mode="r", *a, **k):
            if "w" in mode or "a" in mode:
                written.append(path)
            return real_open(path, mode, *a, **k)

        memfds = []
        real_memfd = os.memfd_create

        def spy_memfd(name, *a):
            fd = real_memfd(name, *a)
            memfds.append(fd)
            return fd

        tmp_before = set(os.listdir(tempfile.gettempdir()))
        with mock.patch.object(tf_ci, "gh", gh), mock.patch.object(tf, "box_env_path", lambda env: None), \
                mock.patch("builtins.open", spy_open), mock.patch.object(tf_ci.os, "memfd_create", spy_memfd):
            result, text = self.sinks(lambda: tf_ci.cmd_plan_show("42", "solo7", self.env))
        self.assertEqual(len(memfds), 1, "the plaintext goes to exactly one memfd")
        self.assertEqual(set(os.listdir(tempfile.gettempdir())) - tmp_before, set(), "nothing left in TMPDIR")
        self.assertEqual(result, 0)
        show = [c for c in t.calls if c[0][0] == "show"][0]
        self.assertTrue(show[0][1].startswith("/proc/self/fd/"), "the plan reaches terraform through a memfd")
        self.assertIn(tf_ci.read_manifest(out)["plans"]["solo7"], text)
        self.assertNotIn("does NOT match", text)
        plaintext = [w for w in written if not str(w).endswith((".tfplan.enc", "manifest.json", "summary.md"))]
        self.assertEqual(plaintext, [], "no plaintext file written")


class PlanKey(Base):

    def setUp(self):
        super().setUp()
        self.box = os.path.join(self.dir, "box.env")
        with open(self.box, "w") as f:
            f.write("GRAFANA_SOLO7DEV_READ_TOKEN=keep\n")
        os.chmod(self.box, 0o600)
        p = mock.patch.object(tf, "box_env_path", lambda env: self.box)
        p.start()
        self.addCleanup(p.stop)
        self.set = []

        def run(args, input=None, **kw):
            self.set.append((tuple(args), input))
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")

        r = mock.patch.object(tf_ci.subprocess, "run", run)
        r.start()
        self.addCleanup(r.stop)

    def test_one_key_in_both_environments_and_box_env(self):
        self.sinks(lambda: tf_ci.cmd_plan_key(False, {}))
        envs = [a[a.index("--env") + 1] for a, _ in self.set]
        self.assertEqual(envs, ["andara-prod-plan", "andara-prod-apply"])
        keys = {inp for _, inp in self.set}
        self.assertEqual(len(keys), 1)
        key = keys.pop()
        self.assertTrue(all(key not in " ".join(a) for a, _ in self.set), "the key is never an argument")
        with open(self.box) as f:
            text = f.read()
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN=keep\n", text)
        self.assertIn("TF_PLAN_KEY_PROD=" + key, text)
        self.assertEqual(os.stat(self.box).st_mode & 0o777, 0o600)

    def test_an_existing_key_is_kept_unless_rotate(self):
        with open(self.box, "a") as f:
            f.write("export TF_PLAN_KEY_PROD=old\n")
        result, _ = self.sinks(lambda: tf_ci.cmd_plan_key(False, {}))
        self.assertIsInstance(result, tf.Exit)
        self.assertEqual(self.set, [])
        self.sinks(lambda: tf_ci.cmd_plan_key(True, {}))
        with open(self.box) as f:
            text = f.read()
        self.assertNotIn("TF_PLAN_KEY_PROD=old", text)
        self.assertEqual(text.count("TF_PLAN_KEY_PROD="), 1)


class Misc(Base):

    def test_pr_plan_inits_with_a_readonly_lock_file_and_a_minimal_environment(self):
        """-lockfile=readonly is what holds a pull request to the base branch's provider pins (AC-2)."""
        t = self.fake()
        seen = []
        real = t.run

        def run(args, cwd, env, capture=False, pass_fds=()):
            seen.append(dict(env))
            return real(args, cwd, env, capture, pass_fds)

        tf.run = run
        self.addCleanup(setattr, tf, "run", t.run)
        env = dict(self.env, ACTIONS_ID_TOKEN_REQUEST_TOKEN="oidc", ACTIONS_ID_TOKEN_REQUEST_URL="https://x",
                   GITHUB_TOKEN="ghs_x", GRAFANA_SOLO7_APPLY_TOKEN="apply", RUNNER_SECRET="x")
        result, _ = self.sinks(lambda: tf_ci.cmd_pr_plan("solo7", tf.TF_DIR, "c", env))
        self.assertEqual(result, 0)
        init = [c for c in t.calls if c[0][0] == "init"][0][0]
        self.assertIn("-lockfile=readonly", init)
        for e in seen:
            for k in ("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL", "GITHUB_TOKEN",
                      "GRAFANA_SOLO7_APPLY_TOKEN", "GRAFANA_SOLO7_READ_TOKEN", "RUNNER_SECRET"):
                self.assertNotIn(k, e)
            self.assertEqual(e["GRAFANA_AUTH"], "read")

    def test_pr_plan_refuses_the_irm_root(self):
        self.assertEqual(tf_ci.main(["pr-plan", "--stack", "solo7-irm", "--commit", "x"], self.env), 2)

    def test_apply_is_solo7dev_only(self):
        self.assertEqual(tf_ci.main(["apply", "--stack", "solo7", "--commit", "x"], self.env), 2)

    def test_matrix_names(self):
        env = tf_ci.matrix_names("solo7", {"TF_CI_READ_TOKEN": "r", "TF_CI_FLEET_READ_TOKEN": "f", "TF_CI_WEBHOOK": "w"})
        self.assertEqual(env, {"GRAFANA_SOLO7_READ_TOKEN": "r", "GRAFANA_SOLO7_FLEET_READ_TOKEN": "f",
                               "SLACK_WEBHOOK_PROD": "w"})

    def test_claims_never_print_the_token(self):
        header = base64.urlsafe_b64encode(b'{"alg":"RS256"}').rstrip(b"=").decode()
        payload = base64.urlsafe_b64encode(json.dumps(
            {"sub": "repo:valesordev/andara.valesordev.com:pull_request", "event_name": "pull_request_target",
             "workflow_ref": "valesordev/andara.valesordev.com/.github/workflows/terraform.yaml@refs/heads/main",
             "exp": 1}).encode()).rstrip(b"=").decode()
        token = "%s.%s.SIGNATURExyz" % (header, payload)
        env = dict(self.env, ACTIONS_ID_TOKEN_REQUEST_URL="https://token.invalid/?x=1",
                   ACTIONS_ID_TOKEN_REQUEST_TOKEN="BEARERsecret", GCP_WIF_PROVIDER="projects/1/locations/global/x")
        resp = types.SimpleNamespace(__enter__=None)
        fake = mock.MagicMock()
        fake.__enter__.return_value = io.BytesIO(json.dumps({"value": token}).encode())
        with mock.patch("urllib.request.urlopen", return_value=fake) as urlopen:
            result, text = self.sinks(lambda: tf_ci.cmd_claims(env))
        self.assertEqual(result, 0)
        self.assertIn('"event_name": "pull_request_target"', text)
        self.assertIn("workflow_ref", text)
        for secret in ("SIGNATURExyz", "BEARERsecret", token):
            self.assertNotIn(secret, text)
        self.assertNotIn('"exp"', text)
        request = urlopen.call_args[0][0]
        self.assertIn("audience=https%3A%2F%2Fiam.googleapis.com%2Fprojects", request.full_url)
        del resp


if __name__ == "__main__":
    unittest.main()
