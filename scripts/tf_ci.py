#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The `terraform` workflow's steps, and `make tf-drift` / `make tf-plan-show` (AW-INF-068, ADR-0012 decision 5).

    scripts/tf_ci.py claims                                   the job's OIDC claims, never the token
    scripts/tf_ci.py pr-plan    --stack <solo7dev|solo7> --tf-dir <dir> --commit <sha>
    scripts/tf_ci.py apply      --stack solo7dev --commit <sha>
    scripts/tf_ci.py plan-prod  --out <dir> --commit <sha>
    scripts/tf_ci.py apply-prod --in <dir> --root <solo7-irm|solo7> --main-head <sha>
    scripts/tf_ci.py drift      --stack <root> [--issue]
    scripts/tf_ci.py plan-show  --run <id> --stack <solo7|solo7-irm>
    scripts/tf_ci.py environments [--apply]                   what the workflow needs on GitHub, by name
    scripts/tf_ci.py plan-key [--rotate]                      make TF_PLAN_KEY_PROD: both prod environments and box.env

The workflow never calls `terraform` or `openssl` itself: every sink (the Actions log, the job summary,
an artifact, a drift issue) is written here, so the rules below are tested in one place.

- A plan's summary names the root, the commit and the add/change/destroy counts, then the plan as
  `terraform show` prints it (sensitive values redacted by Terraform). `terraform show -json` is read
  for the counts and never written to a sink.
- The solo7-irm root's sinks carry resource addresses and actions only: its plan, apply and second plan
  run with their output captured and discarded, because the provider does not mark the IRM
  integration's `link` or the `irm-prod` contact point's `url` sensitive.
- plan-prod encrypts each prod plan with TF_PLAN_KEY_PROD (openssl AES-256-CBC, PBKDF2) before it
  reaches the artifact directory, and records the commit and each plaintext plan's sha256 in
  manifest.json. apply-prod refuses a plan whose commit is not main's head (a superseded plan), and a
  plan whose sha256 differs from the manifest, then applies exactly that plan and checks that a second
  plan is empty.
- drift opens or updates one issue labelled drift:<root>, on drift and on any failure of the job.
- plan-show decrypts into memory and hands Terraform an anonymous in-memory file (memfd): no plaintext
  plan is ever written to disk on the dev box.

Exit codes: 0 ok (for drift: no drift); 1 a refusal, drift, a non-empty second plan, a superseded plan
or a failed step; 2 usage; 3 a credential unset.
"""

import argparse
import base64
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request

import tf

ARTIFACT = "tf-plan-prod"
PROD_ROOTS = ("solo7-irm", "solo7")  # the order plan-prod plans and apply-prod applies them
IRM = "solo7-irm"
# The grafana provider reads its IRM (OnCall) token from this variable. AW-INF-051 adds the IRM
# resources and confirms it; until then the solo7-irm root has nothing that uses it.
IRM_TOKEN_VAR = "GRAFANA_ONCALL_ACCESS_TOKEN"
OPENSSL_ENC = ["enc", "-aes-256-cbc", "-pbkdf2", "-iter", "600000", "-salt", "-pass", "env:TF_PLAN_KEY_PROD"]


def summary(text):
    """Append text to the job summary, or print it outside Actions."""
    path = os.environ.get("GITHUB_STEP_SUMMARY")
    if path:
        with open(path, "a") as f:
            f.write(text.rstrip("\n") + "\n")
    else:
        print(text)


def output(name, value):
    path = os.environ.get("GITHUB_OUTPUT")
    if path:
        with open(path, "a") as f:
            f.write("%s=%s\n" % (name, value))


def root_in(tf_dir, root):
    return os.path.join(tf_dir, "stacks", root)


def irm_changes(plan_json):
    """`address: actions` per changed resource: all that an IRM sink may carry."""
    out = []
    for rc in plan_json.get("resource_changes", []):
        actions = rc.get("change", {}).get("actions", [])
        if actions and actions != ["no-op"] and actions != ["read"]:
            out.append("%s: %s" % (rc.get("address", "?"), ",".join(actions)))
    return out


def counts_line(plan_json):
    return "%d to add, %d to change, %d to destroy" % tf.counts(plan_json)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def matrix_names(stack, env):
    """A matrix job (plan, drift) passes its stack's secrets as TF_CI_READ_TOKEN, TF_CI_FLEET_READ_TOKEN and
    TF_CI_WEBHOOK, because Actions does not evaluate an expression in an `env` key. They become the
    stack's own names here, and the generic ones are dropped."""
    env = dict(env)
    key = tf.TOKEN_STACK.get(stack)
    moves = {"TF_CI_READ_TOKEN": "GRAFANA_%s_READ_TOKEN" % key,
             "TF_CI_FLEET_READ_TOKEN": "GRAFANA_%s_FLEET_READ_TOKEN" % key,
             "TF_CI_WEBHOOK": tf.WEBHOOK.get(stack)}
    for generic, own in moves.items():
        value = env.pop(generic, None)
        if value and own and key:
            env[own] = value
    return env


