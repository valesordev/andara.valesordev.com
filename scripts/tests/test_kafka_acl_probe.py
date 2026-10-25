# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-056: `make kafka-acl-probe` derives its probes from topics.yaml, reads the broker's answer
rather than rpk's exit code, and leaves nothing behind."""
import copy
import io
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import kafka_acl_probe  # noqa: E402
import kafka_users  # noqa: E402

REAL = kafka_users.load()


class FakeBroker:
    """Answers each probe as the declaration says it should, unless told otherwise."""

    def __init__(self, override=None, group_exists=False, hwm=7):
        self.override = override or {}
        self.group_exists_ = group_exists
        self.hwm = hwm
        self.calls = []

    def rpk(self, principal, args, stdin=""):
        self.calls.append((principal, list(args)))
        key = (principal, tuple(args))
        if key in self.override:
            return self.override[key]
        if args[:2] == ["topic", "describe"]:
            return 0, '[{"summary": {}, "partitions": [{"partition": 0, "high_watermark": %d}]}]' % self.hwm
        if args[:2] == ["group", "describe"] and principal == "andara-operator":
            return (0, "STATE Stable") if self.group_exists_ else (0, "ERROR GROUP_ID_NOT_FOUND")
        if args[:2] in (["topic", "trim-prefix"], ["group", "delete"]) and principal == "andara-operator":
            return 0, ""
        want = self.want_for(principal, args)
        return (0, '{"offset": 3}') if want == "allowed" else (1, "TOPIC_AUTHORIZATION_FAILED: no")

    def want_for(self, principal, args):
        for p in kafka_acl_probe.probes(REAL):
            if p.principal == principal and p.args == list(args):
                return p.want
        raise AssertionError("unexpected rpk %s %r" % (principal, args))


def run(broker, doc=REAL):
    out, err = io.StringIO(), io.StringIO()
    return kafka_acl_probe.main(broker=broker, doc=doc, out=out, err=err), out.getvalue(), err.getvalue()


class Classify(unittest.TestCase):
    def test_the_brokers_answer_decides_not_rpks_exit_code(self):
        self.assertEqual(kafka_acl_probe.classify(0, "x  TOPIC_AUTHORIZATION_FAILED: Topic authorization failed."),
                         "denied")
        self.assertEqual(kafka_acl_probe.classify(1, "GROUP_ID_NOT_FOUND: The group id does not exist."), "allowed")
        self.assertEqual(kafka_acl_probe.classify(124, '{"topic": "t", "offset": 3}'), "allowed")
        self.assertEqual(kafka_acl_probe.classify(124, "timed out after 60s"), "error")
        self.assertEqual(kafka_acl_probe.classify(0, "Produced to partition 0 at offset 3"), "allowed")


class Probes(unittest.TestCase):
    def test_every_declared_principal_is_probed_on_the_state_topic(self):
        """AC-12: a principal declared later is covered, because the probes read topics.yaml."""
        doc = copy.deepcopy(REAL)
        doc["principals"].append({"name": "andara-projection-redis", "topics": {"andara.state.v1": ["Read"]}})
        state = [p for p in kafka_acl_probe.probes(doc) if p.ac == "AC-12"]
        who = {(p.principal, p.args[2]) for p in state}
        self.assertIn(("andara-projection-redis-dev", "dev.andara.state.v1"), who)
        self.assertIn(("andara-schema-registry", "local.andara.state.v1"), who)
        self.assertFalse([p for p in state if p.principal == "andara-operator"], "the superuser is not probed")
        allowed = {(p.principal, p.args[2]) for p in state if p.want == "allowed"}
        self.assertEqual(allowed, {("andara-projector-state-dev", "dev.andara.state.v1"),
                                   ("andara-projector-state-local", "local.andara.state.v1")})

    def test_the_state_write_is_a_tombstone_and_the_destructive_probes_name_absent_topics(self):
        for p in kafka_acl_probe.probes(REAL):
            if p.ac == "AC-12":
                self.assertIn("--tombstone", p.args)
            if p.args[:2] in (["topic", "create"], ["topic", "delete"], ["topic", "alter-config"]):
                self.assertTrue(p.args[2].endswith(".andara.acl-probe-absent"), p.args)
            if p.args[:2] == ["topic", "consume"] and "-g" in p.args:
                self.assertTrue(p.args[p.args.index("-g") + 1].endswith("-acl-probe"), p.args)

    def test_each_environment_has_its_cross_environment_denials(self):
        whats = {(p.principal, p.what, p.want) for p in kafka_acl_probe.probes(REAL)}
        self.assertIn(("andara-server-dev", "produce local.andara.commands.v1", "denied"), whats)
        self.assertIn(("andara-projector-state-local", "DeleteGroups andara-projector-state-dev", "denied"), whats)
        self.assertIn(("andara-projector-state-dev", "read dev.andara.audit.v1", "denied"), whats)


class Main(unittest.TestCase):
    def test_all_as_declared_passes_and_cleans_up_after_every_probe(self):
        b = FakeBroker()
        code, out, err = run(b)
        self.assertEqual(code, 0, err)
        cleanups = [i for i, (pr, a) in enumerate(b.calls) if pr == "andara-operator" and a[:2] in
                    (["topic", "trim-prefix"], ["group", "delete"])]
        consumes = [i for i, (pr, a) in enumerate(b.calls) if a[:2] == ["topic", "consume"] and "-g" in a]
        self.assertTrue(cleanups and consumes)
        self.assertGreater(min(cleanups), max(consumes), "a cleanup ran before the consume that needs the record")
        self.assertIn(("andara-operator", ["topic", "trim-prefix", "dev.andara.commands.v1", "-p", "0", "-o", "7",
                                           "--no-confirm"]), b.calls)

    def test_an_unexpected_allow_fails_naming_it(self):
        b = FakeBroker(override={("andara-server-dev", ("topic", "produce", "local.andara.commands.v1", "-p", "0",
                                                         "-k", kafka_acl_probe.PROBE_KEY)): (0, "Produced")})
        code, _, err = run(b)
        self.assertEqual(code, 1)
        self.assertIn("AC-2 andara-server-dev produce local.andara.commands.v1: allowed, want denied", err)

    def test_the_projectors_own_group_is_never_deleted_when_it_exists(self):
        b = FakeBroker(group_exists=True)
        code, out, _ = run(b)
        self.assertEqual(code, 0)
        self.assertIn("skipped, the group exists", out)
        self.assertFalse([c for c in b.calls if c[0].startswith("andara-projector-state")
                          and c[1] == ["group", "delete", c[0]]])

    def test_a_failed_trim_is_reported_as_left_behind(self):
        b = FakeBroker()
        b.override[("andara-operator", ("topic", "describe", "dev.andara.commands.v1", "-p", "--format", "json"))] = (1, "")
        code, _, err = run(b)
        self.assertEqual(code, 1)
        self.assertIn("left behind: could not read dev.andara.commands.v1/0's high watermark", err)


if __name__ == "__main__":
    unittest.main()
