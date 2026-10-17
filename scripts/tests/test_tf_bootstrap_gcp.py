# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make tf-bootstrap-gcp` (AW-INF-046 AC-2, AC-3) against an in-memory GCP, GitHub and Terraform."""
import json
import os
import sys
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import tf_bootstrap_gcp as boot  # noqa: E402

PROJECT = "andara-state"
DEVBOX = "devbox@example.com"
ENV = {"GCP_STATE_PROJECT": PROJECT, "GCP_DEVBOX_IDENTITY": DEVBOX, "GCP_STATE_REGION": "us-central1",
       "GCP_BILLING_ACCOUNT": "000000-000000-000000"}
OBJ = "projects/_/buckets/andara-tfstate/objects/"


class FakeCloud:
    """Just enough of gcloud, gh and the gcs backend for the bootstrap's calls."""

    def __init__(self, account="owner@example.com"):
        self.account = account
        self.project = None
        self.billing = False
        self.apis = set()
        self.bucket = None
        self.bucket_policy = []  # [{role, members, condition?}]
        self.sas = {}  # email -> [{role, members}]
        self.pool = False
        self.provider = None
        self.variables = {}
        self.objects = set()
        self.mutations = []
        self.drop_bucket_bindings = False
        self.adc = DEVBOX  # whose Application Default Credentials the dev box holds
        self.write_state = True
        self.tf_envs = []  # (root, args, env) per terraform call

    def mutate(self, what):
        self.mutations.append(what)

    @staticmethod
    def flag(args, name):
        for a in args:
            if a.startswith(name + "="):
                return a[len(name) + 1:]
        return None

    def gcloud(self, args):
        missing = (1, "", "ERROR: NOT_FOUND: does not exist")
        j = lambda o: (0, json.dumps(o), "")  # noqa: E731
        a = [x for x in args if x != "--format=json"]
        if a[:3] == ["config", "get-value", "account"]:
            return 0, self.account + "\n", ""
        if a[:2] == ["projects", "describe"]:
            return j({"projectId": PROJECT, "projectNumber": "123456789"}) if self.project else missing
        if a[:2] == ["projects", "create"]:
            self.project = a[2]
            self.mutate("project")
            return 0, "", ""
        if a[:3] == ["billing", "projects", "describe"]:
            return j({"billingEnabled": self.billing})
        if a[:3] == ["billing", "projects", "link"]:
            self.billing = True
            self.mutate("billing")
            return 0, "", ""
        if a[:2] == ["services", "list"]:
            return 0, "\n".join(sorted(self.apis)), ""
        if a[:2] == ["services", "enable"]:
            self.apis.update(x for x in a[2:] if not x.startswith("--"))
            self.mutate("apis")
            return 0, "", ""
        if a[:3] == ["storage", "buckets", "describe"]:
            return j(self.bucket) if self.bucket is not None else missing
        if a[:3] == ["storage", "buckets", "create"]:
            self.bucket = {"uniform_bucket_level_access": True, "public_access_prevention": "enforced"}
            self.mutate("bucket")
            return 0, "", ""
        if a[:3] == ["storage", "buckets", "update"]:
            if "--versioning" in a:
                self.bucket["versioning_enabled"] = True
            with open(self.flag(a, "--lifecycle-file")) as f:
                self.bucket["lifecycle_config"] = json.load(f)
            self.mutate("bucket update")
            return 0, "", ""
        if a[:3] == ["storage", "buckets", "get-iam-policy"]:
            return j({"bindings": self.bucket_policy, "version": 3})
        if a[:3] == ["storage", "buckets", "add-iam-policy-binding"]:
            with open(self.flag(a, "--condition-from-file")) as f:
                cond = json.load(f)
            if not self.drop_bucket_bindings:
                self.bucket_policy.append({"role": self.flag(a, "--role"), "members": [self.flag(a, "--member")],
                                           "condition": cond})
            self.mutate("bucket binding")
            return 0, "", ""
        if a[:3] == ["storage", "buckets", "remove-iam-policy-binding"]:
            member, role = self.flag(a, "--member"), self.flag(a, "--role")
            path = self.flag(a, "--condition-from-file")
            if path:
                with open(path) as f:
                    expr = json.load(f)["expression"]
            else:
                assert self.flag(a, "--condition") == "None", a
                expr = None
            for b in self.bucket_policy:
                if b["role"] == role and (b.get("condition") or {}).get("expression") == expr:
                    b["members"] = [m for m in b["members"] if m != member]
            self.bucket_policy = [b for b in self.bucket_policy if b["members"]]
            self.mutate("bucket binding removed")
            return 0, "", ""
        if a[:3] == ["iam", "service-accounts", "remove-iam-policy-binding"]:
            member, role = self.flag(a, "--member"), self.flag(a, "--role")
            for b in self.sas[a[3]]:
                if b["role"] == role:
                    b["members"] = [m for m in b["members"] if m != member]
            self.sas[a[3]] = [b for b in self.sas[a[3]] if b["members"]]
            self.mutate("wi binding removed")
            return 0, "", ""
        if a[:3] == ["iam", "service-accounts", "describe"]:
            return j({"email": a[3]}) if a[3] in self.sas else missing
        if a[:3] == ["iam", "service-accounts", "create"]:
            self.sas[boot.sa_email(a[3], PROJECT)] = []
            self.mutate("sa")
            return 0, "", ""
        if a[:3] == ["iam", "service-accounts", "get-iam-policy"]:
            return j({"bindings": self.sas[a[3]]})
        if a[:3] == ["iam", "service-accounts", "add-iam-policy-binding"]:
            self.sas[a[3]].append({"role": self.flag(a, "--role"), "members": [self.flag(a, "--member")]})
            self.mutate("wi binding")
            return 0, "", ""
        if a[:3] == ["iam", "workload-identity-pools", "describe"]:
            return j({"name": "github"}) if self.pool else missing
        if a[:3] == ["iam", "workload-identity-pools", "create"]:
            self.pool = True
            self.mutate("pool")
            return 0, "", ""
        if a[:4] == ["iam", "workload-identity-pools", "providers", "describe"]:
            return j(self.provider) if self.provider else missing
        if a[:4] in (["iam", "workload-identity-pools", "providers", "create-oidc"],
                     ["iam", "workload-identity-pools", "providers", "update-oidc"]):
            mapping = dict(kv.split("=", 1) for kv in self.flag(a, "--attribute-mapping").split(","))
            self.provider = {"attributeMapping": mapping, "attributeCondition": self.flag(a, "--attribute-condition"),
                             "oidc": {"issuerUri": self.flag(a, "--issuer-uri")}}
            self.mutate("provider")
            return 0, "", ""
        if a[:3] == ["storage", "objects", "describe"]:
            return j({"name": a[3]}) if a[3] in self.objects else missing
        if a[:2] == ["auth", "print-access-token"]:
            return 0, "owner-token\n", ""
        raise AssertionError("unexpected gcloud call: %r" % (args,))

    def gh(self, args):
        if args[:2] == ["variable", "list"]:
            return 0, json.dumps([{"name": k, "value": v} for k, v in self.variables.items()]), ""
        if args[:2] == ["variable", "set"]:
            self.variables[args[2]] = args[args.index("--body") + 1]
            self.mutate("variable")
            return 0, "", ""
        raise AssertionError("unexpected gh call: %r" % (args,))

    def terraform(self, root, args, env):
        self.tf_envs.append((root, args, dict(env)))
        owner = env.get("GOOGLE_OAUTH_ACCESS_TOKEN") == "owner-token" or self.adc == self.account
        if not owner and root != "solo7dev":
            return 1, "", "Error: storage.objects.list 403 Forbidden: %s does not have storage.objects.list" % self.adc
        if args[0] == "init" and self.write_state:
            path = "gs://andara-tfstate/grafana/%s/default.tfstate" % root
            if path not in self.objects:
                self.objects.add(path)
                self.mutate("state " + root)
        return 0, "", ""


