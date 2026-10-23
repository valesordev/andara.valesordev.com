# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/tf_policy.py (AW-INF-068 AC-2): one fixture per refusal, and the real call sites pass.

Each test copies the repository's deploy/terraform (and the files a file() may read) into a base and a
head tree, changes the head the way a pull request would, and runs the check."""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import tf_policy  # noqa: E402

G = "deploy/terraform/grafana"
SOLO7DEV = G + "/stacks/solo7dev"
STACK_MODULE = G + "/modules/stack"


def copy_tree(dst):
    shutil.copytree(os.path.join(REPO, "deploy", "terraform"), os.path.join(dst, "deploy", "terraform"),
                    ignore=shutil.ignore_patterns(".terraform"))
    os.makedirs(os.path.join(dst, "deploy", "helm", "andara", "files"))
    shutil.copy(os.path.join(REPO, tf_policy.ALERTS), os.path.join(dst, tf_policy.ALERTS))


class Base(unittest.TestCase):

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.dir)
        self.base = os.path.join(self.dir, "base")
        self.head = os.path.join(self.dir, "head")
        copy_tree(self.base)
        copy_tree(self.head)

    def write(self, path, text, tree=None):
        full = os.path.join(tree or self.head, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w") as f:
            f.write(text)

    def append(self, path, text):
        with open(os.path.join(self.head, path), "a") as f:
            f.write(text)

    def replace(self, path, old, new):
        full = os.path.join(self.head, path)
        with open(full) as f:
            text = f.read()
        self.assertIn(old, text, "fixture setup: %r not in %s" % (old, path))
        with open(full, "w") as f:
            f.write(text.replace(old, new, 1))

    def lines_of(self, path):
        with open(os.path.join(self.head, path)) as f:
            return len(f.read().splitlines())

    def findings(self):
        return tf_policy.check(self.base, self.head)

    def assertRefused(self, rule, path, line=None, detail=""):
        items = self.findings()
        hits = [i for i in items if i[0] == rule and i[1] == path and (line is None or i[2] == line)
                and detail in i[3]]
        self.assertTrue(hits, "expected %s refusal at %s:%s containing %r; got %s" % (rule, path, line, detail, items))
        return hits

    def assertAllowed(self):
        self.assertEqual(self.findings(), [])


class RealTree(Base):

    def test_the_committed_tree_is_allowed(self):
        self.assertAllowed()

    def test_the_real_call_sites_are_allowed(self):
        """alerts.yaml (AW-INF-047), tick-health.json (AW-INF-049), one literal file() per pipeline (AW-INF-051)."""
        self.write("deploy/grafana/dashboards/tick-health.json", "{}\n")
        self.write(STACK_MODULE + "/pipelines/stamp.alloy", 'prometheus.relabel "stamp" { }\n')
        # Text a pipeline carries is data: nothing evaluates it.
        self.write(STACK_MODULE + "/pipelines/notes.alloy", '// ${file("/proc/self/environ")}\n')
        self.append(STACK_MODULE + "/main.tf", '''
locals {
  rules     = yamldecode(file("${path.module}/../../../../helm/andara/files/alerts.yaml"))
  dashboard = file("${path.root}/../../../../grafana/dashboards/tick-health.json")
  stamp     = file("${path.module}/pipelines/stamp.alloy")
  notes     = file("${path.module}/pipelines/notes.alloy")
}

resource "grafana_folder" "andara" {
  title = "Andara ${replace(var.stack, "-", " ")}"
}
''')
        self.assertAllowed()

    def test_a_non_endpoint_tfvars_change_is_allowed(self):
        self.replace(SOLO7DEV + "/terraform.tfvars", "required_environments = []", 'required_environments = ["dev"]')
        self.assertAllowed()


class Functions(Base):

    def test_templatefile(self):
        self.append(STACK_MODULE + "/main.tf", '\nlocals {\n  t = templatefile("x.tpl", {})\n}\n')
        self.assertRefused("functions", STACK_MODULE + "/main.tf", self.lines_of(STACK_MODULE + "/main.tf") - 1,
                           "templatefile() is refused")

    def test_refused_inside_a_heredoc_interpolation(self):
        self.append(STACK_MODULE + "/main.tf", '\nlocals {\n  h = <<EOT\nline ${fileexists("/proc/self/environ")}\nEOT\n}\n')
        self.assertRefused("functions", STACK_MODULE + "/main.tf", detail="fileexists() is refused")

    def test_escapes_in_a_file_path(self):
        """Terraform decodes \\u002f and \\u002e; the policy must never see a path it doesn't decode."""
        for arg in ('"\\u002fproc\\u002fself\\u002fenviron"',
                    '"..\\u002f..\\u002f..\\u002f..\\u002f..\\u002f..\\u002f..\\u002fproc/self/environ"',
                    '"${path.module}/\\u002e\\u002e/\\u002e\\u002e/\\u002e\\u002e/\\u002e\\u002e/\\u002e\\u002e/x"',
                    '"\\U0000002fproc/self/environ"', '"a\\\\b"'):
            with self.subTest(arg=arg):
                self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = file(%s)\n}\n' % arg)
                self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2, "escape")

    def test_a_directive_in_a_heredoc(self):
        self.append(STACK_MODULE + "/main.tf",
                    '\nlocals {\n  h = <<EOT\n%{ if fileexists("/x") }yes%{ endif }\nEOT\n}\n')
        self.assertRefused("functions", STACK_MODULE + "/main.tf", detail="fileexists() is refused")

    def test_each_refused_function(self):
        # The list is written out here, not read from the module: dropping one from the script must fail.
        expected = {"templatefile", "templatestring", "fileexists", "fileset", "filebase64", "filemd5", "filesha1",
                    "filesha256", "filesha512", "filebase64sha256", "filebase64sha512", "abspath", "pathexpand",
                    "nonsensitive"}
        self.assertTrue(expected <= tf_policy.REFUSED_FUNCTIONS)
        for fn in sorted(expected):
            with self.subTest(fn=fn):
                path = G + "/modules/stack/%s.tf" % fn
                self.write(path, 'locals {\n  x = %s("a")\n}\n' % fn)
                self.assertRefused("functions", path, 2, "%s() is refused" % fn)
                os.remove(os.path.join(self.head, path))

    def test_provider_defined_function(self):
        self.write(STACK_MODULE + "/pf.tf", 'locals {\n  x = provider::grafana::anything("a")\n}\n')
        self.assertRefused("functions", STACK_MODULE + "/pf.tf", 2, "provider-defined")

    def test_file_outside_the_allow_list(self):
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = file("${path.module}/../../../../../scripts/tf.py")\n}\n')
        self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2, "outside")

    def test_a_dotdot_path_that_leaves_the_tree(self):
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = file("../../../../../../../../proc/self/environ")\n}\n')
        self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2, "outside")

    def test_an_absolute_path(self):
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = file("/proc/self/environ")\n}\n')
        self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2, "absolute path")

    def test_a_computed_path(self):
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = file("${path.module}/${var.stack}.alloy")\n}\n')
        self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2, "computed path is refused")

    def test_a_for_each_over_file_names_is_a_computed_path(self):
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = { for n in ["a", "b"] : n => file("${path.module}/pipelines/${n}.alloy") }\n}\n')
        self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2, "computed path is refused")

    def test_a_variable_as_the_path(self):
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = file(var.stack)\n}\n')
        self.assertRefused("file() path", STACK_MODULE + "/f.tf", 2)


