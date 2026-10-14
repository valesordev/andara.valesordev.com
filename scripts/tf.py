#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make tf-*` — the Grafana Cloud Terraform roots under deploy/terraform/grafana (AW-INF-046, ADR-0012).

    scripts/tf.py plan   --stack <root>
    scripts/tf.py apply  --stack <root>
    scripts/tf.py unlock --stack <root> --lock-id <id>
    scripts/tf.py test-alert | test-alert-clean --stack <root> --env <environment>
    scripts/tf.py fmt-check | validate | test | lock

Roots: solo7dev, solo7, solo7-irm. There is no default root. Outside GitHub Actions
(GITHUB_ACTIONS is not `true`) plan, apply and unlock accept solo7dev only: the dev box's
state identity writes solo7dev's prefix only and holds no solo7 apply token (ADR-0012
decisions 3 and 4), so solo7 and solo7-irm are planned and applied by CI (AW-INF-068), and a
prod plan is read with `make tf-plan-show`.

Credentials come from the environment, after .local/box.env is loaded when it exists (a
variable already set wins); a box.env readable by group or others is refused. plan reads
GRAFANA_<STACK>_READ_TOKEN, apply GRAFANA_<STACK>_APPLY_TOKEN (<STACK> is SOLO7DEV, or SOLO7
for solo7 and solo7-irm), and both read SLACK_WEBHOOK_DEV (solo7dev) or SLACK_WEBHOOK_PROD
(solo7). The token reaches the provider only as GRAFANA_AUTH, the webhook as
TF_VAR_slack_webhook. State is reached with Application Default Credentials.

apply plans to a file, applies exactly that plan, then plans again and fails unless the second
plan is empty. A plan with nothing to do prints `0 to add, 0 to change, 0 to destroy` and
applies nothing. A held state lock fails at once with `tf: lock held: id=<id> who=<holder>`;
`make tf-unlock STACK=<root> LOCK_ID=<id>` releases it.

test-alert creates, with the apply token, one always-firing rule labelled environment=<env>
in a throwaway root (deploy/terraform/grafana/test-alert, local state under
.local/tf-test-alert/), so the stack's policy tree can be checked end to end (AC-7);
test-alert-clean destroys it.

fmt-check, validate and test need no credential and no backend: they run in a data directory
of their own (.terraform/check) with `-backend=false`, against the committed lock file.

Exit codes: 0 ok; 1 a Terraform, API, lock or assertion failure; 2 usage; 3 a credential unset.
"""

import argparse
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TF_DIR = os.path.join(REPO, "deploy", "terraform", "grafana")
ROOTS = ("solo7dev", "solo7", "solo7-irm")
TEST_ALERT = "test-alert"
# The configurations validate and lock cover: every root, and the test-alert one.
CONFIGS = ROOTS + (TEST_ALERT,)
TEST_ALERT_STATE = os.path.join(REPO, ".local", "tf-test-alert")
# The environments each stack's policy tree routes (ADR-0012 decision 9).
STACK_ENVIRONMENTS = {"solo7dev": ("local", "dev", "staging"), "solo7": ("prod",)}
BOX_ENV = os.path.join(REPO, ".local", "box.env")
PLUGIN_CACHE = os.path.join(REPO, ".local", "terraform-plugins")
PLATFORMS = ("linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64")

TOKEN_STACK = {"solo7dev": "SOLO7DEV", "solo7": "SOLO7", "solo7-irm": "SOLO7"}
WEBHOOK = {"solo7dev": "SLACK_WEBHOOK_DEV", "solo7": "SLACK_WEBHOOK_PROD"}

LOCK_ERROR = "Error acquiring the state lock"
LOCK_FIELD = re.compile(r"^[\s│]*(ID|Who):\s*(.*?)\s*$", re.M)
ANSI = re.compile(r"\x1b\[[0-9;]*m")


class Exit(Exception):
    def __init__(self, code, msg=None):
        super().__init__(msg)
        self.code = code
        self.msg = msg


def say(msg):
    print("tf: " + msg, flush=True)


def terraform():
    local = os.path.join(REPO, "bin", "terraform")
    found = local if os.access(local, os.X_OK) else shutil.which("terraform")
    if not found:
        raise Exit(1, "terraform not found; run make bootstrap")
    return found


def root_dir(root):
    return os.path.join(TF_DIR, TEST_ALERT) if root == TEST_ALERT else os.path.join(TF_DIR, "stacks", root)


def grafana_url(stack):
    """The grafana_url committed in stack's terraform.tfvars."""
    with open(os.path.join(root_dir(stack), "terraform.tfvars")) as f:
        m = re.search(r'^grafana_url\s*=\s*"([^"]+)"', f.read(), re.M)
    if not m:
        raise Exit(1, "no grafana_url in stacks/%s/terraform.tfvars" % stack)
    return m.group(1)


