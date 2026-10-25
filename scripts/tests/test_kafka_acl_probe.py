# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-056: `make kafka-acl-probe` derives its probes from topics.yaml, reads the broker's answer
rather than rpk's exit code, never writes a record to a World topic, and leaves nothing behind."""
import copy
import io
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import kafka_acl_probe  # noqa: E402
import kafka_users  # noqa: E402

REAL = kafka_users.load()
LIMITS = kafka_acl_probe.max_message_bytes(REAL)


class FakeBroker:
    """Answers each probe as the declaration says it should, unless told otherwise."""

    def __init__(self, override=None, group_state="absent", live=None):
        self.override = override or {}
        self.group_state = group_state
        self.live = live or {}  # {topic: live max.message.bytes}; the declared value otherwise
        self.calls = []
        self.stdins = []

    def rpk(self, principal, args, stdin=""):
        self.calls.append((principal, list(args)))
        self.stdins.append(stdin)
        key = (principal, tuple(args))
        if key in self.override:
            return self.override[key]
        if principal == "andara-operator":
            if args[:2] == ["topic", "describe"]:
                topic = args[2]
                value = self.live.get(topic, LIMITS[topic.split(".", 1)[1]])
                return 0, '[{"summary": {}, "configs": [{"key": "max.message.bytes", "value": "%d"}]}]' % value
            if args[:2] == ["group", "describe"]:
                # One state for every group, or {group: state} with "absent" for the rest.
                state = self.group_state.get(args[2], "absent") if isinstance(self.group_state, dict) \
                    else self.group_state
                return {"absent": (0, "STATE Dead\nERROR GROUP_ID_NOT_FOUND: The group id does not exist."),
                        "exists": (0, "STATE Empty\nMEMBERS 0"),
                        "unknown": (124, "timed out after 90s")}[state]
            if args[:2] == ["group", "delete"]:
                return 0, ""
        want = self.want_for(principal, args)
        if want == "denied":
            return 1, "TOPIC_AUTHORIZATION_FAILED: Not authorized"
        if args[:2] == ["topic", "produce"]:
            return 1, "unable to produce record: MESSAGE_TOO_LARGE: The request included a message larger"
        return 0, ""

    def want_for(self, principal, args):
        for p in kafka_acl_probe.probes(REAL):
            same = p.args == list(args)
            if p.topic and args[:3] == p.args[:3]:
                same = True  # the size flag follows the live limit
            if p.principal == principal and same:
                return p.want
        raise AssertionError("unexpected rpk %s %r" % (principal, args))


def run(broker, doc=REAL):
    out, err = io.StringIO(), io.StringIO()
    return kafka_acl_probe.main(broker=broker, doc=doc, out=out, err=err), out.getvalue(), err.getvalue()


class Classify(unittest.TestCase):
    def test_the_brokers_answer_decides_not_rpks_exit_code(self):
        self.assertEqual(kafka_acl_probe.classify(0, "x  TOPIC_AUTHORIZATION_FAILED: Topic authorization failed."),
                         "denied")
        self.assertEqual(kafka_acl_probe.classify(1, "unable to produce record: MESSAGE_TOO_LARGE: …"), "allowed")
        self.assertEqual(kafka_acl_probe.classify(1, "GROUP_ID_NOT_FOUND: The group id does not exist."), "allowed")
        self.assertEqual(kafka_acl_probe.classify(124, "timed out after 60s"), "error")
        # A refusal wins over anything else in the same output.
        self.assertEqual(kafka_acl_probe.classify(1, "MESSAGE_TOO_LARGE … TOPIC_AUTHORIZATION_FAILED"), "denied")


