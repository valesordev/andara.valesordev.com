# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-065's render and keep-list tests (AC-4, AC-5, AC-8, AC-9): `helm template` of the pinned
k8s-monitoring chart with each profile, read back by scripts/collectors_check.py. Each invariant passes on
the committed files and fails, naming itself, on a fixture that breaks it. Both profiles are rendered once
(the chart is pulled into .local/charts/ on first use); a fixture edits a copy of that render, so the
check reads what Helm produced, not what the values asked for."""
import copy
import glob
import os
import re
import subprocess
import sys
import tempfile
import unittest

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import collectors  # noqa: E402
import collectors_check as check  # noqa: E402
import tf  # noqa: E402

_RENDERS = {}


def renders():
    """{profile: (values, docs)} for the committed files, rendered once per test run."""
    if not _RENDERS:
        for p in ("box", "ci"):
            vals = collectors.values(p)
            _RENDERS[p] = (vals, collectors.template(vals))
    return copy.deepcopy(_RENDERS)


def edit_config(rs, profile, collector, old, new, count=1):
    """rs with collector's rendered config.alloy edited: old replaced by new, which must occur."""
    vals, docs = rs[profile]
    for d in docs:
        if d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "k8s-monitoring-" + collector:
            text = d["data"]["config.alloy"]
            assert text.count(old) >= count, (collector, old)
            d["data"]["config.alloy"] = text.replace(old, new)
            return rs
    raise AssertionError("no collector %s in %s" % (collector, profile))


class Base(unittest.TestCase):

    def problems(self, rs=None, profiles=None, envs=None, kind_text=None, prod_tfvars=None, keep=None):
        rs = rs or renders()
        profiles = profiles or {p: collectors.profile_doc(p) for p in rs}
        return check.render_problems(rs, envs or collectors.environments(), profiles, collectors.chart(),
                                     kind_text=kind_text, prod_tfvars=prod_tfvars, keep=keep)

    def assertProblem(self, pattern, problems):
        self.assertTrue([p for p in problems if re.search(pattern, p)],
                        "no problem matches %r in:\n%s" % (pattern, "\n".join(problems) or "(none)"))


class Committed(Base):

    def test_the_committed_render_has_no_problems(self):
        self.assertEqual(self.problems(), [])

    def test_every_collector_config_validates(self):
        """The rendered Alloy parses (bin/alloy validate), so a values change can't ship a config the
        collectors refuse."""
        alloy = os.environ.get("ALLOY") or os.path.join(REPO, "bin", "alloy")
        if not os.access(alloy, os.X_OK):
            self.fail("%s not found; run `make bootstrap`" % alloy)
        d = tempfile.mkdtemp()
        for profile, (_, docs) in renders().items():
            for name, cfg in collectors.alloy_configs(docs).items():
                path = os.path.join(d, "%s-%s.alloy" % (profile, name))
                with open(path, "w") as f:
                    f.write(cfg)
                p = subprocess.run([alloy, "validate", path], capture_output=True, text=True)
                self.assertEqual(p.returncode, 0, "%s/%s: %s" % (profile, name, p.stderr or p.stdout))

    def test_the_rendered_values_are_the_same_on_every_run(self):
        """AC-1: platform-up compares the merged values, so they must render identically."""
        self.assertEqual(yaml.safe_dump(collectors.values("box")), yaml.safe_dump(collectors.values("box")))

    def test_the_alloy_crd_is_in_the_release(self):
        """[verify 065] item 2: Helm installs a chart's crds/ before its templates, so the Alloy CRD
        precedes every collector in one install."""
        docs = renders()["box"][1]
        crds = [d["metadata"]["name"] for d in docs if d.get("kind") == "CustomResourceDefinition"]
        self.assertIn("alloys.collectors.grafana.com", crds)
        self.assertEqual(sorted(d["metadata"]["name"] for d in docs if d.get("kind") == "Alloy"),
                         ["k8s-monitoring-fleet", "k8s-monitoring-logs", "k8s-monitoring-metrics",
                          "k8s-monitoring-receiver"])

    def test_fleet_registers_with_cluster_and_stack_only(self):
        """AC-3's attributes: cluster and stack, never environment, and each key once."""
        cfg = collectors.alloy_configs(renders()["box"][1])["fleet"]
        remotecfg = check._body(cfg, cfg.index("{", cfg.index("remotecfg {")))
        block = re.search(r"attributes\s*=\s*\{([^}]*)\}", remotecfg).group(1)
        attrs = re.findall(r'^\s*"([^"]+)"\s*=\s*"([^"]*)",', block, re.M)
        keys = [k for k, _ in attrs]
        self.assertEqual(len(keys), len(set(keys)), keys)
        self.assertEqual(dict(attrs).get("cluster"), "andara")
        self.assertEqual(dict(attrs).get("stack"), "solo7dev")
        self.assertNotIn("environment", keys)

    def test_no_collector_holds_the_token_in_its_environment(self):
        """Grafana's default Fleet pipelines write to the stack with sys.env("GCLOUD_RW_API_KEY"), past
        the stamp and keep rule; fleet's is empty, and no collector env-references the Secret."""
        for profile, (_, docs) in renders().items():
            for d in docs:
                if d.get("kind") != "Alloy":
                    continue
                env = ((d.get("spec") or {}).get("alloy") or {}).get("extraEnv") or []
                for e in env:
                    ref = ((e.get("valueFrom") or {}).get("secretKeyRef") or {})
                    self.assertNotEqual(ref.get("name"), "grafana-telemetry", (profile, d["metadata"]["name"], e))
                if d["metadata"]["name"] == "k8s-monitoring-fleet":
                    self.assertIn({"name": "GCLOUD_RW_API_KEY", "value": ""}, env)

    def test_the_destinations_read_the_secret_never_a_value(self):
        for profile, (vals, _) in renders().items():
            for name, dest in vals["destinations"].items():
                self.assertEqual(dest["secret"], {"create": False, "name": "grafana-telemetry",
                                                  "namespace": "observability"}, name)
                self.assertEqual(dest["auth"]["passwordKey"], "token", name)
                self.assertNotIn("password", dest["auth"], name)


