# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make pipelines-check` (AW-INF-051 AC-5) and shared-kafka's allow-list and output (AC-13).

Each break below changes the committed shared-kafka.alloy one way and must be refused, naming the file;
each accept must still pass. They run the pinned bin/alloy, which `make bootstrap` installs.
"""
import contextlib
import io
import os
import re
import shutil
import sys
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import pipelines_check  # noqa: E402

SHARED_KAFKA = os.path.join(pipelines_check.PIPELINES, "shared-kafka.alloy")
HAVE_ALLOY = os.access(pipelines_check.ALLOY, os.X_OK)
OTHER_URL = "https://prometheus-prod-67-prod-us-west-0.grafana.net/api/prom/push"

# The names shared-kafka keeps: ADR-0014 §6's set as the Strimzi Metrics Reporter serves them (AC-12),
# read at :9404 from quay.io/strimzi/kafka:1.2.0-kafka-4.3.1 on 2026-10-22, and its job's up. The ADR's
# `isrshrinks_total` and `isrexpands_total` are served as `isrshrinkspersec_total` and `isrexpandspersec_total`.
KEPT = {
    "kafka_server_replicamanager_underreplicatedpartitions",
    "kafka_server_replicamanager_underminisrpartitioncount",
    "kafka_server_replicamanager_atminisrpartitioncount",
    "kafka_server_replicamanager_isrshrinkspersec_total",
    "kafka_server_replicamanager_isrexpandspersec_total",
    "kafka_controller_kafkacontroller_offlinepartitionscount",
    "kafka_controller_kafkacontroller_activecontrollercount",
    "up",
}


def _append(block):
    return lambda s: s + "\n" + block + "\n"


def _replace(old, new):
    def f(s):
        assert old in s, "fixture setup: %r not in shared-kafka.alloy" % old
        return s.replace(old, new, 1)
    return f


AUTH = 'endpoint {\n\t\turl = "%s"\n' % pipelines_check.RECEIVER

# (name, change, rule the refusal names)
BREAKS = [
    ("a Grafana Cloud token", _append("// glc_eyJvIjoiMSJ9"), "secret"),
    ("a service-account token in a string", _replace('regex         = "andara-kafka"',
                                                     'regex         = "glsa_abc_123"'), "secret"),
    ("basic auth in a URL", _replace(pipelines_check.RECEIVER, "http://u:p@k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write"), "secret"),
    ("a templatefile call", _replace('names = ["andara-shared"]', 'names = [templatefile("x", {})]'), "templatefile"),
    ("a remote_write to Grafana Cloud", _replace(pipelines_check.RECEIVER, OTHER_URL), "remote_write"),
    ("a second remote_write endpoint", _replace(AUTH, AUTH + '\t}\n\n\tendpoint {\n\t\turl = "%s"\n' % OTHER_URL),
     "remote_write"),
    ("a computed remote_write url", _replace('url = "%s"' % pipelines_check.RECEIVER,
                                             'url = "http://" + "k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write"'),
     "remote_write"),
    ("an otelcol exporter", _append('otelcol.exporter.otlphttp "cloud" {\n\tclient {\n\t\tendpoint = "https://otlp.invalid"\n\t}\n}'),
     "writer"),
    ("a loki.write", _append('loki.write "cloud" {\n\tendpoint {\n\t\turl = "https://logs.invalid/loki/api/v1/push"\n\t}\n}'),
     "writer"),
    ("sys.env of a name env-allow.txt doesn't list",
     _replace(AUTH, AUTH + '\n\t\tbasic_auth {\n\t\t\tusername = "1"\n\t\t\tpassword = sys.env("GRAFANA_TELEMETRY_TOKEN")\n\t\t}\n'),
     "sys.env"),
    ("sys.env of a computed name",
     _replace(AUTH, AUTH + '\n\t\tbasic_auth {\n\t\t\tusername = "1"\n\t\t\tpassword = sys.env("PG_" + "PASSWORD")\n\t\t}\n'),
     "sys.env"),
    ("a reference alloy validate refuses", _replace("forward_to = [prometheus.relabel.shared_kafka.receiver]",
                                                    "forward_to = [prometheus.relabel.nope.receiver]"),
     "alloy validate"),
]

# (name, change, env-allow.txt) that must still pass.
ACCEPTS = [
    ("the committed source", lambda s: s, ""),
    ("loki.write and templatefile named in a comment", _append("// no loki.write, no templatefile(), no otelcol.exporter.otlp"), ""),
    ("sys.env of a listed name",
     _replace(AUTH, AUTH + '\n\t\tbasic_auth {\n\t\t\tusername = "1"\n\t\t\tpassword = sys.env("PG_MONITOR_PASSWORD")\n\t\t}\n'),
     "PG_MONITOR_PASSWORD\n"),
]


@unittest.skipUnless(HAVE_ALLOY or os.environ.get("CI"), "bin/alloy absent; make bootstrap installs it")
class Check(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        with open(SHARED_KAFKA) as f:
            self.source = f.read()

    def run_on(self, text, allow=""):
        with open(os.path.join(self.dir, "shared-kafka.alloy"), "w") as f:
            f.write(text)
        with open(os.path.join(self.dir, "env-allow.txt"), "w") as f:
            f.write(allow)
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = pipelines_check.main(["--dir", self.dir])
        return rc, out.getvalue() + err.getvalue()

    def test_each_break_is_refused_naming_the_file(self):
        for name, change, rule in BREAKS:
            with self.subTest(name):
                rc, out = self.run_on(change(self.source))
                self.assertEqual(rc, 1, out)
                self.assertRegex(out, r"refused: .*shared-kafka\.alloy:\d+: %s: " % re.escape(rule))

    def test_each_accept_passes(self):
        for name, change, allow in ACCEPTS:
            with self.subTest(name):
                rc, out = self.run_on(change(self.source), allow)
                self.assertEqual(rc, 0, out)

    def test_the_committed_pipelines_pass(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(pipelines_check.main([]), 0)
        self.assertIn("shared-kafka.alloy", out.getvalue())

    def test_env_allow_is_committed_empty(self):
        with open(os.path.join(pipelines_check.PIPELINES, "env-allow.txt")) as f:
            self.assertEqual(f.read(), "")

    def test_a_missing_env_allow_or_no_pipeline_fails(self):
        rc, out = self.run_on(self.source)
        os.remove(os.path.join(self.dir, "env-allow.txt"))
        with contextlib.redirect_stderr(io.StringIO()) as err:
            self.assertEqual(pipelines_check.main(["--dir", self.dir]), 1)
        self.assertIn("env-allow.txt", err.getvalue())
        os.remove(os.path.join(self.dir, "shared-kafka.alloy"))
        open(os.path.join(self.dir, "env-allow.txt"), "w").close()
        with contextlib.redirect_stderr(io.StringIO()) as err:
            self.assertEqual(pipelines_check.main(["--dir", self.dir]), 1)
        self.assertIn("no pipeline", err.getvalue())


class Usage(unittest.TestCase):
    def test_no_alloy_exits_2(self):
        with contextlib.redirect_stderr(io.StringIO()) as err:
            self.assertEqual(pipelines_check.main(["--alloy", "/nonexistent/alloy"]), 2)
        self.assertIn("make bootstrap", err.getvalue())


class SharedKafka(unittest.TestCase):
    """AC-13: the allow-list AC-12 recorded, no per-topic series, and the receiver as the one output."""

    def setUp(self):
        with open(SHARED_KAFKA) as f:
            self.text = f.read()
        self.code = pipelines_check.code_only(self.text)

    def blocks(self, kind):
        return [m for m in re.finditer(r"^%s\s+\"([^\"]*)\"\s*\{" % re.escape(kind), self.code, re.M)]

    def test_keeps_exactly_the_recorded_names(self):
        relabels = self.blocks("prometheus.relabel")
        self.assertEqual(len(relabels), 1)
        body = self.text[relabels[0].end():pipelines_check.block_end(self.code, relabels[0].end())]
        keeps = re.findall(r'regex\s*=\s*"([^"]*)"\s*\n\s*action\s*=\s*"keep"', body)
        self.assertEqual(len(keeps), 1, "one keep rule on __name__")
        names = keeps[0].split("|")
        self.assertTrue(all(re.fullmatch(r"[a-z_:][a-z0-9_:]*", n) for n in names), names)
        self.assertEqual(set(names), KEPT)
        self.assertFalse([n for n in names if n.startswith("kafka_server_brokertopicmetrics_")])

    def test_one_output_the_receiver(self):
        writes = self.blocks("prometheus.remote_write")
        self.assertEqual(len(writes), 1)
        body = self.text[writes[0].end():pipelines_check.block_end(self.code, writes[0].end())]
        self.assertEqual(re.findall(r'url\s*=\s*"([^"]*)"', body), [pipelines_check.RECEIVER])
        relabel = self.blocks("prometheus.relabel")[0]
        rbody = self.text[relabel.end():pipelines_check.block_end(self.code, relabel.end())]
        self.assertEqual(re.findall(r"forward_to\s*=\s*\[([^\]]*)\]", rbody),
                         ["prometheus.remote_write.%s.receiver" % self.text[writes[0].start(1):writes[0].end(1)]])
        for kind in ("loki.write", "otelcol.exporter.", "pyroscope.write"):
            self.assertNotIn(kind, self.code)

    def test_sets_namespace_and_job_and_no_environment(self):
        targets = re.findall(r'target_label\s*=\s*"([^"]*)"', self.text)
        self.assertIn("namespace", targets)
        self.assertIn("job", targets)
        self.assertNotIn("environment", targets)
        self.assertIn('names = ["andara-shared"]', self.text)
        self.assertIn('regex         = "andara-kafka"', self.text)
        self.assertIn('replacement   = "$1:9404"', self.text)


if __name__ == "__main__":
    unittest.main()