class NeverWrites(unittest.TestCase):
    """Reviewer round 1 P0: the command log is the World's WAL, kept forever; the probe once wrote to
    it and trimmed the partition, which would delete every record below the high watermark."""

    def test_every_produce_is_larger_than_its_topic_accepts_and_uncompressed(self):
        produces = [p for p in kafka_acl_probe.probes(REAL) if p.args[:2] == ["topic", "produce"]]
        self.assertTrue(produces)
        for p in produces:
            topic = p.args[2].split(".", 1)[1]
            self.assertGreater(p.size, LIMITS[topic], p.what)
            self.assertEqual(p.args[p.args.index("--compression") + 1], "none", p.what)
            self.assertGreaterEqual(int(p.args[p.args.index("--max-message-bytes") + 1]), p.size, p.what)
            self.assertNotIn("--tombstone", p.args)

    def test_the_blob_topics_limit_is_read_from_the_declaration(self):
        self.assertEqual(LIMITS["andara.content.blobs.v1"], 9437184)
        blob = next(p for p in kafka_acl_probe.probes(REAL) if p.args[:3] == ["topic", "produce", "dev.andara.content.blobs.v1"])
        self.assertGreater(blob.size, 9437184)

    def test_the_payload_is_the_size_asked_and_incompressible_text(self):
        body = kafka_acl_probe.payload(3 << 20)
        self.assertEqual(len(body), 3 << 20)
        self.assertNotIn("\n", body)
        import zlib
        self.assertGreater(len(zlib.compress(body.encode())), 2 << 20)

    def test_a_run_sends_the_oversized_record_and_no_trim_or_delete_of_data(self):
        b = FakeBroker()
        run(b)
        for (principal, args), stdin in zip(b.calls, b.stdins):
            self.assertNotIn("trim-prefix", args)
            self.assertFalse(args[:2] == ["topic", "delete"] and principal == "andara-operator")
            if args[:2] == ["topic", "produce"]:
                self.assertGreater(len(stdin), LIMITS[args[2].split(".", 1)[1]])


class LiveLimit(unittest.TestCase):
    """Reviewer round 2: the record must exceed the limit the broker applies, not only the declared one."""

    def test_a_live_limit_above_the_declared_one_sizes_the_record_past_it(self):
        b = FakeBroker(live={"dev.andara.commands.v1": 50 << 20})
        code, _, err = run(b)
        self.assertEqual(code, 0, err)
        i = next(i for i, (pr, a) in enumerate(b.calls)
                 if pr == "andara-server-dev" and a[:3] == ["topic", "produce", "dev.andara.commands.v1"])
        self.assertGreater(len(b.stdins[i]), 50 << 20)
        args = b.calls[i][1]
        self.assertGreaterEqual(int(args[args.index("--max-message-bytes") + 1]), len(b.stdins[i]) - 1)

    def test_an_unreadable_live_limit_produces_nothing_and_fails(self):
        b = FakeBroker(override={("andara-operator", ("topic", "describe", "dev.andara.state.v1", "--format", "json")):
                                 (1, "boom")})
        code, _, err = run(b)
        self.assertEqual(code, 1)
        self.assertFalse([c for c in b.calls if c[1][:3] == ["topic", "produce", "dev.andara.state.v1"]])
        self.assertIn("couldn't read dev.andara.state.v1's live max.message.bytes", err)


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

    def test_destructive_probes_name_absent_topics_and_groups_are_probe_groups(self):
        for p in kafka_acl_probe.probes(REAL):
            if p.args[:2] in (["topic", "create"], ["topic", "delete"], ["topic", "alter-config"]):
                self.assertTrue(p.args[2].endswith(".andara.acl-probe-absent"), p.args)
            if p.args[:2] == ["group", "seek"]:
                self.assertTrue(p.args[2].endswith("-acl-probe"), p.args)

    def test_each_environment_has_its_cross_environment_denials(self):
        whats = {(p.principal, p.what, p.want) for p in kafka_acl_probe.probes(REAL)}
        self.assertIn(("andara-server-dev", "produce local.andara.commands.v1", "denied"), whats)
        self.assertIn(("andara-projector-state-local", "DeleteGroups andara-projector-state-dev", "denied"), whats)
        self.assertIn(("andara-projector-state-local", "DeleteGroups andara-projector-state-dev-acl-probe-absent",
                       "denied"), whats)
        # AC-3's allowed half: the projector may delete under its own prefix (`--rebuild`).
        for env in ("dev", "local"):
            proj = "andara-projector-state-" + env
            self.assertIn((proj, "DeleteGroups %s-acl-probe-absent (its own prefix)" % proj, "allowed"), whats)
        self.assertIn(("andara-projector-state-dev", "read dev.andara.audit.v1", "denied"), whats)
        self.assertIn(("andara-server-dev", "commit offsets on dev.andara.commands.v1 in group andara-sim-local-acl-probe",
                       "denied"), whats)


