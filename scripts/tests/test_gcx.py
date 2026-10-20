# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/gcx.py (AW-INF-075): the wrapper against gcx v1.5.0's recorded shapes (AC-2), its argv, errors and
redaction, and make gcx-config (AC-7). A fake gcx prints a fixture; the real one is test_gcx_routing.py's."""
import datetime
import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import gcx  # noqa: E402
import grafana_stack  # noqa: E402

FIXTURES = os.path.join(REPO, "scripts", "tests", "fixtures", "gcx")
TOKEN = "glsa_known-test-token_0123456789abcdef"

FAKE = textwrap.dedent("""\
    #!/usr/bin/env python3
    import json, os, sys
    with open(os.environ["FAKE_GCX_ARGV"], "a") as f:
        f.write(json.dumps({"argv": sys.argv[1:], "agent": [k for k in ("CLAUDECODE", "GCX_AGENT_MODE") if k in os.environ]}) + "\\n")
    if os.environ.get("FAKE_GCX_STDERR"):
        sys.stderr.write(open(os.environ["FAKE_GCX_STDERR"]).read())
    if os.environ.get("FAKE_GCX_FIXTURE"):
        sys.stdout.write(open(os.environ["FAKE_GCX_FIXTURE"]).read())
    sys.exit(int(os.environ.get("FAKE_GCX_EXIT", "0")))
""")


def fixture(name):
    with open(os.path.join(FIXTURES, name)) as f:
        return json.load(f)