class Stamp(Base):
    """AC-8: the stamp, its guard, and box/ci parity."""

    def test_a_metrics_stamp_whose_table_differs_fails(self):
        rs = edit_config(renders(), "box", "metrics", 'regex = "andara-dev"', 'regex = "andara-dex"')
        self.assertProblem(r"box/metrics: solo7dev-metrics' stamp encodes", self.problems(rs))

    def test_a_logs_stamp_whose_table_differs_fails(self):
        rs = edit_config(renders(), "box", "logs", 'environment = "dev"', 'environment = "staging"')
        self.assertProblem(r"box/logs: solo7dev-logs' stamp encodes", self.problems(rs))

    def test_a_traces_stamp_whose_table_differs_fails(self):
        rs = edit_config(renders(), "ci", "receiver", '== \\"andara-dev\\"', '== \\"andara-dex\\"')
        self.assertProblem(r"ci/receiver: solo7dev-traces' stamp encodes", self.problems(rs))

    def test_a_stamp_against_another_table_fails(self):
        envs = dict(collectors.environments(), **{"andara-qa": "staging"})
        self.assertProblem(r"stamp encodes", self.problems(envs=envs))

    def test_a_metrics_destination_without_the_guard_fails(self):
        rs = edit_config(renders(), "box", "metrics", 'replacement = "unknown"', 'replacement = "local"')
        self.assertProblem(r"box/metrics: solo7dev-metrics has no unknown guard", self.problems(rs))

    def test_a_logs_destination_without_the_guard_fails(self):
        rs = edit_config(renders(), "box", "receiver", 'environment = "unknown"', 'environment = "local"')
        self.assertProblem(r"box/receiver: solo7dev-logs has no unknown guard", self.problems(rs))

    def test_a_traces_destination_without_the_guard_fails(self):
        rs = edit_config(renders(), "box", "receiver", '== nil', '== \\"x\\"')
        self.assertProblem(r"box/receiver: solo7dev-traces has no unknown guard", self.problems(rs))

    def test_a_ci_block_that_differs_from_box_fails(self):
        rs = edit_config(renders(), "ci", "metrics", 'regex = "andara-prod"', 'regex = "andara-prod|andara-x"')
        self.assertProblem(r"ci: the stamp, guard and keep blocks differ from box's", self.problems(rs))

    def test_a_guard_that_drops_instead_of_stamping_fails(self):
        """Reviewer (a): a guard with action drop removes every series outside the table."""
        rs = edit_config(renders(), "box", "metrics", 'replacement = "unknown"\n',
                         'replacement = "unknown"\n      action = "drop"\n')
        problems = self.problems(rs)
        self.assertProblem(r"box/metrics: solo7dev-metrics' rules are not what environments.yaml and keep-list.yaml "
                           r"render, at 4: .*'drop'", problems)
        self.assertProblem(r"box/metrics: solo7dev-metrics has no unknown guard \(a replace\)", problems)

    def test_a_kube_drop_rule_that_keeps_fails(self):
        """Reviewer (b): action keep on A1's last rule drops every series but kube_ ones."""
        rs = edit_config(renders(), "ci", "receiver", 'regex = "drop"\n      action = "drop"',
                         'regex = "drop"\n      action = "keep"')
        problems = self.problems(rs)
        self.assertProblem(r"ci/receiver: solo7dev-metrics' rules are not what", problems)
        self.assertProblem(r"ci/receiver: solo7dev-metrics' kube_ namespace rule keeps \[\]", problems)

    def test_a_kube_mark_that_matches_nothing_fails(self):
        """Reviewer (c): A1 switched off by its first rule."""
        rs = edit_config(renders(), "box", "metrics", 'regex = "kube_.+"', 'regex = "kube_NOPE"')
        problems = self.problems(rs)
        self.assertProblem(r"box/metrics: solo7dev-metrics' rules are not what", problems)
        self.assertProblem(r"box/metrics: solo7dev-metrics' kube_ namespace rule keeps \[\]", problems)

    def test_a_keep_rule_that_no_longer_matches_the_keep_list_fails(self):
        """Reviewer (d): the keep regex cut to `up`."""
        names = "|".join(sorted(e["name"] for e in collectors.keep_list()))
        rs = edit_config(renders(), "box", "metrics", 'regex = "%s"' % names, 'regex = "up"')
        problems = self.problems(rs)
        self.assertProblem(r"box/metrics: solo7dev-metrics' rules are not what", problems)
        self.assertProblem(r"box/metrics: solo7dev-metrics' keep rule keeps 1 names, not keep-list.yaml's", problems)

    def test_a_logs_stage_with_an_extra_attribute_fails(self):
        """Round 2: only the full comparison reads a stage.match's action."""
        rs = edit_config(renders(), "box", "logs", 'selector = "{namespace=~\\"andara-dev\\"}"\n',
                         'selector = "{namespace=~\\"andara-dev\\"}"\n    action = "drop"\n')
        self.assertProblem(r"box/logs: solo7dev-logs' stages are not what environments.yaml renders", self.problems(rs))

    def test_a_logs_stage_after_label_keep_fails(self):
        rs = edit_config(renders(), "box", "receiver", '"job", "container"]\n  }\n',
                         '"job", "container"]\n  }\n  stage.drop {\n    expression = ".*"\n  }\n')
        problems = self.problems(rs)
        self.assertProblem(r"box/receiver: solo7dev-logs' stages are not what environments.yaml renders", problems)
        self.assertProblem(r"box/receiver: solo7dev-logs keeps stream labels \['stage.drop'\]", problems)

    def test_a_trace_statement_that_removes_the_environment_fails(self):
        rs = edit_config(renders(), "ci", "receiver", 'where attributes[\\"environment\\"] == nil",\n',
                         'where attributes[\\"environment\\"] == nil",\n      `delete_key(attributes, "environment")`,\n')
        self.assertProblem(r"ci/receiver: solo7dev-traces' environment statements are not what", self.problems(rs))

    def test_a_second_cluster_stage_fails(self):
        """Round 3: only the chart's one cluster stage is skipped, and only with the profile's name."""
        rs = edit_config(renders(), "box", "logs", 'stage.static_labels {\n    values = {\n      "cluster" = "andara",\n    }\n  }\n',
                         'stage.static_labels {\n    values = {\n      "cluster" = "andara",\n    }\n  }\n'
                         '  stage.static_labels {\n    values = {\n      "cluster" = "solo7",\n    }\n  }\n')
        self.assertProblem(r"box/logs: solo7dev-logs' stages are not what environments.yaml renders", self.problems(rs))

    def test_a_trace_statement_after_the_stamp_that_names_nothing_fails(self):
        rs = edit_config(renders(), "ci", "receiver", 'where attributes[\\"environment\\"] == nil",\n',
                         'where attributes[\\"environment\\"] == nil",\n      `keep_keys(attributes, ["service.name"])`,\n')
        self.assertProblem(r"ci/receiver: solo7dev-traces' environment statements are not what", self.problems(rs))

    def test_a_render_against_another_keep_list_fails(self):
        keep = [e for e in collectors.keep_list() if e["name"] != "andara_ticks_total"]
        self.assertProblem(r"solo7dev-metrics' rules are not what", self.problems(keep=keep))

    def test_a_kube_regex_that_differs_from_the_table_fails(self):
        """A1."""
        rs = edit_config(renders(), "box", "receiver", 'drop;(andara-ci|', 'drop;(andara-shared|andara-ci|')
        self.assertProblem(r"kube_ namespace rule keeps .*\(A1\)", self.problems(rs))

    def test_a_keep_rule_that_is_not_last_fails(self):
        rs = renders()
        vals, docs = rs["box"]
        for d in docs:
            if d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "k8s-monitoring-metrics":
                cfg = d["data"]["config.alloy"]
                keep = '|traefik_router_requests_total|up"\n      action = "keep"\n    }\n'
                self.assertIn(keep, cfg)
                d["data"]["config.alloy"] = cfg.replace(
                    keep, keep + '    write_relabel_config {\n      source_labels = ["job"]\n'
                    '      target_label = "x"\n    }\n', 1)
        self.assertProblem(r"box/metrics: solo7dev-metrics' keep rule is not its last rule", self.problems(rs))


