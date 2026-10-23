#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""dev follows the Git tag `dev` on the box: Argo CD (AW-INF-019, AW-INF-059).

  argocd.py install              make argocd-install: Argo CD into `argocd`, at the pinned chart
                                 version; a no-op when it's installed
  argocd.py install dev          make argocd-install ENV=dev: the above, then dev's Secrets (if
                                 absent) and the andara-dev Application, once `make promote` has
                                 pushed the tag `dev` it follows
  argocd.py status [dev]         make argocd-status: sync, health, and the three answers to "which
                                 image is dev" (the commit `dev` points at, Argo's synced revision,
                                 the digest andara-0 runs), with the commit the image was built from
  argocd.py ui                   make argocd-ui: port-forward the UI to localhost:ARGOCD_UI_PORT
  argocd.py recover dev          make argocd-recover ENV=dev: Kubernetes' forced-rollback step
  argocd.py uninstall dev        make argocd-uninstall ENV=dev: remove the Application without
                                 cascading, and hand every resource back to Helm

Only dev: prod stays on `make deploy` (AW-INF-007) and local on a kind-loaded image. What dev runs
changes only through `make promote` (scripts/promote.py, ADR-0013 decision 7).

Progress lines are `argocd: <step>`. Exit: 0 ok · 1 a precondition missing, a refusal, not
Synced/Healthy, or the three answers disagree · 2 bad usage · 3 a tool is missing.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import time

import yaml

import kube

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ARGO_REPO = "https://argoproj.github.io/argo-helm"
# Argo CD's chart and version are deploy/platform/versions.yaml's (AW-INF-054).
VERSIONS = os.path.join(REPO, "deploy", "platform", "versions.yaml")
NS = "argocd"
APP_FILE = os.path.join(REPO, "deploy", "argocd", "andara-dev.yaml")
VALUES = os.path.join(REPO, "deploy", "helm", "values", "dev.yaml")
VALUES_PATH = "deploy/helm/values/dev.yaml"
# The Git tag the Application follows, which `make promote` moves (AW-INF-059).
DEV_TAG = "dev"
FIELD_MANAGER = "andara-argocd-install"
TRACKING = "argocd.argoproj.io/tracking-id"
SYNC_DEADLINE = 600
# Argo CD's settings (annotation tracking, the Ingress health check, no Dex or
# notifications), reapplied whenever the release's values differ from the file.
ARGOCD_VALUES_FILE = os.path.join(REPO, "deploy", "argocd", "argocd-values.yaml")


def say(msg):
    print("argocd: " + msg, flush=True)


def die(cmd, msg, code=1):
    print("make: argocd-%s: %s" % (cmd, msg), file=sys.stderr)
    sys.exit(code)


def run(args, check=True, input=None, timeout=None):
    try:
        p = subprocess.run(args, capture_output=True, text=True, input=input, timeout=timeout)
    except subprocess.TimeoutExpired:
        raise RuntimeError("%s: no answer in %ds" % (" ".join(args[:4]), timeout))
    if check and p.returncode != 0:
        raise RuntimeError("%s: %s" % (" ".join(args[:4]), (p.stderr or p.stdout).strip()))
    return p


def kubectl_json(*args):
    p = run(kube.kubectl(*args, "-o", "json"), check=False)
    return json.loads(p.stdout) if p.returncode == 0 and p.stdout.strip() else None


def need(cmd, *tools):
    for t in tools:
        if shutil.which(t) is None:
            die(cmd, "%s not found" % t, 3)


def env_ns(cmd, env):
    if env == "dev":
        return "andara-dev"
    if env in ("local", "prod"):
        die(cmd, "ENV=%s: only dev is on Argo CD; prod is `make deploy` (AW-INF-007), local a kind-loaded image" % env, 2)
    die(cmd, "usage: make argocd-%s ENV=dev" % cmd, 2)


# --- install ---------------------------------------------------------------------------