class Main(unittest.TestCase):
    def test_all_as_declared_passes_and_deletes_every_probe_group(self):
        b = FakeBroker()
        code, out, err = run(b)
        self.assertEqual(code, 0, err)
        for env in ("dev", "local"):
            self.assertIn(("andara-operator", ["group", "delete", "andara-sim-%s-acl-probe" % env]), b.calls)
        deletes = [i for i, (pr, a) in enumerate(b.calls) if pr == "andara-operator" and a[:2] == ["group", "delete"]]
        seeks = [i for i, (_, a) in enumerate(b.calls) if a[:2] == ["group", "seek"]]
        self.assertGreater(min(deletes), max(seeks))
        self.assertIn("wrote no record", out)

    def test_an_unexpected_allow_fails_naming_it(self):
        p = next(p for p in kafka_acl_probe.probes(REAL)
                 if p.principal == "andara-server-dev" and p.what == "produce local.andara.commands.v1")
        b = FakeBroker(override={(p.principal, tuple(p.args)): (1, "MESSAGE_TOO_LARGE")})
        code, _, err = run(b)
        self.assertEqual(code, 1)
        self.assertIn("AC-2 andara-server-dev produce local.andara.commands.v1: allowed, want denied", err)

    def test_the_projectors_own_group_is_skipped_when_it_exists(self):
        b = FakeBroker(group_state="exists")
        code, out, _ = run(b)
        self.assertEqual(code, 0)
        self.assertIn("skipped, the group exists", out)
        self.assertFalse([c for c in b.calls if c[0].startswith("andara-projector-state")
                          and c[1] == ["group", "delete", c[0]]])

    def test_an_unknown_group_state_never_deletes_and_fails(self):
        """Reviewer round 1 P1: a describe that failed read as 'absent', and the probe deleted the
        projector's real group, a silent rebuild."""
        b = FakeBroker(group_state="unknown")
        code, out, err = run(b)
        self.assertEqual(code, 1)
        self.assertFalse([c for c in b.calls if c[0].startswith("andara-projector-state")
                          and c[1] == ["group", "delete", c[0]]])
        self.assertIn("couldn't tell whether andara-projector-state-dev exists", err)

    def test_no_real_projector_group_is_ever_deleted_unless_seen_absent(self):
        """Codex on #654: the cross-environment DeleteGroups named the other projector's real group,
        so drift granting that Delete (what the probe exists to catch) would erase its checkpoint."""
        real = {"andara-projector-state-dev", "andara-projector-state-local"}
        for state in ("exists", "unknown"):
            b = FakeBroker(group_state=state)
            run(b)
            deleted = [(pr, a[2]) for pr, a in b.calls if a[:2] == ["group", "delete"] and pr != "andara-operator"]
            self.assertFalse([d for d in deleted if d[1] in real], (state, deleted))

    def test_a_disposable_group_under_the_other_projectors_prefix_is_always_probed(self):
        b = FakeBroker(group_state="exists")
        run(b)
        self.assertIn(("andara-projector-state-dev", ["group", "delete", "andara-projector-state-local-acl-probe-absent"]),
                      b.calls)
        self.assertIn(("andara-projector-state-local", ["group", "delete", "andara-projector-state-dev-acl-probe-absent"]),
                      b.calls)

    def test_each_guard_checks_the_group_its_probe_would_delete(self):
        """Reviewer: one state for every group couldn't tell a guard keyed on the wrong group. Here dev's
        group exists and local's is absent: nothing may delete dev's, and local's real name is probed."""
        b = FakeBroker(group_state={"andara-projector-state-dev": "exists"})
        code, out, err = run(b)
        self.assertEqual(code, 0, err)
        self.assertFalse([c for c in b.calls if c[1] == ["group", "delete", "andara-projector-state-dev"]
                          and c[0] != "andara-operator"], "dev's existing group was deleted")
        self.assertIn(("andara-projector-state-dev", ["group", "delete", "andara-projector-state-local"]), b.calls)
        self.assertIn(("andara-projector-state-local", ["group", "delete", "andara-projector-state-local"]), b.calls)
        self.assertIn("2 probe(s) of a real group skipped because it exists", out)
        # And the reverse.
        b = FakeBroker(group_state={"andara-projector-state-local": "exists"})
        run(b)
        self.assertFalse([c for c in b.calls if c[1] == ["group", "delete", "andara-projector-state-local"]
                          and c[0] != "andara-operator"])
        self.assertIn(("andara-projector-state-local", ["group", "delete", "andara-projector-state-dev"]), b.calls)

    def test_the_allowed_half_of_ac3_runs_even_when_the_real_group_exists(self):
        b = FakeBroker(group_state="exists")
        code, _, err = run(b)
        self.assertEqual(code, 0, err)
        for env in ("dev", "local"):
            proj = "andara-projector-state-" + env
            self.assertIn((proj, ["group", "delete", proj + "-acl-probe-absent"]), b.calls)

    def test_create_probes_are_validate_only(self):
        creates = [p for p in kafka_acl_probe.probes(REAL) if p.args[:2] == ["topic", "create"]]
        self.assertTrue(creates)
        self.assertTrue(all("--dry" in p.args for p in creates))

    def test_the_real_other_group_is_probed_when_absent(self):
        b = FakeBroker(group_state="absent")
        code, _, err = run(b)
        self.assertEqual(code, 0, err)
        self.assertIn(("andara-projector-state-dev", ["group", "delete", "andara-projector-state-local"]), b.calls)

    def test_group_state_reads_the_describe(self):
        for text, code, want in (("STATE Dead\nERROR GROUP_ID_NOT_FOUND: x", 0, "absent"),
                                 ("STATE Empty\nMEMBERS 0", 0, "exists"),
                                 ("STATE Dead", 0, "unknown"),
                                 ("STATE Stable\nMEMBERS 1", 0, "exists"),
                                 ("timed out after 90s", 124, "unknown"),
                                 ("GROUP_AUTHORIZATION_FAILED", 1, "unknown"),
                                 ("", 0, "unknown")):
            class B:
                def rpk(self, *a, **k):
                    return code, text
            self.assertEqual(kafka_acl_probe.group_state(B(), "g"), want, text)

    def test_a_refused_group_cleanup_that_exits_0_is_reported(self):
        b = FakeBroker(override={("andara-operator", ("group", "delete", "andara-sim-local-acl-probe")):
                                 (0, "GROUP_AUTHORIZATION_FAILED: no")})
        code, _, err = run(b)
        self.assertEqual(code, 1)
        self.assertIn("left behind: deleting probe group andara-sim-local-acl-probe failed", err)

    def test_a_failed_group_cleanup_is_reported_as_left_behind(self):
        b = FakeBroker(override={("andara-operator", ("group", "delete", "andara-sim-dev-acl-probe")): (1, "boom")})
        code, _, err = run(b)
        self.assertEqual(code, 1)
        self.assertIn("left behind: deleting probe group andara-sim-dev-acl-probe failed: boom", err)


if __name__ == "__main__":
    unittest.main()