class Generator(unittest.TestCase):
    """The rules collectors.py renders, pinned for a small table, so the render checks (which compare the
    chart's output with this generator) can't be passed by a defect that changes both sides."""

    ENVS = {"ns-b": "dev", "ns-a": "local", "ns-c": "local"}
    KEEP = [{"name": "up", "reader": "r"}, {"name": "b_total", "reader": "r"}]

    def test_metric_rules(self):
        self.assertEqual(collectors.metric_rules(self.ENVS, self.KEEP), "\n".join([
            'write_relabel_config {\n  source_labels = ["namespace"]\n  regex = "ns-a|ns-c"\n'
            '  target_label = "environment"\n  replacement = "local"\n}',
            'write_relabel_config {\n  source_labels = ["namespace"]\n  regex = "ns-b"\n'
            '  target_label = "environment"\n  replacement = "dev"\n}',
            'write_relabel_config {\n  source_labels = ["environment"]\n  regex = ""\n'
            '  target_label = "environment"\n  replacement = "unknown"\n}',
            'write_relabel_config {\n  source_labels = ["__name__"]\n  regex = "kube_.+"\n'
            '  target_label = "__tmp_kube_namespace"\n  replacement = "drop"\n}',
            'write_relabel_config {\n  source_labels = ["__tmp_kube_namespace", "namespace"]\n  separator = ";"\n'
            '  regex = "drop;(ns-a|ns-b|ns-c)"\n  target_label = "__tmp_kube_namespace"\n  replacement = ""\n}',
            'write_relabel_config {\n  source_labels = ["__tmp_kube_namespace"]\n  regex = "drop"\n  action = "drop"\n}',
            'write_relabel_config {\n  source_labels = ["__name__"]\n  regex = "b_total|up"\n  action = "keep"\n}',
        ]) + "\n")

    def test_log_stages(self):
        self.assertEqual(collectors.log_stages(self.ENVS), "\n".join([
            'stage.match {\n  selector = "{namespace=~\\"ns-a|ns-c\\"}"\n  stage.static_labels {\n    values = {\n'
            '      environment = "local",\n    }\n  }\n}',
            'stage.match {\n  selector = "{namespace=~\\"ns-b\\"}"\n  stage.static_labels {\n    values = {\n'
            '      environment = "dev",\n    }\n  }\n}',
            'stage.match {\n  selector = "{environment=\\"\\"}"\n  stage.static_labels {\n    values = {\n'
            '      environment = "unknown",\n    }\n  }\n}',
            'stage.label_keep {\n  values = ["environment", "namespace", "cluster", "job", "container"]\n}',
        ]) + "\n")

    def test_trace_statements(self):
        self.assertEqual(collectors.trace_statements(self.ENVS), [
            'set(attributes["environment"], "local") where attributes["k8s.namespace.name"] == "ns-a" or '
            'attributes["k8s.namespace.name"] == "ns-c"',
            'set(attributes["environment"], "dev") where attributes["k8s.namespace.name"] == "ns-b"',
            'set(attributes["environment"], "unknown") where attributes["environment"] == nil',
        ])


