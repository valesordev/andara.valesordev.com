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
variable already set wins; in a linked worktree with no box.env of its own, the main checkout's
is read; ANDARA_BOX_ENV names another file); a box.env readable by group or others is refused. plan reads
GRAFANA_<STACK>_READ_TOKEN, apply GRAFANA_<STACK>_APPLY_TOKEN (<STACK> is SOLO7DEV, or SOLO7
for solo7 and solo7-irm), and both read SLACK_WEBHOOK_DEV (solo7dev) or SLACK_WEBHOOK_PROD
(solo7). A root whose provider block sets fleet_management_url also needs
GRAFANA_<STACK>_FLEET_READ_TOKEN (plan) or GRAFANA_<STACK>_FLEET_APPLY_TOKEN (apply) (AW-INF-051).
The token reaches the provider only as GRAFANA_AUTH, the Fleet token only as
GRAFANA_FLEET_MANAGEMENT_AUTH (`<fleet_user>:<token>`, fleet_user from the root's committed
terraform.tfvars), the webhook as TF_VAR_slack_webhook. State is reached with Application Default
Credentials.

apply plans to a file, applies exactly that plan, then plans again and fails unless the second
plan is empty. A plan with nothing to do prints `0 to add, 0 to change, 0 to destroy` and
applies nothing. A held state lock fails at once with `tf: lock held: id=<id> who=<holder>`;
`make tf-unlock STACK=<root> LOCK_ID=<id>` releases it.

test-alert creates, with the apply token, one always-firing rule labelled environment=<env>
in a throwaway root (deploy/terraform/grafana/test-alert, local state under
.local/tf-test-alert/), so the stack's policy tree can be checked end to end (AC-7);
test-alert-clean destroys it.

fmt-check, validate and test need no credential and no backend: they run in a data directory
of their own (.terraform/check) with `-backend=false`, against the committed lock file. test also
copies the tree to a temporary directory and breaks deploy/grafana/dashboards/tick-health.json one
way at a time (DASHBOARD_BREAKS); solo7dev's tests must fail on each, naming the module's
precondition (AW-INF-049 AC-2).

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


