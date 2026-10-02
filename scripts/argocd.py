#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""dev follows main on the box: Argo CD and Image Updater (AW-INF-019).

  argocd.py install              make argocd-install: Argo CD and Image Updater into `argocd`,
                                 at the pinned chart versions; a no-op when they're installed
  argocd.py install dev          make argocd-install ENV=dev: the above, then dev's Secrets (if
                                 absent), the image seed, and the andara-dev Application
  argocd.py status [dev]         make argocd-status: sync, health, the synced main revision,
                                 and the image the StatefulSet runs with the commit it was built from
  argocd.py ui                   make argocd-ui: port-forward the UI to localhost:ARGOCD_UI_PORT
  argocd.py recover dev          make argocd-recover ENV=dev: Kubernetes' forced-rollback step
  argocd.py uninstall dev        make argocd-uninstall ENV=dev: remove the Application without
                                 cascading, and hand every resource back to Helm

Only dev: prod stays on `make deploy` (AW-INF-007) and local on a kind-loaded image.

Progress lines are `argocd: <step>`. Exit: 0 ok · 1 a precondition missing, a refusal, or not
Synced/Healthy · 2 bad usage · 3 a tool is missing.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import time

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ARGO_REPO = "https://argoproj.github.io/argo-helm"
ARGOCD_CHART_VERSION = "10.9.2"          # Argo CD v3.5.3
IMAGE_UPDATER_CHART_VERSION = "1.3.1"    # Argo CD Image Updater v1.3.0
NS = "argocd"
APP_FILE = os.path.join(REPO, "deploy", "argocd", "andara-dev.yaml")
UPDATER_FILE = os.path.join(REPO, "deploy", "argocd", "andara-dev-image-updater.yaml")
VALUES = os.path.join(REPO, "deploy", "helm", "values", "dev.yaml")
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


def run(args, check=True, input=None):
    p = subprocess.run(args, capture_output=True, text=True, input=input)
    if check and p.returncode != 0:
        raise RuntimeError("%s: %s" % (" ".join(args[:4]), (p.stderr or p.stdout).strip()))
    return p


def kubectl_json(*args):
    p = run(["kubectl", *args, "-o", "json"], check=False)
    return json.loads(p.stdout) if p.returncode == 0 and p.stdout.strip() else None


def need(cmd, *tools):
    for t in tools:
        if shutil.which(t) is None:
            die(cmd, "%s not found" % t, 3)


def env_ns(cmd, env):
    if env == "dev":
        return "andara-dev"
    if env in ("local", "prod"):
        die(cmd, "ENV=%s: only dev follows main; prod is `make deploy` (AW-INF-007), local a kind-loaded image" % env, 2)
    die(cmd, "usage: make argocd-%s ENV=dev" % cmd, 2)


# --- install ---------------------------------------------------------------------------

def helm_release_chart(release):
    p = run(["helm", "list", "-n", NS, "-o", "json"], check=False)
    for r in json.loads(p.stdout or "[]"):
        if r["name"] == release:
            return r["chart"]
    return ""


def release_values(release):
    p = run(["helm", "get", "values", release, "-n", NS, "-o", "json"], check=False)
    return (json.loads(p.stdout) if p.returncode == 0 and p.stdout.strip() else None) or {}


def install_chart(release, chart, version, values_file=None):
    want = (yaml.safe_load(open(values_file)) or {}) if values_file else {}
    if helm_release_chart(release) == "%s-%s" % (chart, version) and release_values(release) == want:
        say("%s %s already installed in %s" % (chart, version, NS))
        return
    extra = ["--values", values_file] if values_file else ["--reset-values"]
    run(["helm", "upgrade", "--install", release, chart, "--repo", ARGO_REPO, "--version", version,
         "--namespace", NS, "--create-namespace", *extra, "--wait", "--timeout", "10m"])
    say("%s %s in %s" % (chart, version, NS))


def install_argocd():
    need("install", "helm", "kubectl")
    install_chart("argocd", "argo-cd", ARGOCD_CHART_VERSION, ARGOCD_VALUES_FILE)
    install_chart("argocd-image-updater", "argocd-image-updater", IMAGE_UPDATER_CHART_VERSION)
    run(["kubectl", "-n", NS, "rollout", "status", "deploy/argocd-server", "--timeout=5m"])
    run(["kubectl", "-n", NS, "rollout", "status", "deploy/argocd-image-updater-controller", "--timeout=5m"], check=False)
    say("Argo CD Ready in %s" % NS)