class Collection(Base):
    """AC-8: the include-lists, the remap, the receiver, the OTLP-log drop, cluster.name, the profiles."""

    def test_an_include_list_naming_andara_shared_fails(self):
        rs = edit_config(renders(), "box", "metrics", '"andara-local","andara-dev","traefik"',
                         '"andara-local","andara-dev","traefik","andara-shared"')
        self.assertProblem(r"box: annotation autodiscovery includes andara-shared", self.problems(rs))

    def test_autodiscovery_without_the_remap_fails(self):
        rs = edit_config(renders(), "box", "metrics", "__meta_kubernetes_pod_annotation_prometheus_io_port",
                         "__meta_kubernetes_pod_annotation_k8s_grafana_com_metrics_portNumber", count=1)
        self.assertProblem(r"does not read prometheus.io/port", self.problems(rs))

    def test_a_job_read_from_another_annotation_fails(self):
        rs = edit_config(renders(), "box", "metrics", '"__meta_kubernetes_pod_annotation_k8s_grafana_com_job"',
                         '"__meta_kubernetes_pod_annotation_app_job"')
        self.assertProblem(r"reads job from .*app_job", self.problems(rs))

    def test_a_receiver_service_with_another_name_fails(self):
        rs = renders()
        for d in rs["box"][1]:
            if d.get("kind") == "Service" and d["metadata"]["name"] == check.RECEIVER_SERVICE:
                d["metadata"]["name"] = "k8s-monitoring-alloy"
        self.assertProblem(r"box: no Service k8s-monitoring-alloy-receiver", self.problems(rs))

    def test_a_receiver_service_without_a_port_fails(self):
        for port in check.RECEIVER_PORTS:
            with self.subTest(port=port):
                rs = renders()
                for d in rs["ci"][1]:
                    if d.get("kind") == "Service" and d["metadata"]["name"] == check.RECEIVER_SERVICE:
                        d["spec"]["ports"] = [p for p in d["spec"]["ports"] if p["port"] != port]
                self.assertProblem(r"ci: Service k8s-monitoring-alloy-receiver has no port %d" % port, self.problems(rs))

    def test_a_missing_otlp_log_drop_fails(self):
        rs = edit_config(renders(), "box", "receiver", 'log_record = [\n        "true",\n      ]', 'log_record = []')
        self.assertProblem(r"box: the receiver does not drop every OTLP log record", self.problems(rs))

    def test_ci_without_cluster_name_ci_fails(self):
        """A3."""
        rs = renders()
        for name in ("metrics", "logs", "receiver"):
            rs = edit_config(rs, "ci", name, '"cluster" = "ci"', '"cluster" = "andara"')
        self.assertProblem(r'ci/metrics: solo7dev-metrics carries cluster=andara, not "ci"', self.problems(rs))
        self.assertProblem(r'ci/logs: solo7dev-logs carries cluster=andara, not "ci"', self.problems(rs))

    def test_destinations_without_a_cluster_label_fail(self):
        """A3, the other way: clusterLabels emptied leaves no cluster on any signal."""
        rs = renders()
        rs = edit_config(rs, "box", "metrics", '"cluster" = "andara",\n', '')
        rs = edit_config(rs, "box", "receiver", '`set(attributes["cluster"], "andara")`,\n', '')
        problems = self.problems(rs)
        self.assertProblem(r'box/metrics: solo7dev-metrics carries cluster=None, not "andara"', problems)
        self.assertProblem(r'box/receiver: solo7dev-traces does not set cluster="andara"', problems)

    def test_a_log_label_added_after_label_keep_fails(self):
        """extraLabels on the logs destination renders into loki.write, after label_keep."""
        rs = edit_config(renders(), "box", "logs", '"cluster" = "andara",\n', '"cluster" = "andara",\n    pod = "x",\n')
        self.assertProblem(r"box/logs: solo7dev-logs adds stream labels outside the allow-list after label_keep: pod",
                           self.problems(rs))

    def test_fleet_with_a_writer_of_its_own_fails(self):
        """fleet's only output is the receiver, through Fleet's pipelines (ADR-0014 §3)."""
        rs = edit_config(renders(), "box", "fleet", "remotecfg {",
                         'prometheus.remote_write "direct" {\n  endpoint {\n    url = "https://x"\n  }\n}\n\nremotecfg {')
        self.assertProblem(r"box: fleet renders its own writers \(prometheus.remote_write\)", self.problems(rs))

    def test_values_ci_sets_cluster_name_ci(self):
        self.assertEqual(collectors.profile_doc("ci")["cluster"]["name"], "ci")

    def test_a_profile_setting_another_key_fails(self):
        profiles = {p: collectors.profile_doc(p) for p in ("box", "ci")}
        profiles["ci"]["clusterMetrics"] = {"enabled": False}
        self.assertProblem(r"ci: values-ci.yaml sets clusterMetrics.enabled, outside the keys",
                           self.problems(profiles=profiles))

    def test_the_profiles_differ_only_in_the_allowed_keys(self):
        for p in ("box", "ci"):
            self.assertEqual(collectors.profile_problems(collectors.profile_doc(p)), [])

    def test_a_default_allow_list_left_on_fails(self):
        rs = renders()
        rs["box"][0]["clusterMetrics"]["kube-state-metrics"]["metricsTuning"]["useDefaultAllowList"] = True
        self.assertProblem(r"box: clusterMetrics.kube-state-metrics.metricsTuning.useDefaultAllowList is True",
                           self.problems(rs))

    def test_a_chart_default_not_set_false_fails(self):
        rs = renders()
        del rs["ci"][0]["clusterMetrics"]["cadvisor"]
        self.assertProblem(r"ci: clusterMetrics.cadvisor.metricsTuning.useDefaultAllowList is not set false",
                           self.problems(rs))

    def test_a_sixth_log_stream_label_fails(self):
        rs = edit_config(renders(), "box", "logs", '"job", "container"]', '"job", "container", "pod"]')
        self.assertProblem(r"box/logs: solo7dev-logs keeps stream labels .*pod", self.problems(rs))

    def test_otlp_records_naming_a_label_outside_the_allow_list_fail(self):
        rs = edit_config(renders(), "box", "receiver", 'cluster, namespace, job, container', 'cluster, namespace, job, pod')
        self.assertProblem(r"OTLP log records name stream labels outside the allow-list: pod", self.problems(rs))

    def test_a_ci_include_list_that_misses_a_kind_namespace_fails(self):
        with open(check.KIND_WORKFLOW) as f:
            kind = f.read() + "\n          NS=andara-lifecycle\n"
        self.assertProblem(r"ci: annotation autodiscovery misses andara-lifecycle", self.problems(kind_text=kind))

    def test_a_ci_include_list_naming_a_namespace_kind_never_uses_fails(self):
        rs = edit_config(renders(), "ci", "metrics", '"andara-local","andara-ci","traefik"',
                         '"andara-local","andara-ci","traefik","andara-dev"')
        self.assertProblem(r"includes andara-dev, which kind.yaml never deploys into", self.problems(rs))

    def test_the_ci_include_list_covers_kind_yaml(self):
        with open(check.KIND_WORKFLOW) as f:
            self.assertEqual(check.kind_namespaces(f.read()), ["andara-local"])