def run(cloud, env=None, sleep=lambda s: None):
    lines = []
    b = boot.Bootstrap(dict(ENV if env is None else env), gcloud=cloud.gcloud, gh=cloud.gh,
                       terraform=cloud.terraform, out=lines.append, sleep=sleep)
    try:
        return b.run(), lines, None
    except boot.Exit as e:
        return e.code, lines, e.msg


class QuotaTest(unittest.TestCase):
    """A 429 from a new project's per-minute service-account quota is waited out, not fatal."""

    def throttled(self, cloud, times):
        real = cloud.gcloud
        left = [times]

        def gcloud(args):
            if args[:3] == ["iam", "service-accounts", "create"] and left[0]:
                left[0] -= 1
                return 1, "", "ERROR: 429 RESOURCE_EXHAUSTED: Service accounts created per minute per project."
            return real(args)
        cloud.gcloud = gcloud

    def test_retried_until_it_succeeds(self):
        cloud = FakeCloud()
        self.throttled(cloud, 2)
        waits = []
        rc, lines, msg = run(cloud, sleep=waits.append)
        self.assertEqual(rc, 0, msg)
        self.assertEqual(waits, [boot.Bootstrap.QUOTA_WAIT] * 2)
        self.assertTrue(any("quota reached" in line for line in lines))

    def test_gives_up_after_the_retries(self):
        cloud = FakeCloud()
        self.throttled(cloud, boot.Bootstrap.QUOTA_RETRIES + 1)
        rc, _, msg = run(cloud)
        self.assertEqual(rc, 1)
        self.assertIn("RESOURCE_EXHAUSTED", msg)


