#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make platform-up | platform-check — the cluster's platform, pinned and reconciled (AW-INF-054).

ADR-0013 decision 2 makes the platform this repo's: every chart is pinned in
deploy/platform/versions.yaml, and `platform-up` installs it in dependency order and reconciles it.
A release whose chart version or values differ from the pin is upgraded, never left alone, and a
release that already matches is reported `unchanged` with its revision untouched.

  up [box|ci]   PROFILE=box (the default), in order: cert-manager; the CA root restore
                (scripts/cluster_secrets.py, cert-manager/andara-ca-tls only); andara-ca.yaml; the
                letsencrypt and letsencrypt-staging ClusterIssuers; Traefik; the namespace
                andara-shared; Strimzi watching andara-shared; Argo CD. PROFILE=ci: cert-manager,
                andara-ca.yaml and Traefik only. CI installs Argo CD with `make argocd-install`, and the
                drills job (AW-INF-061) its own Strimzi.
  check         versions.yaml is valid (every chart item has a repo, chart and version, every image a
                digest, no Image Updater key), and no file in scripts/ or .github/workflows/ outside
                scripts/tests/ carries a version of a listed item.

The context is ANDARA_KUBE_CONTEXT, default kind-andara, passed to every kubectl and helm call
through scripts/kube.py; nothing here runs `kubectl config use-context`. CLOUDFLARE_API_TOKEN is read after .local/box.env is
loaded by scripts/tf.py's rules, written to cert-manager/cloudflare-api-token by server-side apply,
and never printed. Without it both letsencrypt issuers are skipped and the last line says so.

The edge (decision 11): Traefik's websecure entrypoint trusts the PROXY protocol from the `kind`
docker network's IPv4 gateway as a /32, read with `docker network inspect kind` on every run. It's
never a committed constant. The web entrypoint trusts no PROXY header.

Every line is `platform: <step> run=<id>`.

Exit: 0 ok · 1 a release or issuer did not become Ready, a check found a problem, or a step failed ·
2 usage (an unknown PROFILE) · 3 a required credential is unset (REQUIRE_LETSENCRYPT=1 without
CLOUDFLARE_API_TOKEN), or the context, a tool or Docker is missing.
"""
import base64
import ipaddress
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import uuid

import yaml

import cluster_secrets
import kube
import tf

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
VERSIONS = os.path.join(REPO, "deploy", "platform", "versions.yaml")
CA_FILE = os.path.join(REPO, "deploy", "k8s", "cert-manager", "andara-ca.yaml")
LETSENCRYPT_FILE = os.path.join(REPO, "deploy", "k8s", "cert-manager", "letsencrypt.yaml")
TRAEFIK_VALUES = os.path.join(REPO, "deploy", "k8s", "traefik", "values.yaml")
ARGOCD_VALUES = os.path.join(REPO, "deploy", "argocd", "argocd-values.yaml")
PROFILES = ("box", "ci")
SHARED = "andara-shared"
WAIT = "5m"
# The chart items versions.yaml must list; collectors is AW-INF-065's and optional until then.
CHARTS = ("certManager", "traefik", "strimzi", "argocd")
OPTIONAL = ("collectors",)
# item: (release, namespace)
RELEASES = {"certManager": ("cert-manager", "cert-manager"), "traefik": ("traefik", "traefik"),
            "strimzi": ("strimzi", "strimzi"), "argocd": ("argocd", "argocd")}
# The RoleBindings the Strimzi chart puts in each watched namespace. Deleting and recreating the
# namespace removes them while the release still matches (AW-INF-021 AC-7), so a release without
# them is reapplied.
STRIMZI_BINDINGS = ("strimzi-cluster-operator", "strimzi-cluster-operator-watched",
                    "strimzi-cluster-operator-entity-operator-delegation")
ISSUERS = ("letsencrypt", "letsencrypt-staging")
TOKEN = "CLOUDFLARE_API_TOKEN"
TOKEN_SECRET = ("cert-manager", "cloudflare-api-token", "api-token")
FIELD_MANAGER = "andara-platform-up"


class Failed(Exception):
    code = 1


class Usage(Exception):
    code = 2


class Missing(Exception):
    code = 3


class Run:
    def __init__(self):
        self.id = time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:4]
        self.secrets = []

    def say(self, msg):
        print("platform: %s run=%s" % (self.redact(msg), self.id), flush=True)

    def redact(self, text):
        for s in self.secrets:
            text = text.replace(s, "<redacted>")
        return text


# --- versions.yaml -------------------------------------------------------------------------

def load_versions(path=VERSIONS):
    with open(path) as f:
        return yaml.safe_load(f) or {}


def images(node, path=""):
    """(path, value) of every image reference in node: a key named image or ending in Image."""
    if isinstance(node, dict):
        for k, v in node.items():
            p = "%s.%s" % (path, k) if path else str(k)
            if (k == "image" or str(k).endswith("Image")) and isinstance(v, str):
                yield p, v
            else:
                yield from images(v, p)


def keys(node, path=""):
    if isinstance(node, dict):
        for k, v in node.items():
            p = "%s.%s" % (path, k) if path else str(k)
            yield p
            yield from keys(v, p)


def validate(doc):
    """The problems with a parsed versions.yaml, as messages; [] when it's valid."""
    if not isinstance(doc, dict):
        return ["versions.yaml is not a mapping"]
    errors = []
    for k in keys(doc):
        if "updater" in k.lower().replace("_", "").replace("-", ""):
            errors.append("%s: Argo CD Image Updater is not a platform item; it keeps its pin in "
                          "scripts/argocd.py until AW-INF-059 removes it" % k)
    for k in doc:
        if k not in ("kind",) + CHARTS + OPTIONAL:
            errors.append("%s: not a platform item (%s)" % (k, ", ".join(("kind",) + CHARTS + OPTIONAL)))
    if not ((doc.get("kind") or {}).get("nodeImage")):
        errors.append("kind.nodeImage is missing")
    for p, v in images(doc):
        if not re.search(r"@sha256:[0-9a-f]{64}$", v):
            errors.append("%s: %r has no digest; pin it by tag and @sha256:" % (p, v))
    for item in CHARTS + OPTIONAL:
        if item not in doc and item in OPTIONAL:
            continue
        entry = doc.get(item)
        if not isinstance(entry, dict):
            errors.append("%s is missing" % item)
            continue
        for field in ("repo", "chart", "version"):
            if not isinstance(entry.get(field), str) or not entry[field].strip():
                errors.append("%s.%s is missing" % (item, field))
        extra = sorted(set(entry) - {"repo", "chart", "version"})
        if extra:
            errors.append("%s: unknown field(s) %s" % (item, ", ".join(extra)))
    return errors