class Blocks(Base):

    def test_a_provisioner(self):
        self.write(STACK_MODULE + "/p.tf", 'resource "terraform_data" "x" {\n  provisioner "local-exec" {\n    command = "env"\n  }\n}\n')
        self.assertRefused("blocks", STACK_MODULE + "/p.tf", 2, "provisioner blocks are refused")

    def test_resource_local_file(self):
        self.write(STACK_MODULE + "/r.tf", 'resource "local_file" "x" {\n  filename = "x"\n}\n')
        self.assertRefused("blocks", STACK_MODULE + "/r.tf", 1, "resource type 'local_file' is refused")

    def test_data_external(self):
        self.write(STACK_MODULE + "/d.tf", 'data "external" "x" {\n  program = ["env"]\n}\n')
        self.assertRefused("blocks", STACK_MODULE + "/d.tf", 1, "data source 'external' is refused")

    def test_terraform_remote_state(self):
        self.write(STACK_MODULE + "/d.tf", 'data "terraform_remote_state" "x" {\n  backend = "gcs"\n}\n')
        self.assertRefused("blocks", STACK_MODULE + "/d.tf", 1, "terraform_remote_state")

    def test_a_remote_module(self):
        self.write(STACK_MODULE + "/m.tf", 'module "x" {\n  source = "git::https://example.com/x.git"\n}\n')
        self.assertRefused("blocks", STACK_MODULE + "/m.tf", 1, "not a local path")

    def test_an_escaped_module_source(self):
        self.write(SOLO7DEV + "/m.tf", 'module "e" {\n  source = "../../modules/m/\\u002e\\u002e/\\u002e\\u002e/\\u002e\\u002e/evil"\n}\n')
        self.assertRefused("blocks", SOLO7DEV + "/m.tf", 1, "escape")

    def test_an_escaped_run_module_source(self):
        self.replace(TestFiles.TEST, 'source = "../../tests/route"', 'source = "../../tests/route\\u002f..\\u002f..\\u002fevil"')
        self.assertRefused("test files", TestFiles.TEST, detail="run.module must be exactly")

    def test_a_module_outside_modules(self):
        self.write(SOLO7DEV + "/m.tf", 'module "x" {\n  source = "../../test-alert"\n}\n')
        self.assertRefused("blocks", SOLO7DEV + "/m.tf", 1, "outside")

    def test_an_import_block(self):
        self.write(SOLO7DEV + "/i.tf", 'import {\n  to = grafana_folder.x\n  id = "x"\n}\n')
        self.assertRefused("blocks", SOLO7DEV + "/i.tf", 1, "import blocks are refused")

    def test_a_resource_in_tests_route(self):
        self.write(G + "/tests/route/r.tf", 'resource "grafana_folder" "x" {\n  title = "x"\n}\n')
        self.assertRefused("blocks", G + "/tests/route/r.tf", 1, "tests/route has no resources")