class ConditionTest(unittest.TestCase):
    """AC-3: the solo7 condition's object and list clauses match solo7's prefix and nothing else."""

    def test_solo7_condition(self):
        expr = boot.condition(("solo7",))["expression"]
        self.assertIn('resource.name.startsWith("projects/_/buckets/andara-tfstate/objects/grafana/solo7/")', expr)
        self.assertIn('api.getAttribute("storage.googleapis.com/objectListPrefix", "").startsWith("grafana/solo7/")', expr)
        for name in ("default.tfstate", "default.tflock"):
            self.assertTrue(boot.allows(expr, object_name=OBJ + "grafana/solo7/" + name))
        self.assertTrue(boot.allows(expr, list_prefix="grafana/solo7/"))
        for other in ("solo7dev", "solo7-irm"):
            self.assertFalse(boot.allows(expr, object_name=OBJ + "grafana/%s/default.tfstate" % other), other)
            self.assertFalse(boot.allows(expr, list_prefix="grafana/%s/" % other), other)
        self.assertFalse(boot.allows(expr, list_prefix="grafana/"))
        self.assertFalse(boot.allows(expr, list_prefix=""))

    def test_prod_plan_reaches_both_prod_roots_only(self):
        expr = boot.condition(("solo7", "solo7-irm"))["expression"]
        for root in ("solo7", "solo7-irm"):
            self.assertTrue(boot.allows(expr, object_name=OBJ + "grafana/%s/default.tfstate" % root))
            self.assertTrue(boot.allows(expr, list_prefix="grafana/%s/" % root))
        self.assertFalse(boot.allows(expr, object_name=OBJ + "grafana/solo7dev/default.tfstate"))

    def test_every_account_and_the_dev_box(self):
        bindings = boot.expected_bucket_bindings(PROJECT, DEVBOX)
        self.assertEqual(len(bindings), 7)
        self.assertEqual(bindings["user:" + DEVBOX][0], boot.OBJECT_USER)
        devbox = bindings["user:" + DEVBOX][1]["expression"]
        self.assertTrue(boot.allows(devbox, object_name=OBJ + "grafana/solo7dev/default.tflock"))
        self.assertFalse(boot.allows(devbox, object_name=OBJ + "grafana/solo7/default.tfstate"))
        plan = bindings["serviceAccount:" + boot.sa_email("tf-solo7-plan", PROJECT)]
        self.assertEqual(plan[0], boot.OBJECT_VIEWER)
        self.assertNotIn("serviceAccount:" + boot.sa_email("tf-solo7-irm-plan", PROJECT), bindings,
                         "no plan account reads the IRM state")


class ReadbackTest(unittest.TestCase):
    def setUp(self):
        self.expected = boot.expected_bucket_bindings(PROJECT, DEVBOX)
        self.policy = {"bindings": [{"role": r, "members": [m], "condition": c} for m, (r, c) in self.expected.items()]}

    def test_matching_policy(self):
        self.assertEqual(boot.readback(self.policy, self.expected), [])

    def test_missing_binding_names_the_account(self):
        sa = "serviceAccount:" + boot.sa_email("tf-solo7-apply", PROJECT)
        self.policy["bindings"] = [b for b in self.policy["bindings"] if b["members"] != [sa]]
        problems = boot.readback(self.policy, self.expected)
        self.assertEqual(len(problems), 1)
        self.assertIn(sa, problems[0])

    def test_condition_differs(self):
        sa = "serviceAccount:" + boot.sa_email("tf-solo7-plan", PROJECT)
        for b in self.policy["bindings"]:
            if b["members"] == [sa]:
                b["condition"] = boot.condition(("solo7dev",))
        problems = boot.readback(self.policy, self.expected)
        self.assertTrue(problems and all(sa in p for p in problems), problems)

    def test_unconditioned_extra_grant_refused(self):
        self.policy["bindings"].append({"role": "roles/storage.objectViewer", "members": ["user:" + DEVBOX]})
        problems = boot.readback(self.policy, self.expected)
        self.assertEqual(len(problems), 1)
        self.assertIn("user:" + DEVBOX, problems[0])
        self.assertIn("always", problems[0])