class FakeGcx(unittest.TestCase):
    """Each test runs the wrapper against a fake bin/gcx that prints one fixture."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.bin = os.path.join(self.tmp, "gcx")
        with open(self.bin, "w") as f:
            f.write(FAKE)
        os.chmod(self.bin, 0o755)
        self.argv_log = os.path.join(self.tmp, "argv")
        self.env = {"PATH": os.environ["PATH"], "FAKE_GCX_ARGV": self.argv_log,
                    "GCX_CONFIG": os.path.join(self.tmp, "gcx.yaml")}
        patcher = mock.patch.object(gcx, "GCX", self.bin)
        patcher.start()
        self.addCleanup(patcher.stop)

    def serve(self, name, **extra):
        env = dict(self.env, FAKE_GCX_FIXTURE=os.path.join(FIXTURES, name), **extra)
        return {"env": env}

    def calls(self):
        with open(self.argv_log) as f:
            return [json.loads(l) for l in f]


class Shapes(FakeGcx):
    """AC-2: every empty shape is [], every non-empty one a parsed list, read by named fields."""

    def test_empty_shapes_are_empty_lists(self):
        for name, call in [
            ("alert_rules_list_empty.json", lambda kw: gcx.rule_groups("solo7dev", **kw)),            # null
            ("alert_instances_json_empty.json", lambda kw: gcx.instances("solo7dev", "^X$", **kw)),   # all-null object
            ("alert_instances_name_empty.json", lambda kw: gcx.instances("solo7dev", "^X$", **kw)),   # []
            ("alert_instances_legacy_empty.json", lambda kw: gcx.legacy_instances("solo7", **kw)),    # null
            ("metrics_query_vector_empty.json", lambda kw: gcx.metrics_query("solo7dev", "up", **kw)),  # result: []
            ("metrics_series_empty.json", lambda kw: gcx.metrics_series("solo7dev", "{a=\"b\"}", **kw)),
            ("logs_query_empty.json", lambda kw: gcx.logs_query("solo7dev", "{a=\"b\"}", **kw)),
            ("traces_query_empty.json", lambda kw: gcx.traces_query("solo7dev", "{ }", **kw)),
            ("traces_get_missing.json", lambda kw: gcx.trace_get("solo7dev", "00", **kw)),            # {}
            ("irm_alert_groups_empty.json", lambda kw: gcx.irm_alert_groups("solo7", **kw)),           # items: []
        ]:
            with self.subTest(name):
                self.assertEqual(call(self.serve(name)), [])

    def test_rule_groups(self):
        groups = gcx.rule_groups("solo7dev", **self.serve("alert_rules_list.json"))
        self.assertEqual([g["name"] for g in groups], ["andara-recovery"])
        rule = gcx.rule_defined("solo7dev", "andara-recovery", "RecoveryStateMismatch",
                                **self.serve("alert_rules_list.json"))
        self.assertEqual((rule["uid"], rule["isPaused"], rule["health"], rule["interval"]),
                         ("andara-recovery-state-mismatch", False, "ok", 60))
        self.assertIsNone(gcx.rule_defined("solo7dev", "andara-recovery", "Nope", **self.serve("alert_rules_list.json")))
        grouped = [c["argv"] for c in self.calls() if "--group" in c["argv"]]
        self.assertEqual(grouped[0][grouped[0].index("--group") + 1], "andara-recovery")
        self.assertNotIn("--group", self.calls()[0]["argv"])

    def test_rule_live(self):
        at = datetime.datetime(2026, 10, 19, 21, 38, 50, tzinfo=datetime.timezone.utc)
        live = lambda now, name="alert_rules_list.json": gcx.rule_live(  # noqa: E731
            "solo7dev", "andara-recovery", "RecoveryStateMismatch", now, **self.serve(name))
        self.assertTrue(live(at + datetime.timedelta(seconds=120)))
        self.assertFalse(live(at + datetime.timedelta(seconds=121)), "older than twice the 60 s interval")
        paused = fixture("alert_rules_list.json")
        paused[0]["rules"][0]["isPaused"] = True
        path = os.path.join(self.tmp, "paused.json")
        with open(path, "w") as f:
            json.dump(paused, f)
        self.assertFalse(gcx.rule_live("solo7dev", "andara-recovery", "RecoveryStateMismatch", at,
                                       env=dict(self.env, FAKE_GCX_FIXTURE=path)), "a paused rule is never live")

    def test_instances_map_state_and_filter_labels(self):
        got = gcx.instances("solo7dev", "^RecoveryStateMismatch$", **self.serve("alert_instances_json.json"))
        self.assertEqual([(i["labels"]["environment"], i["state"]) for i in got],
                         [("dev", "firing"), ("local", "normal"), ("staging", "pending")])
        dev = gcx.instances("solo7dev", "^RecoveryStateMismatch$", labels={"environment": "dev"},
                            **self.serve("alert_instances_json.json"))
        self.assertEqual([(i["ruleName"], i["activeAt"]) for i in dev], [("RecoveryStateMismatch", "2026-10-19T21:30:50Z")])
        argv = self.calls()[-1]["argv"]
        self.assertIn("--json", argv)
        self.assertEqual(argv[argv.index("--json") + 1], "ruleName,labels,state,activeAt")

    def test_state_vocabulary(self):
        for raw, want in [("Alerting", "firing"), ("firing", "firing"), ("Pending", "pending"),
                          ("Normal", "normal"), ("Normal (NoData)", "normal"), ("inactive", "normal"),
                          ("Recovering", "recovering")]:
            self.assertEqual(gcx.state(raw), want, raw)

    def test_metrics(self):
        vec = gcx.metrics_query("solo7dev", "up", **self.serve("metrics_query_vector.json"))
        self.assertEqual((vec[0]["metric"]["job"], vec[0]["value"][1]), ("andara-server", "1"))
        mat = gcx.metrics_query("solo7dev", "up", since="30m", **self.serve("metrics_query_matrix.json"))
        self.assertEqual(len(mat[0]["values"]), 2)
        self.assertEqual(self.calls()[-1]["argv"][-5:], ["up", "--since", "30m", "-o", "json"])
        self.assertNotIn("--since", self.calls()[-2]["argv"], "no since is an instant query")

    def test_series_and_labels(self):
        series = gcx.metrics_series("solo7dev", '{__name__="andara_ticks_total"}', **self.serve("metrics_series.json"))
        self.assertEqual([s["environment"] for s in series], ["dev", "local"])
        self.assertEqual(gcx.metrics_labels("solo7dev", "environment", **self.serve("metrics_labels.json")),
                         ["dev", "local"])

    def test_logs_flatten_to_lines(self):
        lines = gcx.logs_query("solo7dev", '{namespace="andara-dev"}', **self.serve("logs_query.json"))
        self.assertEqual(len(lines), 1)
        self.assertEqual(json.loads(lines[0]["line"])["msg"], "session opened")
        self.assertEqual((lines[0]["timestamp"], lines[0]["stream"]["container"]), ("1792445731500248487", "server"))

    def test_traces(self):
        found = gcx.traces_query("solo7dev", "{ }", **self.serve("traces_query.json"))
        self.assertEqual(found[0]["traceID"], "9f330ddce795582aca223dff19488478")
        trace = gcx.trace_get("solo7dev", "9f33", **self.serve("traces_get.json"))
        self.assertEqual(trace[0]["resourceSpans"][0]["scopeSpans"][0]["spans"][0]["name"],
                         "andara.game.v1.Game/OpenSession")

    def test_irm(self):
        groups = gcx.irm_alert_groups("solo7", **self.serve("irm_alert_groups.json"))
        self.assertEqual((groups[0]["status"]["state"], groups[0]["status"]["subject"]["labels"]["environment"]),
                         ("firing", "prod"))

    def test_a_changed_field_fails(self):
        """A field gcx renames between versions changes what the wrapper returns, so the assertions above fail."""
        doc = fixture("logs_query.json")
        doc["data"]["result"][0]["entries"] = doc["data"]["result"][0].pop("values")
        path = os.path.join(self.tmp, "renamed.json")
        with open(path, "w") as f:
            json.dump(doc, f)
        self.assertEqual(gcx.logs_query("solo7dev", "{}", env=dict(self.env, FAKE_GCX_FIXTURE=path)), [])

    def test_all_null_items_in_a_list_are_dropped(self):
        self.assertEqual(gcx.normalise([{"ruleName": None, "state": None}]), [])
        self.assertEqual(gcx.normalise([{"ruleName": None, "state": None}, {"ruleName": "R", "state": None}]),
                         [{"ruleName": "R", "state": None}])

    def test_an_unknown_shape_is_an_error(self):
        with self.assertRaises(gcx.GcxError):
            gcx.normalise({"something": "else"})


class Run(FakeGcx):

    def test_argv_always_carries_config_context_and_json(self):
        gcx.metrics_query("solo7dev", "up", **self.serve("metrics_query_vector.json"))
        argv = self.calls()[-1]["argv"]
        self.assertEqual(argv[:4], ["--config", self.env["GCX_CONFIG"], "--context", "solo7dev"])
        self.assertEqual(argv[-2:], ["-o", "json"])
        self.assertEqual(argv[4:9], ["metrics", "query", "-d", "grafanacloud-prom", "up"])

    def test_default_config_path(self):
        self.assertEqual(gcx.config_path({}), os.path.join(REPO, ".local", "gcx.yaml"))
        self.assertEqual(gcx.config_path({"RUNNER_TEMP": "/rt"}), "/rt/gcx.yaml")
        self.assertEqual(gcx.config_path({"RUNNER_TEMP": "/rt", "GCX_CONFIG": "/c.yaml"}), "/c.yaml")

    def test_a_cut_never_leaves_a_token_prefix(self):
        """gcx's --json error path prints the raw token on stdout; redaction happens before the 300-byte cut."""
        out = os.path.join(self.tmp, "out")
        with open(out, "w") as f:
            f.write("x" * 290 + TOKEN + "\n")
        with self.assertRaises(gcx.GcxError) as e:
            gcx.metrics_query("solo7dev", "up", env=dict(self.env, FAKE_GCX_FIXTURE=out, FAKE_GCX_EXIT="1",
                                                       GRAFANA_SOLO7DEV_READ_TOKEN=TOKEN))
        self.assertNotIn(TOKEN[:10], str(e.exception))
        self.assertIn("***", str(e.exception))
        with self.assertRaises(gcx.GcxError) as e:  # not JSON
            gcx.metrics_query("solo7dev", "up", env=dict(self.env, FAKE_GCX_FIXTURE=out,
                                                       GRAFANA_SOLO7DEV_READ_TOKEN=TOKEN))
        self.assertNotIn(TOKEN[:10], str(e.exception))

    def test_agent_mode_is_switched_off(self):
        gcx.metrics_query("solo7dev", "up", **self.serve("metrics_query_vector.json", CLAUDECODE="1", GCX_AGENT_MODE="1"))
        self.assertEqual(self.calls()[-1]["agent"], [])

    def test_nonzero_exit_is_an_error_with_gcx_message(self):
        with self.assertRaises(gcx.GcxError) as e:
            gcx.metrics_query("solo7dev", "up{", **self.serve(
                "metrics_query_vector_empty.json", FAKE_GCX_EXIT="1",
                FAKE_GCX_STDERR=os.path.join(FIXTURES, "error_invalid_promql.stderr")))
        msg = str(e.exception)
        self.assertIn("gcx: solo7dev metrics query -d grafanacloud-prom up{: exit 1: Error: Invalid PromQL query", msg)
        self.assertIn("parse error: unexpected end of input inside braces", msg)
        self.assertNotIn("Suggestions", msg)
        self.assertNotIn("gcx metrics query --help", msg)

    def test_unknown_context_message(self):
        with self.assertRaises(gcx.GcxError) as e:
            gcx.metrics_query("nosuch", "up", **self.serve(
                "metrics_query_vector_empty.json", FAKE_GCX_EXIT="1",
                FAKE_GCX_STDERR=os.path.join(FIXTURES, "error_unknown_context.stderr")))
        self.assertIn('invalid context "nosuch": context not found', str(e.exception))

    def test_missing_binary(self):
        with mock.patch.object(gcx, "GCX", os.path.join(self.tmp, "absent")):
            with self.assertRaisesRegex(gcx.GcxError, "make bootstrap"):
                gcx.metrics_query("solo7dev", "up", env=self.env)

    def test_no_line_carries_a_token(self):
        """Observability requirement: a known token is absent from everything the wrapper prints or raises."""
        leak = os.path.join(self.tmp, "leak")
        with open(leak, "w") as f:
            f.write("Error: boom\n│ token %s rejected\n" % TOKEN)
        cfg = os.path.join(self.tmp, "gcx.yaml")
        with open(cfg, "w") as f:
            f.write('stacks:\n  solo7dev:\n    grafana:\n      token: "%s-cfg"\n' % TOKEN)
        env = dict(self.env, FAKE_GCX_EXIT="1", FAKE_GCX_STDERR=leak, GCX_DEBUG="1",
                   GRAFANA_SOLO7DEV_READ_TOKEN=TOKEN)
        code = ("import sys; sys.path.insert(0, %r); import gcx\n"
                "gcx.GCX = %r\n"
                "try:\n    gcx.metrics_query('solo7dev', 'up')\n"
                "except gcx.GcxError as e:\n    print(e); print(e, file=sys.stderr)\n"
                % (os.path.join(REPO, "scripts"), self.bin))
        p = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, env=env)
        self.assertIn("gcx: solo7dev metrics query", p.stderr, "the debug line")
        self.assertIn("exit 1: Error: boom", p.stdout)
        for out in (p.stdout, p.stderr):
            self.assertNotIn(TOKEN, out)
            self.assertNotIn("0123456789abcdef", out)
        with open(leak, "w") as f:
            f.write("Error: boom %s-cfg\n" % TOKEN)
        p = subprocess.run([sys.executable, "-c", code], capture_output=True, text=True,
                           env=dict(env, GRAFANA_SOLO7DEV_READ_TOKEN=""))
        self.assertNotIn(TOKEN, p.stdout + p.stderr, "a token only the config holds is redacted too")