class Prod(Base):
    """AC-5: no prod (solo7) credential name or endpoint is rendered for this cluster."""

    def test_the_committed_render_names_no_prod_endpoint(self):
        self.assertTrue(check.prod_values())
        self.assertFalse([p for p in self.problems() if "prod" in p])

    def test_prods_fleet_endpoint_in_the_render_fails(self):
        fleet = check.prod_values()["fleet_url"]
        rs = renders()
        rs["box"][0]["collectors"]["fleet"]["remoteConfig"]["url"] = fleet
        self.assertProblem(r"box: the render names prod's fleet_url", self.problems(rs))

    def test_prods_tempo_host_as_rendered_for_grpc_fails(self):
        """Tempo renders as host:443, never as the tfvars url, so the host alone must be caught."""
        rs = renders()
        rs["box"][0]["destinations"]["solo7dev-traces"]["url"] = collectors.tempo_grpc(check.prod_values()["tempo_url"])
        self.assertProblem(r"box: the render names prod's tempo_url", self.problems(rs))

    def test_a_prod_credential_name_in_the_render_fails(self):
        rs = edit_config(renders(), "ci", "metrics", 'name      = "grafana-telemetry"', 'name      = "GRAFANA_SOLO7_TELEMETRY"')
        self.assertProblem(r"ci: the render names GRAFANA_SOLO7_", self.problems(rs))


