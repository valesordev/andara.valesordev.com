#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make cluster-up | cluster-down | cluster-rebuild — the kind cluster `andara`, from the repo (AW-INF-052).

ADR-0013 decision 1: the cluster is this repo's alone, defined by deploy/kind/cluster.yaml, with the
node image pinned in deploy/platform/versions.yaml. Context kind-andara. These targets call bin/kind
and bin/kubectl, the versions `make bootstrap` pins, never whatever is on PATH, and they never read,
export from or delete any other cluster.

  up       create `andara` if absent; a rerun says it exists and changes nothing, except to add the
           context kind-andara if a failed first run left it out or incomplete. Every run, first or not, ends
           only once every node is Ready, and fails if one isn't. Ports 8080 and 8443 must be free
           first: the compose stack publishes them as ANDARA_HTTP_PORT and ANDARA_GRPC_PORT until
           AW-INF-060 and AW-INF-064 retire it. kind writes its context to a kubeconfig of its own,
           copied into the user's with `kubectl config set`, so the current-context never moves and
           nothing that targets the old cluster today is retargeted.
  down     CONFIRM=andara, then delete `andara`. No such cluster exits 0.
  rebuild  CONFIRM=andara, checked first; export the two certificate Secrets (scripts/cluster_secrets.py),
           delete, create, then `make platform-up` (scripts/platform_up.py), which restores the CA root.
           It deletes only once the export accounted for every Secret, each one written or absent. The
           steps later stories own are named at the end.
  kind-cluster [NAME]
           print the kind cluster kind-load uses: NAME when given, else the only
           cluster there is. Two or more and no NAME exits 2 naming them; guessing the first line of
           `kind get clusters` would silently become `andara` once it exists beside solo7.

Every line is `cluster: <step> run=<id>` (a rebuild's platform-up lines are `platform: …` with the
same run id); the last names the context.

Exit: 0 ok · 1 a runtime failure (a port in use, kind failed, the export failed, platform-up failed, no
kind cluster for kind-cluster) · 2 usage (a missing or wrong CONFIRM, an ambiguous KIND_CLUSTER) · 3 a
prerequisite missing (Docker, bin/kind, bin/kubectl, the context kind-andara when rebuild exports, or
a credential platform-up requires).
"""
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid

import yaml

import cluster_secrets
import platform_up

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NAME = "andara"
CONTEXT = "kind-" + NAME
CONFIG = os.path.join(REPO, "deploy", "kind", "cluster.yaml")
VERSIONS = os.path.join(REPO, "deploy", "platform", "versions.yaml")
BIN = os.path.join(REPO, "bin")
# The host ports cluster.yaml publishes, and the variable that holds each one in the compose stack.
PORTS = [("ANDARA_HTTP_PORT", 8080), ("ANDARA_GRPC_PORT", 8443)]
WAIT = "5m"
# The steps a rebuild doesn't do yet. Each story that lands one replaces its line with the call.
NOT_YET = [
    "shared-up (Kafka, object store in andara-shared): AW-INF-055, AW-INF-058",
    "the dev edge certificate restore (andara-dev/andara-edge-tls, once andara-dev exists; "
    "platform-up restores the CA root only): AW-INF-066",
    "the schema registry: AW-INF-057",
    "the collectors: AW-INF-065",
]


class Failed(Exception):
    code = 1


class Usage(Exception):
    code = 2


class Missing(Exception):
    code = 3


class Run:
    def __init__(self):
        self.id = time.strftime("%Y%m%d-%H%M%S") + "-" + uuid.uuid4().hex[:4]

    def say(self, msg):
        print("cluster: %s run=%s" % (msg, self.id), flush=True)


def tool(name):
    path = os.path.join(BIN, name)
    if not os.access(path, os.X_OK):
        raise Missing("%s not found; run `make bootstrap`" % os.path.relpath(path, REPO))
    return path


def prerequisites():
    """bin/kind, bin/kubectl and Docker, in that order of what to fix first."""
    kind, kubectl = tool("kind"), tool("kubectl")
    if shutil.which("docker") is None:
        raise Missing("Docker not found; kind runs its nodes as Docker containers, so install Docker")
    return kind, kubectl


def run(*cmd, check=True):
    try:
        p = subprocess.run(list(cmd), capture_output=True, text=True)
    except FileNotFoundError:
        raise Missing("%s not found; run `make bootstrap`" % cmd[0])
    if check and p.returncode != 0:
        raise Failed("%s %s failed: %s" % (os.path.basename(cmd[0]), " ".join(cmd[1:3]), (p.stderr or p.stdout).strip()))
    return p


def clusters(kind):
    out = run(kind, "get", "clusters").stdout
    return [line.strip() for line in out.splitlines() if line.strip() and not line.startswith("No kind clusters")]


def check_confirm(confirm):
    if not confirm:
        raise Usage("CONFIRM is missing; it must name the cluster: CONFIRM=%s" % NAME)
    if confirm != NAME:
        raise Usage("CONFIRM=%s is not the cluster %s" % (confirm, NAME))


def port_in_use(port):
    """True if something holds 127.0.0.1:port: a bind there fails, or a connect succeeds.
    SO_REUSEADDR, as docker-proxy binds, so a connection in TIME_WAIT (nginx's keep-alives to the
    cluster a rebuild just deleted) doesn't count; a listener still does."""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        s.bind(("127.0.0.1", port))
    except OSError:
        return True
    finally:
        s.close()
    c = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    c.settimeout(0.5)
    try:
        return c.connect_ex(("127.0.0.1", port)) == 0
    finally:
        c.close()