# A version literal: 1.2.0, v1.21.1, 41.4.0.
VERSION_RE = re.compile(r"(?<![\w.])v?\d+\.\d+\.\d+(?![\w.])")
# A kind node image reference with a tag or digest.
NODE_IMAGE_RE = re.compile(r"kindest/node(:v?\d|@sha256:)")
# What names each chart item on a line, lowercased with `-`, `_` and spaces removed.
ALIASES = {"certManager": ("certmanager",), "traefik": ("traefik",), "strimzi": ("strimzi",),
           "argocd": ("argocd",)}
# Not a platform item, though its name contains one: it keeps its own pin until AW-INF-059.
NOT_ITEMS = ("imageupdater",)
PIN_DIRS = ("scripts", os.path.join(".github", "workflows"))
PIN_EXEMPT = (os.path.join("scripts", "tests"),)


def pin_findings(repo=REPO, aliases=None):
    """`path:line: <item>` for every line in scripts/ or .github/workflows/ (not scripts/tests/)
    that pins a listed item: a kind node image with a tag, or an item's name beside a version."""
    aliases = aliases or ALIASES
    found = []
    for top in PIN_DIRS:
        for root, dirs, files in os.walk(os.path.join(repo, top)):
            rel_root = os.path.relpath(root, repo)
            if any(rel_root == e or rel_root.startswith(e + os.sep) for e in PIN_EXEMPT):
                dirs[:] = []
                continue
            dirs[:] = [d for d in dirs if d != "__pycache__"]
            for name in sorted(files):
                path = os.path.join(root, name)
                try:
                    with open(path, encoding="utf-8") as f:
                        lines = f.read().splitlines()
                except (UnicodeDecodeError, OSError):
                    continue
                rel = os.path.relpath(path, repo)
                for chunk in logical_lines(lines):
                    text = " ".join(t for _, t in chunk)
                    images = [n for n, t in chunk if NODE_IMAGE_RE.search(t)]
                    if images:
                        found.append("%s:%d: kind.nodeImage (a kindest/node image)" % (rel, images[0]))
                        continue
                    versions = [n for n, t in chunk if VERSION_RE.search(t)]
                    item = versions and item_named(text, aliases)
                    if item:
                        found.append("%s:%d: %s beside a version" % (rel, versions[0], item))
                for n, item in chart_version_pairs(lines, aliases):
                    found.append("%s:%d: %s chart with a version: in its entry" % (rel, n, item))
    return found