class KeepList(Base):
    """AC-9."""

    def test_the_committed_keep_list_has_no_problems(self):
        self.assertEqual(check.keep_problems(collectors.keep_list(), check.source_names(), check.pipelines()), [])

    def test_every_dashboard_is_read(self):
        dashboards = sorted(glob.glob(os.path.join(check.DASHBOARDS, "*.json")))
        self.assertTrue(dashboards)
        names = check.source_names()
        for path in dashboards:
            self.assertLessEqual(check._dashboard_names(path), names, path)

    def test_a_name_a_rule_reads_but_the_list_misses_fails(self):
        keep = [e for e in collectors.keep_list() if e["name"] != "andara_snapshot_age_seconds"]
        self.assertIn("andara_snapshot_age_seconds: read by a rule or dashboard panel, missing from keep-list.yaml",
                      check.keep_problems(keep, check.source_names(), check.pipelines()))

    def test_a_fleet_name_the_pipeline_does_not_keep_fails(self):
        keep = collectors.keep_list() + [{"name": "kafka_server_brokertopicmetrics_messagesin_total",
                                          "reader": "x", "owner": "fleet"}]
        self.assertIn("kafka_server_brokertopicmetrics_messagesin_total: owner fleet, but shared_kafka.alloy does not keep it",
                      check.keep_problems(keep, check.source_names(), check.pipelines()))

    def test_a_name_the_pipeline_keeps_but_the_list_misses_fails(self):
        keep = [e for e in collectors.keep_list() if e["name"] != "kafka_server_replicamanager_isrexpandspersec_total"]
        self.assertIn("kafka_server_replicamanager_isrexpandspersec_total: shared_kafka.alloy keeps it, but keep-list.yaml "
                      "does not list it with owner: fleet, so the destination drops it",
                      check.keep_problems(keep, check.source_names(), check.pipelines()))

    def test_a_pg_name_with_no_pipeline_fails(self):
        keep = collectors.keep_list() + [{"name": "pg_up", "reader": "x", "owner": "fleet"}]
        self.assertIn("pg_up: owner fleet, but no pipeline shared_postgres.alloy keeps it",
                      check.keep_problems(keep, check.source_names(), check.pipelines()))

    def test_ownership_follows_the_prefix(self):
        d = tempfile.mkdtemp()
        path = os.path.join(d, "keep-list.yaml")
        for entry, why in (({"name": "kafka_x", "reader": "r"}, "owner: fleet"),
                           ({"name": "andara_x", "reader": "r", "owner": "fleet"}, "owner: fleet"),
                           ({"name": "andara_x"}, "names no reader"),
                           ({"name": "up", "reader": "r", "extra": 1}, "unknown field")):
            with self.subTest(entry=entry):
                with open(path, "w") as f:
                    yaml.safe_dump({"metrics": [entry]}, f)
                with self.assertRaisesRegex(collectors.Invalid, why):
                    collectors.keep_list(path)

    def test_up_is_listed_once_and_shared(self):
        ups = [e for e in collectors.keep_list() if e["name"] == "up"]
        self.assertEqual(len(ups), 1)
        self.assertNotIn("owner", ups[0])