def check_stack(stack, env):
    """Refuse an unset or unknown root, and solo7/solo7-irm outside GitHub Actions (AC-4)."""
    if stack not in ROOTS:
        raise Exit(2, "STACK must be one of %s (got %r)" % (", ".join(ROOTS), stack or ""))
    if stack != "solo7dev" and env.get("GITHUB_ACTIONS") != "true":
        raise Exit(2, "%s is planned and applied by CI only (ADR-0012 decision 4); "
                      "read a prod plan with make tf-plan-show" % stack)


def load_box_env(env, path=None):
    """env with path's KEY=VALUE lines added where env has no value. Refuses a group- or world-readable file."""
    env = dict(env)
    path = path or BOX_ENV
    if not os.path.exists(path):
        return env
    mode = stat.S_IMODE(os.stat(path).st_mode)
    if mode & 0o077:
        raise Exit(1, "%s is mode %04o; it holds credentials and must be readable by its owner only "
                      "(chmod 600 %s)" % (os.path.relpath(path, REPO), mode, os.path.relpath(path, REPO)))
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            if line.startswith("export "):
                line = line[len("export "):].lstrip()
            key, sep, value = line.partition("=")
            key = key.strip()
            if not sep or not re.match(r"^[A-Za-z_][A-Za-z0-9_]*$", key):
                continue
            value = value.strip()
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "'\"":
                value = value[1:-1]
            if not env.get(key):
                env[key] = value
    return env


def credentials(stack, op, env):
    """(token variable, webhook variable or None) for op; Exit 3 naming whichever is unset."""
    names = []
    token = None
    if op in ("plan", "apply"):
        token = "GRAFANA_%s_%s_TOKEN" % (TOKEN_STACK[stack], "READ" if op == "plan" else "APPLY")
        names.append(token)
    webhook = WEBHOOK.get(stack) if op in ("plan", "apply") else None
    if webhook:
        names.append(webhook)
    unset = [n for n in names if not env.get(n)]
    if unset:
        raise Exit(3, "%s unset (set it in the environment or .local/box.env)" % ", ".join(unset))
    return token, webhook


def child_env(env, token=None, webhook=None, data_dir=None):
    """The environment Terraform runs in: the token as GRAFANA_AUTH only, no other Grafana token."""
    out = {k: v for k, v in env.items()
           if not (k.startswith("GRAFANA_") and k.endswith("_TOKEN"))
           and not k.startswith("TF_VAR_") and not k.startswith("TF_CLI_ARGS")
           and k not in ("GRAFANA_AUTH", "SLACK_WEBHOOK_DEV", "SLACK_WEBHOOK_PROD", "TF_PLAN_KEY_PROD")}
    out["TF_IN_AUTOMATION"] = "1"
    out["TF_INPUT"] = "0"
    out["TF_PLUGIN_CACHE_DIR"] = PLUGIN_CACHE
    if token:
        out["GRAFANA_AUTH"] = env[token]
    if webhook:
        out["TF_VAR_slack_webhook"] = env[webhook]
    if data_dir:
        out["TF_DATA_DIR"] = data_dir
    return out


