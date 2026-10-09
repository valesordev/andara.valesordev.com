# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make env-recover`'s guard, kill mechanism and assertions (AW-INF-034)."""
import json
import os
import subprocess
import sys
import types
import unittest

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
               types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 1}}), stderr="")]
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