def need(env, *names):
    unset = [n for n in names if not env.get(n)]
    if unset:
        raise tf.Exit(3, "%s unset" % ", ".join(unset))


# What Terraform may see of the job's environment. Everything else (the OIDC request token and URL,
# GITHUB_TOKEN, the runner's own variables) is dropped, so a gap in tf_policy.py that let a plan read
# /proc/self/environ would find only what the plan already holds.
ALLOWED_ENV = re.compile(r"^(PATH|HOME|TMPDIR|LANG|LC_[A-Z]+|TZ|SSL_CERT_FILE|SSL_CERT_DIR|TF_[A-Z_]+|GRAFANA_AUTH"
                         r"|" + IRM_TOKEN_VAR + r"|GOOGLE_APPLICATION_CREDENTIALS|GOOGLE_GHA_CREDS_PATH"
                         r"|GOOGLE_CLOUD_PROJECT|GCLOUD_PROJECT|CLOUDSDK_[A-Z_]+)$")


def minimal(tenv):
    return {k: v for k, v in tenv.items() if ALLOWED_ENV.match(k)}


def root_env(root, env, op):
    """The child environment for root: GRAFANA_AUTH, the webhook, and for solo7-irm the IRM token, plus
    only what Terraform and the gcs backend need to run (ALLOWED_ENV)."""
    token = "GRAFANA_%s_%s_TOKEN" % (tf.TOKEN_STACK[root], "APPLY" if op == "apply" else "READ")
    webhook = tf.WEBHOOK.get(root)
    names = [token] + ([webhook] if webhook else [])
    need(env, *names)
    tenv = tf.child_env(env, token, webhook)
    if root == IRM and env.get("GRAFANA_SOLO7_IRM_TOKEN"):
        tenv[IRM_TOKEN_VAR] = env["GRAFANA_SOLO7_IRM_TOKEN"]
    return minimal(tenv)


def init(cwd, root, tenv, quiet=False):
    tf.say("init %s" % root)
    rc, out = tf.run(["init", "-input=false", "-lockfile=readonly"], cwd, tenv, capture=quiet)
    if rc and quiet:
        raise tf.Exit(1, "init %s failed (terraform exit %d); output withheld for %s" % (root, rc, root))
    tf.checked("init", rc, out)


def plan_to(cwd, root, tenv, plan_file, lock=True, detailed=False):
    """terraform plan -out=plan_file; the exit code. solo7-irm's output is never printed."""
    args = ["plan", "-input=false", "-out=" + plan_file] + ([] if lock else ["-lock=false"])
    args += ["-detailed-exitcode"] if detailed else []
    quiet = root == IRM
    tf.say("plan %s%s" % (root, " (output withheld: addresses and actions only)" if quiet else ""))
    rc, out = tf.run(args, cwd, tenv, capture=quiet)
    if rc not in ((0, 2) if detailed else (0,)):
        if quiet:
            raise tf.Exit(1, "plan %s failed (terraform exit %d); output withheld" % (root, rc))
        tf.checked("plan", rc, out)
    return rc


def show_json(cwd, tenv, plan_file):
    rc, shown = tf.run(["show", "-json", plan_file], cwd, tenv, capture=True)
    if rc:
        raise tf.Exit(1, "show failed (terraform exit %d)" % rc)
    return json.loads(shown)


