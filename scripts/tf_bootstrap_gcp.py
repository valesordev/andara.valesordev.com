#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make tf-bootstrap-gcp` — the Terraform state bucket and who may reach it (AW-INF-046, ADR-0012 decision 3).

Run once by Brian, as the GCP project's owner (his `gcloud auth login` identity), and again
whenever it should be checked: every step verifies what exists and creates or corrects only
what does not match, so a second run changes nothing and says so. In order:

  1. the project GCP_STATE_PROJECT (created when missing; billing linked from
     GCP_BILLING_ACCOUNT when it is not), and the Storage, IAM, IAM Credentials, STS and
     Resource Manager APIs;
  2. the bucket andara-tfstate in GCP_STATE_REGION (read only when the bucket is created):
     uniform bucket-level access, public access prevention enforced, versioning on, and a
     lifecycle rule deleting non-current versions older than 90 days beyond the newest 20;
  3. the six keyless service accounts, and their bucket roles conditioned to their root's prefix
     (the object clause and the listing clause), and GCP_DEVBOX_IDENTITY's objectUser on
     solo7dev's prefix;
  4. the workload identity pool `github` and its OIDC provider `github-actions`, which accepts
     this repository's terraform.yaml on main only and maps `environment` and `event_name`;
  5. each account's workloadIdentityUser binding, to one value of one attribute;
  6. the repository variables TFSTATE_*_SA and GCP_WIF_PROVIDER (with `gh`);
  7. the bucket's IAM policy, read back: exit 1 naming the account if a binding is missing,
     or a managed account has a binding whose condition differs from the expected one;
  8. the state for all three roots, seeded as the owner (terraform init writes an empty
     state under its lock when there is none), then `terraform init` of solo7dev as
     GCP_DEVBOX_IDENTITY through Application Default Credentials, which must succeed.

The workloadIdentityUser bindings are written from the claim values GitHub is expected to
send; AW-INF-068's debug step observes the real ones and, if they differ, changes them here.

Exit codes: 0 ok; 1 a gcloud, gh, terraform or read-back failure; 3 a variable unset or
gcloud not logged in.
"""

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tf  # noqa: E402

BUCKET = "andara-tfstate"
REPO_SLUG = "valesordev/andara.valesordev.com"
WORKFLOW_REF = REPO_SLUG + "/.github/workflows/terraform.yaml@refs/heads/main"
POOL = "github"
PROVIDER = "github-actions"
ISSUER = "https://token.actions.githubusercontent.com"
APIS = ("storage.googleapis.com", "iam.googleapis.com", "iamcredentials.googleapis.com",
        "sts.googleapis.com", "cloudresourcemanager.googleapis.com")

OBJECT_USER = "roles/storage.objectUser"
OBJECT_VIEWER = "roles/storage.objectViewer"
WI_USER = "roles/iam.workloadIdentityUser"

# account -> (bucket role, roots it may reach, [(attribute, value)] it is impersonable from)
ACCOUNTS = {
    "tf-solo7dev-apply": (OBJECT_USER, ("solo7dev",), [("environment", "andara-main")]),
    "tf-solo7-apply": (OBJECT_USER, ("solo7",), [("environment", "andara-prod-apply")]),
    "tf-solo7-irm-apply": (OBJECT_USER, ("solo7-irm",), [("environment", "andara-prod-apply")]),
    "tf-solo7dev-plan": (OBJECT_VIEWER, ("solo7dev",),
                         [("event_name", "pull_request_target"), ("event_name", "schedule")]),
    "tf-solo7-plan": (OBJECT_VIEWER, ("solo7",),
                      [("event_name", "pull_request_target"), ("event_name", "schedule")]),
    "tf-prod-plan": (OBJECT_VIEWER, ("solo7", "solo7-irm"), [("environment", "andara-prod-plan")]),
}

GH_VARIABLES = {
    "TFSTATE_SOLO7DEV_PLAN_SA": "tf-solo7dev-plan",
    "TFSTATE_SOLO7_PLAN_SA": "tf-solo7-plan",
    "TFSTATE_SOLO7DEV_APPLY_SA": "tf-solo7dev-apply",
    "TFSTATE_SOLO7_APPLY_SA": "tf-solo7-apply",
    "TFSTATE_SOLO7_IRM_APPLY_SA": "tf-solo7-irm-apply",
    "TFSTATE_PRODPLAN_SA": "tf-prod-plan",
}

LIFECYCLE = {"rule": [{"action": {"type": "Delete"},
                       "condition": {"daysSinceNoncurrentTime": 90, "numNewerVersions": 20}}]}

# A PR or drift job has no `environment` claim; the guard keeps the exchange from failing on it.
ATTRIBUTE_MAPPING = {
    "google.subject": "assertion.sub",
    "attribute.environment": '"environment" in assertion ? assertion.environment : "none"',
    "attribute.event_name": "assertion.event_name",
}
ATTRIBUTE_CONDITION = ("assertion.repository == '%s' && assertion.workflow_ref == '%s'"
                       % (REPO_SLUG, WORKFLOW_REF))

OBJECT_CLAUSE = 'resource.name.startsWith("projects/_/buckets/%s/objects/grafana/%s/")'
LIST_CLAUSE = 'api.getAttribute("storage.googleapis.com/objectListPrefix", "").startsWith("grafana/%s/")'


class Exit(Exception):
    def __init__(self, code, msg):
        super().__init__(msg)
        self.code = code
        self.msg = msg


def condition(roots):
    """The IAM condition limiting a bucket role to roots' prefixes (ADR-0012 decision 3)."""
    clauses = []
    for root in roots:
        clauses.append(OBJECT_CLAUSE % (BUCKET, root))
        clauses.append(LIST_CLAUSE % root)
    return {"title": "tfstate " + " ".join("grafana/%s/" % r for r in roots),
            "expression": " || ".join(clauses)}


