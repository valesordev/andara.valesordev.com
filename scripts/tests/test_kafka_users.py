# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-055 AC-9: the KafkaUsers rendered from topics.yaml stay inside their environment, and
no environment principal can create, delete or reconfigure a topic."""
import copy
import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import kafka_users  # noqa: E402

REAL = kafka_users.load()


def users_of(doc):
    return kafka_users.render(doc)


def acl(user, topic):
    return next(a for a in user["spec"]["authorization"]["acls"] if a["resource"].get("name") == topic)


class Real(unittest.TestCase):
    def test_the_declaration_passes(self):
        self.assertEqual(kafka_users.check(users_of(REAL), REAL), [])

    def test_each_workload_has_a_principal_in_each_environment(self):
        names = {u["metadata"]["name"] for u in users_of(REAL)}
        self.assertEqual(names, {"andara-server-local", "andara-server-dev",
                                 "andara-projector-state-local", "andara-projector-state-dev"})

    def test_acls_are_on_the_environments_names(self):
        by = {u["metadata"]["name"]: u for u in users_of(REAL)}
        a = acl(by["andara-server-dev"], "dev.andara.commands.v1")
        self.assertEqual(a["resource"]["patternType"], "literal")
        self.assertEqual(acl(by["andara-server-dev"], "andara-sim-dev")["resource"]["patternType"], "prefix")
        self.assertEqual(acl(by["andara-projector-state-local"], "andara-projector-state-local")["operations"],
                         ["Read", "Delete"])
        # Only the projector writes the state topic (AW-SRV-019 AC-9).
        self.assertFalse([a for a in by["andara-server-dev"]["spec"]["authorization"]["acls"]
                          if a["resource"].get("name") == "dev.andara.state.v1"])

    def test_only_local_is_throttled(self):
        for u in users_of(REAL):
            env = u["metadata"]["labels"]["andara.valesor/env"]
            self.assertEqual("quotas" in u["spec"], env == "local", u["metadata"]["name"])

    def test_prod_has_no_principal_on_this_cluster(self):
        self.assertNotIn("prod", kafka_users.environments(REAL))


class Breaks(unittest.TestCase):
    """Each rule fails on a render that breaks it, naming the KafkaUser."""

    def setUp(self):
        self.users = users_of(REAL)
        self.by = {u["metadata"]["name"]: u for u in self.users}

    def errors(self, users=None, doc=REAL):
        return kafka_users.check(self.users if users is None else users, doc)

    def assertFails(self, needle, users=None, doc=REAL):
        errs = self.errors(users, doc)
        self.assertTrue([e for e in errs if needle in e], errs)

    def add(self, name, resource, ops):
        self.by[name]["spec"]["authorization"]["acls"].append({"resource": resource, "operations": ops})

    def test_create_alter_or_alterconfigs_on_a_topic(self):
        for op in ("Create", "Alter", "AlterConfigs", "Delete", "All"):
            with self.subTest(op=op):
                self.setUp()
                acl(self.by["andara-server-dev"], "dev.andara.commands.v1")["operations"].append(op)
                self.assertFails("andara-server-dev: topic literal dev.andara.commands.v1")

    def test_a_wildcard_topic_or_group(self):
        self.add("andara-server-local", {"type": "topic", "name": "*", "patternType": "literal"}, ["Read"])
        self.assertFails("andara-server-local: topic literal * Read: a wildcard name")
        self.setUp()
        self.add("andara-server-local", {"type": "group", "name": "*", "patternType": "literal"}, ["Read"])
        self.assertFails("andara-server-local: group literal * Read: a wildcard name")

    def test_a_topic_outside_the_prefix(self):
        acl(self.by["andara-server-local"], "local.andara.commands.v1")["resource"]["name"] = "dev.andara.commands.v1"
        self.assertFails("outside environment local's prefix local.")

    def test_a_group_outside_the_environment(self):
        acl(self.by["andara-server-local"], "andara-sim-local")["resource"]["name"] = "andara-sim-dev"
        self.assertFails("a group outside environment local")

    def test_group_delete_is_the_projectors_own_only(self):
        acl(self.by["andara-server-dev"], "andara-sim-dev")["operations"].append("Delete")
        self.assertFails("andara-server-dev: group prefix andara-sim-dev Delete,Read: Delete on a group")

    def test_cluster_operations_beyond_describe(self):
        self.add("andara-projector-state-dev", {"type": "cluster"}, ["Create"])
        self.assertFails("andara-projector-state-dev: cluster")

    def test_a_missing_principal(self):
        users = [u for u in self.users if u["metadata"]["name"] != "andara-projector-state-dev"]
        self.assertFails("andara-projector-state-dev: no KafkaUser", users)

    def test_dev_with_locals_quotas(self):
        self.by["andara-server-dev"]["spec"]["quotas"] = dict(REAL["quotas"]["local"])
        self.assertFails("andara-server-dev: quotas")

    def test_an_acl_on_an_undeclared_topic(self):
        doc = copy.deepcopy(REAL)
        doc["principals"][0]["topics"]["andara.nowhere.v1"] = ["Read"]
        self.assertFails("an ACL on andara.nowhere.v1", users_of(doc), doc)

    def test_the_suite_exception_is_by_name_and_prefix_only(self):
        it = {"apiVersion": kafka_users.API, "kind": "KafkaUser",
              "metadata": {"name": "andara-it", "labels": {"strimzi.io/cluster": "andara-log"}},
              "spec": {"authorization": {"type": "simple", "acls": [
                  {"resource": {"type": "topic", "name": "andara.test.", "patternType": "prefix"},
                   "operations": ["Create", "Delete", "Read", "Write"]},
                  {"resource": {"type": "topic", "name": "it", "patternType": "prefix"},
                   "operations": ["Create", "Delete", "Read", "Write"]}]}}}
        self.assertEqual(self.errors(self.users + [it]), [])
        # On any other topic, the suite's principal is held to the rule.
        bad = copy.deepcopy(it)
        bad["spec"]["authorization"]["acls"][0]["resource"]["name"] = "dev."
        self.assertFails("andara-it: topic prefix dev. Create,Delete,Read,Write: Create,Delete", self.users + [bad])
        # And another principal can't borrow it.
        self.add("andara-server-dev", {"type": "topic", "name": "andara.test.", "patternType": "prefix"}, ["Create"])
        self.assertFails("andara-server-dev: topic prefix andara.test. Create: Create on a topic")


if __name__ == "__main__":
    unittest.main()