def run(args, cwd, env, capture=False):
    """Run terraform, streaming its output; (exit code, combined output)."""
    os.makedirs(PLUGIN_CACHE, exist_ok=True)
    proc = subprocess.Popen([terraform()] + args, cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    out = []
    for line in proc.stdout:
        out.append(line)
        if not capture:
            sys.stdout.write(line)
            sys.stdout.flush()
    return proc.wait(), "".join(out)


def lock_holder(output):
    """(id, who) from Terraform's lock error, or None when the output is not one."""
    output = ANSI.sub("", output)
    if LOCK_ERROR not in output:
        return None
    fields = dict(LOCK_FIELD.findall(output))
    return fields.get("ID", "?"), fields.get("Who", "?")


def checked(step, rc, output):
    if rc == 0:
        return
    held = lock_holder(output)
    if held:
        say("lock held: id=%s who=%s" % held)
        raise Exit(1, "the state is locked by another run; nothing was changed "
                      "(make tf-unlock STACK=<root> LOCK_ID=%s once that run is gone)" % held[0])
    raise Exit(1, "%s failed (terraform exit %d)" % (step, rc))


def counts(plan_json):
    """(add, change, destroy) from `terraform show -json <plan>`, as Terraform's own summary counts them."""
    add = change = destroy = 0
    for rc in plan_json.get("resource_changes", []):
        actions = rc.get("change", {}).get("actions", [])
        if "create" in actions:
            add += 1
        if "delete" in actions:
            destroy += 1
        if actions == ["update"]:
            change += 1
    return add, change, destroy


def init(root, env):
    say("init %s" % root)
    rc, out = run(["init", "-input=false", "-lockfile=readonly"], root_dir(root), env)
    checked("init", rc, out)


def cmd_plan(stack, env):
    token, webhook = credentials(stack, "plan", env)
    tenv = child_env(env, token, webhook)
    init(stack, tenv)
    say("plan %s" % stack)
    rc, out = run(["plan", "-input=false", "-lock-timeout=0s"], root_dir(stack), tenv)
    checked("plan", rc, out)
    return 0


def cmd_apply(stack, env):
    token, webhook = credentials(stack, "apply", env)
    tenv = child_env(env, token, webhook)
    cwd = root_dir(stack)
    init(stack, tenv)
    # The saved plan embeds the webhook, so it lives in a private directory and is removed.
    tmp = tempfile.mkdtemp(prefix="andara-tf-")
    try:
        plan_file = os.path.join(tmp, "plan")
        say("plan %s" % stack)
        rc, out = run(["plan", "-input=false", "-lock-timeout=0s", "-out=" + plan_file], cwd, tenv)
        checked("plan", rc, out)
        rc, shown = run(["show", "-json", plan_file], cwd, tenv, capture=True)
        checked("show", rc, shown)
        add, change, destroy = counts(json.loads(shown))
        say("plan: %d to add, %d to change, %d to destroy" % (add, change, destroy))
        if add == change == destroy == 0:
            say("apply %s: nothing to apply" % stack)
            return 0
        say("apply %s" % stack)
        rc, out = run(["apply", "-input=false", "-lock-timeout=0s", plan_file], cwd, tenv)
        checked("apply", rc, out)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    say("verify %s: a second plan must be empty" % stack)
    rc, out = run(["plan", "-input=false", "-lock-timeout=0s", "-detailed-exitcode"], cwd, tenv)
    if rc == 2:
        raise Exit(1, "the second plan is not empty: %s did not converge" % stack)
    checked("verify", rc, out)
    say("verify %s: 0 to add, 0 to change, 0 to destroy" % stack)
    return 0


def cmd_unlock(stack, lock_id, env):
    if not lock_id:
        raise Exit(2, "LOCK_ID is required: make tf-unlock STACK=%s LOCK_ID=<id> "
                      "(the id a failed run printed as tf: lock held: id=<id>)" % stack)
    tenv = child_env(env)
    init(stack, tenv)
    say("unlock %s %s" % (stack, lock_id))
    rc, out = run(["force-unlock", "-force", lock_id], root_dir(stack), tenv)
    checked("unlock", rc, out)
    return 0


def cmd_test_alert(stack, environment, env, destroy=False):
    if stack not in STACK_ENVIRONMENTS:
        raise Exit(2, "%s has no policy tree to test; STACK must be solo7dev or solo7" % stack)
    if environment not in STACK_ENVIRONMENTS[stack]:
        raise Exit(2, "ENV must be one of %s for %s (got %r)"
                      % (", ".join(STACK_ENVIRONMENTS[stack]), stack, environment or ""))
    token = "GRAFANA_%s_APPLY_TOKEN" % TOKEN_STACK[stack]
    if not env.get(token):
        raise Exit(3, "%s unset (set it in the environment or .local/box.env)" % token)
    name = "%s-%s" % (stack, environment)
    tenv = child_env(env, token, data_dir=os.path.join(root_dir(TEST_ALERT), ".terraform", name))
    os.makedirs(TEST_ALERT_STATE, mode=0o700, exist_ok=True)
    cwd = root_dir(TEST_ALERT)
    say("init test-alert %s" % name)
    rc, out = run(["init", "-input=false", "-lockfile=readonly", "-reconfigure",
                   "-backend-config=path=" + os.path.join(TEST_ALERT_STATE, name + ".tfstate")], cwd, tenv)
    checked("init", rc, out)
    variables = ["-var", "grafana_url=" + grafana_url(stack), "-var", "environment=" + environment]
    verb = "destroy" if destroy else "apply"
    say("%s test-alert %s" % (verb, name))
    rc, out = run([verb, "-input=false", "-auto-approve"] + variables, cwd, tenv)
    checked(verb, rc, out)
    if destroy:
        say("test alert for environment=%s removed from %s" % (environment, stack))
    else:
        say("AndaraTestAlert (environment=%s, severity=warning) fires in %s within two minutes; "
            "remove it with make tf-test-alert-clean STACK=%s ENV=%s" % (environment, stack, stack, environment))
    return 0


def offline(root, env):
    """An environment for credential-free work on root, in its own data directory."""
    return child_env(env, data_dir=os.path.join(root_dir(root), ".terraform", "check"))


def offline_init(root, tenv):
    say("init %s (no backend)" % root)
    rc, out = run(["init", "-input=false", "-backend=false", "-lockfile=readonly"], root_dir(root), tenv,
                  capture=True)
    if rc:
        sys.stdout.write(out)
    checked("init", rc, out)


def cmd_fmt_check(env):
    say("fmt-check deploy/terraform")
    rc, out = run(["fmt", "-check", "-recursive", "-diff", os.path.join(REPO, "deploy", "terraform")],
                  REPO, child_env(env))
    if rc:
        raise Exit(1, "terraform fmt would change the files above; run terraform fmt -recursive deploy/terraform")
    return 0


# Files that are the same in several roots; validate fails when a copy drifts.
SHARED = {"endpoints.tf": ROOTS, "variables.tf": ("solo7dev", "solo7")}


def check_shared():
    for name, roots in SHARED.items():
        texts = {}
        for root in roots:
            with open(os.path.join(root_dir(root), name)) as f:
                texts.setdefault(f.read(), []).append(root)
        if len(texts) > 1:
            raise Exit(1, "stacks/*/%s differs between %s; it must be the same file in each"
                          % (name, " and ".join("/".join(r) for r in texts.values())))
    say("shared files identical: %s" % ", ".join(sorted(SHARED)))


def cmd_validate(env):
    check_shared()
    for root in CONFIGS:
        tenv = offline(root, env)
        offline_init(root, tenv)
        say("validate %s" % root)
        rc, out = run(["validate"], root_dir(root), tenv)
        checked("validate " + root, rc, out)
    return 0


def cmd_test(env):
    for root in ROOTS:
        tenv = offline(root, env)
        offline_init(root, tenv)
        say("test %s" % root)
        rc, out = run(["test"], root_dir(root), tenv)
        checked("test " + root, rc, out)
    return 0


def cmd_lock(env):
    for root in CONFIGS:
        tenv = offline(root, env)
        say("init %s (no backend, lock file writable)" % root)
        rc, out = run(["init", "-input=false", "-backend=false", "-upgrade"], root_dir(root), tenv, capture=True)
        if rc:
            sys.stdout.write(out)
        checked("init " + root, rc, out)
        say("lock %s (%s)" % (root, ", ".join(PLATFORMS)))
        rc, out = run(["providers", "lock"] + ["-platform=" + p for p in PLATFORMS], root_dir(root), tenv)
        checked("lock " + root, rc, out)
    return 0


def main(argv, environ=None):
    p = argparse.ArgumentParser(prog="tf.py")
    p.add_argument("cmd", choices=("plan", "apply", "unlock", "test-alert", "test-alert-clean",
                                   "fmt-check", "validate", "test", "lock"))
    p.add_argument("--stack", default="")
    p.add_argument("--lock-id", default="")
    p.add_argument("--env", default="")
    try:
        a = p.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    env = dict(os.environ if environ is None else environ)
    try:
        if a.cmd in ("plan", "apply", "unlock", "test-alert", "test-alert-clean"):
            check_stack(a.stack, env)
            env = load_box_env(env)
            if a.cmd.startswith("test-alert"):
                return cmd_test_alert(a.stack, a.env, env, destroy=a.cmd == "test-alert-clean")
            if a.cmd == "plan":
                return cmd_plan(a.stack, env)
            if a.cmd == "apply":
                return cmd_apply(a.stack, env)
            return cmd_unlock(a.stack, a.lock_id, env)
        return {"fmt-check": cmd_fmt_check, "validate": cmd_validate, "test": cmd_test,
                "lock": cmd_lock}[a.cmd](env)
    except Exit as e:
        if e.msg:
            print("tf: " + e.msg, file=sys.stderr)
        return e.code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