_CLAUSE = re.compile(r'^(?:resource\.name|api\.getAttribute\("storage\.googleapis\.com/objectListPrefix", ""\))'
                     r'\.startsWith\("([^"]*)"\)$')


def allows(expression, object_name=None, list_prefix=None):
    """Evaluate a condition of the two clause forms above, as GCS would, for one object or one listing."""
    for clause in expression.split(" || "):
        m = _CLAUSE.match(clause.strip())
        if not m:
            raise ValueError("clause not of a known form: %r" % clause)
        if clause.startswith("resource.name"):
            if object_name is not None and object_name.startswith(m.group(1)):
                return True
        elif list_prefix is not None and list_prefix.startswith(m.group(1)):
            return True
    return False


def member_of(identity):
    if identity.startswith(("user:", "serviceAccount:", "group:")):
        return identity
    return ("serviceAccount:" if identity.endswith(".gserviceaccount.com") else "user:") + identity


def sa_email(name, project):
    return "%s@%s.iam.gserviceaccount.com" % (name, project)


def expected_bucket_bindings(project, devbox):
    """{member: (role, condition)} the bucket must grant, and nothing else to these members."""
    out = {"serviceAccount:" + sa_email(name, project): (role, condition(roots))
           for name, (role, roots, _) in ACCOUNTS.items()}
    out[member_of(devbox)] = (OBJECT_USER, condition(("solo7dev",)))
    return out


def readback(policy, expected):
    """Problems with policy against expected, one per account, naming it (AC-3)."""
    have = {}
    for b in policy.get("bindings", []):
        cond = b.get("condition") or {}
        for m in b.get("members", []):
            have.setdefault(m, []).append((b["role"], cond.get("expression")))
    problems = []
    for member, (role, cond) in sorted(expected.items()):
        grants = have.get(member, [])
        if (role, cond["expression"]) not in grants:
            problems.append("%s: no %s binding with the condition for its prefix" % (member, role))
        extra = [g for g in grants if g != (role, cond["expression"])]
        if extra:
            problems.append("%s: unexpected binding(s) %s; only %s on its prefix is allowed"
                            % (member, ", ".join("%s if %s" % (r, c or "always") for r, c in extra), role))
    return problems


def principal_set(project_number, attribute, value):
    return ("principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.%s/%s"
            % (project_number, POOL, attribute, value))


