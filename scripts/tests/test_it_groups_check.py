# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The `it-` group-ID check in `make check` (AW-INF-060 AC-12)."""
import os
import shutil
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import it_groups_check  # noqa: E402


class Tree(unittest.TestCase):

    def setUp(self):
        self.root = tempfile.mkdtemp()

    def tearDown(self):
        shutil.rmtree(self.root)

    def write(self, rel, text):
        path = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as f:
            f.write(text)

    def check(self, exceptions=None):
        return it_groups_check.check(self.root, exceptions or {})

    def test_a_group_without_the_prefix_fails_naming_file_and_id(self):
        self.write("server/x/a_integration_test.go",
                   'package x\n\nfunc f() {\n\tgroup := "andara-sim-test-" + commands\n}\n')
        errors = self.check()
        self.assertEqual(len(errors), 1, errors)
        self.assertIn("server/x/a_integration_test.go:4", errors[0])
        self.assertIn("'andara-sim-test-'", errors[0])

    def test_every_group_position_is_read(self):
        for line in ('src := NewKafkaSource(ctx, KafkaSourceOptions{Kafka: bk, Group: "sim-x", Topic: t})',
                     'group = "sim-x"',
                     'cl, _ := kgo.NewClient(kgo.ConsumerGroup("sim-x"))',
                     '_, _ = adm.DeleteGroups(dctx, "it-ok", "sim-x"+nonce)'):
            with self.subTest(line=line):
                self.write("p/b_integration_test.go", "package p\n\nfunc f() {\n\t%s\n}\n" % line)
                errors = self.check()
                self.assertEqual(len(errors), 1, errors)
                self.assertIn("'sim-x'", errors[0])

    def test_prefixed_groups_variables_comments_and_unit_tests_pass(self):
        self.write("p/c_integration_test.go", "\n".join([
            "package p",
            'func f() {',
            '\tgroup := "it-sim-" + commands',
            '\tsrc := NewKafkaSource(ctx, KafkaSourceOptions{Group: group})',
            '\t// group := "andara-sim-dev" is what the server uses',
            '\tx := fmt.Sprintf("it-projector-%d", n) // Group: "nope"',
            '}', ""]))
        self.write("p/c_test.go", 'package p\nvar group = "andara-sim-dev"\n')
        self.assertEqual(self.check(), [])

    def test_an_exception_admits_its_site_and_a_stale_one_fails(self):
        self.write("p/d_integration_test.go", 'package p\nfunc f() { group := "andara-sim-it" + n }\n')
        self.assertEqual(self.check({("p/d_integration_test.go", "andara-sim-it"): "AW-INF-060"}), [])
        errors = self.check({("p/d_integration_test.go", "andara-sim-it"): "AW-INF-060",
                             ("p/gone_integration_test.go", "x"): "AW-INF-999"})
        self.assertEqual(len(errors), 1, errors)
        self.assertIn("matches no group site", errors[0])


class Repo(unittest.TestCase):

    def test_the_repo_passes(self):
        # AC-12's second half: with AW-SRV-058's renames landed, only the listed exceptions remain.
        self.assertEqual(it_groups_check.check(), [])


if __name__ == "__main__":
    unittest.main()
