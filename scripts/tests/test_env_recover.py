# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make env-recover`'s guard, kill mechanism and assertions (AW-INF-034)."""
import json
import os
import subprocess
import sys
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import env_recover  # noqa: E402


def make(*args):
    # PATH without kubectl: a refusal that reached the cluster would fail differently.
    env = dict(os.environ, PATH="/usr/bin:/bin")
    return subprocess.run(["make", "-s", "-C", REPO, "env-recover", *args],
                          capture_output=True, text=True, env=env)


class Guard(unittest.TestCase):

    def assertRefused(self, r, text):
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("env-recover: " + text, r.stderr)

    def test_missing_confirm(self):
        self.assertRefused(make("ENV=dev"), "refusing without CONFIRM=andara-dev; this kills dev's server")

    def test_wrong_confirm(self):
        self.assertRefused(make("ENV=dev", "CONFIRM=dev"), "refusing without CONFIRM=andara-dev; this kills dev's server")

    def test_prod(self):
        self.assertRefused(make("ENV=prod", "CONFIRM=andara-prod"), "prod is not enabled")

    def test_local(self):
        self.assertRefused(make("ENV=local", "CONFIRM=andara-local"), "local is make stack-recover")

    def test_no_env(self):
        self.assertEqual(make("CONFIRM=andara-dev").returncode, 2)

    def test_accepted(self):
        self.assertEqual(env_recover.check_args(["dev", "andara-dev"]), ("dev", "andara-dev"))


def pod_doc(uid="u1", node="kind-worker", restarts=0, exit_code=None, ready=True, cid="containerd://abc123"):
    srv = {"name": "server", "containerID": cid, "restartCount": restarts}
    if exit_code is not None:
        srv["lastState"] = {"terminated": {"exitCode": exit_code}}
    other = {"name": "sidecar", "containerID": "containerd://zzz", "restartCount": 9}
    return {"metadata": {"uid": uid}, "spec": {"nodeName": node},
            "status": {"containerStatuses": [other, srv],
                       "conditions": [{"type": "Ready", "status": "True" if ready else "False"}]}}


class Kill(unittest.TestCase):

    def test_pid_comes_from_the_server_containers_id_on_its_node(self):
        pod = env_recover.parse_pod(pod_doc())
        calls = []

        def run(args):
            calls.append(args)
            return types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 4242}}), stderr="")

        self.assertEqual(env_recover.resolve_pid(pod["container_id"], pod["node"], run), 4242)
        self.assertEqual(calls, [["docker", "exec", "kind-worker", "crictl", "inspect", "abc123"]])

    def test_pid_failures(self):
        bad = [types.SimpleNamespace(returncode=1, stdout="", stderr="no such container"),
               types.SimpleNamespace(returncode=0, stdout="{}", stderr=""),
               types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 1}}), stderr=""),
               types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": "4242"}}), stderr=""),
               types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": None}}), stderr="")]
        for out in bad:
            with self.assertRaises(env_recover.Failed):
                env_recover.resolve_pid("containerd://abc", "n", lambda a, out=out: out)
        with self.assertRaises(env_recover.Failed):
            env_recover.resolve_pid("", "n", lambda a: bad[0])

    def test_restart_versus_reschedule(self):
        before = env_recover.parse_pod(pod_doc())
        ok = env_recover.parse_pod(pod_doc(restarts=1, exit_code=137))
        self.assertIsNone(env_recover.check_restart(before, ok))
        moved = env_recover.parse_pod(pod_doc(uid="u2", restarts=1, exit_code=137))
        self.assertIn("reschedule", env_recover.check_restart(before, moved))
        twice = env_recover.parse_pod(pod_doc(restarts=2, exit_code=137))
        self.assertIn("exactly +1", env_recover.check_restart(before, twice))
        sigterm = env_recover.parse_pod(pod_doc(restarts=1, exit_code=143))
        self.assertIn("137", env_recover.check_restart(before, sigterm))

    def test_parse_pod_reads_the_server_container_not_the_first(self):
        pod = env_recover.parse_pod(pod_doc(restarts=3))
        self.assertEqual((pod["restarts"], pod["container_id"]), (3, "containerd://abc123"))
        self.assertTrue(pod["ready"])
        self.assertFalse(env_recover.parse_pod(pod_doc(ready=False))["ready"])