class Bootstrap:
    # A new project allows only a few service accounts created per minute; a 429 is waited out.
    QUOTA_RETRIES = 5
    QUOTA_WAIT = 65

    def __init__(self, env, gcloud=None, gh=None, terraform=None, out=print, sleep=time.sleep):
        self.env = env
        self.sleep = sleep
        self.changes = 0
        self.gcloud_run = gcloud or self._run_gcloud
        self.gh_run = gh or self._run_gh
        self.tf_run = terraform or self._run_terraform
        self.out = out

    # --- process wrappers -------------------------------------------------------------
    @staticmethod
    def _run(cmd, env=None):
        p = subprocess.run(cmd, capture_output=True, text=True, env=env)
        return p.returncode, p.stdout, p.stderr

    def _run_gcloud(self, args):
        exe = shutil.which("gcloud")
        if not exe:
            raise Exit(1, "gcloud not found; install the Google Cloud CLI")
        return self._run([exe] + args + ["--quiet"])

    def _run_gh(self, args):
        exe = shutil.which("gh")
        if not exe:
            raise Exit(1, "gh not found; install the GitHub CLI")
        return self._run([exe] + args)

    def _run_terraform(self, root, args, env):
        return self._run([tf.terraform(), "-chdir=" + tf.root_dir(root)] + args, env=env)

    def say(self, step, what):
        self.out("tf-bootstrap-gcp: %s: %s" % (step, what))

    def changed(self, step, what):
        self.changes += 1
        self.say(step, what)

    def gcloud(self, *args, ok_missing=False):
        rc, out, err = self.gcloud_run(list(args))
        for _ in range(self.QUOTA_RETRIES):
            if rc == 0 or "RESOURCE_EXHAUSTED" not in err:
                break
            self.out("tf-bootstrap-gcp: gcloud %s: quota reached; retrying in %ds"
                     % (" ".join(args[:4]), self.QUOTA_WAIT))
            self.sleep(self.QUOTA_WAIT)
            rc, out, err = self.gcloud_run(list(args))
        if rc != 0:
            if ok_missing and re.search(r"NOT_FOUND|not found|does not exist|404", err):
                return None
            raise Exit(1, "gcloud %s failed: %s" % (" ".join(args[:4]), err.strip()))
        return out

    def gcloud_json(self, *args, ok_missing=False):
        out = self.gcloud(*(list(args) + ["--format=json"]), ok_missing=ok_missing)
        return None if out is None else json.loads(out or "null")

    # --- steps ------------------------------------------------------------------------
    def require(self):
        unset = [k for k in ("GCP_STATE_PROJECT", "GCP_DEVBOX_IDENTITY") if not self.env.get(k)]
        if unset:
            raise Exit(3, "%s unset (set it in the environment or .local/box.env)" % ", ".join(unset))
        account = (self.gcloud("config", "get-value", "account") or "").strip()
        if not account:
            raise Exit(3, "gcloud has no logged-in account; run gcloud auth login as the project owner")
        self.project = self.env["GCP_STATE_PROJECT"]
        self.devbox = self.env["GCP_DEVBOX_IDENTITY"]
        if member_of(self.devbox) == "user:" + account:
            raise Exit(1, "GCP_DEVBOX_IDENTITY is the owner's own account (%s); it must be a second, "
                          "non-owner identity (ADR-0012 decision 3)" % account)
        self.say("account", account)

    def project_and_apis(self):
        proj = self.gcloud_json("projects", "describe", self.project, ok_missing=True)
        if proj is None:
            self.gcloud("projects", "create", self.project)
            self.changed("project", "%s created" % self.project)
            proj = self.gcloud_json("projects", "describe", self.project)
        else:
            self.say("project", "%s exists" % self.project)
        self.number = str(proj["projectNumber"])
        billing = self.gcloud_json("billing", "projects", "describe", self.project) or {}
        if not billing.get("billingEnabled"):
            account = self.env.get("GCP_BILLING_ACCOUNT")
            if not account:
                raise Exit(3, "%s has no billing account and GCP_BILLING_ACCOUNT is unset" % self.project)
            self.gcloud("billing", "projects", "link", self.project, "--billing-account=" + account)
            self.changed("billing", "linked %s" % account)
        enabled = set((self.gcloud("services", "list", "--enabled", "--project=" + self.project,
                                   "--format=value(config.name)") or "").split())
        missing = [a for a in APIS if a not in enabled]
        if missing:
            self.gcloud("services", "enable", *missing, "--project=" + self.project)
            self.changed("apis", "enabled %s" % ", ".join(missing))
        else:
            self.say("apis", "enabled")

    def bucket(self):
        url = "gs://" + BUCKET
        b = self.gcloud_json("storage", "buckets", "describe", url, ok_missing=True)
        if b is None:
            region = self.env.get("GCP_STATE_REGION")
            if not region:
                raise Exit(3, "the bucket %s does not exist and GCP_STATE_REGION is unset" % BUCKET)
            self.gcloud("storage", "buckets", "create", url, "--project=" + self.project,
                        "--location=" + region, "--uniform-bucket-level-access", "--public-access-prevention")
            self.changed("bucket", "%s created in %s" % (BUCKET, region))
            b = self.gcloud_json("storage", "buckets", "describe", url)
        wrong = []
        if not b.get("versioning_enabled"):
            wrong.append("--versioning")
        if not b.get("uniform_bucket_level_access"):
            wrong.append("--uniform-bucket-level-access")
        if b.get("public_access_prevention") != "enforced":
            wrong.append("--public-access-prevention")
        lifecycle_ok = (b.get("lifecycle_config") or {}).get("rule") == LIFECYCLE["rule"]
        if not wrong and lifecycle_ok:
            self.say("bucket", "%s settings match" % BUCKET)
            return
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(LIFECYCLE, f)
        try:
            self.gcloud("storage", "buckets", "update", url, *wrong, "--lifecycle-file=" + f.name)
        finally:
            os.unlink(f.name)
        self.changed("bucket", "%s set %s" % (BUCKET, " ".join(wrong + ([] if lifecycle_ok else ["lifecycle"]))))

    def service_accounts(self):
        for name in ACCOUNTS:
            email = sa_email(name, self.project)
            if self.gcloud_json("iam", "service-accounts", "describe", email, "--project=" + self.project,
                                ok_missing=True) is None:
                self.gcloud("iam", "service-accounts", "create", name, "--project=" + self.project,
                            "--display-name=Terraform state: " + name)
                self.changed("service account", "%s created" % email)
            else:
                self.say("service account", "%s exists" % email)

    def bucket_bindings(self):
        url = "gs://" + BUCKET
        policy = self.gcloud_json("storage", "buckets", "get-iam-policy", url) or {}
        have = {(m, b["role"], (b.get("condition") or {}).get("expression"))
                for b in policy.get("bindings", []) for m in b.get("members", [])}
        for member, (role, cond) in sorted(expected_bucket_bindings(self.project, self.devbox).items()):
            if (member, role, cond["expression"]) in have:
                self.say("bucket binding", "%s %s ok" % (member, role))
                continue
            with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
                json.dump(cond, f)
            try:
                self.gcloud("storage", "buckets", "add-iam-policy-binding", url, "--member=" + member,
                            "--role=" + role, "--condition-from-file=" + f.name)
            finally:
                os.unlink(f.name)
            self.changed("bucket binding", "%s %s on %s" % (member, role, cond["title"]))

    def workload_identity(self):
        loc = ["--location=global", "--project=" + self.project]
        if self.gcloud_json("iam", "workload-identity-pools", "describe", POOL, *loc, ok_missing=True) is None:
            self.gcloud("iam", "workload-identity-pools", "create", POOL, *loc, "--display-name=GitHub Actions")
            self.changed("pool", "%s created" % POOL)
        else:
            self.say("pool", "%s exists" % POOL)
        flags = ["--workload-identity-pool=" + POOL, *loc, "--issuer-uri=" + ISSUER,
                 "--attribute-mapping=" + ",".join("%s=%s" % kv for kv in ATTRIBUTE_MAPPING.items()),
                 "--attribute-condition=" + ATTRIBUTE_CONDITION]
        p = self.gcloud_json("iam", "workload-identity-pools", "providers", "describe", PROVIDER,
                             "--workload-identity-pool=" + POOL, *loc, ok_missing=True)
        if p is None:
            self.gcloud("iam", "workload-identity-pools", "providers", "create-oidc", PROVIDER, *flags)
            self.changed("provider", "%s created" % PROVIDER)
        elif (p.get("attributeMapping") != ATTRIBUTE_MAPPING or p.get("attributeCondition") != ATTRIBUTE_CONDITION
              or (p.get("oidc") or {}).get("issuerUri") != ISSUER):
            self.gcloud("iam", "workload-identity-pools", "providers", "update-oidc", PROVIDER, *flags)
            self.changed("provider", "%s mapping and condition updated" % PROVIDER)
        else:
            self.say("provider", "%s matches" % PROVIDER)
        for name, (_, _, claims) in ACCOUNTS.items():
            email = sa_email(name, self.project)
            policy = self.gcloud_json("iam", "service-accounts", "get-iam-policy", email,
                                      "--project=" + self.project) or {}
            have = {(b["role"], m) for b in policy.get("bindings", []) for m in b.get("members", [])}
            for attribute, value in claims:
                member = principal_set(self.number, attribute, value)
                if (WI_USER, member) in have:
                    self.say("impersonation", "%s from %s=%s ok" % (name, attribute, value))
                    continue
                self.gcloud("iam", "service-accounts", "add-iam-policy-binding", email,
                            "--project=" + self.project, "--role=" + WI_USER, "--member=" + member)
                self.changed("impersonation", "%s from %s=%s" % (name, attribute, value))

    def github_variables(self):
        want = {k: sa_email(v, self.project) for k, v in GH_VARIABLES.items()}
        want["GCP_WIF_PROVIDER"] = ("projects/%s/locations/global/workloadIdentityPools/%s/providers/%s"
                                    % (self.number, POOL, PROVIDER))
        rc, out, err = self.gh_run(["variable", "list", "--repo", REPO_SLUG, "--json", "name,value"])
        if rc:
            raise Exit(1, "gh variable list failed: %s" % err.strip())
        have = {v["name"]: v["value"] for v in json.loads(out or "[]")}
        for name, value in sorted(want.items()):
            if have.get(name) == value:
                self.say("repository variable", "%s ok" % name)
                continue
            rc, _, err = self.gh_run(["variable", "set", name, "--repo", REPO_SLUG, "--body", value])
            if rc:
                raise Exit(1, "gh variable set %s failed: %s" % (name, err.strip()))
            self.changed("repository variable", "%s=%s" % (name, value))

    def verify(self):
        policy = self.gcloud_json("storage", "buckets", "get-iam-policy", "gs://" + BUCKET) or {}
        problems = readback(policy, expected_bucket_bindings(self.project, self.devbox))
        if problems:
            raise Exit(1, "the bucket's IAM policy does not match ADR-0012 decision 3:\n  " + "\n  ".join(problems))
        self.say("read-back", "every account's bucket binding and condition match")

    def state_exists(self, root):
        return self.gcloud_json("storage", "objects", "describe",
                                "gs://%s/grafana/%s/default.tfstate" % (BUCKET, root), ok_missing=True) is not None

    def seed_states(self):
        token = (self.gcloud("auth", "print-access-token") or "").strip()
        for root in tf.ROOTS:
            existed = self.state_exists(root)
            env = tf.child_env(self.env, data_dir=os.path.join(tf.root_dir(root), ".terraform", "bootstrap"))
            env["GOOGLE_OAUTH_ACCESS_TOKEN"] = token
            for args in (["init", "-input=false", "-lockfile=readonly", "-reconfigure"], ["state", "list"]):
                rc, out, err = self.tf_run(root, args, env)
                if rc:
                    raise Exit(1, "terraform %s in %s as the owner failed:\n%s%s" % (args[0], root, out, err))
            if not self.state_exists(root):
                raise Exit(1, "grafana/%s/default.tfstate is still missing after terraform init" % root)
            if existed:
                self.say("state", "grafana/%s/default.tfstate exists, left untouched" % root)
            else:
                self.changed("state", "grafana/%s/default.tfstate created" % root)

    def devbox_init(self):
        env = tf.child_env(self.env)
        env.pop("GOOGLE_OAUTH_ACCESS_TOKEN", None)
        for args in (["init", "-input=false", "-lockfile=readonly", "-reconfigure"], ["state", "list"]):
            rc, out, err = self.tf_run("solo7dev", args, env)
            if rc:
                raise Exit(1, "terraform %s of solo7dev as GCP_DEVBOX_IDENTITY (Application Default "
                              "Credentials) failed; run gcloud auth application-default login "
                              "--disable-quota-project as %s:\n%s%s"
                           % (args[0], self.devbox, out, err))
        self.say("dev box", "terraform init of solo7dev as %s ok" % self.devbox)

    def run(self):
        self.require()
        self.project_and_apis()
        self.bucket()
        self.service_accounts()
        self.bucket_bindings()
        self.workload_identity()
        self.github_variables()
        self.verify()
        self.seed_states()
        self.devbox_init()
        if self.changes:
            self.out("tf-bootstrap-gcp: done, %d change(s)" % self.changes)
        else:
            self.out("tf-bootstrap-gcp: no changes; everything already matched")
        return 0


def main(argv, environ=None):
    if argv:
        print("usage: tf_bootstrap_gcp.py (no arguments; see make help)", file=sys.stderr)
        return 2
    try:
        env = tf.load_box_env(dict(os.environ if environ is None else environ))
        return Bootstrap(env).run()
    except (Exit, tf.Exit) as e:
        print("tf-bootstrap-gcp: " + (e.msg or ""), file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