def item_named(text, aliases):
    """The first item whose name text contains, with the names that aren't items taken out first."""
    squashed = re.sub(r"[-_\s]", "", text.lower())
    for x in NOT_ITEMS:
        squashed = squashed.replace("argocd" + x, "").replace(x, "")
    for item, names in aliases.items():
        if any(a in squashed for a in names):
            return item
    return None


def code(line):
    """line without a trailing `#` comment, for counting brackets."""
    return line.split("#", 1)[0]


def logical_lines(lines, limit=8):
    """Physical lines grouped into logical ones, each a list of (line number, text): a run ending in
    a backslash (shell) joins with no limit; a `(` or `[` still open (a Python `run([...])` call)
    joins at most limit lines, so one unbalanced bracket can't fold a file together. `{` never
    joins: a shell function body is many commands, not one."""
    out, chunk, depth = [], [], 0
    for n, line in enumerate(lines, 1):
        text = line.rstrip()
        backslash = text.endswith("\\")
        chunk.append((n, text[:-1] if backslash else text))
        c = code(text)
        depth = max(0, depth + c.count("(") + c.count("[") - c.count(")") - c.count("]"))
        if backslash or (depth > 0 and len(chunk) < limit):
            continue
        out.append(chunk)
        chunk, depth = [], 0
    if chunk:
        out.append(chunk)
    return out


CHART_KEY_RE = re.compile(r"^(\s*)(-\s+)?chart:\s*[\"']?(\S+?)[\"']?\s*(?:#.*)?$")
VERSION_KEY_RE = re.compile(r"^\s*(?:-\s+)?version:\s*[\"']?v?\d+\.\d+\.\d+")


def indent(line):
    return len(line) - len(line.lstrip())


def entry(lines, i, key_col, dash):
    """The sibling keys of the key at lines[i], column key_col, in the YAML mapping that holds it:
    bounded by indentation, so nested lists stay inside it and the next or previous list entry
    stays out. Nested values are skipped, not returned."""
    out = []
    if not dash:
        for j in range(i - 1, -1, -1):
            line = lines[j]
            if not line.strip():
                continue
            ind = indent(line)
            if ind > key_col:
                continue
            if ind == key_col:
                out.append(line)
                continue
            # A `- key:` line whose key sits at key_col starts this entry; anything else ends it.
            if line.lstrip().startswith("- ") and ind + 2 + indent(line.lstrip()[2:]) == key_col:
                out.append(line)
            break
    for j in range(i + 1, len(lines)):
        line = lines[j]
        if not line.strip():
            continue
        ind = indent(line)
        if ind < key_col or (ind == key_col and line.lstrip().startswith("- ")):
            break
        if ind == key_col:
            out.append(line)
    return out


def chart_version_pairs(lines, aliases):
    """(line, item) for YAML `chart: <item>` with a sibling `version:` literal in the same mapping."""
    found = []
    for i, line in enumerate(lines):
        m = CHART_KEY_RE.match(line)
        item = m and item_named(m.group(3), aliases)
        if not item:
            continue
        key_col = len(m.group(1)) + len(m.group(2) or "")
        if any(VERSION_KEY_RE.match(x) for x in entry(lines, i, key_col, bool(m.group(2)))):
            found.append((i + 1, item))
    return found


def check(say=print, path=VERSIONS, repo=REPO):
    """Exit status of `make platform-check`: 0 clean, 1 with every problem printed."""
    try:
        errors = validate(load_versions(path))
    except (OSError, yaml.YAMLError) as e:
        errors = ["cannot read %s: %s" % (os.path.relpath(path, repo), e)]
    errors = ["%s: %s" % (os.path.relpath(path, repo), e) for e in errors]
    errors += ["%s; the version belongs in deploy/platform/versions.yaml only" % f for f in pin_findings(repo)]
    for e in errors:
        say("platform-check: " + e)
    if errors:
        say("platform-check: %d problem(s)" % len(errors))
        return 1
    say("platform-check: ok")
    return 0