def vec(*values):
    return [({"namespace": "andara-dev"}, v) for v in values]


class Recovery(unittest.TestCase):
    KILL = 1000.0

    def check(self, rounds, matches, stamps, r=60):
        return env_recover.check_recovery("andara-dev", rounds, matches, stamps, r, self.KILL)

    def test_holds(self):
        self.assertIsNone(self.check(vec(60), vec(1), vec(1001.0)))

    def test_pre_kill_sample_fails(self):
        # The hash match was 1 before the kill too; a sample from before it proves nothing.
        self.assertIn("not after the kill", self.check(vec(60), vec(1), vec(990.0)))

    def test_old_round_fails(self):
        self.assertIn("round_tick", self.check(vec(30), vec(1), vec(1001.0)))

    def test_mixed_rounds_fail(self):
        self.assertIn("round_tick", self.check(vec(60, 120), vec(1), vec(1001.0)))

    def test_newer_round_fails(self):
        self.assertIn("round_tick", self.check(vec(120), vec(1), vec(1001.0)))

    def test_mismatch_fails(self):
        self.assertIn("hash_match", self.check(vec(60), vec(0), vec(1001.0)))

    def test_no_samples_yet(self):
        self.assertIn("no sample", self.check([], [], []))


class Inconclusive(unittest.TestCase):

    def test_rule_not_loaded(self):
        loaded = yaml_rules("RecoveryStateMismatch")
        self.assertTrue(env_recover.rule_loaded(loaded))
        self.assertFalse(env_recover.rule_loaded(yaml_rules("Other")))
        self.assertFalse(env_recover.rule_loaded(yaml_rules("RecoveryStateMismatch").replace("andara:", "other:")))
        self.assertFalse(env_recover.rule_loaded(""))
        self.assertFalse(env_recover.rule_loaded("andara: []"))

    def test_newer_round_is_inconclusive(self):
        env_recover.check_round_unchanged(60, 60)
        with self.assertRaisesRegex(env_recover.Failed, "inconclusive"):
            env_recover.check_round_unchanged(90, 60)

    def _preflight(self, loaded, firing):
        run = env_recover.Run("dev", "andara-dev", {})
        self.addCleanup(run.cleanup)
        run.pod = lambda: {"ready": True}
        g = types.SimpleNamespace(firing_between=lambda *a: [{"metric": {}}] if firing else [])
        real = (env_recover.fetch_ruler, env_recover.shutil.which, os.access)
        env_recover.fetch_ruler = lambda env: yaml_rules("RecoveryStateMismatch" if loaded else "Other")
        env_recover.shutil.which = lambda n: "/bin/" + n
        run.cli = "/bin/true"
        try:
            run.preflight(g, {})
        finally:
            env_recover.fetch_ruler, env_recover.shutil.which = real[0], real[1]

    def test_rule_not_loaded_is_inconclusive(self):
        with self.assertRaisesRegex(env_recover.Failed, "inconclusive.*not loaded"):
            self._preflight(loaded=False, firing=False)

    def test_alert_already_firing_is_inconclusive(self):
        with self.assertRaisesRegex(env_recover.Failed, "inconclusive.*already firing"):
            self._preflight(loaded=True, firing=True)

    def test_preflight_passes_when_loaded_and_quiet(self):
        self._preflight(loaded=True, firing=False)

    def test_newest_round_is_the_first_complete_row(self):
        table = "TICK AGE ZONES COMPLETE\n90 1s 2 false\n60 61s 2 true\n30 2m 2 true\n"
        self.assertEqual(env_recover.newest_round(table), 60)
        self.assertEqual(env_recover.newest_round("TICK AGE ZONES COMPLETE\n"), 0)

    def test_missing_credentials_name_the_variable(self):
        absent, _ = env_recover.missing_credentials({"GRAFANA_CLOUD_READ_TOKEN": "t"})
        self.assertIn("GRAFANA_CLOUD_PROM_URL", absent)
        self.assertIn("MIMIR_API_KEY", absent)
        absent, env = env_recover.missing_credentials({
            "GRAFANA_CLOUD_READ_TOKEN": "t", "GRAFANA_CLOUD_PROM_URL": "https://p.example/api/prom",
            "GRAFANA_CLOUD_PROM_USER": "123", "MIMIR_API_KEY": "k"})
        self.assertEqual(absent, [])
        self.assertEqual(env["MIMIR_TENANT_ID"], "123")


