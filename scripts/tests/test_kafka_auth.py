# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-056 AC-11: every operator tool on the shared broker authenticates as andara-operator, with
the password on stdin, never on a command line."""
import base64
import contextlib
import io
import os
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import kafka_auth  # noqa: E402
import topics  # noqa: E402

FIXTURE = os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixtures", "kubeconfig")
PASSWORD = "Zx81spectrumQL1984"


def secret(stdout, code=0):
    return lambda argv, **kw: SimpleNamespace(returncode=code, stdout=stdout, stderr="")


class Cluster(unittest.TestCase):
    def setUp(self):
        env = {k: v for k, v in os.environ.items() if k not in ("ANDARA_KUBE_CONTEXT", "ANDARA_KAFKA_NAMESPACE")}
        p = mock.patch.dict(os.environ, dict(env, KUBECONFIG=FIXTURE), clear=True)
        p.start()
        self.addCleanup(p.stop)


class OperatorPassword(Cluster):
    def test_it_is_read_from_the_user_operators_secret(self):
        seen = []

        def run(argv, **kw):
            seen.append(argv)
            return SimpleNamespace(returncode=0, stdout=base64.b64encode(PASSWORD.encode()).decode(), stderr="")
        self.assertEqual(kafka_auth.operator_password(run=run), PASSWORD)
        self.assertEqual(seen[0][:5], ["kubectl", "--context", "kind-andara", "-n", "andara-shared"])
        self.assertEqual(seen[0][5:8], ["get", "secret", "andara-operator"])

    def test_a_missing_secret_exits_3_naming_it(self):
        for run in (secret("", 1), secret("")):
            with contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit) as e:
                kafka_auth.operator_password(run=run)
            self.assertEqual(e.exception.code, 3)
            self.assertIn("no Secret andara-shared/andara-operator", err.getvalue())

    def test_a_password_the_jaas_line_cannot_quote_exits_3(self):
        for bad in ('a"b', "a\\b", "a\nb"):
            with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as e:
                kafka_auth.operator_password(run=secret(base64.b64encode(bad.encode()).decode()))
            self.assertEqual(e.exception.code, 3)


class RpkWrapper(Cluster):
    def test_the_cluster_prefix_carries_no_credential(self):
        p = topics.rpk_prefix("dev", False, False)
        self.assertEqual(p[p.index("--") + 1:p.index("--") + 3], ["bash", "-c"])
        self.assertIn(kafka_auth.RPK_SCRIPT, p)
        self.assertNotIn(PASSWORD, " ".join(p))

    def test_the_runner_sends_the_password_on_stdin_only(self):
        calls = []

        def run(argv, **kw):
            calls.append((argv, kw))
            return SimpleNamespace(returncode=0, stdout="", stderr="")
        with mock.patch.object(kafka_auth, "operator_password", lambda ns=None, run=None: PASSWORD), \
                mock.patch("shutil.which", return_value="/usr/bin/kubectl"), \
                mock.patch.object(topics.subprocess, "run", run):
            topics.rpk_runner("dev", "cluster")(["topic", "list"])
        argv, kw = calls[0]
        self.assertEqual(kw["input"], PASSWORD + "\n")
        self.assertNotIn(PASSWORD, " ".join(argv))
        self.assertEqual(argv[-2:], ["topic", "list"])

    def test_the_runner_without_the_secret_exits_3_before_any_rpk(self):
        with mock.patch("shutil.which", return_value="/usr/bin/kubectl"), \
                mock.patch.object(kafka_auth.subprocess, "run", secret("", 1)), \
                contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(SystemExit) as e:
            topics.rpk_runner("dev", "cluster")
        self.assertEqual(e.exception.code, 3)
        self.assertIn("andara-operator", err.getvalue())

    def test_the_compose_broker_needs_no_credential(self):
        with mock.patch.object(topics, "compose_redpanda_running", lambda: True), \
                mock.patch.object(kafka_auth, "operator_password", side_effect=AssertionError("read a Secret")):
            topics.rpk_runner("local", "compose")


class Scripts(unittest.TestCase):
    """The two shell scripts, run for real against stand-in tools."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(subprocess.run, ["rm", "-rf", self.dir])

    def tool(self, name, body):
        path = os.path.join(self.dir, name)
        with open(path, "w") as f:
            f.write("#!/bin/bash\n" + body)
        os.chmod(path, 0o755)
        return path

    def test_rpk_gets_the_operator_and_the_rest_of_stdin(self):
        self.tool("rpk", 'echo "$* $RPK_USER $RPK_SASL_MECHANISM [$RPK_PASS]"; cat\n')
        env = dict(os.environ, PATH=self.dir + ":" + os.environ["PATH"])
        r = subprocess.run(["bash", "-c", kafka_auth.RPK_SCRIPT, "rpk", "-X", "brokers=b:9092", "topic", "list"],
                           input=PASSWORD + "\nrecords\n", capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout, "-X brokers=b:9092 topic list andara-operator SCRAM-SHA-512 [%s]\nrecords\n"
                         % PASSWORD)

    def test_a_broker_tool_gets_a_config_file_that_is_removed(self):
        tool = self.tool("kafka-tool", 'while [ $# -gt 0 ]; do [ "$1" = --command-config ] && '
                                       '{ echo "file=$2"; cat "$2"; }; shift; done; exit 7\n')
        r = subprocess.run(["bash", "-c", kafka_auth.BROKER_SCRIPT, "sh", tool, "--describe"],
                           input=PASSWORD + "\n", capture_output=True, text=True)
        self.assertEqual(r.returncode, 7, "the tool's own exit code")
        lines = r.stdout.splitlines()
        self.assertEqual(lines[1:], [
            "security.protocol=SASL_PLAINTEXT", "sasl.mechanism=SCRAM-SHA-512",
            'sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required '
            'username="andara-operator" password="%s";' % PASSWORD])
        self.assertFalse(os.path.exists(lines[0].split("=", 1)[1]), "the properties file outlived the tool")

    def test_broker_argv_names_the_kafka_container_and_no_credential(self):
        with mock.patch.dict(os.environ, {"KUBECONFIG": FIXTURE}):
            argv = kafka_auth.broker_argv("andara-shared", "andara-log-broker-1", ["/opt/kafka/bin/kafka-topics.sh"])
        self.assertIn("-i", argv)
        self.assertEqual(argv[argv.index("-c") + 1], "kafka")
        self.assertEqual(argv[-1], "/opt/kafka/bin/kafka-topics.sh")


if __name__ == "__main__":
    unittest.main()