class Check(FakeGcx):
    """make gcx-check STACK=<stack>: one stack's read token reads its rule groups (AW-INF-076's rotation check)."""

    def env_for(self, name, **extra):
        return dict(self.serve(name)["env"], ANDARA_BOX_ENV=os.path.join(self.tmp, "no-box.env"), **extra)

    def test_reads_the_named_stack_with_its_token(self):
        env = self.env_for("alert_rules_list.json", GRAFANA_SOLO7DEV_READ_TOKEN="dev", GRAFANA_SOLO7_READ_TOKEN=TOKEN)
        n = gcx.check("solo7", env)
        self.assertEqual(n, len(gcx.normalise(fixture("alert_rules_list.json"))))
        argv = self.calls()[-1]["argv"]
        self.assertEqual(argv[argv.index("--context") + 1], "solo7")
        self.assertIn("rules", argv)
        import yaml
        with open(self.env["GCX_CONFIG"]) as f:
            self.assertEqual(yaml.safe_load(f)["stacks"]["solo7"]["grafana"]["token"], TOKEN)

    def test_a_refused_token_raises(self):
        env = self.env_for("metrics_query_vector_empty.json", GRAFANA_SOLO7_READ_TOKEN=TOKEN, FAKE_GCX_EXIT="1")
        with self.assertRaises(gcx.GcxError):
            gcx.check("solo7", env)

    def test_the_named_stacks_token_unset_exits_3(self):
        # The other stack's token being set must not stand in for it.
        env = self.env_for("alert_rules_list.json", GRAFANA_SOLO7DEV_READ_TOKEN="dev")
        with self.assertRaises(grafana_stack.Exit) as e:
            gcx.check("solo7", env)
        self.assertEqual(e.exception.code, 3)
        self.assertIn("GRAFANA_SOLO7_READ_TOKEN", str(e.exception))
        self.assertEqual(os.path.exists(self.argv_log), False, "gcx is not run without the token")