def preconditions(ns):
    # The same checks, and messages, helm_install.sh makes.
    if run(["kubectl", "get", "ingressclass", "traefik"], check=False).returncode:
        die("install", "no IngressClass 'traefik' — run `make kind-platform` on a fresh cluster")
    if run(["kubectl", "get", "crd", "clusterissuers.cert-manager.io"], check=False).returncode:
        die("install", "cert-manager CRDs absent — run `make kind-platform` on a fresh cluster")
    run(["kubectl", "apply", "-f", os.path.join(REPO, "deploy", "k8s", "cert-manager", "andara-ca.yaml")])
    if run(["kubectl", "wait", "--for=condition=Ready", "clusterissuer/andara-ca", "--timeout=60s"], check=False).returncode:
        die("install", "clusterissuer andara-ca is not Ready")
    k = kubectl_json("-n", ns, "get", "kafka", "andara-log") or {}
    ready = [c for c in k.get("status", {}).get("conditions", []) if c.get("type") == "Ready"]
    if not ready or ready[0].get("status") != "True":
        die("install", "no Ready kafka/andara-log in %s; run `make kafka-install ENV=dev` first" % ns)
    say("preconditions: traefik, cert-manager, andara-ca, kafka/andara-log in %s" % ns)


def secrets(ns):
    """dev's two Secrets, created only when absent; the Application never manages them."""
    if run(["kubectl", "-n", ns, "get", "secret", "andara-server-token-key"], check=False).returncode:
        run([os.path.join(REPO, "scripts", "auth_keys.sh")])
        auth = os.environ.get("ANDARA_AUTH_DIR", os.path.join(REPO, ".local", "auth"))
        run(["kubectl", "-n", ns, "create", "secret", "generic", "andara-server-token-key",
             "--from-file=token.keys=" + os.path.join(auth, "token-keys")])
        say("secret andara-server-token-key created from %s" % auth)
    else:
        say("secret andara-server-token-key exists; left as it is")
    if run(["kubectl", "-n", ns, "get", "secret", "andara-server-bootstrap"], check=False).returncode:
        op = os.environ.get("ANDARA_BOOTSTRAP_OPERATOR", "")
        if not op:
            die("install", "ENV=dev needs ANDARA_BOOTSTRAP_OPERATOR=<user>:<password>; the local default is public")
        # From stdin, never argv: the value must not show in a process listing.
        run(["kubectl", "-n", ns, "create", "secret", "generic", "andara-server-bootstrap",
             "--from-file=bootstrap-operator=/dev/stdin"], input=op)
        say("secret andara-server-bootstrap created")
    else:
        say("secret andara-server-bootstrap exists; left as it is")


def image_tag_param(app):
    for p in (((app or {}).get("spec") or {}).get("source") or {}).get("helm", {}).get("parameters") or []:
        if p.get("name") == "image.tag":
            return p.get("value", "")
    return ""


def seed_value():
    v = (yaml.safe_load(open(VALUES)) or {}).get("image", {})
    repo, tag = v["repository"], v["tag"]
    p = run([os.path.join(REPO, "scripts", "image_digest.sh"), repo, tag], check=False)
    if p.returncode:
        die("install", "cannot resolve %s:%s to a digest; is it published? (make image-check ENV=dev)" % (repo, tag))
    return "%s@%s" % (tag, p.stdout.strip())