class SeparateMerge(Base):

    def test_a_changed_variable(self):
        self.replace(STACK_MODULE + "/variables.tf", "variable \"slack_webhook\" {", "variable \"slack_webhook\" {\n  nullable = true")
        self.assertRefused("separate-merge", STACK_MODULE, detail="variable slack_webhook changed")

    def test_a_removed_sensitive_flag(self):
        self.replace(SOLO7DEV + "/variables.tf", "  sensitive   = true\n", "")
        self.assertRefused("separate-merge", SOLO7DEV, detail="variable slack_webhook changed")

    def test_an_added_output(self):
        self.append(SOLO7DEV + "/main.tf", '\noutput "leak" {\n  value = var.slack_webhook\n}\n')
        self.assertRefused("separate-merge", SOLO7DEV, detail="output leak changed")

    def test_a_changed_output(self):
        self.replace(SOLO7DEV + "/main.tf", "value       = module.stack.policy_tree", "value       = module.stack")
        self.assertRefused("separate-merge", SOLO7DEV, detail="output policy_tree changed")

    def test_a_changed_backend(self):
        self.replace(SOLO7DEV + "/backend.tf", 'prefix = "grafana/solo7dev"', 'prefix = "grafana/solo7"')
        self.assertRefused("separate-merge", SOLO7DEV, detail="backend changed")

    def test_a_changed_provider(self):
        self.replace(SOLO7DEV + "/main.tf", "url                  = var.grafana_url",
                     'url                  = "https://evil.example"')
        self.assertRefused("separate-merge", SOLO7DEV, detail="provider grafana 0 changed")

    def test_changed_required_providers(self):
        self.replace(SOLO7DEV + "/main.tf", 'version = "~> 4.47.0"', 'version = "~> 4.48.0"')
        self.assertRefused("separate-merge", SOLO7DEV, detail="required_providers changed")

    def test_no_base_to_compare_to(self):
        shutil.rmtree(os.path.join(self.base, G))
        self.assertRefused("separate-merge", G, 1, "no deploy/terraform/grafana")


class Endpoints(Base):

    def refused_url(self, url, detail):
        self.replace(SOLO7DEV + "/terraform.tfvars", 'grafana_url = "https://solo7dev.grafana.net"',
                     'grafana_url = "%s"' % url)
        self.assertRefused("endpoints", SOLO7DEV + "/terraform.tfvars", detail=detail)

    def test_a_foreign_host_with_the_real_one_as_prefix(self):
        self.refused_url("https://solo7dev.grafana.net.evil.com", "not one of this stack's hosts")

    def test_user_at_host(self):
        self.refused_url("https://user@solo7dev.grafana.net", "carries a user or a port")

    def test_a_port(self):
        self.refused_url("https://solo7dev.grafana.net:8443", "carries a user or a port")

    def test_a_query(self):
        self.refused_url("https://solo7dev.grafana.net?x=1", "query or fragment")

    def test_a_fragment(self):
        self.refused_url("https://solo7dev.grafana.net#x", "query or fragment")

    def test_http(self):
        self.refused_url("http://solo7dev.grafana.net", "must be https")

    def test_another_stacks_host(self):
        self.refused_url("https://solo7.grafana.net", "not one of this stack's hosts")

    def test_grafana_url_with_a_path(self):
        self.refused_url("https://solo7dev.grafana.net/x", "empty path")

    def test_a_sender_path(self):
        self.replace(SOLO7DEV + "/terraform.tfvars", "/api/prom/push", "/api/v1/write")
        self.assertRefused("endpoints", SOLO7DEV + "/terraform.tfvars", detail="prom_url's path")

    def test_a_user_that_is_not_digits(self):
        self.replace(SOLO7DEV + "/terraform.tfvars", 'prom_user   = "3652529"', 'prom_user   = "3652529x"')
        self.assertRefused("endpoints", SOLO7DEV + "/terraform.tfvars", detail="prom_user must be digits")

    def test_a_changed_endpoint_takes_the_separate_merge_path(self):
        self.replace(SOLO7DEV + "/terraform.tfvars", "logs-prod-021.grafana.net/loki/api/v1/push",
                     "prometheus-prod-67-prod-us-west-0.grafana.net/loki/api/v1/push")
        self.assertRefused("separate-merge", SOLO7DEV + "/terraform.tfvars", detail="endpoint loki_url changed")