class CorrectionTest(unittest.TestCase):
    """The bootstrap corrects what differs, not only adds what is missing."""

    def test_wider_bucket_grant_is_removed(self):
        cloud = FakeCloud()
        run(cloud)
        sa = "serviceAccount:" + boot.sa_email("tf-solo7-plan", PROJECT)
        cloud.bucket_policy.append({"role": "roles/storage.objectViewer", "members": [sa],
                                    "condition": boot.condition(("solo7", "solo7dev"))})
        cloud.bucket_policy.append({"role": "roles/storage.admin", "members": [sa]})
        rc, lines, err = run(cloud)
        self.assertEqual(rc, 0, err)
        grants = [(b["role"], (b.get("condition") or {}).get("title")) for b in cloud.bucket_policy
                  if sa in b["members"]]
        self.assertEqual(grants, [("roles/storage.objectViewer", "tfstate grafana/solo7/")])
        self.assertEqual(sum("removed" in line for line in lines), 2)
        self.assertEqual(boot.readback({"bindings": cloud.bucket_policy},
                                       boot.expected_bucket_bindings(PROJECT, DEVBOX)), [])

    def test_unmanaged_member_is_left_alone(self):
        cloud = FakeCloud()
        run(cloud)
        other = {"role": "roles/storage.admin", "members": ["user:someone@example.com"]}
        cloud.bucket_policy.append(dict(other))
        cloud.mutations.clear()
        rc, _, err = run(cloud)
        self.assertEqual(rc, 0, err)
        self.assertIn(other, cloud.bucket_policy)
        self.assertEqual(cloud.mutations, [])

    def test_stale_impersonation_grant_is_removed(self):
        cloud = FakeCloud()
        run(cloud)
        email = boot.sa_email("tf-solo7-plan", PROJECT)
        stale = boot.principal_set("123456789", "event_name", "push")
        cloud.sas[email].append({"role": boot.WI_USER, "members": [stale]})
        rc, lines, err = run(cloud)
        self.assertEqual(rc, 0, err)
        members = {m for b in cloud.sas[email] for m in b["members"]}
        self.assertNotIn(stale, members)
        self.assertEqual(len(members), 2)
        self.assertTrue(any("removed" in line for line in lines))


