# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/topics.py picks its broker by environment (AW-INF-014 AC-3)."""
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import topics  # noqa: E402


class RpkPrefix(unittest.TestCase):
    def test_local_prefers_the_compose_broker(self):
        p = topics.rpk_prefix("local", compose_redpanda_running=True, host_rpk=True)
        self.assertEqual(p[:2], ["docker", "compose"])
        self.assertEqual(p[-2:], ["redpanda", "rpk"])

    def test_local_falls_back_to_host_rpk(self):
        self.assertEqual(topics.rpk_prefix("local", False, True), ["rpk"])

    def test_local_with_neither_has_no_broker(self):
        self.assertIsNone(topics.rpk_prefix("local", False, False))

    def test_cluster_env_never_reaches_compose(self):
        # The defect this story fixes: with `make up` running, dev went to the laptop.
        for env in ("dev", "prod"):
            with self.subTest(env=env):
                p = topics.rpk_prefix(env, compose_redpanda_running=True, host_rpk=True)
                self.assertNotIn("docker", p)
                self.assertEqual(p[:3], ["kubectl", "-n", "andara-" + env])
                self.assertIn(topics.CLUSTER_TOOLBOX, p)
                self.assertEqual(p[-3:], ["rpk", "-X", "brokers=" + topics.CLUSTER_BOOTSTRAP])


if __name__ == "__main__":
    unittest.main()


# --- AW-INF-018: apply aligns existing topics, and refuses what can delete data ---------------

import contextlib  # noqa: E402
import io  # noqa: E402
import json  # noqa: E402
import tempfile  # noqa: E402
import textwrap  # noqa: E402
from types import SimpleNamespace  # noqa: E402

DECL = textwrap.dedent("""\
    defaults:
      replication_factor:
        local: 1
        dev: 3
      config:
        min.insync.replicas:
          local: 1
          dev: 2
    topics:
      - name: log
        partitions: 4
        cleanup.policy: delete
        retention.ms: -1
      - name: events
        partitions: 4
        cleanup.policy: delete
        retention.ms: 1000
      - name: state
        partitions: 4
        cleanup.policy: compact
        min.compaction.lag.ms: 60000
    broker:
      assert:
        unclean.leader.election.enable: "false"
      local:
        some.local.setting: "1"
    """)


class FakeBroker:
    """rpk as far as topics.py uses it: list, describe, create, alter-config, cluster set."""

    def __init__(self, topics, refuse=None):
        # topics: {name: (partitions, {key: value})}. A key the broker "doesn't report" is
        # simply absent, as on Redpanda.
        self.topics = {n: (p, dict(c)) for n, (p, c) in topics.items()}
        self.refuse = refuse or set()   # {(topic, key)} the broker rejects
        self.calls = []

    def __call__(self, args):
        self.calls.append(list(args))
        ok = lambda out="": SimpleNamespace(returncode=0, stdout=out, stderr="")  # noqa: E731
        if args[:2] == ["topic", "list"]:
            return ok(json.dumps([{"name": n, "partitions": p} for n, (p, _) in self.topics.items()]))
        if args[:2] == ["topic", "describe"]:
            _, cfg = self.topics[args[2]]
            return ok("KEY  VALUE  SOURCE\n" + "\n".join(
                "%s  %s  DYNAMIC_TOPIC_CONFIG" % kv for kv in cfg.items()))
        if args[:2] == ["topic", "alter-config"]:
            name, (key, val) = args[2], args[4].split("=", 1)
            if (name, key) in self.refuse:
                return SimpleNamespace(returncode=1, stdout="", stderr="INVALID_CONFIG: no")
            self.topics[name][1][key] = val
            return ok()
        if args[:2] == ["topic", "create"]:
            self.topics[args[2]] = (int(args[4]), {})
            return ok()
        if args[:3] == ["cluster", "config", "set"]:
            return ok()
        raise AssertionError("unexpected rpk call %r" % (args,))

    def alters(self):
        return [c for c in self.calls if c[:2] == ["topic", "alter-config"]]


IN_LINE = {
    "log": (4, {"cleanup.policy": "delete", "retention.ms": "-1"}),
    "events": (4, {"cleanup.policy": "delete", "retention.ms": "1000"}),
    "state": (4, {"cleanup.policy": "compact"}),
}


def with_(**changes):
    t = {n: (p, dict(c)) for n, (p, c) in IN_LINE.items()}
    for name, cfg in changes.items():
        t[name][1].update(cfg)
    return t