def show_text(cwd, tenv, plan_file):
    rc, shown = tf.run(["show", "-no-color", plan_file], cwd, tenv, capture=True)
    tf.checked("show", rc, shown)
    return shown


def plan_report(root, commit, cwd, tenv, plan_file):
    """The summary section for a plan: root, commit, counts, then the plan (IRM: addresses and actions)."""
    pj = show_json(cwd, tenv, plan_file)
    lines = ["### %s at %s" % (root, commit), "", counts_line(pj), ""]
    if root == IRM:
        changes = irm_changes(pj)
        lines += ["Changes (addresses and actions only; read the plan with `make tf-plan-show`):", "```"]
        lines += changes or ["(none)"]
        lines += ["```"]
    else:
        lines += ["```", show_text(cwd, tenv, plan_file).rstrip(), "```"]
    return "\n".join(lines), pj


# --- jobs --------------------------------------------------------------------------------------

def cmd_claims(env):
    """Print this job's OIDC claims (ADR-0012 decision 3's debug step). The token is never printed."""
    url, bearer = env.get("ACTIONS_ID_TOKEN_REQUEST_URL"), env.get("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
    if not url or not bearer:
        raise tf.Exit(3, "no OIDC token available to this job (permissions: id-token: write)")
    audience = "https://iam.googleapis.com/" + env.get("GCP_WIF_PROVIDER", "")
    req = urllib.request.Request(url + "&audience=" + urllib.parse.quote(audience, safe=""),
                                 headers={"Authorization": "Bearer " + bearer})
    with urllib.request.urlopen(req, timeout=30) as r:
        token = json.load(r)["value"]
    payload = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    keep = {k: claims[k] for k in sorted(claims) if k not in ("jti", "nbf", "iat", "exp")}
    text = json.dumps(keep, indent=2, sort_keys=True)
    tf.say("OIDC claims (audience %s):" % audience)
    print(text)
    summary("### OIDC claims\n\n```json\n%s\n```" % text)
    return 0


def cmd_pr_plan(stack, tf_dir, commit, env):
    if stack not in ("solo7dev", "solo7"):
        raise tf.Exit(2, "pr-plan plans solo7dev or solo7; the IRM root is never planned on a pull request")
    tenv = root_env(stack, env, "plan")
    cwd = root_in(tf_dir, stack)
    init(cwd, stack, tenv)
    tmp = tempfile.mkdtemp(prefix="andara-tf-")
    try:
        plan_file = os.path.join(tmp, "plan")
        plan_to(cwd, stack, tenv, plan_file, lock=False)
        text, _ = plan_report(stack, commit, cwd, tenv, plan_file)
        summary(text)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    return 0


def cmd_apply(stack, commit, env):
    if stack != "solo7dev":
        raise tf.Exit(2, "apply applies solo7dev; solo7 and solo7-irm go through plan-prod and apply-prod")
    summary("### apply %s at %s" % (stack, commit))
    rc = tf.cmd_apply(stack, env)
    summary("applied; a second plan is empty")
    return rc


def encrypt(plain, enc, env):
    p = subprocess.run(["openssl"] + OPENSSL_ENC + ["-in", plain, "-out", enc],
                       env={"PATH": env.get("PATH", "/usr/bin:/bin"), "TF_PLAN_KEY_PROD": env["TF_PLAN_KEY_PROD"]},
                       capture_output=True)
    if p.returncode:
        raise tf.Exit(1, "encrypting the plan failed (openssl exit %d)" % p.returncode)


def decrypt(enc, env):
    """The plaintext plan as bytes, in memory."""
    p = subprocess.run(["openssl"] + OPENSSL_ENC + ["-d", "-in", enc],
                       env={"PATH": env.get("PATH", "/usr/bin:/bin"), "TF_PLAN_KEY_PROD": env["TF_PLAN_KEY_PROD"]},
                       capture_output=True)
    if p.returncode:
        raise tf.Exit(1, "decrypting %s failed (openssl exit %d): wrong TF_PLAN_KEY_PROD?"
                      % (os.path.basename(enc), p.returncode))
    return p.stdout


def cmd_plan_prod(out, commit, env):
    need(env, "TF_PLAN_KEY_PROD")
    os.makedirs(out, exist_ok=True)
    manifest = {"commit": commit, "plans": {}}
    changed = False
    tmp = tempfile.mkdtemp(prefix="andara-tf-")
    try:
        for root in PROD_ROOTS:
            tenv = root_env(root, env, "plan")
            cwd = root_in(tf.TF_DIR, root)
            init(cwd, root, tenv, quiet=root == IRM)
            plan_file = os.path.join(tmp, root + ".tfplan")
            # -lock=false: the prod-plan account holds objectViewer only; the concurrency group
            # serialises this job against apply-prod (ADR-0012 decision 3).
            plan_to(cwd, root, tenv, plan_file, lock=False)
            text, pj = plan_report(root, commit, cwd, tenv, plan_file)
            digest = sha256_file(plan_file)
            summary(text + "\n\nsha256 of the plan file: `%s`\n" % digest)
            tf.say("%s: %s; sha256 %s" % (root, counts_line(pj), digest))
            changed = changed or tf.counts(pj) != (0, 0, 0)
            encrypt(plan_file, os.path.join(out, root + ".tfplan.enc"), env)
            os.remove(plan_file)
            manifest["plans"][root] = digest
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    with open(os.path.join(out, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2)
    output("changes", "true" if changed else "false")
    summary("Changes to apply: %s. Read the full plans with `make tf-plan-show RUN=%s STACK=<solo7|solo7-irm>`."
            % ("yes" if changed else "none", env.get("GITHUB_RUN_ID", "<id>")))
    return 0


def read_manifest(directory):
    try:
        with open(os.path.join(directory, "manifest.json")) as f:
            return json.load(f)
    except (OSError, ValueError) as e:
        raise tf.Exit(1, "no readable manifest.json in the plan artifact: %s" % e)


def cmd_apply_prod(src, root, main_head, env):
    if root not in PROD_ROOTS:
        raise tf.Exit(2, "apply-prod applies solo7-irm or solo7")
    need(env, "TF_PLAN_KEY_PROD")
    manifest = read_manifest(src)
    if manifest.get("commit") != main_head:
        raise tf.Exit(1, "superseded plan: it was made at %s, and main is now at %s. Nothing was applied; "
                         "re-run plan-prod on the new head and approve again" % (manifest.get("commit"), main_head))
    want = manifest.get("plans", {}).get(root)
    if not want:
        raise tf.Exit(1, "the artifact has no plan for %s" % root)
    tenv = root_env(root, env, "apply")
    cwd = root_in(tf.TF_DIR, root)
    tmp = tempfile.mkdtemp(prefix="andara-tf-")
    try:
        plan_file = os.path.join(tmp, "plan")
        with open(plan_file, "wb") as f:
            f.write(decrypt(os.path.join(src, root + ".tfplan.enc"), env))
        digest = sha256_file(plan_file)
        tf.say("%s: sha256 of the plan file %s" % (root, digest))
        summary("### apply-prod %s at %s\n\nsha256 of the plan file: `%s`" % (root, main_head, digest))
        if digest != want:
            raise tf.Exit(1, "the plan's sha256 %s is not the one plan-prod printed (%s); nothing applied"
                          % (digest, want))
        quiet = root == IRM
        init(cwd, root, tenv, quiet=quiet)
        tf.say("apply %s%s" % (root, " (output withheld)" if quiet else ""))
        rc, out = tf.run(["apply", "-input=false", plan_file], cwd, tenv, capture=quiet)
        if rc:
            if quiet:
                raise tf.Exit(1, "apply %s failed (terraform exit %d); output withheld" % (root, rc))
            tf.checked("apply", rc, out)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    tf.say("verify %s: a second plan must be empty" % root)
    rc, out = tf.run(["plan", "-input=false", "-detailed-exitcode"], cwd, tenv, capture=root == IRM)
    if root == IRM:
        tf.say("verify %s: second plan exit %d" % (root, rc))
    if rc == 2:
        raise tf.Exit(1, "the second plan is not empty: %s did not converge" % root)
    if rc:
        raise tf.Exit(1, "the second plan of %s failed (terraform exit %d)" % (root, rc))
    summary("applied; a second plan is empty")
    return 0


def run_url(env):
    if env.get("GITHUB_RUN_ID"):
        return "%s/%s/actions/runs/%s" % (env.get("GITHUB_SERVER_URL", "https://github.com"),
                                          env.get("GITHUB_REPOSITORY", ""), env["GITHUB_RUN_ID"])
    return "(not a GitHub Actions run)"


def gh(*args, input_text=None):
    p = subprocess.run(["gh", *args], input=input_text, capture_output=True, text=True)
    if p.returncode:
        raise tf.Exit(1, "gh %s failed: %s" % (" ".join(args[:2]), p.stderr.strip()))
    return p.stdout


def drift_issue(root, title, body):
    """Open the drift:<root> issue, or add body to the open one."""
    label = "drift:" + root
    gh("label", "create", label, "--color", "B60205", "--description",
       "Terraform drift or a failed drift job for %s (AW-INF-068)" % root, "--force")
    found = json.loads(gh("issue", "list", "--label", label, "--state", "open", "--json", "number", "--limit", "1"))
    if found:
        gh("issue", "comment", str(found[0]["number"]), "--body-file", "-", input_text=body)
        tf.say("updated issue #%s (%s)" % (found[0]["number"], label))
    else:
        gh("issue", "create", "--title", title, "--label", label, "--body-file", "-", input_text=body)
        tf.say("opened an issue labelled %s" % label)
    # The workflow's fallback step opens the issue itself unless this says it was done, so a drift step that
    # failed any other way (an exception that isn't tf.Exit, a failed gh) is still reported.
    output("reported", "true")


def close_drift_issue(root, url):
    """A clean check closes the open drift:<root> issue: an open one means drift or a failure now. The
    comparison already succeeded, so a gh failure here is reported, never turned into a failed check."""
    try:
        found = json.loads(gh("issue", "list", "--label", "drift:" + root, "--state", "open", "--json", "number",
                              "--limit", "1") or "[]")
    except tf.Exit as e:
        tf.say("drift %s: none, but listing its drift issue failed: %s" % (root, e.msg))
        return
    if not found:
        return
    number = found[0]["number"]
    try:
        gh("issue", "close", str(number), "--comment", "No drift in `%s` as of %s; closing." % (root, url))
    except tf.Exit as e:
        tf.say("drift %s: none, but closing issue #%s failed: %s" % (root, number, e.msg))
        return
    tf.say("closed issue #%s (drift:%s)" % (number, root))


# The drift check plans with -lock=false (its accounts can only read), so a plan that overlapped an apply could
# report a half-applied stack as drift. A concurrency group would serialise them, but a scheduled job queued
# into an apply's group can replace that group's pending apply (GitHub keeps one pending job per group). So
# after planning, the check asks whether one of the workflow's push or dispatch runs overlapped it, and calls
# its result inconclusive if so. A run awaiting approval in an environment ("waiting") changes nothing and
# doesn't count, so a prod approval left pending never stalls drift.
APPLY_EVENTS = ("push", "workflow_dispatch")
APPLYING = ("queued", "in_progress", "pending", "requested")
CLOCK_SLACK_SECONDS = 60


def iso(t):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t))


