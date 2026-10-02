# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make kafka-operator` re-applies the chart when a watched namespace lost its RoleBindings.

Found by AW-INF-021 AC-7 (2026-10-02): deleting andara-dev removed Strimzi's RoleBindings there,
while the release still matched, so the operator got 403 on every watch and kafka-install waited
until it timed out. The fakes stand in for helm and kubectl and record every call.
"""
import os
import stat
import subprocess
import tempfile
import textwrap
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

HELM = textwrap.dedent("""\\
    #!/bin/sh
    echo "helm $*" >> "$FAKE_DIR/calls"
    case "$1" in
      list) echo '[{"name":"strimzi","chart":"strimzi-kafka-operator-1.2.0"}]' ;;
      get)  echo '{"watchNamespaces":["andara-dev","andara-prod"]}' ;;
    esac
    exit 0
""")

KUBECTL = textwrap.dedent("""\\
    #!/bin/sh
    echo "kubectl $*" >> "$FAKE_DIR/calls"
    # kubectl -n <ns> get rolebinding ...: present unless the namespace is listed in MISSING.
    if [ "$1" = "-n" ] && [ "$3" = "get" ] && [ "$4" = "rolebinding" ]; then
      case " $MISSING " in *" $2 "*) exit 1 ;; esac
    fi
    exit 0
""")


class Operator(unittest.TestCase):

    def run_operator(self, missing=""):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: subprocess.run(["rm", "-rf", d]))
        for name, body in (("helm", HELM), ("kubectl", KUBECTL)):
            p = os.path.join(d, name)
            with open(p, "w") as f:
                f.write(body)
            os.chmod(p, os.stat(p).st_mode | stat.S_IEXEC)
        env = dict(os.environ, PATH=d + ":" + os.environ["PATH"], FAKE_DIR=d, MISSING=missing)
        r = subprocess.run([os.path.join(REPO, "scripts", "kafka.sh"), "operator"],
                           capture_output=True, text=True, env=env)
        with open(os.path.join(d, "calls")) as f:
            calls = f.read().splitlines()
        return r, calls

    def test_matching_release_with_bindings_is_a_no_op(self):
        r, calls = self.run_operator()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("already installed", r.stdout)
        self.assertFalse([c for c in calls if c.startswith("helm upgrade")], calls)

    def test_a_namespace_missing_its_bindings_reapplies_the_chart(self):
        r, calls = self.run_operator(missing="andara-dev")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("RoleBindings are missing in andara-dev", r.stdout)
        self.assertTrue([c for c in calls if c.startswith("helm upgrade --install strimzi")], calls)


if __name__ == "__main__":
    unittest.main()