def check_ports(ports=None):
    for var, port in ports or PORTS:
        if port_in_use(port):
            raise Failed("port %d is in use; the compose stack publishes it as %s (default %d). Stop it "
                         "(make down) or move it (%s=<port> make up) before creating the cluster"
                         % (port, var, port, var))


def node_image():
    with open(VERSIONS) as f:
        image = ((yaml.safe_load(f) or {}).get("kind") or {}).get("nodeImage", "")
    if "@sha256:" not in image:
        raise Failed("%s: kind.nodeImage must be pinned by tag and digest, got %r"
                     % (os.path.relpath(VERSIONS, REPO), image))
    return image


# What a usable kind-andara needs in a kubeconfig, per section.
FIELDS = (("clusters", ("server", "certificate-authority-data")),
          ("users", ("client-certificate-data", "client-key-data")),
          ("contexts", ("cluster", "user")))


def entries(cfg):
    """kind-andara's cluster, user and context from a parsed kubeconfig, each {} when absent."""
    return {kind: next((e.get(kind[:-1]) or {} for e in cfg.get(kind) or [] if e.get("name") == CONTEXT), {})
            for kind, _ in FIELDS}


def complete(found):
    return all(found[kind].get(field) for kind, fields in FIELDS for field in fields)


def merge_kubeconfig(kubectl, path):
    """Copy kind-andara's cluster, user and context from kind's kubeconfig at path into the user's,
    with `kubectl config set`, which never changes current-context."""
    with open(path) as f:
        found = entries(yaml.safe_load(f) or {})
    if not complete(found):
        raise Failed("kind's kubeconfig has no complete %s entry: %s" % (CONTEXT, sorted(k for k, v in found.items() if not v)))
    for kind, fields in FIELDS:
        for field in fields:
            run(kubectl, "config", "set", "%s.%s.%s" % (kind, CONTEXT, field), str(found[kind][field]))


def context_complete(kubectl):
    """Whether the user's kubeconfig holds every field kind-andara needs. A first run that died part
    way through merge_kubeconfig can leave a context that exists but has no user."""
    p = run(kubectl, "config", "view", "--raw", "-o", "json", check=False)
    if p.returncode != 0:
        return False
    try:
        return complete(entries(json.loads(p.stdout or "{}")))
    except ValueError:
        return False


def up(r, ports=None):
    kind, kubectl = prerequisites()
    if NAME in clusters(kind):
        if context_complete(kubectl):
            r.say("%s exists; nothing to do" % NAME)
        else:
            # A first run that created the cluster and failed before or during the merge. `kind export
            # kubeconfig` would make the context current; `kind get kubeconfig` only prints it.
            with tempfile.TemporaryDirectory() as tmp:
                own = os.path.join(tmp, "kubeconfig")
                with open(own, "w") as f:
                    f.write(run(kind, "get", "kubeconfig", "--name", NAME).stdout)
                merge_kubeconfig(kubectl, own)
            r.say("%s exists; context %s was missing or incomplete, merged; current-context unchanged" % (NAME, CONTEXT))
        # An existing cluster is ready only if its nodes are: a first run may have failed in the
        # wait, or a node may have gone NotReady since.
        wait_ready(r, kubectl)
        return
    check_ports(ports)
    image = node_image()
    r.say("create %s image=%s config=%s" % (NAME, image, os.path.relpath(CONFIG, REPO)))
    # kind writes its context to a kubeconfig of its own, not the user's: kind would make it current
    # there for the whole create, and leave current-context empty if the create failed. The entries
    # are copied over once the cluster exists, and the user's current-context never moves.
    with tempfile.TemporaryDirectory() as tmp:
        own = os.path.join(tmp, "kubeconfig")
        run(kind, "create", "cluster", "--name", NAME, "--config", CONFIG, "--image", image,
            "--wait", WAIT, "--kubeconfig", own)
        merge_kubeconfig(kubectl, own)
    r.say("context %s added; current-context unchanged" % CONTEXT)
    wait_ready(r, kubectl)