class Environments(unittest.TestCase):

    def write(self, doc):
        d = tempfile.mkdtemp()
        path = os.path.join(d, "environments.yaml")
        with open(path, "w") as f:
            yaml.safe_dump(doc, f)
        return path

    def test_the_committed_table_is_decision_9s(self):
        self.assertEqual(collectors.environments(), {"andara-local": "local", "andara-ci": "local",
                                                     "andara-dev": "dev", "andara-staging": "staging",
                                                     "andara-prod": "prod"})

    def test_unknown_is_reserved_and_values_are_bounded(self):
        for doc, why in (({"andara-x": "unknown"}, "reserved"), ({"andara-x": "qa"}, "not one of"),
                         ({"Andara": "dev"}, "not a namespace"), ({}, "non-empty")):
            with self.subTest(doc=doc), self.assertRaisesRegex(collectors.Invalid, why):
                collectors.environments(self.write(doc))


class Runbooks(unittest.TestCase):
    """AC-8: the runbooks' LogQL selects on allow-listed stream labels only."""

    def test_the_committed_runbooks_pass(self):
        self.assertEqual(check.runbook_problems(), [])

    def test_a_service_name_selector_fails(self):
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "x.md"), "w") as f:
            f.write('```\n{service_name="andara-server"} |= "refused"\n```\n'
                    'PromQL is not LogQL: `up{job="andara-server", pod="andara-0"}`\n')
        problems = check.runbook_problems(d)
        self.assertEqual(len(problems), 1, problems)
        self.assertIn("names service_name, outside the stream-label allow-list", problems[0])

    def test_every_logql_shape_is_read(self):
        """A bare service_name selector, a log range function, a filter on the next line, a ```logql block;
        PromQL beside them is not LogQL."""
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "z.md"), "w") as f:
            f.write('`{service_name="andara-server"}`\n'
                    '`sum(count_over_time({app="andara"}[5m]))`\n'
                    '```\n{namespace="andara-dev", pod=~"andara-.*"}\n  |= "refused"\n```\n'
                    '```logql\n{instance="andara-0"}\n```\n'
                    '`rate(up{job="andara-server"}[5m])` and `{job="andara-server", pod="andara-0"}`\n')
        problems = check.runbook_problems(d)
        for label in ("service_name", "app", "pod", "instance"):
            self.assertTrue([p for p in problems if "names %s," % label in p], (label, problems))
        self.assertEqual(len(problems), 4, problems)

    def test_promql_comparisons_are_not_logql_and_pattern_filters_and_unwrap_are(self):
        """Round 2: `!= 0` is PromQL's comparison; `|>` and `| unwrap` under *_over_time are LogQL's."""
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "w.md"), "w") as f:
            f.write('`kafka_consumergroup_lag{topic="andara.events"} != 0`\n'
                    '`andara_tick_overruns_total{pod="andara-0"} !=\n 0`\n'
                    '`{namespace="andara-dev", stream="x"} |> "<_> refused"`\n'
                    '`{namespace="andara-dev", filename="x"} | unwrap latency`\n'
                    '`rate_counter({namespace="andara-dev", node="x"}[5m])`\n'
                    '`sum_over_time({namespace="andara-dev", host="x"} | label_format a="b" [5m])`\n')
        problems = check.runbook_problems(d)
        self.assertEqual(sorted(re.search(r"names (\w+),", p).group(1) for p in problems),
                         ["filename", "host", "node", "stream"], problems)

    def test_a_pod_selector_with_a_parser_fails(self):
        d = tempfile.mkdtemp()
        with open(os.path.join(d, "y.md"), "w") as f:
            f.write('`{namespace="andara-dev", pod="andara-0"} | json`\n')
        self.assertIn("names pod", check.runbook_problems(d)[0])


class Token(unittest.TestCase):
    """AC-4: the telemetry token is in no tracked file."""

    def tracked(self):
        out = subprocess.run(["git", "-C", REPO, "ls-files", "-z"], capture_output=True, text=True, check=True).stdout
        for rel in out.split("\0"):
            path = os.path.join(REPO, rel)
            if rel and os.path.isfile(path) and not os.path.islink(path):
                try:
                    with open(path, encoding="utf-8") as f:
                        yield rel, f.read()
                except UnicodeDecodeError:
                    continue

    def test_no_tracked_file_holds_a_grafana_cloud_token(self):
        pattern = re.compile(r"glc_[A-Za-z0-9+/=_-]{32,}")
        self.assertEqual([rel for rel, text in self.tracked() if pattern.search(text)], [])

    def test_no_tracked_file_holds_the_tokens_value(self):
        """Read from the environment or .local/box.env when either has it; nothing to compare otherwise."""
        value = tf.load_box_env(dict(os.environ)).get(collectors.TOKEN, "")
        if len(value) < 16:
            return
        self.assertEqual([rel for rel, text in self.tracked() if value in text], [])


if __name__ == "__main__":
    unittest.main()