def chart(item, doc=None):
    """(repo, chart, version) of a chart item, from versions.yaml."""
    entry = (doc if doc is not None else load_versions()).get(item) or {}
    if not all(entry.get(k) for k in ("repo", "chart", "version")):
        raise Failed("%s: %s has no repo, chart and version" % (os.path.relpath(VERSIONS, REPO), item))
    return entry["repo"], entry["chart"], entry["version"]


# --- running things -------------------------------------------------------------------------

def tool(name, env_var):
    path = os.environ.get(env_var) or os.path.join(REPO, "bin", name)
    if not os.access(path, os.X_OK):
        raise Missing("%s not found; run `make bootstrap`" % os.path.relpath(path, REPO))
    return path


def run(*cmd, check=True, stdin=None):
    try:
        p = subprocess.run(list(cmd), capture_output=True, text=True, input=stdin)
    except FileNotFoundError:
        raise Missing("%s not found" % cmd[0])
    if check and p.returncode != 0:
        raise Failed("%s %s failed: %s" % (os.path.basename(cmd[0]), " ".join(cmd[1:4]), (p.stderr or p.stdout).strip()))
    return p


class Cluster:
    """kubectl and helm against one context, and docker for the gateway."""

    def __init__(self, context, env=None):
        self.context = context
        self.env = env
        self.kubectl_path = tool("kubectl", "KUBECTL")
        self.helm_path = tool("helm", "HELM")
        self.docker_path = os.environ.get("DOCKER") or "docker"

    def kubectl(self, *args, check=True, stdin=None):
        return run(*kube.kubectl(*args, tool=self.kubectl_path, env=self.env, ctx=self.context),
                   check=check, stdin=stdin)

    def helm(self, *args, check=True):
        return run(*kube.helm(*args, tool=self.helm_path, env=self.env, ctx=self.context), check=check)

    def docker(self, *args, check=True):
        return run(self.docker_path, *args, check=check)


def require_context(c):
    try:
        kube.require(env=c.env, ctx=c.context)
    except kube.Missing as e:
        raise Missing(str(e))


def gateway(inspect_json):
    """The kind network's IPv4 gateway as a /32, from `docker network inspect kind`. The cluster
    publishes IPv4 loopback only, so an IPv6 gateway is never trusted."""
    try:
        nets = json.loads(inspect_json)
    except ValueError:
        raise Failed("docker network inspect kind printed no JSON")
    found = []
    for net in nets if isinstance(nets, list) else [nets]:
        for cfg in ((net or {}).get("IPAM") or {}).get("Config") or []:
            gw = (cfg or {}).get("Gateway") or ""
            try:
                if ipaddress.ip_address(gw).version == 4:
                    found.append(gw)
            except ValueError:
                continue
    if len(found) != 1:
        raise Failed("the kind network has %d IPv4 gateways (%s); expected exactly one"
                     % (len(found), ", ".join(found) or "none"))
    return found[0] + "/32"


def edge_cidr(c):
    p = c.docker("network", "inspect", "kind", check=False)
    if p.returncode != 0:
        raise Missing("docker network inspect kind failed (%s); the cluster's kind network must exist"
                      % (p.stderr or p.stdout).strip())
    return gateway(p.stdout)


def strip_none(v):
    """v without null values, at any depth: Helm may drop a null from stored values, and a null in
    the file must not make a matching release look changed."""
    if isinstance(v, dict):
        return {k: strip_none(x) for k, x in v.items() if x is not None}
    if isinstance(v, list):
        return [strip_none(x) for x in v]
    return v


def desired_values(item, cidr=None):
    if item == "certManager":
        return {"crds": {"enabled": True}}
    if item == "traefik":
        with open(TRAEFIK_VALUES) as f:
            values = yaml.safe_load(f) or {}
        websecure = values.setdefault("ports", {}).setdefault("websecure", {})
        websecure["proxyProtocol"] = {"trustedIPs": [cidr]}
        return values
    if item == "strimzi":
        return {"watchNamespaces": [SHARED]}
    if item == "argocd":
        with open(ARGOCD_VALUES) as f:
            return yaml.safe_load(f) or {}
    raise Failed("no values for %s" % item)


def installed(c, release, ns):
    """(chart-version, status) of a release, or ("", "") when there's none."""
    p = c.helm("list", "-n", ns, "-a", "-o", "json", check=False)
    try:
        rows = json.loads(p.stdout or "[]") if p.returncode == 0 else []
    except ValueError:
        rows = []
    for r in rows:
        if r.get("name") == release:
            return r.get("chart", ""), r.get("status", "")
    return "", ""


