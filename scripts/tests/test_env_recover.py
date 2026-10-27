# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make env-recover`'s guard, kill mechanism and assertions (AW-INF-034), on the backend AW-INF-069 selects."""
import contextlib
import io
import json
import os
import subprocess
import sys
import time
import types
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import env_recover  # noqa: E402
import gcx  # noqa: E402
import rule_backend  # noqa: E402


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


class Rebound(unittest.TestCase):

    def test_holds(self):
        self.assertIsNone(env_recover.check_rebound([], vec(2)))
        self.assertIsNone(env_recover.check_rebound(vec(0), vec(3)))

    def test_a_despawn_fails(self):
        self.assertIn("character_despawned", env_recover.check_rebound(vec(1), vec(2)))

    def test_too_few_or_no_reconnects_fail(self):
        self.assertIn("reconnected", env_recover.check_rebound([], vec(1)))
        self.assertIn("absent", env_recover.check_rebound([], []))


class Inconclusive(unittest.TestCase):

    def test_newer_round_is_inconclusive(self):
        env_recover.check_round_unchanged(60, 60)
        with self.assertRaisesRegex(env_recover.Failed, "inconclusive"):
            env_recover.check_round_unchanged(90, 60)

    def _preflight(self, state):
        run = env_recover.Run("dev", "andara-dev", {})
        self.addCleanup(run.cleanup)
        run.pod = lambda: {"ready": True}
        g = types.SimpleNamespace(sample=lambda: rule_backend.Sample(0, state, state.title()))
        real = env_recover.shutil.which
        env_recover.shutil.which = lambda n: "/bin/" + n
        run.cli = "/bin/true"
        try:
            run.preflight(g)
        finally:
            env_recover.shutil.which = real

    def test_alert_already_firing_or_pending_is_inconclusive(self):
        for state in ("firing", "pending"):
            with self.assertRaisesRegex(env_recover.Failed, "inconclusive.*already %s" % state):
                self._preflight(state)

    def test_preflight_passes_when_quiet(self):
        self._preflight("normal")

    def test_newest_round_is_the_first_complete_row(self):
        table = "TICK AGE ZONES COMPLETE\n90 1s 2 false\n60 61s 2 true\n30 2m 2 true\n"
        self.assertEqual(env_recover.newest_round(table), 60)
        self.assertEqual(env_recover.newest_round("TICK AGE ZONES COMPLETE\n"), 0)


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

    def test_a_restart_after_ready_fails_the_run(self):
        run = self.run_()
        run.uid = "u1"
        run.pod = lambda: env_recover.parse_pod(pod_doc(restarts=2, exit_code=137))
        with self.assertRaisesRegex(env_recover.Failed, "restarted again"):
            run.assert_no_further_restart(1)
        run.pod = lambda: env_recover.parse_pod(pod_doc(restarts=1, exit_code=137))
        run.assert_no_further_restart(1)

    def test_kill_records_the_uid_and_an_unrecorded_one_fails_closed(self):
        run = self.run_()
        run.rto = 120
        states = iter([pod_doc(), pod_doc(restarts=1, exit_code=137, ready=False), pod_doc(restarts=1, exit_code=137)])
        run.pod = lambda: env_recover.parse_pod(next(states))
        run.sh = lambda args: types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 4242}}), stderr="")
        with mock.patch.object(env_recover.time, "sleep", lambda s: None):
            run.kill()
        self.assertEqual(run.uid, "u1")
        run.uid = None
        run.pod = lambda: env_recover.parse_pod(pod_doc(restarts=1, exit_code=137))
        with self.assertRaisesRegex(env_recover.Failed, "no pod UID"):
            run.assert_no_further_restart(1)

    def test_a_reschedule_after_ready_fails_the_run(self):
        run = self.run_()
        run.uid = "u1"
        run.pod = lambda: env_recover.parse_pod(pod_doc(uid="u2", restarts=1, exit_code=137))
        with self.assertRaisesRegex(env_recover.Failed, "rescheduled"):
            run.assert_no_further_restart(1)

    def test_rebound_counters_query_both_series_and_retry_to_the_deadline(self):
        run = self.run_()
        queries = []
        answers = iter([[], [], [], [({"x": 1}, 2.0)]])

        def instant(q):
            queries.append(q)
            return [{"metric": {}, "value": [0, str(v)]} for _, v in next(answers)]

        g = types.SimpleNamespace(instant=instant, matchers='environment="dev",namespace="andara-dev"')
        env_recover.time.sleep, real_sleep = lambda s: None, env_recover.time.sleep
        self.addCleanup(setattr, env_recover.time, "sleep", real_sleep)
        # despawn absent (empty), reconnected absent on the first pass, then 2.
        run.rebound_counters(g)
        self.assertEqual(queries[0], 'sum(andara_events_emitted_total{type="character_despawned",'
                                     'environment="dev",namespace="andara-dev"})')
        self.assertEqual(queries[1], 'sum(andara_linkdead_outcomes_total{outcome="reconnected",'
                                     'environment="dev",namespace="andara-dev"})')
        self.assertEqual(len(queries), 4)

    def test_rebound_counters_fail_at_the_deadline(self):
        run = self.run_()
        g = types.SimpleNamespace(instant=lambda q: [], matchers='namespace="andara-dev"')
        real = env_recover.METRIC_WAIT
        env_recover.METRIC_WAIT = 0
        self.addCleanup(setattr, env_recover, "METRIC_WAIT", real)
        with self.assertRaisesRegex(env_recover.Failed, "within 0s"):
            run.rebound_counters(g)

    def _players(self, run, script):
        """Players whose lines() follow `script` (a list of line lists, the last one repeating)."""
        class P:
            def __init__(self, calls):
                self.calls, self.n = calls, 0

            def lines(self):
                out = self.calls[min(self.n, len(self.calls) - 1)]
                self.n += 1
                return out() if callable(out) else out

            def send(self, text):
                pass

            def wait_for(self, pattern, after=0, secs=20):
                return True

        if isinstance(script, dict):
            run.players = {"a": P(script["a"]), "b": P(script["b"])}
        else:
            run.players = {"a": P(script), "b": P(script)}

    def test_rebind_accepts_a_reconnect_inside_the_window_and_rejects_one_after_it(self):
        run = self.run_()
        self._players(run, [["-- Connected to x as a, playing A"]])
        run.rebind({"A": 0, "B": 0}, time.time() - 50)
        # The line is already there but the window closed 440 s ago: a scan after the deadline proves nothing.
        with self.assertRaisesRegex(env_recover.Failed, "did not reconnect within 60s"):
            run.rebind({"A": 0, "B": 0}, time.time() - 500)

    def test_rebind_rejects_a_line_that_arrives_after_the_deadline(self):
        run = self.run_()
        self._players(run, [[], [], ["-- Connected to x as a, playing A"]])
        real_sleep = time.sleep
        with mock.patch.object(env_recover.time, "sleep", lambda s: real_sleep(0.06)):
            with self.assertRaisesRegex(env_recover.Failed, "did not reconnect within 60s"):
                run.rebind({"A": 0, "B": 0}, time.time() - env_recover.RECONNECT_WAIT + 0.05)

    def test_rebind_needs_both_players_and_names_the_one_missing(self):
        ok = ["-- Connected to x as a, playing A"]
        for script, who in (({"a": [ok], "b": [[]]}, "B"), ({"a": [[]], "b": [ok]}, "A")):
            run = self.run_()
            self._players(run, script)
            with mock.patch.object(env_recover.time, "sleep", lambda s: None):
                with self.assertRaisesRegex(env_recover.Failed, "^%s's play did not reconnect" % who):
                    run.rebind({"A": 0, "B": 0}, time.time() - env_recover.RECONNECT_WAIT + 0.2)

    def test_rebind_ignores_a_connect_line_from_before_the_mark(self):
        run = self.run_()
        self._players(run, [["-- Connected to x as a, playing A", "old"]])
        with mock.patch.object(env_recover.time, "sleep", lambda s: None):
            with self.assertRaisesRegex(env_recover.Failed, "did not reconnect"):
                run.rebind({"A": 1, "B": 1}, time.time() - env_recover.RECONNECT_WAIT + 0.2)

    def test_rebind_stamps_after_the_read_and_accepts_a_stamp_at_the_deadline(self):
        line = ["-- Connected to x as a, playing A"]
        for advance, ok in ((0.0, True), (0.5, False)):
            run = self.run_()
            now = {"t": 100.0}

            def read(advance=advance, now=now):
                now["t"] += advance  # the read takes `advance` s; the stamp comes after it
                return line

            self._players(run, [read])
            deadline_ready = 100.0 - env_recover.RECONNECT_WAIT
            with mock.patch.object(env_recover.time, "time", lambda: now["t"]), \
                    mock.patch.object(env_recover.time, "sleep", lambda s: now.update(t=now["t"] + 1)):
                if ok:
                    run.rebind({"A": 0, "B": 0}, deadline_ready)
                else:
                    with self.assertRaisesRegex(env_recover.Failed, "did not reconnect"):
                        run.rebind({"A": 0, "B": 0}, deadline_ready)

    def test_a_ready_seen_after_the_rto_fails(self):
        run = self.run_()
        run.rto = 0
        states = iter([pod_doc(), pod_doc(restarts=1, exit_code=137, ready=False), pod_doc(restarts=1, exit_code=137)])
        run.pod = lambda: env_recover.parse_pod(next(states))
        run.sh = lambda args: types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 4242}}), stderr="")
        real_pod = run.pod

        def slow():
            time.sleep(0.01)
            return real_pod()

        run.pod = slow
        with self.assertRaisesRegex(env_recover.Failed, "not Ready within 0s"):
            run.kill()

    def test_kill_to_ready_is_the_instant_the_poll_saw_ready(self):
        run = self.run_()
        run.rto = 120
        states = iter([pod_doc(), pod_doc(restarts=1, exit_code=137, ready=False), pod_doc(restarts=1, exit_code=137)])
        run.pod = lambda: env_recover.parse_pod(next(states))
        run.sh = lambda args: types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 4242}}), stderr="")
        clock = iter([100.0, 105.0])  # t_kill, then the poll that saw Ready; any later read is 999
        with mock.patch.object(env_recover.time, "time", lambda: next(clock, 999.0)):
            _, kill_to_ready, _, _ = run.kill()
        self.assertEqual(kill_to_ready, 5.0)

    def test_a_restart_seen_after_the_rto_is_reported_as_a_restart(self):
        run = self.run_()
        run.rto = 0
        states = iter([pod_doc(), pod_doc(restarts=1, exit_code=137, ready=False), pod_doc(restarts=2, exit_code=137)])
        real_pod = lambda: env_recover.parse_pod(next(states))

        def slow():
            time.sleep(0.01)
            return real_pod()

        run.pod = slow
        run.sh = lambda args: types.SimpleNamespace(returncode=0, stdout=json.dumps({"info": {"pid": 4242}}), stderr="")
        with self.assertRaisesRegex(env_recover.Failed, "a further restart"):
            run.kill()

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