def _main_checkout():
    """The main checkout's directory when REPO is a linked worktree, else REPO."""
    try:
        common = subprocess.run(["git", "-C", REPO, "rev-parse", "--path-format=absolute", "--git-common-dir"],
                                capture_output=True, text=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return REPO
    return os.path.dirname(common) if os.path.basename(common) == ".git" else REPO


# A worktree (.claude/worktrees/<name>) has its own .local/, but credentials live once, in the
# main checkout's .local/box.env. ANDARA_BOX_ENV names another file outright.
MAIN_BOX_ENV = os.path.join(_main_checkout(), ".local", "box.env")
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


def tfvar(stack, name):
    """The string value of name committed in stack's terraform.tfvars."""
    with open(os.path.join(root_dir(stack), "terraform.tfvars")) as f:
        m = re.search(r'^%s\s*=\s*"([^"]+)"' % re.escape(name), f.read(), re.M)
    if not m:
        raise Exit(1, "no %s in stacks/%s/terraform.tfvars" % (name, stack))
    return m.group(1)


def grafana_url(stack):
    """The grafana_url committed in stack's terraform.tfvars."""
    return tfvar(stack, "grafana_url")


def has_fleet(stack):
    """Whether stack's provider block configures Fleet Management (solo7dev from AW-INF-051, solo7 in AW-INF-078),
    read from every .tf file of the root."""
    root = root_dir(stack)
    for name in sorted(os.listdir(root)):
        if name.endswith(".tf"):
            with open(os.path.join(root, name)) as f:
                if re.search(r"^\s*fleet_management_url\s*=", f.read(), re.M):
                    return True
    return False


def check_stack(stack, env):
    """Refuse an unset or unknown root, and solo7/solo7-irm outside GitHub Actions (AC-4)."""
    if stack not in ROOTS:
        raise Exit(2, "STACK must be one of %s (got %r)" % (", ".join(ROOTS), stack or ""))
    if stack != "solo7dev" and env.get("GITHUB_ACTIONS") != "true":
        raise Exit(2, "%s is planned and applied by CI only (ADR-0012 decision 4); "
                      "read a prod plan with make tf-plan-show (AW-INF-068)" % stack)


def box_env_path(env):
    """ANDARA_BOX_ENV, else this checkout's .local/box.env, else the main checkout's; None when none exists."""
    if env.get("ANDARA_BOX_ENV"):
        return env["ANDARA_BOX_ENV"]
    for path in (BOX_ENV, MAIN_BOX_ENV):
        if os.path.exists(path):
            return path
    return None


def load_box_env(env, path=None):
    """env with path's KEY=VALUE lines added where env has no value. Refuses a group- or world-readable file."""
    env = dict(env)
    path = path or box_env_path(env)
    if not path or not os.path.exists(path):
        return env
    mode = stat.S_IMODE(os.stat(path).st_mode)
    if mode & 0o077:
        raise Exit(1, "%s is mode %04o; it holds credentials and must be readable by its owner only "
                      "(chmod 600 %s)" % (path, mode, path))
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


def fleet_token(stack, op):
    """The Fleet token variable op reads for stack, or None when its root has no Fleet provider."""
    if not has_fleet(stack):
        return None
    return "GRAFANA_%s_FLEET_%s_TOKEN" % (TOKEN_STACK[stack], "APPLY" if op == "apply" else "READ")


def fleet_auth(stack, env, token):
    """The Fleet provider's basic auth, `<fleet_user>:<token>`; the one place it is composed (AW-INF-051)."""
    return "%s:%s" % (tfvar(stack, "fleet_user"), env[token])


def credentials(stack, op, env):
    """(token variable, webhook variable or None, Fleet token variable or None) for op; Exit 3 naming
    whichever is unset."""
    names = []
    token = fleet = None
    if op in ("plan", "apply"):
        token = "GRAFANA_%s_%s_TOKEN" % (TOKEN_STACK[stack], "READ" if op == "plan" else "APPLY")
        names.append(token)
        fleet = fleet_token(stack, op)
    webhook = WEBHOOK.get(stack) if op in ("plan", "apply") else None
    if webhook:
        names.append(webhook)
    if fleet:
        names.append(fleet)
    unset = [n for n in names if not env.get(n)]
    if unset:
        raise Exit(3, "%s unset (set it in the environment or .local/box.env)" % ", ".join(unset))
    return token, webhook, fleet


def child_env(env, token=None, webhook=None, data_dir=None, fleet=None):
    """The environment Terraform runs in: the token as GRAFANA_AUTH and fleet (already `user:token`) as
    GRAFANA_FLEET_MANAGEMENT_AUTH only, no other Grafana token."""
    out = {k: v for k, v in env.items()
           if not (k.startswith("GRAFANA_") and k.endswith("_TOKEN"))
           and not k.startswith("GRAFANA_FLEET_MANAGEMENT_")
           and not k.startswith("TF_VAR_") and not k.startswith("TF_CLI_ARGS")
           and k not in ("GRAFANA_AUTH", "SLACK_WEBHOOK_DEV", "SLACK_WEBHOOK_PROD", "TF_PLAN_KEY_PROD")}
    out["TF_IN_AUTOMATION"] = "1"
    out["TF_INPUT"] = "0"
    out["TF_PLUGIN_CACHE_DIR"] = PLUGIN_CACHE
    if token:
        out["GRAFANA_AUTH"] = env[token]
    if webhook:
        out["TF_VAR_slack_webhook"] = env[webhook]
    if fleet:
        out["GRAFANA_FLEET_MANAGEMENT_AUTH"] = fleet
    if data_dir:
        out["TF_DATA_DIR"] = data_dir
    return out


def run(args, cwd, env, capture=False, pass_fds=()):
    """Run terraform, streaming its output; (exit code, combined output)."""
    os.makedirs(PLUGIN_CACHE, exist_ok=True)
    proc = subprocess.Popen([terraform()] + args, cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, pass_fds=pass_fds)
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
    token, webhook, fleet = credentials(stack, "plan", env)
    tenv = child_env(env, token, webhook, fleet=fleet and fleet_auth(stack, env, fleet))
    init(stack, tenv)
    say("plan %s" % stack)
    # No lock: a plan writes nothing, and the plan accounts hold objectViewer only (ADR-0012 decision 3).
    rc, out = run(["plan", "-input=false", "-lock=false"], root_dir(stack), tenv)
    checked("plan", rc, out)
    return 0


def cmd_apply(stack, env):
    token, webhook, fleet = credentials(stack, "apply", env)
    tenv = child_env(env, token, webhook, fleet=fleet and fleet_auth(stack, env, fleet))
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
    return dashboard_breaks(env)


# AC-2's other half (AW-INF-049): the module's preconditions refuse a broken tick-health.json. Each entry
# breaks the committed source one way, and solo7dev's tests must then fail with the precondition's message.
DASHBOARD = os.path.join(REPO, "deploy", "grafana", "dashboards", "tick-health.json")
FILTER = ('environment="$environment"', 'namespace="$namespace"')


def _drop_variable(name):
    def f(d):
        d["templating"]["list"] = [v for v in d["templating"]["list"] if v["name"] != name]
    return f


def _first_target(d):
    return next(t for p in d["panels"] for t in p.get("targets", []) if "{" in t["expr"])


def _fixed_ds(d):
    d["panels"][0]["datasource"]["uid"] = "grafanacloud-prom"


def _unfilter(matcher):
    def f(d):
        t = _first_target(d)
        t["expr"] = re.sub(r",?\s*" + re.escape(matcher) + r",?\s*", "", t["expr"])
    return f


def _bare_metric(d):
    t = _first_target(d)
    t["expr"] = re.sub(r"\{[^}]*\}", "", t["expr"])


def _suffix_label(d):
    t = _first_target(d)
    t["expr"] = t["expr"].replace(FILTER[0], "kube_" + FILTER[0])


def _bare_up(d):
    d["panels"][-1]["targets"][0]["expr"] = "up"


DASHBOARD_BREAKS = (
    ("no ds variable", _drop_variable("ds"), "lacks the variable(s) ds"),
    ("no environment variable", _drop_variable("environment"), "lacks the variable(s) environment"),
    ("no namespace variable", _drop_variable("namespace"), "lacks the variable(s) namespace"),
    ("a panel with a fixed datasource", _fixed_ds, "must use the datasource"),
    ("a query without environment", _unfilter(FILTER[0]), "read a series without"),
    ("a query without namespace", _unfilter(FILTER[1]), "read a series without"),
    ("a query with a bare metric name", _bare_metric, "read a series without"),
    ("a label that only ends in environment", _suffix_label, "read a series without"),
    ("a bare metric name without an underscore", _bare_up, "read a series without"),
    ("a matcher hidden in a single-quoted value", lambda d: _set_first(
        d, "andara_ticks_total{namespace=\"$namespace\", job=',%s,'}" % FILTER[0]), "read a series without"),
    # A quote of one kind inside a string of another must not pair with the next string (pre-PR review).
    ("a matcher hidden after a quote inside a double-quoted value", lambda d: _set_first(
        d, "andara_ticks_total{a!=\"'\", b!=',%s,%s,'}" % FILTER), "read a series without"),
    ("a bare metric between double quotes inside single-quoted strings", lambda d: _set_first(
        d, "label_replace(vector(1),'a','\"','b','') or andara_ticks_total or label_replace(vector(1),'a','\"','b','')"),
     "read a series without"),
    ("a row panel", lambda d: d["panels"].append({"type": "row", "title": "r", "panels": []}), "row panel(s)"),
)

SELECTOR = "{%s, %s}" % FILTER


def _set_first(d, expr):
    _first_target(d)["expr"] = expr


# Valid PromQL the preconditions must still accept (pre-PR review: each was once refused).
DASHBOARD_ACCEPTS = (
    ("an offset", lambda d: _set_first(d, "andara_ticks_total%s offset 5m" % SELECTOR)),
    ("a number in scientific notation", lambda d: _set_first(d, "andara_simulation_lag_seconds%s * 1e3" % SELECTOR)),
    ("an upper-case grouping clause", lambda d: _set_first(
        d, "sum(rate(andara_ticks_total%s[1m])) BY (zone_id)" % SELECTOR)),
    ("single-quoted function arguments", lambda d: _set_first(
        d, "label_replace(andara_build_info%s, 'v', '$1', 'version', '(.*)')" % SELECTOR)),
    ("atan2", lambda d: _set_first(d, "andara_ticks_total%s atan2 andara_tick_overruns_total%s" % (SELECTOR, SELECTOR))),
    ("an upper-case keyword", lambda d: _set_first(
        d, "andara_ticks_total%s AND andara_tick_overruns_total%s" % (SELECTOR, SELECTOR))),
)


def dashboard_breaks(env, breaks=DASHBOARD_BREAKS, accepts=DASHBOARD_ACCEPTS):
    """Run solo7dev's tests against a copy of the tree with each break applied; each must fail as named.
    Then each accept: valid PromQL the tests must pass."""
    with open(DASHBOARD) as f:
        source = f.read()
    with tempfile.TemporaryDirectory(prefix="tf-dashboard-") as tmp:
        tf_copy = os.path.join(tmp, os.path.relpath(TF_DIR, REPO))
        shutil.copytree(TF_DIR, tf_copy, ignore=shutil.ignore_patterns(".terraform"))
        # Every other file a root may read (tf_policy's allow-list): the rules, once AW-INF-047 reads them.
        alerts = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
        os.makedirs(os.path.join(tmp, os.path.dirname(os.path.relpath(alerts, REPO))))
        shutil.copy(alerts, os.path.join(tmp, os.path.relpath(alerts, REPO)))
        dash = os.path.join(tmp, os.path.relpath(DASHBOARD, REPO))
        os.makedirs(os.path.dirname(dash))
        root = os.path.join(tf_copy, "stacks", "solo7dev")
        tenv = child_env(env, data_dir=os.path.join(tmp, ".terraform"))
        with open(dash, "w") as f:
            f.write(source)
        say("init a copy of solo7dev for the dashboard checks (no backend)")
        rc, out = run(["init", "-input=false", "-backend=false", "-lockfile=readonly"], root, tenv, capture=True)
        if rc:
            sys.stdout.write(out)
        checked("init the dashboard copy", rc, out)
        for what, breaker, message in breaks:
            d = json.loads(source)
            breaker(d)
            with open(dash, "w") as f:
                json.dump(d, f)
            rc, out = run(["test"], root, tenv, capture=True)
            # Terraform wraps a long message across lines, each behind the diagnostic's box drawing.
            plain = " ".join(re.sub(r"[│╷╵]", " ", ANSI.sub("", out)).split())
            if rc == 0 or message not in plain:
                sys.stdout.write(out)
                raise Exit(1, "tick-health.json with %s: tf-test %s, want a failure naming %r (AW-INF-049 AC-2)"
                              % (what, "passed" if rc == 0 else "failed for another reason", message))
            say("dashboard: %s is refused" % what)
        for what, accept in accepts:
            d = json.loads(source)
            accept(d)
            with open(dash, "w") as f:
                json.dump(d, f)
            rc, out = run(["test"], root, tenv, capture=True)
            if rc:
                sys.stdout.write(out)
                raise Exit(1, "tick-health.json with %s: tf-test failed, want a pass (AW-INF-049 AC-2)" % what)
            say("dashboard: %s is accepted" % what)
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