def stored_values(c, release, ns):
    p = c.helm("get", "values", release, "-n", ns, "-o", "json", check=False)
    if p.returncode != 0 or not p.stdout.strip():
        return None
    try:
        return json.loads(p.stdout)
    except ValueError:
        return None


def bindings_missing(c, ns):
    p = c.kubectl("-n", ns, "get", "rolebinding", *STRIMZI_BINDINGS, "-o", "name", check=False)
    return p.returncode != 0


def reconcile(r, c, item, values, doc):
    """helm upgrade --install item at its pin with values, unless the release already matches."""
    repo, name, version = chart(item, doc)
    release, ns = RELEASES[item]
    have, status = installed(c, release, ns)
    want = "%s-%s" % (name, version)
    reason = None
    if not have:
        reason = "not installed"
    elif have != want:
        reason = "was %s" % have
    elif status != "deployed":
        reason = "status %s" % status
    elif strip_none(stored_values(c, release, ns)) != strip_none(json.loads(json.dumps(values))):
        reason = "values differ"
    elif item == "strimzi" and bindings_missing(c, SHARED):
        reason = "RoleBindings missing in %s" % SHARED
    if reason is None:
        r.say("%s %s unchanged" % (item, version))
        return
    with tempfile.NamedTemporaryFile("w", suffix=".yaml", prefix="platform-%s-" % item) as f:
        yaml.safe_dump(values, f)
        f.flush()
        c.helm("upgrade", "--install", release, name, "--repo", repo, "--version", version,
               "--namespace", ns, "--create-namespace", "--values", f.name, "--reset-values",
               "--wait", "--timeout", WAIT)
    r.say("%s %s installed (%s)" % (item, version, reason))


def apply_file(r, c, path, attempts=12, pause=5):
    """kubectl apply, retried while cert-manager's webhook is still starting. Returns True when
    every object was unchanged."""
    for i in range(attempts):
        p = c.kubectl("apply", "-f", path, check=False)
        if p.returncode == 0:
            lines = [l for l in p.stdout.splitlines() if l.strip()]
            return bool(lines) and all(l.endswith(" unchanged") for l in lines)
        if "webhook" not in p.stderr or i == attempts - 1:
            raise Failed("apply %s: %s" % (os.path.relpath(path, REPO), p.stderr.strip()))
        time.sleep(pause)
    return False


def wait_ready(c, kind, name, ns=None, timeout="120s"):
    args = (["-n", ns] if ns else []) + ["wait", "--for=condition=Ready", "%s/%s" % (kind, name), "--timeout=" + timeout]
    p = c.kubectl(*args, check=False)
    if p.returncode != 0:
        raise Failed("%s %s did not become Ready within %s: %s" % (kind, name, timeout, (p.stderr or p.stdout).strip()))


def ingress_node(c):
    p = c.kubectl("get", "nodes", "-l", "ingress-ready=true", "-o", "name")
    if not p.stdout.strip():
        raise Failed("no node labelled ingress-ready=true; create the cluster with `make cluster-up` "
                     "(CI: deploy/kind/config.yaml)")


def restore_ca(r, c, restore=cluster_secrets.restore):
    """Restore the exported CA root only when the cluster has none: before the first apply of
    andara-ca.yaml (ADR-0013 decision 11). A live Secret is never overwritten, so a root cert-manager
    renewed, or one an operator rotated, stays."""
    p = c.kubectl("-n", "cert-manager", "get", "secret", "andara-ca-tls", "-o", "name", check=False)
    if p.returncode == 0:
        r.say("cert-manager/andara-ca-tls present; restore skipped")
        return
    if "NotFound" not in p.stderr:
        raise Failed("read cert-manager/andara-ca-tls (only NotFound means absent): %s" % p.stderr.strip())
    try:
        restore(context=c.context, namespaces=("cert-manager",), say=lambda m: r.say("restore " + m))
    except cluster_secrets.Missing as e:
        raise Missing(str(e))
    except cluster_secrets.Failed as e:
        raise Failed("CA root restore: %s" % e)


def private_ca(r, c):
    unchanged = apply_file(r, c, CA_FILE)
    wait_ready(c, "certificate", "andara-ca", ns="cert-manager")
    wait_ready(c, "clusterissuer", "andara-ca")
    r.say("andara-ca %s; ClusterIssuer Ready" % ("unchanged" if unchanged else "applied"))