def overlapping_apply(env, started):
    """A push or dispatch run of this workflow that is applying now, or that started or changed at or after
    started (a time.time() value, less a minute of clock slack), or None. One query per event, so --limit
    counts only those runs."""
    since = iso(started - CLOCK_SLACK_SECONDS)
    me = str(env.get("GITHUB_RUN_ID", ""))
    for event in APPLY_EVENTS:
        runs = json.loads(gh("run", "list", "--workflow", "terraform.yaml", "--event", event, "--json",
                             "databaseId,event,status,startedAt,updatedAt", "--limit", "30") or "[]")
        for r in runs:
            if str(r.get("databaseId")) == me:
                continue
            if r.get("status") in APPLYING or max(str(r.get("startedAt") or ""), str(r.get("updatedAt") or "")) >= since:
                return r
    return None


def cmd_drift(root, issue, env):
    if root not in tf.ROOTS:
        raise tf.Exit(2, "STACK must be one of %s" % ", ".join(tf.ROOTS))
    if root != "solo7dev" and env.get("GITHUB_ACTIONS") != "true":
        raise tf.Exit(2, "%s's drift is checked by CI only (ADR-0012 decision 4)" % root)
    env = tf.load_box_env(env)
    started = time.time()
    moved = None
    try:
        tenv = root_env(root, env, "plan")
        cwd = root_in(tf.TF_DIR, root)
        init(cwd, root, tenv, quiet=root == IRM)
        tmp = tempfile.mkdtemp(prefix="andara-tf-")
        try:
            plan_file = os.path.join(tmp, "plan")
            rc = plan_to(cwd, root, tenv, plan_file, lock=False, detailed=True)
            text, pj = (plan_report(root, "the live stack", cwd, tenv, plan_file) if rc else (None, None))
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
        if issue:
            moved = overlapping_apply(env, started)
    except tf.Exit as e:
        if issue and e.code != 2:
            drift_issue(root, "Terraform drift job failed: %s" % root,
                        "The drift job for `%s` failed before it could report: %s\n\nRun: %s\n"
                        % (root, "credential unset" if e.code == 3 else e.msg, run_url(env)))
        raise
    if moved:
        tf.say("drift %s: inconclusive; run %s (%s) overlapped this check, so the issue is left as it was. The "
               "next scheduled check covers it." % (root, moved.get("databaseId"), moved.get("status")))
        summary("### drift %s: inconclusive\n\nRun %s (%s) overlapped the check; nothing was reported."
                % (root, moved.get("databaseId"), moved.get("status")))
        return 0
    if rc == 0:
        tf.say("drift %s: none" % root)
        if issue:
            close_drift_issue(root, run_url(env))
        return 0
    tf.say("drift %s: %s" % (root, counts_line(pj)))
    if issue:
        body = "The live `%s` stack differs from `main` (someone changed a managed resource by hand, or an "
        body = body % root + "apply did not finish).\n\nRun: %s\n\n%s\n" % (run_url(env), text)
        drift_issue(root, "Terraform drift: %s" % root, body)
    else:
        print(text)
    raise tf.Exit(1, "drift in %s: %s" % (root, counts_line(pj)))