class RunSteps(unittest.TestCase):

    def run_(self, rto="120"):
        run = env_recover.Run("dev", "andara-dev", {"ENV_RECOVER_RTO": rto})
        run.cli = "/bin/true"  # teardown's set-status must not reach a real CLI
        self.addCleanup(run.cleanup)
        return run

    def test_make_player_records_the_account_id_from_create(self):
        run = self.run_()
        calls = []

        def cli_run(*args, stdin=None, creds=None, check=True):
            calls.append(args)
            if args[:3] == ("--output", "json", "account"):
                return json.dumps({"account_id": "acc-1", "username": "recover-a-x"})
            return ""

        run.cli_run = cli_run
        user, name = run.make_player("a", "ab12", "Recovered")
        self.assertEqual(run.account_ids, {"a": "acc-1"})
        self.assertTrue(name.startswith("Recovered"))
        self.assertFalse(any("whoami" in c for c in calls))

    def test_account_id_kept_when_a_later_step_fails(self):
        run = self.run_()

        def cli_run(*args, stdin=None, creds=None, check=True):
            if args[0] == "auth":
                raise env_recover.Failed("login failed")
            return json.dumps({"account_id": "acc-1"})

        run.cli_run = cli_run
        with self.assertRaises(env_recover.Failed):
            run.make_player("a", "ab12", "Recovered")
        self.assertEqual(run.account_ids, {"a": "acc-1"})

    def test_cleanup_survives_a_timeout_and_still_disables_the_other(self):
        run = self.run_()
        run.account_ids = {"a": "acc-a", "b": "acc-b"}
        run.cli = "andara-cli"
        seen = []

        def fake(cmd, **kw):
            seen.append(cmd[3])
            if cmd[3] == "acc-a":
                raise env_recover.subprocess.TimeoutExpired(cmd, 60)
            return types.SimpleNamespace(returncode=0, stdout="", stderr="")

        real = env_recover.subprocess.run
        env_recover.subprocess.run = fake
        self.addCleanup(setattr, env_recover.subprocess, "run", real)
        run.cleanup()
        run.account_ids = {}
        self.assertEqual(seen, ["acc-a", "acc-b"])
        self.assertFalse(os.path.exists(run.work))

    def test_unlanded_kill(self):
        run = self.run_()
        run.pod = lambda: env_recover.parse_pod(pod_doc())
        run.sh = lambda args: types.SimpleNamespace(
            returncode=0, stdout=json.dumps({"info": {"pid": 4242}}), stderr="")
        real = env_recover.KILL_LANDS_WITHIN
        env_recover.KILL_LANDS_WITHIN = 0
        self.addCleanup(setattr, env_recover, "KILL_LANDS_WITHIN", real)
        with self.assertRaisesRegex(env_recover.Failed, "SIGKILL didn't land; restartCount unchanged"):
            run.kill()

    def test_cleanup_disables_both_accounts(self):
        run = self.run_()
        run.account_ids = {"a": "acc-a", "b": "acc-b"}
        run.cli = "andara-cli"
        seen = []
        real = env_recover.subprocess.run
        env_recover.subprocess.run = lambda cmd, **kw: seen.append(cmd) or types.SimpleNamespace(
            returncode=0, stdout="", stderr="")
        self.addCleanup(setattr, env_recover.subprocess, "run", real)
        run.cleanup()
        run.account_ids = {}  # teardown's cleanup has nothing left to disable
        self.assertEqual([c[1:] for c in seen], [["account", "set-status", "acc-a", "disabled"],
                                                  ["account", "set-status", "acc-b", "disabled"]])