class Apply(unittest.TestCase):
    def setUp(self):
        fd, self.decl = tempfile.mkstemp(suffix=".yaml")
        with os.fdopen(fd, "w") as f:
            f.write(DECL)

    def tearDown(self):
        os.unlink(self.decl)

    def apply(self, broker, allow=(), action="apply", env="local"):
        out, err = io.StringIO(), io.StringIO()
        try:
            with contextlib.redirect_stderr(err):   # die() writes to sys.stderr
                code = topics.run_action(action, env, list(allow), broker, decl=self.decl, out=out, err=err)
        except SystemExit as e:
            code = e.code
        return code, out.getvalue(), err.getvalue()

    def test_no_drift_makes_no_alter_call(self):
        b = FakeBroker(IN_LINE)
        code, out, _ = self.apply(b)
        self.assertEqual(code, 0)
        self.assertEqual(b.alters(), [])
        self.assertNotIn("altered", out)

    def test_widening_retention_is_applied_and_printed(self):
        b = FakeBroker(with_(events={"retention.ms": "500"}))
        code, out, _ = self.apply(b)
        self.assertEqual(code, 0, out)
        self.assertIn("topics: altered events retention.ms 500 -> 1000\n", out)
        self.assertEqual(b.topics["events"][1]["retention.ms"], "1000")
        code, _, _ = self.apply(b, action="diff")
        self.assertEqual(code, 0)

    def test_each_compared_key_is_altered(self):
        # min.insync.replicas on a broker that reports it (Kafka), cleanup.policy dropping a
        # policy, and a lag the broker reports differently.
        b = FakeBroker(with_(log={"min.insync.replicas": "3"},
                             events={"retention.ms": "500"},
                             state={"cleanup.policy": "compact,delete", "min.compaction.lag.ms": "0"}))
        code, out, err = self.apply(b)
        self.assertEqual(code, 0, err)
        altered = sorted((c[2], c[4]) for c in b.alters())
        self.assertEqual(altered, [
            ("events", "retention.ms=1000"),
            ("log", "min.insync.replicas=1"),
            ("state", "cleanup.policy=compact"),
            ("state", "min.compaction.lag.ms=60000"),
        ])

    def test_policy_sets_compare_as_sets(self):
        b = FakeBroker(with_(state={"cleanup.policy": "compact"}))
        self.assertTrue(topics.same("cleanup.policy", "delete,compact", "compact,delete"))
        code, _, _ = self.apply(b)
        self.assertEqual(code, 0)
        self.assertEqual(b.alters(), [])

    def test_unlimited_retention_orders_above_every_finite_value(self):
        self.assertTrue(topics.destructive("retention.ms", "-1", "31536000000"))
        self.assertFalse(topics.destructive("retention.ms", "31536000000", "-1"))
        self.assertFalse(topics.destructive("retention.ms", "1000", "2000"))

    def test_partition_drift_is_refused_before_any_alter(self):
        t = with_(events={"retention.ms": "500"})
        t["state"] = (8, t["state"][1])
        b = FakeBroker(t)
        code, _, err = self.apply(b)
        self.assertEqual(code, 1)
        self.assertIn("state has 8 partitions, declared 4; repartitioning is refused", err)
        self.assertEqual(b.alters(), [])

    def destructive_cases(self):
        return [
            ("events", {"retention.ms": "5000"}, "retention.ms 5000 -> 1000"),
            ("log", {"cleanup.policy": "compact"}, "cleanup.policy compact -> delete"),
            ("state", {"cleanup.policy": "delete"}, "cleanup.policy delete -> compact"),
        ]

    def test_a_destructive_change_is_refused_without_its_topic(self):
        for name, live, text in self.destructive_cases():
            with self.subTest(topic=name):
                # A harmless alter elsewhere must not happen either.
                b = FakeBroker(with_(**{name: live}, **({"events": {"retention.ms": "500"}} if name != "events" else {})))
                code, _, err = self.apply(b, allow=["some-other-topic"])
                self.assertEqual(code, 1)
                self.assertIn("topics: %s %s can delete data the broker cannot restore; "
                              "re-run with ALLOW_DATA_LOSS=%s to apply it" % (name, text, name), err)
                self.assertEqual(b.alters(), [])

    def test_a_destructive_change_is_applied_when_its_topic_is_named(self):
        for name, live, text in self.destructive_cases():
            with self.subTest(topic=name):
                b = FakeBroker(with_(**{name: live}))
                code, out, err = self.apply(b, allow=[name])
                self.assertEqual(code, 0, err)
                self.assertIn("topics: altered %s %s (data loss accepted)\n" % (name, text), out)

    def test_compact_to_compact_delete_is_destructive(self):
        self.assertTrue(topics.destructive("cleanup.policy", "compact", "compact,delete"))
        self.assertFalse(topics.destructive("cleanup.policy", "compact,delete", "compact"))

    def test_dropping_a_policy_needs_no_confirmation(self):
        b = FakeBroker(with_(state={"cleanup.policy": "compact,delete"}))
        code, out, err = self.apply(b)
        self.assertEqual(code, 0, err)
        self.assertIn("topics: altered state cleanup.policy compact,delete -> compact\n", out)

    def test_an_allowance_with_nothing_destructive_is_named_once(self):
        b = FakeBroker(IN_LINE)
        code, out, _ = self.apply(b, allow=["events"])
        self.assertEqual(code, 0)
        self.assertEqual(out.count("topics: ALLOW_DATA_LOSS names events, which has no destructive change"), 1)

    def test_unreported_keys_are_skipped_locally_and_said_once(self):
        b = FakeBroker(IN_LINE)
        code, out, _ = self.apply(b)
        self.assertEqual(code, 0)
        self.assertEqual(out.count(
            "topics: local broker does not report min.compaction.lag.ms, min.insync.replicas; skipped\n"), 1)

    def test_a_broker_refusal_names_what_already_applied(self):
        b = FakeBroker(with_(events={"retention.ms": "500"}, state={"min.compaction.lag.ms": "0"}),
                       refuse={("state", "min.compaction.lag.ms")})
        code, out, err = self.apply(b)
        self.assertEqual(code, 1)
        self.assertIn("the broker refused state min.compaction.lag.ms=60000: INVALID_CONFIG: no", err)
        self.assertIn("already applied: events retention.ms", err)

    def test_a_missing_topic_is_created_and_printed(self):
        t = dict(IN_LINE)
        del t["state"]
        b = FakeBroker(t)
        code, out, _ = self.apply(b)
        self.assertEqual(code, 0)
        self.assertIn("topics: created state\n", out)

    def test_diff_still_reports_without_altering(self):
        b = FakeBroker(with_(events={"retention.ms": "500"}))
        code, _, err = self.apply(b, action="diff")
        self.assertEqual(code, 1)
        self.assertIn("events: retention.ms is '500', declaration says '1000'", err)
        self.assertEqual(b.alters(), [])