def cmd_plan_show(run, root, env):
    if not run:
        raise tf.Exit(2, "RUN is required: make tf-plan-show RUN=<run id> STACK=<solo7|solo7-irm>")
    if root not in PROD_ROOTS:
        raise tf.Exit(2, "STACK must be solo7 or solo7-irm (got %r)" % root)
    env = tf.load_box_env(env)
    need(env, "TF_PLAN_KEY_PROD")
    if not hasattr(os, "memfd_create"):
        raise tf.Exit(1, "plan-show needs Linux (memfd_create), so the plaintext plan never touches a disk")
    tmp = tempfile.mkdtemp(prefix="andara-tf-plan-show-")
    try:
        gh("run", "download", run, "--name", ARTIFACT, "--dir", tmp)
        manifest = read_manifest(tmp)
        plain = decrypt(os.path.join(tmp, root + ".tfplan.enc"), env)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    digest = hashlib.sha256(plain).hexdigest()
    fd = os.memfd_create("andara-tfplan")
    try:
        os.write(fd, plain)
        tenv = tf.offline(root, env)
        tf.offline_init(root, tenv)
        tf.say("%s plan from run %s, made at %s" % (root, run, manifest.get("commit")))
        rc, _ = tf.run(["show", "/proc/self/fd/%d" % fd], tf.root_dir(root), tenv, pass_fds=(fd,))
        tf.checked("show", rc, "")
    finally:
        os.close(fd)
    tf.say("sha256 of the plan file: %s%s" % (digest, "" if digest == manifest.get("plans", {}).get(root)
                                               else " (does NOT match the manifest's %s)"
                                               % manifest.get("plans", {}).get(root)))
    return 0