class Files(Base):

    def test_auto_tfvars(self):
        self.write(SOLO7DEV + "/x.auto.tfvars", 'slack = false\n')
        self.assertRefused("files", SOLO7DEV + "/x.auto.tfvars", 1)

    def test_another_variable_file(self):
        self.write(SOLO7DEV + "/prod.tfvars", 'slack = false\n')
        self.assertRefused("files", SOLO7DEV + "/prod.tfvars", 1)

    def test_json_configuration(self):
        self.write(SOLO7DEV + "/x.tf.json", '{}\n')
        self.assertRefused("files", SOLO7DEV + "/x.tf.json", 1, "JSON")

    def test_an_override_file(self):
        self.write(SOLO7DEV + "/main_override.tf", 'provider "grafana" {\n  url = "https://evil"\n}\n')
        self.assertRefused("files", SOLO7DEV + "/main_override.tf", 1, "override")

    def test_a_committed_terraform_directory(self):
        self.write(SOLO7DEV + "/.terraform/providers/x", "x\n")
        self.assertRefused("files", SOLO7DEV + "/.terraform/providers/x", 1)

    def test_a_lock_hash_change_without_a_version_change(self):
        lock = SOLO7DEV + "/.terraform.lock.hcl"
        with open(os.path.join(self.head, lock)) as f:
            text = f.read()
        self.write(lock, text.replace('"h1:', '"h1:AAAA', 1))
        self.assertRefused("files", lock, 1, "hashes changed without a version change")

    def test_a_lock_version_change_takes_the_separate_merge_path(self):
        lock = SOLO7DEV + "/.terraform.lock.hcl"
        with open(os.path.join(self.head, lock)) as f:
            text = f.read()
        self.write(lock, text.replace('version     = "4.47.0"', 'version     = "4.48.0"', 1))
        self.assertRefused("separate-merge", lock, 1, "provider version changed")

    def test_a_deleted_lock_file(self):
        os.remove(os.path.join(self.head, SOLO7DEV, ".terraform.lock.hcl"))
        self.assertRefused("files", SOLO7DEV + "/.terraform.lock.hcl", 1, "deleted")

    def test_a_committed_plugin_directory(self):
        self.write(SOLO7DEV + "/terraform.d/plugins/registry.terraform.io/grafana/grafana/4.0.0/linux_amd64/x", "x\n")
        self.assertRefused("files", SOLO7DEV + "/terraform.d/plugins/registry.terraform.io/grafana/grafana/4.0.0/linux_amd64/x",
                           1, "terraform.d")

    def test_terraform_files_outside_deploy_terraform(self):
        for path in ("deploy/grafana/dashboards/evil/main.tf", "deploy/grafana/dashboards/x.tf.json",
                     "deploy/grafana/dashboards/x.tfvars", "deploy/grafana/dashboards/x.hcl"):
            with self.subTest(path=path):
                self.write(path, 'resource "local_file" "x" {}\n')
                self.assertRefused("files", path, 1, "outside deploy/terraform")
                os.remove(os.path.join(self.head, path))

    def test_another_provider_in_the_lock_file(self):
        lock = SOLO7DEV + "/.terraform.lock.hcl"
        self.append(lock, '\nprovider "registry.terraform.io/hashicorp/local" {\n  version = "2.5.0"\n}\n')
        self.assertRefused("files", lock, detail="holds only")


