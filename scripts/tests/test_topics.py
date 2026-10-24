# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/topics.py picks its broker by environment (AW-INF-014 AC-3)."""
import os
import sys
import unittest
from unittest import mock

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
        fixture = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures", "kubeconfig")
        env = {k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"}
        p = mock.patch.dict(os.environ, dict(env, KUBECONFIG=fixture), clear=True)
        p.start()
        self.addCleanup(p.stop)
        for env, broker in (("dev", "compose"), ("prod", "compose"), ("local", "cluster")):
            with self.subTest(env=env, broker=broker):
                p = topics.rpk_prefix(env, compose_redpanda_running=True, host_rpk=True, broker=broker)
                self.assertNotIn("docker", p)
                # One shared broker for every environment (AW-INF-055), never andara-<env>.
                self.assertEqual(p[:5], ["kubectl", "--context", "kind-andara", "-n", "andara-shared"])
                self.assertIn(topics.CLUSTER_TOOLBOX, p)
                self.assertEqual(p[-3:], ["rpk", "-X", "brokers=" + topics.CLUSTER_BOOTSTRAP])

    def test_the_kafka_namespace_is_overridable(self):
        fixture = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures", "kubeconfig")
        env = {k: v for k, v in os.environ.items() if k != "ANDARA_KUBE_CONTEXT"}
        with mock.patch.dict(os.environ, dict(env, KUBECONFIG=fixture, ANDARA_KAFKA_NAMESPACE="ci-kafka"),
                             clear=True):
            self.assertEqual(topics.rpk_prefix("dev", False, False)[3:5], ["-n", "ci-kafka"])

    def test_the_compose_broker_is_only_locals(self):
        self.assertTrue(topics.redpanda("local", "compose"))
        self.assertFalse(topics.redpanda("local", "cluster"))
        self.assertFalse(topics.redpanda("dev", "compose"))


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
    environments:
      local:
        prefix: ""
      dev:
        prefix: ""
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

    def test_the_blob_record_limit_is_compared_and_never_data_loss(self):
        # AW-SRV-013: andara.content.blobs.v1 takes an 8 MiB blob only with max.message.bytes
        # raised. Raising it, lowering it, or setting one the broker didn't report deletes nothing.
        self.assertIn("max.message.bytes", topics.COMPARED)
        for have, want in (("1048588", "9437184"), ("9437184", "1048576"), (None, "9437184")):
            self.assertFalse(topics.destructive("max.message.bytes", have, want), (have, want))

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
            ("state", {"min.compaction.lag.ms": "120000"}, "min.compaction.lag.ms 120000 -> 60000"),
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

    def test_a_longer_compaction_lag_needs_no_confirmation(self):
        self.assertFalse(topics.destructive("min.compaction.lag.ms", "0", "60000"))
        self.assertTrue(topics.destructive("min.compaction.lag.ms", "60000", "0"))

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


# --- AW-INF-055: one shared broker, each environment's topics under its prefix ------------------

PREFIXED = textwrap.dedent("""\
    environments:
      local:
        prefix: local
        retention.bytes: 67108864
        retention.ms: 604800000
        segment.bytes: 16777216
        segment.ms: 86400000
      dev:
        prefix: dev
      prod:
        prefix: ""
    defaults:
      replication_factor:
        local: 1
        dev: 3
        prod: 3
      config:
        min.insync.replicas:
          local: 1
          dev: 2
          prod: 2
    topics:
      - name: andara.log.v1
        partitions: 4
        cleanup.policy: delete
        retention.ms: -1
      - name: andara.kv.v1
        partitions: 2
        cleanup.policy: compact
    broker:
      assert:
        unclean.leader.election.enable: "false"
      local:
        some.local.setting: "1"
    principals:
      - name: andara-server
        topics:
          andara.log.v1: [Read, Write]
    """)

REAL_DECL = topics.DECL


class Prefixed(unittest.TestCase):
    def setUp(self):
        fd, self.decl = tempfile.mkstemp(suffix=".yaml")
        with os.fdopen(fd, "w") as f:
            f.write(PREFIXED)
        self.addCleanup(os.unlink, self.decl)

    def apply(self, broker, env, rp, action="apply", allow=()):
        out, err = io.StringIO(), io.StringIO()
        try:
            with contextlib.redirect_stderr(err):
                code = topics.run_action(action, env, list(allow), broker, decl=self.decl, out=out,
                                         err=err, rp=rp)
        except SystemExit as e:
            code = e.code
        return code, out.getvalue(), err.getvalue()

    def creates(self, b):
        return {c[2]: c for c in b.calls if c[:2] == ["topic", "create"]}

    def test_names_are_prefix_dot_name_and_prod_is_bare(self):
        self.assertEqual(topics.topic_name("dev", "andara.commands.v1"), "dev.andara.commands.v1")
        self.assertEqual(topics.topic_name("", "andara.commands.v1"), "andara.commands.v1")

    def test_dev_creates_only_dev_topics_with_rf_3_and_isr_2(self):
        b = FakeBroker({"local.andara.log.v1": (4, {"cleanup.policy": "delete"})})
        code, out, _ = self.apply(b, "dev", rp=False)
        self.assertEqual(code, 0, out)
        made = self.creates(b)
        self.assertEqual(sorted(made), ["dev.andara.kv.v1", "dev.andara.log.v1"])
        log = made["dev.andara.log.v1"]
        self.assertEqual(log[3:7], ["-p", "4", "-r", "3"])
        self.assertIn("min.insync.replicas=2", log)
        self.assertIn("retention.ms=-1", log)
        self.assertFalse(any("retention.bytes" in a for a in log), "dev keeps its declared retention")
        # The other environment's topic is never read, let alone altered.
        self.assertFalse([c for c in b.calls if "local.andara.log.v1" in c])
        self.assertIn("prefix=dev", out)

    def test_local_on_the_cluster_is_bounded_and_kafka(self):
        b = FakeBroker({})
        code, out, _ = self.apply(b, "local", rp=False)
        self.assertEqual(code, 0, out)
        made = self.creates(b)
        self.assertEqual(sorted(made), ["local.andara.kv.v1", "local.andara.log.v1"])
        for name, cmd in made.items():
            with self.subTest(topic=name):
                self.assertEqual(cmd[5:7], ["-r", "1"])
                self.assertIn("min.insync.replicas=1", cmd)
                # Every local topic rolls segments at 16 MiB and at most a day (the contract's
                # segment.ms <= 86400000), or no bound touches the active segment.
                self.assertIn("segment.bytes=16777216", cmd)
                self.assertIn("segment.ms=86400000", cmd)
        log, kv = made["local.andara.log.v1"], made["local.andara.kv.v1"]
        # local's bound wins over the delete topic's own retention (-1 on the log) …
        self.assertIn("retention.bytes=67108864", log)
        self.assertIn("retention.ms=604800000", log)
        # … and a compacted topic declares no retention (broker-contract.md, `local.` bounds).
        self.assertFalse([a for a in kv if a.startswith("retention.")], kv)
        # Kafka, not Redpanda: no Redpanda cluster setting is written to the shared broker.
        self.assertFalse([c for c in b.calls if c[:3] == ["cluster", "config", "set"]])

    def test_local_on_kafka_asserts_the_broker_settings(self):
        cfg = {"cleanup.policy": "delete", "retention.ms": "604800000", "retention.bytes": "67108864",
               "min.insync.replicas": "1"}
        b = FakeBroker({"local.andara.log.v1": (4, dict(cfg)),
                        "local.andara.kv.v1": (2, dict(cfg, **{"cleanup.policy": "compact"}))})
        code, _, err = self.apply(b, "local", rp=False, action="diff")
        self.assertEqual(code, 1)
        self.assertIn("unclean.leader.election.enable is not reported", err)

    def test_an_existing_local_topic_at_kafkas_segment_defaults_is_drift_and_aligned(self):
        # Without segment.* in COMPARED, a live topic left at 1 GiB / 7 days would never be bounded.
        cfg = {"cleanup.policy": "delete", "retention.ms": "604800000", "retention.bytes": "67108864",
               "min.insync.replicas": "1", "unclean.leader.election.enable": "false",
               "segment.bytes": "1073741824", "segment.ms": "604800000"}
        kv = dict(cfg, **{"cleanup.policy": "compact"})
        for k in ("retention.ms", "retention.bytes"):
            del kv[k]
        live = {"local.andara.log.v1": (4, cfg), "local.andara.kv.v1": (2, kv)}
        code, _, err = self.apply(FakeBroker(live), "local", rp=False, action="diff")
        self.assertEqual(code, 1)
        for key, have, want in (("segment.bytes", "1073741824", "16777216"), ("segment.ms", "604800000", "86400000")):
            self.assertIn("local.andara.log.v1: %s is '%s', declaration says '%s'" % (key, have, want), err)
        b = FakeBroker(live)
        code, out, err = self.apply(b, "local", rp=False)
        self.assertEqual(code, 0, err)
        altered = {(c[2], c[4].split("=")[0]) for c in b.alters()}
        for topic in ("local.andara.log.v1", "local.andara.kv.v1"):
            self.assertIn((topic, "segment.bytes"), altered)
            self.assertIn((topic, "segment.ms"), altered)
        self.assertNotIn("data loss accepted", out)

    def test_local_on_compose_still_writes_redpandas_settings(self):
        b = FakeBroker({})
        code, _, _ = self.apply(b, "local", rp=True)
        self.assertEqual(code, 0)
        self.assertIn(["cluster", "config", "set", "some.local.setting", "1"], b.calls)

    def test_a_lower_retention_bytes_is_data_loss(self):
        self.assertTrue(topics.destructive("retention.bytes", "-1", "67108864"))
        self.assertTrue(topics.destructive("retention.bytes", None, "67108864"))
        self.assertFalse(topics.destructive("retention.bytes", "67108864", "-1"))
        cfg = {"cleanup.policy": "delete", "retention.ms": "604800000", "retention.bytes": "-1",
               "min.insync.replicas": "1"}
        b = FakeBroker({"local.andara.log.v1": (4, dict(cfg)),
                        "local.andara.kv.v1": (2, dict(cfg, **{"cleanup.policy": "compact"}))})
        code, _, err = self.apply(b, "local", rp=True)
        self.assertEqual(code, 1)
        self.assertIn("re-run with ALLOW_DATA_LOSS=local.andara.log.v1", err)
        self.assertEqual(b.alters(), [])

    def test_an_environment_without_a_prefix_entry_is_refused(self):
        b = FakeBroker({})
        code, _, err = self.apply(b, "staging", rp=False)
        self.assertEqual(code, 1)
        self.assertIn("no environment 'staging'", err)
        self.assertEqual(b.calls, [])

    def test_the_real_declaration_prefixes_local_and_dev(self):
        envs = topics.parse_environments(REAL_DECL)
        self.assertEqual(envs["local"]["prefix"], "local")
        self.assertEqual(envs["dev"]["prefix"], "dev")
        self.assertEqual(envs["prod"]["prefix"], "")
        self.assertEqual(envs["local"]["config"],
                         {"retention.bytes": "67108864", "retention.ms": "604800000",
                          "segment.bytes": "16777216", "segment.ms": "86400000"})
        self.assertEqual(envs["dev"]["config"], {})


class Usage(unittest.TestCase):
    def main(self, *argv):
        err = io.StringIO()
        with mock.patch.object(sys, "argv", ["topics.py"] + list(argv)), \
                mock.patch.object(topics, "rpk_runner", side_effect=AssertionError("reached a broker")), \
                contextlib.redirect_stderr(err):
            with self.assertRaises(SystemExit) as e:
                topics.main()
        return e.exception.code, err.getvalue()

    def test_broker_with_dev_is_a_usage_error(self):
        code, err = self.main("apply", "--env", "dev", "--broker", "cluster")
        self.assertEqual(code, 2)
        self.assertIn("only for local", err)

    def test_prod_is_refused_before_any_broker(self):
        # Its unprefixed names would be strays on the shared broker (ADR-0013 decision 10).
        code, err = self.main("apply", "--env", "prod")
        self.assertEqual(code, 2)
        self.assertIn("prod's Kafka is not on this cluster", err)

    def test_the_reserved_groups_are_the_environments(self):
        groups = topics.consumer_groups("dev")
        self.assertIn("andara-sim-dev", groups)
        self.assertIn("andara-projector-state-dev", groups)
        self.assertTrue(all(g.endswith("-dev") for g in groups), groups)

    def test_an_unknown_broker_is_a_usage_error(self):
        code, err = self.main("apply", "--env", "local", "--broker", "laptop")
        self.assertEqual(code, 2)
        self.assertIn("compose or cluster", err)


class Platform(unittest.TestCase):
    """AW-INF-057: `topics.py apply --platform`, the platform topics on the shared broker (literal
    names, outside every prefix), from the real declaration."""

    def run_platform(self, broker, action="apply", decl=topics.DECL):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch("sys.exit", side_effect=SystemExit) as _:
            try:
                code = topics.run_platform(action, broker, decl=decl, out=out, err=err)
            except SystemExit as e:
                code = e.code if e.code is not None else 1
        return code, out.getvalue(), err.getvalue()

    def test_the_declaration_has_the_registry_topic(self):
        got = {t["name"]: t for t in topics.parse_platform_topics(topics.DECL)}
        self.assertEqual(got["_schemas"]["partitions"], "1")
        self.assertEqual(got["_schemas"]["replication_factor"], "3")
        self.assertEqual(got["_schemas"]["cleanup.policy"], "compact")
        self.assertNotIn("why", got["_schemas"])  # prose is skipped

    def test_a_missing_topic_is_created_literally_with_its_settings(self):
        b = FakeBroker({})
        code, out, _ = self.run_platform(b)
        self.assertEqual(code, 0)
        create = [c for c in b.calls if c[:2] == ["topic", "create"]][0]
        self.assertEqual(create[:7], ["topic", "create", "_schemas", "-p", "1", "-r", "3"])
        self.assertIn("cleanup.policy=compact", create)
        self.assertIn("min.insync.replicas=2", create)
        self.assertIn("created _schemas (platform)", out)

    def test_a_matching_topic_is_left(self):
        b = FakeBroker({"_schemas": (1, {"cleanup.policy": "compact", "min.insync.replicas": "2"})})
        code, out, _ = self.run_platform(b)
        self.assertEqual(code, 0)
        self.assertFalse([c for c in b.calls if c[1] in ("create", "alter-config")])
        self.assertIn("1 platform topic(s) match", out)

    def test_a_drifted_setting_is_altered_on_apply_and_reported_on_diff(self):
        b = FakeBroker({"_schemas": (1, {"cleanup.policy": "compact", "min.insync.replicas": "1"})})
        code, _, err = self.run_platform(b, "diff")
        self.assertEqual(code, 1)
        self.assertIn("_schemas: min.insync.replicas is '1', declaration says '2'", err)
        code, out, _ = self.run_platform(b)
        self.assertEqual((code, b.topics["_schemas"][1]["min.insync.replicas"]), (0, "2"))

    def test_adding_delete_to_the_compacted_topic_is_refused(self):
        # Anchored on the platform entry: the first `cleanup.policy: compact` in the file is an
        # environment topic's.
        decl = self.decl_with("    cleanup.policy: compact\n    min.insync.replicas: 2\n    owner: AW-INF-057",
                              "    cleanup.policy: delete\n    min.insync.replicas: 2\n    owner: AW-INF-057")
        b = FakeBroker({"_schemas": (1, {"cleanup.policy": "compact", "min.insync.replicas": "2"})})
        code, _, err = self.run_platform(b, decl=decl)
        self.assertEqual(code, 1)
        self.assertIn("_schemas cleanup.policy compact -> delete can delete data", err)
        self.assertFalse(b.alters())

    def test_repartitioning_is_refused(self):
        code, _, _ = self.run_platform(FakeBroker({"_schemas": (3, {"cleanup.policy": "compact"})}))
        self.assertEqual(code, 1)

    def test_a_platform_topic_inside_a_prefix_or_andara_is_refused(self):
        for name in ("dev.schemas", "andara.schemas.v1"):
            with self.subTest(name=name):
                code, _, _ = self.run_platform(FakeBroker({}), decl=self.decl_with("- name: _schemas", "- name: " + name))
                self.assertEqual(code, 1)

    def decl_with(self, old, new):
        import tempfile
        with open(topics.DECL) as f:
            text = f.read()
        assert old in text
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d)
        path = os.path.join(d, "topics.yaml")
        with open(path, "w") as f:
            f.write(text.replace(old, new, 1))
        return path