# What the terraform workflow needs, where (ADR-0012 decision 4's table). Names only; values are pasted by
# Brian with `gh secret set` and never pass through this script. A test keeps this in step with the workflow.
REPO_SECRETS = ("GRAFANA_SOLO7DEV_READ_TOKEN", "GRAFANA_SOLO7_READ_TOKEN", "GRAFANA_SOLO7DEV_FLEET_READ_TOKEN",
                "GRAFANA_SOLO7_FLEET_READ_TOKEN", "SLACK_WEBHOOK_DEV", "SLACK_WEBHOOK_PROD")
REPO_VARS = ("GCP_WIF_PROVIDER", "TFSTATE_SOLO7DEV_PLAN_SA", "TFSTATE_SOLO7_PLAN_SA", "TFSTATE_SOLO7DEV_APPLY_SA",
             "TFSTATE_SOLO7_APPLY_SA", "TFSTATE_SOLO7_IRM_APPLY_SA", "TFSTATE_PRODPLAN_SA")
ENVIRONMENTS = {
    "andara-main": {"reviewer": False,
                    "secrets": ("GRAFANA_SOLO7DEV_APPLY_TOKEN", "GRAFANA_SOLO7DEV_FLEET_APPLY_TOKEN")},
    "andara-prod-plan": {"reviewer": False, "secrets": ("GRAFANA_SOLO7_IRM_TOKEN", "TF_PLAN_KEY_PROD")},
    "andara-prod-apply": {"reviewer": True,
                          "secrets": ("GRAFANA_SOLO7_APPLY_TOKEN", "GRAFANA_SOLO7_FLEET_APPLY_TOKEN",
                                      "GRAFANA_SOLO7_IRM_TOKEN", "TF_PLAN_KEY_PROD")},
}