def helm_release_chart(release):
    p = run(kube.helm("list", "-n", NS, "-o", "json"), check=False)
    for r in json.loads(p.stdout or "[]"):
        if r["name"] == release:
            return r["chart"]
    return ""


def release_values(release):
    p = run(kube.helm("get", "values", release, "-n", NS, "-o", "json"), check=False)
    return (json.loads(p.stdout) if p.returncode == 0 and p.stdout.strip() else None) or {}


def install_chart(release, chart, version, values_file=None):
    want = (yaml.safe_load(open(values_file)) or {}) if values_file else {}
    if helm_release_chart(release) == "%s-%s" % (chart, version) and release_values(release) == want:
        say("%s %s already installed in %s" % (chart, version, NS))
        return
    extra = ["--values", values_file] if values_file else ["--reset-values"]
    run(kube.helm("upgrade", "--install", release, chart, "--repo", ARGO_REPO, "--version", version,
                  "--namespace", NS, "--create-namespace", *extra, "--wait", "--timeout", "10m"))
    say("%s %s in %s" % (chart, version, NS))


def argocd_chart():
    """(chart, version) of Argo CD from versions.yaml."""
    with open(VERSIONS) as f:
        entry = (yaml.safe_load(f) or {}).get("argocd") or {}
    if not entry.get("chart") or not entry.get("version"):
        raise RuntimeError("deploy/platform/versions.yaml has no argocd chart and version")
    return entry["chart"], entry["version"]


def install_argocd():
    need("install", "helm", "kubectl")
    install_chart("argocd", *argocd_chart(), ARGOCD_VALUES_FILE)
    run(kube.kubectl("-n", NS, "rollout", "status", "deploy/argocd-server", "--timeout=5m"))
    say("Argo CD Ready in %s" % NS)


def issuer_ready(name):
    issuer = kubectl_json("get", "clusterissuer", name) or {}
    return any(c.get("type") == "Ready" and c.get("status") == "True"
               for c in (issuer.get("status") or {}).get("conditions") or [])


def preconditions(ns):
    # The same checks, and messages, helm_install.sh makes. Nothing under deploy/k8s/cert-manager/ is
    # applied here: platform-up is its one applier, after the CA root restore (AW-INF-054).
    if run(kube.kubectl("get", "ingressclass", "traefik"), check=False).returncode:
        die("install", "no IngressClass 'traefik'; run `make platform-up`")
    if run(kube.kubectl("get", "crd", "clusterissuers.cert-manager.io"), check=False).returncode:
        die("install", "cert-manager CRDs absent; run `make platform-up`")
    edge = ((yaml.safe_load(open(VALUES)) or {}).get("tls") or {}).get("issuer") or "andara-ca"
    for issuer in dict.fromkeys(("andara-ca", edge)):
        if not issuer_ready(issuer):
            die("install", "ClusterIssuer %s is not Ready; run `make platform-up`" % issuer)
    # The broker dev.yaml names: andara-log-kafka-bootstrap.<namespace>.svc, the shared one in
    # andara-shared since AW-INF-059 (ADR-0013 amendment 16), or a bare name in dev's own.
    brokers = str((((yaml.safe_load(open(VALUES)) or {}).get("server") or {}).get("kafka") or {}).get("brokers") or "")
    parts = brokers.split(":", 1)[0].split(".")
    kns = parts[1] if len(parts) > 1 else ns
    k = kubectl_json("-n", kns, "get", "kafka", "andara-log") or {}
    ready = [c for c in k.get("status", {}).get("conditions", []) if c.get("type") == "Ready"]
    if not ready or ready[0].get("status") != "True":
        die("install", "no Ready kafka/andara-log in %s; run `make shared-up` first" % kns)
    say("preconditions: traefik, cert-manager, ClusterIssuers %s, kafka/andara-log in %s"
        % (", ".join(dict.fromkeys(("andara-ca", edge))), kns))