class Config(unittest.TestCase):
    """AC-7: make gcx-config."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.path = os.path.join(self.tmp, "sub", "gcx.yaml")
        self.base = {"GCX_CONFIG": self.path, "ANDARA_BOX_ENV": os.path.join(self.tmp, "no-box.env")}

    def test_one_token_one_context_mode_0600(self):
        stacks = gcx.write_config(dict(self.base, GRAFANA_SOLO7DEV_READ_TOKEN=TOKEN))
        self.assertEqual(stacks, ["solo7dev"])
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)
        with open(self.path) as f:
            text = f.read()
        import yaml
        doc = yaml.safe_load(text)
        self.assertEqual(list(doc["contexts"]), ["solo7dev"])
        self.assertEqual(doc["contexts"]["solo7dev"], {"stack": "solo7dev"})
        self.assertEqual(doc["stacks"]["solo7dev"]["grafana"],
                         {"server": "https://solo7dev.grafana.net", "token": TOKEN})
        self.assertEqual(doc["current-context"], "solo7dev")

    def test_both_tokens(self):
        stacks = gcx.write_config(dict(self.base, GRAFANA_SOLO7DEV_READ_TOKEN=TOKEN, GRAFANA_SOLO7_READ_TOKEN="other"))
        self.assertEqual(stacks, ["solo7dev", "solo7"])
        import yaml
        with open(self.path) as f:
            doc = yaml.safe_load(f)
        self.assertEqual(doc["stacks"]["solo7"]["grafana"]["server"], "https://solo7.grafana.net")

    def test_rewrite_keeps_mode(self):
        gcx.write_config(dict(self.base, GRAFANA_SOLO7_READ_TOKEN="a"))
        os.chmod(self.path, 0o644)
        gcx.write_config(dict(self.base, GRAFANA_SOLO7_READ_TOKEN="b"))
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)

    def test_none_set_exits_3(self):
        with self.assertRaises(grafana_stack.Exit) as e:
            gcx.write_config(dict(self.base))
        self.assertEqual(e.exception.code, 3)
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN", str(e.exception))
        self.assertFalse(os.path.exists(self.path))

    def test_script_exits_3_without_tokens(self):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GRAFANA_")}
        env.update(self.base)
        p = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "gcx.py"), "config"],
                           capture_output=True, text=True, env=env)
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("gcx-config: no read token set", p.stderr)
        # make reports any failed recipe as its own exit 2; the script's message reaches the operator.
        p = subprocess.run(["make", "-s", "-C", REPO, "gcx-config"], capture_output=True, text=True, env=env)
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("gcx-config: no read token set", p.stderr)

    def test_script_usage_exits_2(self):
        for argv in (["check"], ["check", "nope"], ["check", "solo7", "x"], ["bogus"]):
            p = subprocess.run([sys.executable, os.path.join(REPO, "scripts", "gcx.py")] + argv,
                               capture_output=True, text=True, env=dict(os.environ, **self.base))
            self.assertEqual(p.returncode, 2, argv)

    def test_default_path_is_gitignored(self):
        p = subprocess.run(["git", "-C", REPO, "check-ignore", "-q", ".local/gcx.yaml"])
        self.assertEqual(p.returncode, 0)


if __name__ == "__main__":
    unittest.main()