def gh_json(*args):
    p = subprocess.run(["gh", "api", *args], capture_output=True, text=True)
    if p.returncode:
        return None
    return json.loads(p.stdout or "null")


def cmd_environments(apply, env):
    """Report (and with apply, create) the three environments; report every missing secret and variable."""
    repo = env.get("GITHUB_REPOSITORY") or gh_json("repos/{owner}/{repo}", "--jq", "{full_name}")["full_name"]
    missing = []
    me = gh_json("user") if apply else None
    for name, want in ENVIRONMENTS.items():
        current = gh_json("repos/%s/environments/%s" % (repo, name))
        if apply:
            body = {"deployment_branch_policy": {"protected_branches": False, "custom_branch_policies": True}}
            if want["reviewer"]:
                body["reviewers"] = [{"type": "User", "id": me["id"]}]
            p = subprocess.run(["gh", "api", "-X", "PUT", "repos/%s/environments/%s" % (repo, name), "--input", "-"],
                               input=json.dumps(body), capture_output=True, text=True)
            if p.returncode:
                raise tf.Exit(1, "creating environment %s failed: %s" % (name, p.stderr.strip()))
            policies = gh_json("repos/%s/environments/%s/deployment-branch-policies" % (repo, name)) or {}
            if not any(b.get("name") == "main" for b in policies.get("branch_policies", [])):
                gh_json("-X", "POST", "repos/%s/environments/%s/deployment-branch-policies" % (repo, name),
                        "-f", "name=main", "-f", "type=branch")
            tf.say("environment %s: deployable from main only%s" % (name, ", reviewer %s" % me["login"]
                                                                     if want["reviewer"] else ""))
            current = gh_json("repos/%s/environments/%s" % (repo, name))
        if not current:
            missing.append("environment %s (make tf-ci-environments APPLY=1)" % name)
            continue
        rules = [r.get("type") for r in current.get("protection_rules", [])]
        if want["reviewer"] and "required_reviewers" not in rules:
            missing.append("environment %s: a required reviewer (make tf-ci-environments APPLY=1)" % name)
        policies = gh_json("repos/%s/environments/%s/deployment-branch-policies" % (repo, name)) or {}
        if [b.get("name") for b in policies.get("branch_policies", [])] != ["main"]:
            missing.append("environment %s: deployable from main only (make tf-ci-environments APPLY=1)" % name)
        have = {s["name"] for s in (gh_json("repos/%s/environments/%s/secrets?per_page=100" % (repo, name)) or {}).get("secrets", [])}
        for s in want["secrets"]:
            if s not in have:
                missing.append("secret %s in %s: gh secret set %s --env %s" % (s, name, s, name))
    have = {s["name"] for s in (gh_json("repos/%s/actions/secrets?per_page=100" % repo) or {}).get("secrets", [])}
    missing += ["repository secret %s: gh secret set %s" % (s, s) for s in REPO_SECRETS if s not in have]
    have = {v["name"] for v in (gh_json("repos/%s/actions/variables?per_page=100" % repo) or {}).get("variables", [])}
    missing += ["repository variable %s (make tf-bootstrap-gcp sets it)" % v for v in REPO_VARS if v not in have]
    box = tf.load_box_env({})
    if not box.get("TF_PLAN_KEY_PROD"):
        missing.append("TF_PLAN_KEY_PROD in .local/box.env (the same value as the environments'; tf-plan-show reads it)")
    for m in missing:
        tf.say("missing: " + m)
    if missing:
        raise tf.Exit(1, "%d item(s) missing for the terraform workflow" % len(missing))
    tf.say("the terraform workflow's environments, secrets and variables are all present")
    return 0