def secrets(ns):
    """dev's two Secrets, created only when absent; the Application never manages them."""
    if run(kube.kubectl("-n", ns, "get", "secret", "andara-server-token-key"), check=False).returncode:
        run([os.path.join(REPO, "scripts", "auth_keys.sh")])
        auth = os.environ.get("ANDARA_AUTH_DIR", os.path.join(REPO, ".local", "auth"))
        run(kube.kubectl("-n", ns, "create", "secret", "generic", "andara-server-token-key",
                         "--from-file=token.keys=" + os.path.join(auth, "token-keys")))
        say("secret andara-server-token-key created from %s" % auth)
    else:
        say("secret andara-server-token-key exists; left as it is")
    if run(kube.kubectl("-n", ns, "get", "secret", "andara-server-bootstrap"), check=False).returncode:
        op = os.environ.get("ANDARA_BOOTSTRAP_OPERATOR", "")
        if not op:
            die("install", "ENV=dev needs ANDARA_BOOTSTRAP_OPERATOR=<user>:<password>; the local default is public")
        # From stdin, never argv: the value must not show in a process listing.
        run(kube.kubectl("-n", ns, "create", "secret", "generic", "andara-server-bootstrap",
                         "--from-file=bootstrap-operator=/dev/stdin"), input=op)
        say("secret andara-server-bootstrap created")
    else:
        say("secret andara-server-bootstrap exists; left as it is")


def tag_commit():
    """The commit the tag `dev` points at on origin, or "" when there's none. Fetched too, so
    values_at() can read it. Bounded, so a hung remote can't hold a caller's poll past its deadline."""
    p = run(["git", "-C", REPO, "ls-remote", "origin", "refs/tags/%s^{}" % DEV_TAG], check=False, timeout=30)
    sha = p.stdout.split("\t", 1)[0].strip() if p.returncode == 0 else ""
    if sha:
        run(["git", "-C", REPO, "fetch", "-q", "--no-tags", "origin",
             "+refs/tags/%s:refs/tags/%s" % (DEV_TAG, DEV_TAG)], check=False, timeout=60)
    return sha


def have_commit(commit):
    """Whether commit is in this clone, fetching it from origin when it isn't (a promotion made
    from another clone)."""
    if not commit:
        return False
    if run(["git", "-C", REPO, "cat-file", "-e", commit + "^{commit}"], check=False).returncode == 0:
        return True
    run(["git", "-C", REPO, "fetch", "-q", "--no-tags", "origin", commit], check=False, timeout=60)
    return run(["git", "-C", REPO, "cat-file", "-e", commit + "^{commit}"], check=False).returncode == 0


def values_at(commit):
    """dev.yaml at a commit, parsed, or {} when the commit isn't here."""
    p = run(["git", "-C", REPO, "show", "%s:%s" % (commit, VALUES_PATH)], check=False)
    return (yaml.safe_load(p.stdout) or {}) if p.returncode == 0 else {}


def image_tag_at(commit):
    return str(((values_at(commit) if commit else {}).get("image") or {}).get("tag") or "")


def application(ns):
    """Create or update the Application. It follows the tag `dev`, so the tag must exist first:
    `make promote ENV=dev SHA=<main sha>` makes it."""
    app_doc = yaml.safe_load(open(APP_FILE))
    name = app_doc["metadata"]["name"]
    if not tag_commit():
        die("install", "no tag %s on origin, and the Application follows it; run `make promote ENV=dev "
            "SHA=<main sha>` first (docs/runbooks/promote.md)" % DEV_TAG)
    live = kubectl_json("-n", NS, "get", "application", name)
    # An Application Image Updater wrote to (AW-INF-019) carries image.tag as a parameter in no
    # commit, which would override the promoted values; it goes.
    if ((((live or {}).get("spec") or {}).get("source") or {}).get("helm") or {}).get("parameters"):
        run(kube.kubectl("-n", NS, "patch", "application", name, "--type", "json", "-p",
                         json.dumps([{"op": "remove", "path": "/spec/source/helm/parameters"}])))
        say("application %s: removed helm.parameters, which no commit carries" % name)
    run(kube.kubectl("apply", "--server-side", "--force-conflicts", "--field-manager", FIELD_MANAGER, "-f", APP_FILE))
    say("applied deploy/argocd/andara-dev.yaml: application %s follows tag %s (automated sync, prune, selfHeal)"
        % (name, DEV_TAG))
    return name