class FakeWatch:
    """rule_backend.Watch's surface, its record given."""

    def __init__(self, events, samples=(), problems=()):
        self.events, self.samples, self._problems = events, list(samples), list(problems)
        self.started = self.stopped = None

    def start(self):
        self.events.append("watch")
        self.started = 0.0

    def stop(self):
        self.events.append("watch_stop")
        self.stopped = 60.0
        return self

    def problems(self):
        return self._problems

    def firing(self):
        return [x for x in self.samples if x.state == "firing"]


class Main(unittest.TestCase):
    CREDS = {"ANDARA_BOX_ENV": "/nonexistent"}

    def go(self, environ, events=None, watch=None, exists=True, **overrides):
        """main() with the backend, the watch and the cluster-touching pieces stubbed; returns (status, events)."""
        events = [] if events is None else events

        class FakeRun(env_recover.Run):
            def dump(self):
                events.append("dump")

            def cleanup(self):
                events.append("cleanup")
                shutil_rm(self.work)

        for name, fn in overrides.items():
            setattr(FakeRun, name, fn)

        def confirm():
            events.append("confirm_exists")
            if not exists:
                raise rule_backend.Inconclusive("RecoveryStateMismatch is not defined in grafana solo7dev")

        g = types.SimpleNamespace(header=lambda: ["backend: grafana solo7dev"], settle=0, confirm_exists=confirm,
                                  matchers='environment="dev",namespace="andara-dev"', __str__=lambda: "grafana solo7dev")
        w = watch or FakeWatch(events)
        with mock.patch.object(env_recover, "Run", FakeRun), mock.patch.object(env_recover, "backend", lambda env: g), \
                mock.patch.object(env_recover.rule_backend, "Watch", lambda b, say: w), \
                mock.patch.dict(os.environ, environ, clear=True):
            status = env_recover.main(["dev", "andara-dev"])
        return status, events

    def test_bad_rto_fails_before_the_cluster(self):
        for rto in ("abc", "1.5", "0", ""):
            touched = []
            status, _ = self.go(dict(self.CREDS, ENV_RECOVER_RTO=rto),
                                preflight=lambda self, g: touched.append(1))
            self.assertEqual((status, touched), (1, []), rto)

    def test_an_unset_credential_exits_3_before_any_gcx_call(self):
        """AC-6."""
        calls = []
        env = {"GRAFANA_SOLO7DEV_READ_TOKEN": "  ", "GRAFANA_SOLO7_READ_TOKEN": "t",
               "GRAFANA_CLOUD_PROM_URL": "https://p.example/api/prom", "GRAFANA_CLOUD_PROM_USER": "1",
               "GRAFANA_CLOUD_READ_TOKEN": "t", "ANDARA_BOX_ENV": "/nonexistent"}
        err = io.StringIO()
        with mock.patch.object(gcx, "run", lambda *a, **k: calls.append(a)), mock.patch.dict(os.environ, env, clear=True), \
                contextlib.redirect_stderr(err):
            status = env_recover.main(["dev", "andara-dev"])
        self.assertEqual((status, calls), (3, []))
        self.assertIn("GRAFANA_SOLO7DEV_READ_TOKEN", err.getvalue())

    def test_an_inconclusive_selection_exits_1_before_the_cluster(self):
        """AC-2: paused, live in both, unhealthy: rule_backend.select's message, exit 1, nothing touched."""
        def select(*a, **k):
            raise rule_backend.Inconclusive("RecoveryStateMismatch is live in neither backend: grafana solo7dev: paused")

        err = io.StringIO()
        with mock.patch.object(env_recover.rule_backend, "credentials", lambda env, e=None: ("solo7dev", {}, {})), \
                mock.patch.object(gcx, "write_config", lambda e=None: []), \
                mock.patch.object(env_recover.rule_backend, "select", select), \
                mock.patch.object(env_recover, "Run", lambda *a: self.fail("reached the run")), \
                contextlib.redirect_stderr(err):
            status = env_recover.main(["dev", "andara-dev"])
        self.assertEqual(status, 1)
        self.assertIn("inconclusive: RecoveryStateMismatch is live in neither backend: grafana solo7dev: paused",
                      err.getvalue())

    def test_unexpected_exception_dumps_and_cleans_up(self):
        def boom(self, g):
            raise RuntimeError("boom")

        status, events = self.go(self.CREDS, preflight=boom)
        self.assertEqual((status, events), (1, ["dump", "cleanup"]))

    def test_rebind_gets_the_ready_instant(self):
        got = []

        class P:
            def lines(self):
                return []

        def setup(self):
            self.players = {"a": P(), "b": P()}

        def rebind(self, marks, t_ready):
            got.append(t_ready)
            raise env_recover.Failed("stop")

        self.go(self.CREDS, preflight=lambda self, g: None, setup_cli=setup, play_until_round=lambda self: 60,
                kill=lambda self: (1000.0, 5.0, 0, 1), rebind=rebind)
        self.assertEqual(got, [1005.0])

    def test_reconnect_is_awaited_before_the_metrics_poll(self):
        events = []

        class P:
            def lines(self):
                return []

        def setup(self):
            self.players = {"a": P(), "b": P()}

        def metrics(self, g, r, t_kill):
            events.append("metrics")
            raise env_recover.Failed("stop")

        status, _ = self.go(
            self.CREDS, preflight=lambda self, g: None, setup_cli=setup, play_until_round=lambda self: 60,
            kill=lambda self: (1000.0, 5.0, 0, 1), rebind=lambda self, marks, t_ready: events.append("rebind"),
            recovered_metrics=metrics)
        self.assertEqual((status, events), (1, ["rebind", "metrics"]))

    def test_the_end_of_run_checks_run_in_order_and_fail_the_run(self):
        full = ["watch", "rebind", "recovered_metrics", "rebound_counters", "rebound_counters", "watch_stop",
                "confirm_exists", "assert_no_further_restart"]
        # (name that fails, which call of it, events seen up to and including the failure)
        for failing, nth, upto in (("rebound_counters", 1, 4), ("rebound_counters", 2, 5),
                                   ("assert_no_further_restart", 1, 8)):
            events = []

            class P:
                def lines(self):
                    return []

            def setup(self):
                self.players = {"a": P(), "b": P()}

            def rec(name):
                def f(self, *a):
                    events.append(name)
                    if name == failing and events.count(name) == nth:
                        raise env_recover.Failed(name)
                return f

            with mock.patch.object(env_recover.time, "sleep", lambda s: None):
                status, _ = self.go(
                    self.CREDS, events=events, preflight=lambda self, g: None, setup_cli=setup,
                    play_until_round=lambda self: 60, kill=lambda self: (1000.0, 5.0, 0, 1),
                    rebind=rec("rebind"), recovered_metrics=rec("recovered_metrics"),
                    trace_id=lambda self, since: "", rebound_counters=rec("rebound_counters"),
                    assert_no_further_restart=rec("assert_no_further_restart"))
            self.assertEqual(status, 1, (failing, nth))
            # A failure before the watch is stopped stops it in the run's finally.
            seen = [e for e in events if e not in ("dump", "cleanup")]
            self.assertEqual(seen[:upto], full[:upto], (failing, nth))

    def test_a_restart_during_cleanup_fails_the_run(self):
        for fail_on, expect in ((None, 0), (2, 1)):
            events = []

            class P:
                def lines(self):
                    return []

            def setup(self):
                self.players = {"a": P(), "b": P()}

            def check(self, *a):
                events.append("assert_no_further_restart")
                if events.count("assert_no_further_restart") == fail_on:
                    raise env_recover.Failed("restarted again")

            with mock.patch.object(env_recover.time, "sleep", lambda s: None):
                status, _ = self.go(
                    self.CREDS, events=events, preflight=lambda self, g: None, setup_cli=setup,
                    play_until_round=lambda self: 60, kill=lambda self: (1000.0, 5.0, 0, 1),
                    rebind=lambda self, *a: None, recovered_metrics=lambda self, *a: None,
                    trace_id=lambda self, since: "", rebound_counters=lambda self, g: None,
                    assert_no_further_restart=check)
            self.assertEqual(status, expect, fail_on)
            self.assertEqual(events[-2:], ["cleanup", "assert_no_further_restart"], fail_on)

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

        status, _ = self.go(self.CREDS, preflight=lambda self, g: None, setup_cli=lambda self: setup(self),
                            play_until_round=lambda self: 60, kill=kill)
        self.assertEqual(status, 1)
        self.assertEqual([e for e in events if not e.startswith("watch")], ["lines", "lines", "kill"])