def cmd_plan_key(rotate, env):
    """Make TF_PLAN_KEY_PROD: one random key, set in both prod environments and .local/box.env (mode 0600).
    Refuses to replace an existing box.env key unless rotate. The key reaches gh on stdin, never argv."""
    path = tf.box_env_path(env) or tf.BOX_ENV
    existing = tf.load_box_env({}, path) if os.path.exists(path) else {}
    if existing.get("TF_PLAN_KEY_PROD") and not rotate:
        raise tf.Exit(1, "%s already holds TF_PLAN_KEY_PROD; ROTATE=1 replaces it (and invalidates any plan "
                         "artifact not yet applied)" % path)
    key = base64.b64encode(os.urandom(32)).decode()
    for name in ("andara-prod-plan", "andara-prod-apply"):
        p = subprocess.run(["gh", "secret", "set", "TF_PLAN_KEY_PROD", "--env", name], input=key,
                           capture_output=True, text=True)
        if p.returncode:
            raise tf.Exit(1, "gh secret set TF_PLAN_KEY_PROD --env %s failed: %s (make tf-ci-environments APPLY=1 "
                             "creates the environments)" % (name, p.stderr.strip()))
        tf.say("TF_PLAN_KEY_PROD set in %s" % name)
    lines = []
    if os.path.exists(path):
        with open(path) as f:
            lines = [ln for ln in f.read().splitlines() if not ln.strip().removeprefix("export ").lstrip().startswith("TF_PLAN_KEY_PROD=")]
    lines.append("TF_PLAN_KEY_PROD=" + key)
    os.makedirs(os.path.dirname(path), exist_ok=True)
    fd = os.open(path + ".tmp", os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w") as f:
        f.write("\n".join(lines) + "\n")
    os.replace(path + ".tmp", path)
    os.chmod(path, 0o600)
    tf.say("TF_PLAN_KEY_PROD written to %s (mode 0600); the same key is in both prod environments" % path)
    return 0


def main(argv, environ=None):
    p = argparse.ArgumentParser(prog="tf_ci.py")
    p.add_argument("cmd", choices=("claims", "pr-plan", "apply", "plan-prod", "apply-prod", "drift",
                                   "plan-show", "environments", "plan-key"))
    p.add_argument("--stack", default="")
    p.add_argument("--root", default="")
    p.add_argument("--tf-dir", default="")
    p.add_argument("--commit", default="")
    p.add_argument("--out", default="")
    p.add_argument("--in", dest="src", default="")
    p.add_argument("--main-head", default="")
    p.add_argument("--run", default="")
    p.add_argument("--issue", action="store_true")
    p.add_argument("--apply", action="store_true")
    p.add_argument("--rotate", action="store_true")
    try:
        a = p.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    env = matrix_names(a.stack, dict(os.environ if environ is None else environ))
    try:
        if a.cmd == "claims":
            return cmd_claims(env)
        if a.cmd == "pr-plan":
            return cmd_pr_plan(a.stack, a.tf_dir or tf.TF_DIR, a.commit, env)
        if a.cmd == "apply":
            return cmd_apply(a.stack, a.commit, env)
        if a.cmd == "plan-prod":
            return cmd_plan_prod(a.out, a.commit, env)
        if a.cmd == "apply-prod":
            return cmd_apply_prod(a.src, a.root, a.main_head, env)
        if a.cmd == "drift":
            return cmd_drift(a.stack, a.issue, env)
        if a.cmd == "environments":
            return cmd_environments(a.apply, env)
        if a.cmd == "plan-key":
            return cmd_plan_key(a.rotate, env)
        return cmd_plan_show(a.run, a.stack, env)
    except tf.Exit as e:
        if e.msg:
            print("tf: " + e.msg, file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