def app_state(name):
    app = kubectl_json("-n", NS, "get", "application", name) or {}
    st = app.get("status", {})
    return (st.get("sync", {}).get("status", "?"), st.get("health", {}).get("status", "?"),
            st.get("sync", {}).get("revision", ""), app)


def wait_synced_healthy(name, waiting=lambda: False):
    """True once the Application is Synced and Healthy. "waiting" when it's Synced and the
    server is up but waiting for its first content (AW-SRV-042): a store with no pack with
    Zones, on a rebuild from nothing, can't be Healthy before `make content-seed`, which
    needs this install to have finished (AW-INF-021 AC-7). False at the deadline."""
    end = time.monotonic() + SYNC_DEADLINE
    last = None
    while time.monotonic() < end:
        sync, health, rev, _ = app_state(name)
        if (sync, health) != last:
            say("application %s: %s / %s" % (name, sync, health))
            last = (sync, health)
        if sync == "Synced" and health == "Healthy":
            return True
        if sync == "Synced" and waiting():
            return "waiting"
        time.sleep(5)
    return False


def install(env):
    # The environment is checked before anything is installed: a refused ENV changes nothing.
    ns = env_ns("install", env) if env else None
    install_argocd()
    if not env:
        return
    preconditions(ns)
    secrets(ns)
    name = application(ns)
    import world_reset  # noqa: E402  (scripts/, beside this file)
    up = wait_synced_healthy(name, waiting=lambda: world_reset.waiting_for_content(ns))
    if not up:
        die("install", "application %s not Synced/Healthy within %ds; see make argocd-status" % (name, SYNC_DEADLINE))
    if up == "waiting":
        say("application %s is Synced, and andara-0 is waiting for content: the store holds no pack "
            "with Zones (AW-SRV-042). Next: make content-seed ENV=%s" % (name, env))
    # The Helm release's history, now that Argo CD owns its objects. Never `helm uninstall`,
    # which would delete what the Application just adopted.
    p = run(kube.kubectl("-n", ns, "delete", "secret", "-l", "owner=helm,name=andara"), check=False)
    gone = [l for l in p.stdout.splitlines() if "deleted" in l]
    say("helm release history in %s: %s" % (ns, "%d record(s) removed" % len(gone) if gone else "none"))
    status(env, waiting_ok=(up == "waiting"))


# --- status ------------------------------------------------------------------------------