def token_secret(token):
    ns, name, key = TOKEN_SECRET
    return {"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
            "metadata": {"name": name, "namespace": ns},
            "data": {key: base64.b64encode(token.encode()).decode()}}


def letsencrypt(r, c, token):
    """Both ACME issuers: True when applied, False when skipped for want of the token."""
    if not token:
        r.say("letsencrypt, letsencrypt-staging skipped: %s is unset (set it in .local/box.env)" % TOKEN)
        return False
    secret = token_secret(token)
    r.secrets += [token, secret["data"][TOKEN_SECRET[2]]]
    # Server-side apply, from stdin: no last-applied annotation holding the value, and nothing in argv.
    p = c.kubectl("apply", "--server-side", "--force-conflicts", "--field-manager", FIELD_MANAGER,
                  "-f", "-", check=False, stdin=json.dumps(secret))
    if p.returncode != 0:
        raise Failed(r.redact("write the Secret %s/%s: %s" % (TOKEN_SECRET[0], TOKEN_SECRET[1], p.stderr.strip())))
    r.say("secret %s/%s written from %s" % (TOKEN_SECRET[0], TOKEN_SECRET[1], TOKEN))
    unchanged = apply_file(r, c, LETSENCRYPT_FILE)
    for issuer in ISSUERS:
        wait_ready(c, "clusterissuer", issuer)
    r.say("%s %s; ClusterIssuers Ready" % (", ".join(ISSUERS), "unchanged" if unchanged else "applied"))
    return True


def shared_namespace(r, c):
    if c.kubectl("get", "namespace", SHARED, "-o", "name", check=False).returncode == 0:
        r.say("namespace %s unchanged" % SHARED)
        return
    c.kubectl("create", "namespace", SHARED)
    r.say("namespace %s created" % SHARED)


def up(r, profile="box", env=None, restore=cluster_secrets.restore):
    if profile not in PROFILES:
        raise Usage("PROFILE=%s: must be one of %s" % (profile, ", ".join(PROFILES)))
    env = dict(os.environ if env is None else env)
    try:
        env = tf.load_box_env(env)
    except tf.Exit as e:
        raise Failed(e.msg)
    token = env.get(TOKEN, "")
    if profile == "box" and env.get("REQUIRE_LETSENCRYPT") == "1" and not token:
        raise Missing("%s is unset and REQUIRE_LETSENCRYPT=1; set it in .local/box.env" % TOKEN)
    c = Cluster(kube.context(env), env)
    require_context(c)
    doc = load_versions()
    errors = validate(doc)
    if errors:
        raise Failed("%s: %s" % (os.path.relpath(VERSIONS, REPO), "; ".join(errors)))
    r.say("profile=%s context=%s" % (profile, c.context))
    ingress_node(c)
    cidr = edge_cidr(c)

    reconcile(r, c, "certManager", desired_values("certManager"), doc)
    if profile == "box":
        restore_ca(r, c, restore)
    private_ca(r, c)
    skipped = []
    if profile == "box" and not letsencrypt(r, c, token):
        skipped.append("letsencrypt, letsencrypt-staging (%s unset)" % TOKEN)
    r.say("edge trusts PROXY from %s" % cidr)
    reconcile(r, c, "traefik", desired_values("traefik", cidr), doc)
    if profile == "box":
        shared_namespace(r, c)
        reconcile(r, c, "strimzi", desired_values("strimzi"), doc)
        reconcile(r, c, "argocd", desired_values("argocd"), doc)
    else:
        skipped.append("CA root restore, letsencrypt issuers, %s, strimzi, argocd (PROFILE=ci)" % SHARED)
    r.say("done profile=%s context=%s skipped: %s" % (profile, c.context, "; ".join(skipped) or "none"))


def main(argv):
    cmd, args = (argv[0], argv[1:]) if argv else ("", [])
    if cmd == "check" and not args:
        return check()
    r = Run()
    try:
        if cmd == "up" and len(args) <= 1:
            up(r, args[0] if args and args[0] else "box")
        else:
            raise Usage("usage: scripts/platform_up.py up [box|ci] | check")
    except (Failed, Usage, Missing) as e:
        print("platform: %s run=%s" % (r.redact(str(e)), r.id), file=sys.stderr)
        return e.code
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