class BootstrapTest(unittest.TestCase):
    def test_first_run_creates_everything_second_changes_nothing(self):
        """AC-2."""
        cloud = FakeCloud()
        rc, lines, err = run(cloud)
        self.assertEqual(rc, 0, err)
        self.assertEqual(len(cloud.sas), 6)
        self.assertEqual(cloud.objects, {"gs://andara-tfstate/grafana/%s/default.tfstate" % r
                                                 for r in ("solo7", "solo7-irm", "solo7dev")})
        self.assertEqual(set(cloud.variables), set(boot.GH_VARIABLES) | {"GCP_WIF_PROVIDER"})
        self.assertTrue(cloud.bucket["versioning_enabled"])
        self.assertEqual(cloud.bucket["lifecycle_config"],
                         {"rule": [{"action": {"type": "Delete"},
                                    "condition": {"daysSinceNoncurrentTime": 90, "numNewerVersions": 20}}]})
        self.assertEqual(cloud.provider["attributeMapping"], {
            "google.subject": "assertion.sub",
            "attribute.environment": '"environment" in assertion ? assertion.environment : "none"',
            "attribute.event_name": "assertion.event_name",
        })
        self.assertEqual(cloud.provider["attributeCondition"],
                         "assertion.repository == 'valesordev/andara.valesordev.com' && assertion.workflow_ref == "
                         "'valesordev/andara.valesordev.com/.github/workflows/terraform.yaml@refs/heads/main'")
        self.assertIn("change(s)", lines[-1])

        cloud.mutations.clear()
        rc, lines, err = run(cloud)
        self.assertEqual(rc, 0, err)
        self.assertEqual(cloud.mutations, [])
        self.assertEqual(lines[-1], "tf-bootstrap-gcp: no changes; everything already matched")

    def test_impersonation_bindings(self):
        cloud = FakeCloud()
        run(cloud)
        members = lambda name: {m for b in cloud.sas[boot.sa_email(name, PROJECT)] for m in b["members"]}  # noqa: E731
        base = "principalSet://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/github/"
        self.assertEqual(members("tf-solo7dev-plan"), {base + "attribute.event_name/pull_request_target",
                                                       base + "attribute.event_name/schedule"})
        self.assertEqual(members("tf-solo7-irm-apply"), {base + "attribute.environment/andara-prod-apply"})
        self.assertEqual(members("tf-prod-plan"), {base + "attribute.environment/andara-prod-plan"})

    def test_readback_failure_exits_1_naming_the_account(self):
        """AC-3: a binding that did not land fails the run, before any state is seeded."""
        cloud = FakeCloud()
        cloud.drop_bucket_bindings = True
        rc, _, err = run(cloud)
        self.assertEqual(rc, 1)
        self.assertIn("serviceAccount:tf-solo7-apply@%s.iam.gserviceaccount.com" % PROJECT, err)
        self.assertIn("user:" + DEVBOX, err)
        self.assertEqual(cloud.objects, set())

    def test_unset_variables_exit_3(self):
        for drop in ("GCP_STATE_PROJECT", "GCP_DEVBOX_IDENTITY"):
            env = dict(ENV)
            del env[drop]
            rc, _, err = run(FakeCloud(), env)
            self.assertEqual(rc, 3)
            self.assertIn(drop, err)

    def test_not_logged_in_exits_3(self):
        rc, _, err = run(FakeCloud(account=""))
        self.assertEqual(rc, 3)
        self.assertIn("gcloud auth login", err)

    def test_devbox_must_not_be_the_owner(self):
        rc, _, err = run(FakeCloud(account=DEVBOX))
        self.assertEqual(rc, 1)
        self.assertIn("non-owner", err)

    def test_states_seeded_as_the_owner_and_checked_as_the_dev_box(self):
        cloud = FakeCloud()
        rc, lines, err = run(cloud)
        self.assertEqual(rc, 0, err)
        seeds = [e for root, args, e in cloud.tf_envs if "bootstrap" in e.get("TF_DATA_DIR", "")]
        self.assertEqual(len(seeds), 6)  # init and state list, three roots
        self.assertTrue(all(e["GOOGLE_OAUTH_ACCESS_TOKEN"] == "owner-token" for e in seeds))
        devbox = [(root, e) for root, args, e in cloud.tf_envs if "bootstrap" not in e.get("TF_DATA_DIR", "")]
        self.assertEqual([r for r, _ in devbox], ["solo7dev", "solo7dev", "solo7"])
        self.assertTrue(all("GOOGLE_OAUTH_ACCESS_TOKEN" not in e for _, e in devbox))
        self.assertIn("tf-bootstrap-gcp: dev box: solo7's state refused to %s, as decision 3 requires" % DEVBOX,
                      lines)

    def test_owner_token_in_the_environment_is_not_used_for_the_dev_box(self):
        env = dict(ENV, GOOGLE_OAUTH_ACCESS_TOKEN="owner-token")
        cloud = FakeCloud()
        rc, _, err = run(cloud, env)
        self.assertEqual(rc, 0, err)
        devbox = [e for root, args, e in cloud.tf_envs if "bootstrap" not in e.get("TF_DATA_DIR", "")]
        self.assertTrue(all("GOOGLE_OAUTH_ACCESS_TOKEN" not in e for e in devbox))

    def test_owner_adc_refused(self):
        """ADC holding the owner's credentials reads solo7's state: the run fails naming the dev box."""
        cloud = FakeCloud()
        cloud.adc = cloud.account
        rc, _, err = run(cloud)
        self.assertEqual(rc, 1)
        self.assertIn("can read solo7's state", err)
        self.assertIn(DEVBOX, err)

    def test_state_missing_after_init_exits_1(self):
        cloud = FakeCloud()
        cloud.write_state = False
        rc, _, err = run(cloud)
        self.assertEqual(rc, 1)
        self.assertIn("still missing after terraform init", err)

    def test_existing_state_left_untouched(self):
        cloud = FakeCloud()
        cloud.objects.add("gs://andara-tfstate/grafana/solo7/default.tfstate")
        rc, lines, _ = run(cloud)
        self.assertEqual(rc, 0)
        self.assertIn("tf-bootstrap-gcp: state: grafana/solo7/default.tfstate exists, left untouched", lines)
        self.assertNotIn("state solo7", cloud.mutations)


if __name__ == "__main__":
    unittest.main()