class Symlinks(Base):

    def test_a_symlink_on_disk(self):
        os.symlink("/proc/self/environ", os.path.join(self.head, STACK_MODULE, "environ.alloy"))
        self.assertRefused("symlinks", STACK_MODULE + "/environ.alloy", 1)

    def test_a_symlinked_directory(self):
        os.symlink("/proc/self", os.path.join(self.head, STACK_MODULE, "pipelines"))
        self.assertRefused("symlinks", STACK_MODULE + "/pipelines", 1)

    def test_a_symlink_in_git_is_never_written(self):
        repo = os.path.join(self.dir, "repo")
        copy_tree(repo)
        os.symlink("/proc/self/environ", os.path.join(repo, STACK_MODULE, "environ.alloy"))
        git = ["git", "-C", repo, "-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"]
        subprocess.run(git[:3] + ["init", "-q"], check=True)
        subprocess.run(git + ["add", "-A"], check=True)
        subprocess.run(git + ["commit", "-qm", "x"], check=True)
        out = os.path.join(self.dir, "out")
        items = tf_policy.extract(repo, "HEAD", out)
        self.assertIn(("symlinks", STACK_MODULE + "/environ.alloy", 1, "a symlink in git (mode 120000) is refused"),
                      items)
        self.assertFalse(os.path.lexists(os.path.join(out, STACK_MODULE, "environ.alloy")))
        self.assertTrue(os.path.isfile(os.path.join(out, SOLO7DEV, "main.tf")))


class TestFiles(Base):
    TEST = SOLO7DEV + "/tests/policy.tftest.hcl"

    def test_a_block_outside_the_list(self):
        self.append(self.TEST, '\nprovider "grafana" {\n  url = "https://evil"\n}\n')
        self.assertRefused("test files", self.TEST, detail="provider blocks are refused in a test file")

    def test_another_mock_provider(self):
        self.append(self.TEST, '\nmock_provider "local" {}\n')
        self.assertRefused("test files", self.TEST, detail="mock_provider 'local'")

    def test_a_run_module_elsewhere(self):
        self.replace(self.TEST, 'source = "../../tests/route"', 'source = "../../../test-alert"')
        self.assertRefused("test files", self.TEST, detail="run.module must be exactly")

    def test_a_run_attribute_outside_the_list(self):
        self.replace(self.TEST, "command = apply", "command = apply\n  state_key = \"x\"")
        self.assertRefused("test files", self.TEST, detail="run.state_key is refused")

    def test_a_test_file_outside_tests(self):
        self.write(G + "/modules/stack/x.tftest.hcl", 'run "x" {}\n')
        self.assertRefused("files", G + "/modules/stack/x.tftest.hcl", 1)


class Main(Base):

    def test_exit_codes_and_the_message(self):
        self.assertEqual(tf_policy.main(["check", "--base", self.base, "--head", self.head]), 0)
        self.write(STACK_MODULE + "/f.tf", 'locals {\n  x = templatefile("a", {})\n}\n')
        err = []
        with mock.patch("sys.stderr") as stderr:
            stderr.write.side_effect = err.append
            self.assertEqual(tf_policy.main(["check", "--base", self.base, "--head", self.head]), 1)
        self.assertIn("tf-policy: refused: functions: %s/f.tf:2: templatefile() is refused" % STACK_MODULE, "".join(err))
        self.assertEqual(tf_policy.main([]), 2)


class Tokenizer(unittest.TestCase):

    def test_escapes_and_literal_interpolation_markers_are_not_calls(self):
        items = tf_policy.parse('locals {\n  a = "$${templatefile(x)}"\n  b = "%%{if}"\n}\n')
        f = tf_policy.Findings()
        tf_policy.check_expressions(f, STACK_MODULE + "/x.tf", items, "/nonexistent")
        self.assertEqual(f.items, [])

    def test_comments_are_not_calls(self):
        items = tf_policy.parse('# templatefile(x)\n// fileset(x)\n/* abspath(x) */\nlocals {\n  a = 1\n}\n')
        f = tf_policy.Findings()
        tf_policy.check_expressions(f, STACK_MODULE + "/x.tf", items, "/nonexistent")
        self.assertEqual(f.items, [])

    def test_a_call_in_a_nested_interpolation(self):
        items = tf_policy.parse('locals {\n  a = "x${"y${templatefile("a", {})}"}"\n}\n')
        f = tf_policy.Findings()
        tf_policy.check_expressions(f, STACK_MODULE + "/x.tf", items, "/nonexistent")
        self.assertEqual([i[0] for i in f.items], ["functions"])



class Syntax(Base):

    def test_unparseable_is_refused(self):
        self.write(STACK_MODULE + "/bad.tf", 'locals {\n  a = "unterminated\n}\n')
        self.assertRefused("syntax", STACK_MODULE + "/bad.tf", 2)

if __name__ == "__main__":
    unittest.main()