def application(ns):
    """Create or update the Application, with image.tag seeded before automated sync is on."""
    app_doc = yaml.safe_load(open(APP_FILE))
    name = app_doc["metadata"]["name"]
    live = kubectl_json("-n", NS, "get", "application", name)
    if not image_tag_param(live):
        seed = seed_value()
        if live is None:
            # Created with the seed and without syncPolicy, so nothing renders `:dev` unpinned.
            first = json.loads(json.dumps(app_doc))
            first["spec"].pop("syncPolicy", None)
            first["spec"]["source"]["helm"]["parameters"] = [{"name": "image.tag", "value": seed}]
            run(["kubectl", "create", "-f", "-"], input=json.dumps(first))
            say("application %s created, image.tag seeded %s, sync off" % (name, seed))
        else:
            # Found without the parameter: turn sync off, seed, then the file turns it on.
            run(["kubectl", "-n", NS, "patch", "application", name, "--type", "json", "-p",
                 json.dumps([{"op": "remove", "path": "/spec/syncPolicy"}])], check=False)
            params = [{"name": "image.tag", "value": seed}]
            run(["kubectl", "-n", NS, "patch", "application", name, "--type", "merge", "-p",
                 json.dumps({"spec": {"source": {"helm": {"parameters": params}}}})])
            say("application %s had no image.tag; seeded %s" % (name, seed))
    else:
        say("application %s keeps image.tag %s (Image Updater's)" % (name, image_tag_param(live)))
    run(["kubectl", "apply", "--server-side", "--force-conflicts", "--field-manager", FIELD_MANAGER, "-f", APP_FILE])
    run(["kubectl", "apply", "--server-side", "--force-conflicts", "--field-manager", FIELD_MANAGER, "-f", UPDATER_FILE])
    say("applied deploy/argocd/: application %s (automated sync, prune, selfHeal) and its image updater" % name)
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
    p = run(["kubectl", "-n", ns, "delete", "secret", "-l", "owner=helm,name=andara"], check=False)
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
    pf = subprocess.Popen(["kubectl", "-n", ns, "port-forward", "pod/andara-0", "%d:8080" % port],
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
    print("application  %s" % name)
    print("sync         %s   main@%s" % (sync, rev[:12] or "?"))
    print("health       %s" % health)
    print("image        %s" % image)
    print("built from   %s" % built)
    packs = active_packs(ns)
    if isinstance(packs, str):
        print("content      %s" % packs)
    else:
        for pack, version in packs:
            print("content      %s@%d" % (pack, version))
    stuck = stalled(ns)
    if stuck:
        print("stalled      %s — once a good build has synced, `make argocd-recover ENV=dev` replaces the pod" % stuck)
    if waiting_ok and sync == "Synced":
        print("waiting      andara-0 is up and waiting for content — next: make content-seed ENV=%s" % (env or "dev"))
        return
    if sync != "Synced" or health != "Healthy":
        sys.exit(1)


# --- ui ----------------------------------------------------------------------------------

def ui():
    port = os.environ.get("ARGOCD_UI_PORT", "8090")
    say("UI on http://localhost:%s (user admin)" % port)
    say("password: kubectl -n %s get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d" % NS)
    os.execvp("kubectl", ["kubectl", "-n", NS, "port-forward", "svc/argocd-server", "%s:443" % port])


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
    _, _, _, app = app_state(ns)
    tag = image_tag_param(app)
    if not tag or not want.endswith(":" + tag):
        die("recover", "updateRevision %s runs %s, not the Application's image.tag %s; refusing" % (update, want or "?", tag or "?"))
    run(["kubectl", "-n", ns, "delete", "pod", "andara-0", "--wait=false"])
    say("deleted andara-0 (revision %s); the StatefulSet recreates it at %s (%s)" % (on, update, want))


# --- uninstall ---------------------------------------------------------------------------

def uninstall(env):
    ns = env_ns("uninstall", env)
    name = ns
    if kubectl_json("-n", NS, "get", "application", name) is None:
        say("no application %s; nothing to remove" % name)
    else:
        # Without the resources finalizer, deleting the Application cascades to nothing.
        run(["kubectl", "-n", NS, "patch", "application", name, "--type", "json", "-p",
             json.dumps([{"op": "remove", "path": "/spec/syncPolicy"}])], check=False)
        run(["kubectl", "-n", NS, "patch", "application", name, "--type", "merge", "-p",
             json.dumps({"metadata": {"finalizers": None}})])
        run(["kubectl", "-n", NS, "delete", "application", name, "--wait=true"])
        say("application %s removed without cascading" % name)
    run(["kubectl", "-n", NS, "delete", "imageupdater", name, "--ignore-not-found"], check=False)
    # Hand everything the Application tracked to Helm, so `helm upgrade --install` adopts it,
    # including anything the chart gained after the move that Helm never created (AC-10).
    kinds = run(["kubectl", "api-resources", "--namespaced", "--verbs=list,patch", "-o", "name"]).stdout.split()
    handed = 0
    for kind in kinds:
        items = (kubectl_json("-n", ns, "get", kind) or {}).get("items", [])
        for it in items:
            if TRACKING not in (it["metadata"].get("annotations") or {}):
                continue
            ref = "%s/%s" % (kind, it["metadata"]["name"])
            run(["kubectl", "-n", ns, "label", ref, "app.kubernetes.io/managed-by=Helm", "--overwrite"])
            run(["kubectl", "-n", ns, "annotate", ref, "meta.helm.sh/release-name=andara",
                 "meta.helm.sh/release-namespace=" + ns, "--overwrite"])
            handed += 1
    say("%d resource(s) in %s given Helm's ownership metadata; `make helm-install ENV=%s` takes them back" % (handed, ns, env))


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