def wait_ready(r, kubectl):
    """Wait for every node to be Ready, then report them; a node that isn't is a failure."""
    run(kubectl, "--context", CONTEXT, "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=" + WAIT)
    nodes = run(kubectl, "--context", CONTEXT, "get", "nodes", "--no-headers").stdout
    rows = [line.split() for line in nodes.splitlines() if line.strip()]
    # STATUS is the Ready condition plus flags: a cordoned node is `Ready,SchedulingDisabled`.
    ready = sum(1 for row in rows if len(row) > 1 and row[1].split(",")[0] == "Ready")
    if not rows or ready < len(rows):
        raise Failed("nodes %d/%d Ready after the wait: %s" % (ready, len(rows), nodes.strip() or "no nodes"))
    r.say("nodes %d/%d Ready" % (ready, len(rows)))
    r.say("ready context=%s" % CONTEXT)


def down(r, confirm):
    check_confirm(confirm)
    kind, _ = prerequisites()
    if NAME not in clusters(kind):
        r.say("no cluster %s; nothing to delete context=%s" % (NAME, CONTEXT))
        return
    r.say("delete %s" % NAME)
    run(kind, "delete", "cluster", "--name", NAME)
    r.say("deleted context=%s" % CONTEXT)


def run_platform(r):
    """`make platform-up` on the new cluster, its exit status carried over."""
    p = platform_up.Run()
    p.id = r.id
    try:
        platform_up.up(p, "box", env=dict(os.environ, ANDARA_KUBE_CONTEXT=CONTEXT))
    except platform_up.Missing as e:
        raise Missing("platform-up: %s" % p.redact(str(e)))
    except (platform_up.Failed, platform_up.Usage) as e:
        raise Failed("platform-up: %s" % p.redact(str(e)))


def rebuild(r, confirm, ports=None, export=cluster_secrets.export):
    check_confirm(confirm)
    kind, _ = prerequisites()
    if NAME in clusters(kind):
        r.say("export certificate Secrets to %s" % os.path.relpath(cluster_secrets.TLS_DIR, REPO))
        try:
            result = export(context=CONTEXT, say=lambda m: r.say("export " + m))
        except cluster_secrets.Missing as e:
            raise Missing("%s; %s was not deleted" % (e, NAME))
        except (cluster_secrets.Failed, OSError) as e:
            raise Failed("export failed, so %s was not deleted: %s" % (NAME, e))
        want = {"%s/%s" % s for s in cluster_secrets.SECRETS}
        if set(result) != want or not all(v in ("written", "absent") for v in result.values()):
            raise Failed("export accounted for %s of %s, so %s was not deleted"
                         % (sorted(result), sorted(want), NAME))
        down(r, confirm)
    else:
        r.say("no cluster %s; skipping the export and the delete" % NAME)
    up(r, ports)
    run_platform(r)
    r.say("not yet part of the rebuild, run by hand or by its story once it lands:")
    for step in NOT_YET:
        r.say("  pending " + step)
    r.say("rebuilt context=%s" % CONTEXT)


def kind_cluster(name, kind="kind"):
    """The cluster kind-load targets (printed on stdout)."""
    if name:
        return name
    found = clusters(kind)
    if not found:
        raise Failed("no kind cluster found (KIND_CLUSTER=<name>)")
    if len(found) > 1:
        raise Usage("KIND_CLUSTER is unset and there are %d kind clusters (%s); name one: KIND_CLUSTER=<name>"
                    % (len(found), ", ".join(found)))
    return found[0]


def main(argv):
    cmd, args = (argv[0], argv[1:]) if argv else ("", [])
    r = Run()
    try:
        if cmd == "kind-cluster" and len(args) <= 2:
            print(kind_cluster(args[0] if args else "", *(args[1:] or ["kind"])))
            return 0
        if cmd == "up" and not args:
            up(r)
        elif cmd == "down" and len(args) <= 1:
            down(r, args[0] if args else "")
        elif cmd == "rebuild" and len(args) <= 1:
            rebuild(r, args[0] if args else "")
        else:
            raise Usage("usage: scripts/cluster.py up | down CONFIRM | rebuild CONFIRM | kind-cluster [NAME [KIND]]")
    except (Failed, Usage, Missing) as e:
        print("cluster: %s run=%s" % (e, r.id), file=sys.stderr)
        return e.code
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