class Quiet(unittest.TestCase):
    """AC-7 over the run's own record, and "none" only once the rule exists in the backend."""

    def ok_run(self, watch, exists=True):
        class P:
            def lines(self):
                return []

        def setup(self):
            self.players = {"a": P(), "b": P()}

        err = io.StringIO()
        with mock.patch.object(env_recover.time, "sleep", lambda s: None), contextlib.redirect_stderr(err):
            status, events = Main.go(Main(), Main.CREDS, watch=watch, exists=exists,
                                     preflight=lambda self, g: None, setup_cli=setup, play_until_round=lambda self: 60,
                                     kill=lambda self: (1000.0, 5.0, 0, 1), rebind=lambda self, *a: None,
                                     recovered_metrics=lambda self, *a: None, trace_id=lambda self, since: "",
                                     rebound_counters=lambda self, g: None, assert_no_further_restart=lambda self, *a: None)
        return status, err.getvalue()

    def sample(self, state):
        return rule_backend.Sample(1000.0, state, {"firing": "Alerting"}.get(state, state.title()))

    def test_a_quiet_record_passes(self):
        status, err = self.ok_run(FakeWatch([], [self.sample("normal"), self.sample("pending")]))
        self.assertEqual(status, 0, err)

    def test_an_empty_record_passes_once_the_rule_exists(self):
        self.assertEqual(self.ok_run(FakeWatch([]))[0], 0)

    def test_an_empty_record_for_a_rule_that_is_gone_is_inconclusive(self):
        status, err = self.ok_run(FakeWatch([]), exists=False)
        self.assertEqual(status, 1)
        self.assertIn("inconclusive: RecoveryStateMismatch is not defined", err)

    def test_a_firing_sample_fails(self):
        status, err = self.ok_run(FakeWatch([], [self.sample("normal"), self.sample("firing")]))
        self.assertEqual(status, 1)
        self.assertIn("RecoveryStateMismatch fired during the run", err)
        self.assertIn("gcx: Alerting", err)

    def test_a_gap_in_the_record_is_inconclusive(self):
        status, err = self.ok_run(FakeWatch([], [self.sample("normal")], problems=["no successful poll from a to b"]))
        self.assertEqual(status, 1)
        self.assertIn("inconclusive: no successful poll", err)


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