def image_revision(ref):
    """The org.opencontainers.image.revision label of ghcr.io/<name>@sha256:…, anonymously."""
    try:
        import urllib.request
        repo, digest = ref.split("@", 1)
        name = repo.split(":", 1)[0].replace("ghcr.io/", "", 1)
        tok = json.load(urllib.request.urlopen(
            "https://ghcr.io/token?scope=repository:%s:pull" % name, timeout=20))["token"]

        def get(path, accept):
            req = urllib.request.Request("https://ghcr.io/v2/%s/%s" % (name, path),
                                         headers={"Authorization": "Bearer " + tok, "Accept": accept})
            return json.load(urllib.request.urlopen(req, timeout=20))
        idx = get("manifests/" + digest, "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, "
                  "application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json")
        if "manifests" in idx:
            m = [x for x in idx["manifests"] if x.get("platform", {}).get("architecture") == "amd64"] or idx["manifests"]
            idx = get("manifests/" + m[0]["digest"], "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
        cfg = get("blobs/" + idx["config"]["digest"], "*/*")
        return (cfg.get("config", {}).get("Labels") or {}).get("org.opencontainers.image.revision", "?")
    except Exception as e:  # a status line, not a failure
        return "? (%s)" % e.__class__.__name__


def stalled(ns):
    """The rolling update stuck on a pod that never became Ready (AC-8), or None."""
    sts = kubectl_json("-n", ns, "get", "statefulset", "andara") or {}
    pod = kubectl_json("-n", ns, "get", "pod", "andara-0") or {}
    if not sts or not pod:
        return None
    ready = [c for c in pod.get("status", {}).get("conditions", []) if c.get("type") == "Ready"]
    if ready and ready[0].get("status") == "True":
        return None
    # Init containers first: one stuck pulling leaves the server waiting on PodInitializing,
    # which names the symptom, not the cause (AW-INF-019 §8, 2026-09-27).
    reasons = []
    st = pod.get("status", {})
    for cs in st.get("initContainerStatuses", []) + st.get("containerStatuses", []):
        s = cs.get("state", {})
        w = s.get("waiting") or s.get("terminated") or {}
        if w.get("reason") and w["reason"] != "Completed":
            reasons.append("%s: %s" % (cs["name"], w["reason"]))
    causes = [r for r in reasons if not r.endswith(": PodInitializing")]
    return (causes or reasons or ["andara-0 not Ready"])[0]


ACTIVE_VERSION = re.compile(r'^andara_content_active_version\{(?:[^}]*,)?pack="([^"]+)"[^}]*\} ([0-9.e+]+)$')


def pod_metrics(ns):
    """andara-0's /metrics, through a short-lived `kubectl port-forward`. The pod proxy can't be
    used: the namespace's NetworkPolicy admits no traffic from the API server. Returns the text,
    or raises RuntimeError naming why."""
    import socket
    import urllib.request
    with socket.socket() as sk:
        sk.bind(("127.0.0.1", 0))
        port = sk.getsockname()[1]
    pf = subprocess.Popen(kube.kubectl("-n", ns, "port-forward", "pod/andara-0", "%d:8080" % port),
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        end = time.monotonic() + 15
        while True:
            try:
                return urllib.request.urlopen("http://127.0.0.1:%d/metrics" % port, timeout=5).read().decode()
            except OSError as e:
                if pf.poll() is not None:
                    raise RuntimeError((pf.stderr.read() or "port-forward exited").strip().splitlines()[-1])
                if time.monotonic() > end:
                    raise RuntimeError("no /metrics within 15s: %s" % e)
                time.sleep(0.5)
    finally:
        pf.terminate()
        try:
            pf.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pf.kill()


def active_packs(ns, metrics=None):
    """The packs in effect on andara-0, from andara_content_active_version{pack} (AW-INF-021):
    [(pack, version)] sorted, or a reason string when the pod can't be read."""
    if metrics is None:
        try:
            metrics = pod_metrics(ns)
        except RuntimeError as e:
            return "? (%s)" % str(e)[:80]
    packs = []
    for line in metrics.splitlines():
        m = ACTIVE_VERSION.match(line)
        if m:
            packs.append((m.group(1), int(float(m.group(2)))))
    return sorted(packs) or "? (no andara_content_active_version series)"


def content_waiting(ns):
    """andara-0 is started and waiting for its first content (world_reset's check)."""
    import world_reset  # noqa: E402  (scripts/, beside this file)
    return world_reset.waiting_for_content(ns)


def running_digest(ns):
    """The digest andara-0's server container runs (its imageID), or ""."""
    pod = kubectl_json("-n", ns, "get", "pod", "andara-0") or {}
    for cs in (pod.get("status") or {}).get("containerStatuses") or []:
        if cs.get("name") == "server":
            m = re.search(r"sha256:[0-9a-f]{64}", cs.get("imageID") or "")
            return m.group(0) if m else ""
    return ""


def agreement(ns):
    """The three answers to "which image is dev" (AW-INF-059): the commit the tag `dev` points at,
    Argo CD's synced revision, and the digest andara-0 runs, held against the digest that commit's
    dev.yaml pins. "mismatch" lists every disagreement; empty is one answer."""
    tag = tag_commit()
    _, _, argo, _ = app_state(ns)
    pinned = image_tag_at(tag)
    want = pinned.split("@", 1)[1] if "@" in pinned else ""
    running = running_digest(ns)
    mismatch = []
    if not tag:
        mismatch.append("no tag %s on origin" % DEV_TAG)
    elif argo != tag:
        mismatch.append("argo synced %s, tag %s is %s" % (argo[:12] or "?", DEV_TAG, tag[:12]))
    if tag and not want:
        mismatch.append("%s at %s pins no digest (image.tag %r)" % (VALUES_PATH, tag[:12], pinned))
    elif tag and running != want:
        mismatch.append("andara-0 runs %s, the tag pins %s" % (running[:19] or "?", want[:19]))
    return {"tag": tag, "argo": argo, "pinned": want, "running": running, "mismatch": mismatch}


def status(env, waiting_ok=False):
    """Print the Application's state; exit 1 unless Synced and Healthy. With waiting_ok (an
    install whose server is waiting for its first content, AW-SRV-042), Synced is enough: the
    seed that makes it Healthy can only run after the install returns (Codex on #327)."""
    ns = env_ns("status", env or "dev")
    name = ns
    sync, health, rev, app = app_state(name)
    if not app:
        die("status", "no application %s in %s; run make argocd-install ENV=dev" % (name, NS))
    image = ""
    sts = kubectl_json("-n", ns, "get", "statefulset", "andara") or {}
    for c in sts.get("spec", {}).get("template", {}).get("spec", {}).get("containers", []):
        if c["name"] == "server":
            image = c["image"]
    built = image_revision(image) if "@" in image else "?"
    a = agreement(ns)
    print("application  %s" % name)
    print("sync         %s   %s@%s" % (sync, DEV_TAG, rev[:12] or "?"))
    print("health       %s" % health)
    print("tag dev      %s   pins %s" % (a["tag"][:12] or "?", a["pinned"] or "?"))
    print("running      %s" % (a["running"] or "?"))
    print("image        %s" % image)
    print("built from   %s" % built)
    for m in a["mismatch"]:
        print("disagree     %s" % m)
    packs = active_packs(ns)
    if isinstance(packs, str):
        print("content      %s" % packs)
    else:
        for pack, version in packs:
            print("content      %s@%d" % (pack, version))
    # A server waiting for its first content (AW-SRV-042) isn't Ready either, and stalled()
    # can't tell the two apart; argocd-recover would refuse it (AW-INF-021's §8 close). A pod
    # that looks stuck is asked now: install's waiting_ok was decided before it may have rolled.
    stuck = stalled(ns)
    waiting = content_waiting(ns) if stuck else waiting_ok
    if stuck and not waiting:
        print("stalled      %s — once a good build has synced, `make argocd-recover ENV=dev` replaces the pod" % stuck)
    if waiting:
        print("waiting      andara-0 is up and waiting for content — next: make content-seed ENV=%s" % (env or "dev"))
    if waiting_ok and sync == "Synced":
        return
    if sync != "Synced" or health != "Healthy" or a["mismatch"]:
        sys.exit(1)


# --- ui ----------------------------------------------------------------------------------

def ui():
    port = os.environ.get("ARGOCD_UI_PORT", "8090")
    say("UI on http://localhost:%s (user admin)" % port)
    say("password: kubectl --context %s -n %s get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' "
        "| base64 -d" % (kube.context(), NS))
    argv = kube.kubectl("-n", NS, "port-forward", "svc/argocd-server", "%s:443" % port)
    os.execvp(argv[0], argv)


# --- recover -----------------------------------------------------------------------------

def recover(env):
    """Kubernetes' forced rollback (AC-8): an OrderedReady StatefulSet whose pod never became
    Ready isn't replaced by a later good template. Delete that pod, but only once the
    StatefulSet's updateRevision is the Application's current image, not the failing one."""
    ns = env_ns("recover", env)
    sts = kubectl_json("-n", ns, "get", "statefulset", "andara")
    pod = kubectl_json("-n", ns, "get", "pod", "andara-0")
    if not sts or not pod:
        die("recover", "no statefulset/andara or pod andara-0 in %s" % ns)
    update = sts.get("status", {}).get("updateRevision", "")
    on = pod["metadata"].get("labels", {}).get("controller-revision-hash", "")
    if not stalled(ns):
        die("recover", "andara-0 is Ready; nothing to recover")
    if on == update:
        die("recover", "andara-0 is on the StatefulSet's updateRevision %s, the failing one; "
            "let a good build sync first (make argocd-status)" % update)
    rev = kubectl_json("-n", ns, "get", "controllerrevision", update) or {}
    want = ""
    for c in rev.get("data", {}).get("spec", {}).get("template", {}).get("spec", {}).get("containers", []):
        if c["name"] == "server":
            want = c["image"]
    _, _, synced, _ = app_state(ns)
    if not have_commit(synced):
        die("recover", "the synced commit %s isn't in this clone or on origin; `git fetch origin` and retry"
            % (synced[:12] or "?"))
    tag = image_tag_at(synced)
    if not tag or not want.endswith(":" + tag):
        die("recover", "updateRevision %s runs %s, not image.tag %s of the synced commit %s; refusing"
            % (update, want or "?", tag or "?", synced[:12] or "?"))
    run(kube.kubectl("-n", ns, "delete", "pod", "andara-0", "--wait=false"))
    say("deleted andara-0 (revision %s); the StatefulSet recreates it at %s (%s)" % (on, update, want))


# --- uninstall ---------------------------------------------------------------------------

def uninstall(env):
    ns = env_ns("uninstall", env)
    name = ns
    if kubectl_json("-n", NS, "get", "application", name) is None:
        say("no application %s; nothing to remove" % name)
    else:
        # Without the resources finalizer, deleting the Application cascades to nothing.
        run(kube.kubectl("-n", NS, "patch", "application", name, "--type", "json", "-p",
                         json.dumps([{"op": "remove", "path": "/spec/syncPolicy"}])), check=False)
        run(kube.kubectl("-n", NS, "patch", "application", name, "--type", "merge", "-p",
                         json.dumps({"metadata": {"finalizers": None}})))
        run(kube.kubectl("-n", NS, "delete", "application", name, "--wait=true"))
        say("application %s removed without cascading" % name)
    # Hand everything the Application tracked to Helm, so `helm upgrade --install` adopts it,
    # including anything the chart gained after the move that Helm never created (AC-10).
    kinds = run(kube.kubectl("api-resources", "--namespaced", "--verbs=list,patch", "-o", "name")).stdout.split()
    handed = 0
    for kind in kinds:
        items = (kubectl_json("-n", ns, "get", kind) or {}).get("items", [])
        for it in items:
            if TRACKING not in (it["metadata"].get("annotations") or {}):
                continue
            ref = "%s/%s" % (kind, it["metadata"]["name"])
            run(kube.kubectl("-n", ns, "label", ref, "app.kubernetes.io/managed-by=Helm", "--overwrite"))
            run(kube.kubectl("-n", ns, "annotate", ref, "meta.helm.sh/release-name=andara",
                             "meta.helm.sh/release-namespace=" + ns, "--overwrite"))
            handed += 1
    say("%d resource(s) in %s given Helm's ownership metadata; `make helm-install ENV=%s%s` takes them back" % (handed, ns, env, " TAG=<the build make argocd-status showed>" if env == "dev" else ""))


def main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    env = sys.argv[2] if len(sys.argv) > 2 else ""
    try:
        if cmd == "install":
            install(env)
        elif cmd == "status":
            status(env)
        elif cmd == "ui":
            ui()
        elif cmd == "recover":
            recover(env)
        elif cmd == "uninstall":
            uninstall(env)
        else:
            die(cmd or "?", "usage: argocd.py install|status|ui|recover|uninstall [dev]", 2)
    except RuntimeError as e:
        die(cmd, str(e))


if __name__ == "__main__":
    main()