class Main(unittest.TestCase):
    CREDS = {"GRAFANA_CLOUD_READ_TOKEN": "t", "GRAFANA_CLOUD_PROM_URL": "https://p.example/api/prom",
             "GRAFANA_CLOUD_PROM_USER": "123", "MIMIR_API_KEY": "k"}

    def go(self, environ, **overrides):
        """main() with the cluster-touching pieces stubbed; returns (status, events)."""
        events = []

        class FakeRun(env_recover.Run):
            def dump(self):
                events.append("dump")

            def cleanup(self):
                events.append("cleanup")
                shutil_rm(self.work)

        for name, fn in overrides.items():
            setattr(FakeRun, name, fn)
        real = (env_recover.Run, env_recover.Grafana)
        env_recover.Run, env_recover.Grafana = FakeRun, lambda: object()
        try:
            with mock.patch.dict(os.environ, environ, clear=True):
                status = env_recover.main(["dev", "andara-dev"])
        finally:
            env_recover.Run, env_recover.Grafana = real
        return status, events

    def test_bad_rto_fails_before_the_cluster(self):
        for rto in ("abc", "1.5", "0", ""):
            touched = []
            status, _ = self.go(dict(self.CREDS, ENV_RECOVER_RTO=rto),
                                preflight=lambda self, g, e: touched.append(1))
            self.assertEqual((status, touched), (1, []), rto)

    def test_whitespace_token_is_missing(self):
        touched = []
        status, _ = self.go(dict(self.CREDS, GRAFANA_CLOUD_READ_TOKEN="  "),
                            preflight=lambda self, g, e: touched.append(1))
        self.assertEqual((status, touched), (1, []))

    def test_unexpected_exception_dumps_and_cleans_up(self):
        def boom(self, g, e):
            raise RuntimeError("boom")

        status, events = self.go(self.CREDS, preflight=boom)
        self.assertEqual((status, events), (1, ["dump", "cleanup"]))

    def test_transcript_marks_are_taken_before_the_kill(self):
        events = []

        class P:
            def lines(self):
                events.append("lines")
                return []

        def setup(self):
            self.players = {"a": P(), "b": P()}

        def kill(self):
            events.append("kill")
            raise env_recover.Failed("stop here")

        status, _ = self.go(self.CREDS, preflight=lambda self, g, e: None, setup_cli=lambda self: setup(self),
                            play_until_round=lambda self: 60, kill=kill)
        self.assertEqual(status, 1)
        self.assertEqual(events, ["lines", "lines", "kill"])


def shutil_rm(path):
    import shutil
    shutil.rmtree(path, ignore_errors=True)


class Transcripts(unittest.TestCase):

    def test_clean(self):
        self.assertEqual(env_recover.transcript_problems(["-- Connected to x", "Town Hall"], "A"), [])

    def test_already_live_and_despawn(self):
        self.assertTrue(env_recover.transcript_problems(["reason=already_live"], "A"))
        self.assertTrue(env_recover.transcript_problems(["Waiting for your previous session to end."], "A"))
        self.assertTrue(env_recover.transcript_problems(["Wren fades from the world."], "B"))


def yaml_rules(name):
    return "andara:\n- name: g\n  rules:\n  - alert: %s\n    expr: up\n" % name


if __name__ == "__main__":
    unittest.main()
