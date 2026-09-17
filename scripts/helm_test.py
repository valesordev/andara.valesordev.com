#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Render-level tests for deploy/helm/andara (AW-INF-003 and AW-INF-006 test plans, unit half).

`helm unittest` is a plugin that cannot be pinned the way ./bin tools are, so these are the
same assertions over `helm template` output, in the language the rest of the repo's checks
are written in. Each test names the acceptance criterion it holds.
"""
import os
import subprocess
import sys
import tempfile

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CHART = os.path.join(REPO, "deploy", "helm", "andara")
VALUES = os.path.join(REPO, "deploy", "helm", "values")
KUBE_VERSION = "1.36.1"
ENVS = ("local", "dev", "prod")

failures = []


def fail(msg):
    failures.append(msg)
    print("helm-test: FAIL: " + msg, file=sys.stderr)


def render(env, *extra):
    cmd = ["helm", "template", "andara", CHART, "--kube-version", KUBE_VERSION,
           "--values", os.path.join(VALUES, env + ".yaml"), *extra]
    p = subprocess.run(cmd, capture_output=True, text=True)
    return p.returncode, p.stdout, p.stderr


def docs(out):
    return [d for d in yaml.safe_load_all(out) if d]


def find(ds, kind, name=None):
    for d in ds:
        if d.get("kind") == kind and (name is None or d["metadata"]["name"] == name):
            return d
    return None


def test_pvc_retained(env):
    """AC-3: the snapshot claim is kept on uninstall and on scale-down."""
    code, out, err = render(env)
    if code:
        return fail("%s: render failed: %s" % (env, err.strip()))
    sts = find(docs(out), "StatefulSet", "andara")
    pol = sts["spec"].get("persistentVolumeClaimRetentionPolicy", {})
    if pol.get("whenDeleted") != "Retain" or pol.get("whenScaled") != "Retain":
        fail("%s: StatefulSet retention policy is %r, want Retain/Retain" % (env, pol))
    claims = sts["spec"].get("volumeClaimTemplates", [])
    snap = next((c for c in claims if c["metadata"]["name"] == "snapshots"), None)
    if snap is None:
        return fail("%s: no `snapshots` volumeClaimTemplate" % env)
    if snap["metadata"].get("annotations", {}).get("helm.sh/resource-policy") != "keep":
        fail("%s: snapshots claim lacks helm.sh/resource-policy: keep" % env)


def test_no_secret_material(env):
    """AC-4: secrets are references only; nothing secret-shaped is rendered anywhere."""
    planted = "planted-secret-name-7f3a"
    code, out, err = render(env, "--set", "secrets.tokenKey.secretName=" + planted,
                            "--set", "secrets.tlsCert.secretName=" + planted + "-tls")
    if code:
        return fail("%s: render failed: %s" % (env, err.strip()))
    ds = docs(out)
    if find(ds, "Secret"):
        fail("%s: chart renders a Secret; it must only reference them" % env)
    cm = find(ds, "ConfigMap", "andara-config")
    for k, v in (cm.get("data") or {}).items():
        if planted in str(v):
            fail("%s: secret name leaked into ConfigMap key %s" % (env, k))
        # A *_FILE key names where a mounted Secret is, which is a reference, not
        # material (ANDARA_AUTH_TOKEN_KEY_FILE, AW-SRV-008).
        if not k.endswith("_FILE") and any(w in k for w in ("PASSWORD", "SECRET", "TOKEN_KEY", "API_KEY", "BOOTSTRAP")):
            fail("%s: ConfigMap carries a secret-shaped key %s" % (env, k))
    sts = find(ds, "StatefulSet", "andara")
    for c in sts["spec"]["template"]["spec"]["containers"]:
        for e in c.get("env") or []:
            if "value" in e and any(w in e["name"] for w in ("PASSWORD", "SECRET", "TOKEN", "KEY")):
                fail("%s: env %s carries a literal value" % (env, e["name"]))
    vols = sts["spec"]["template"]["spec"]["volumes"]
    names = {v["name"]: v for v in vols}
    if names.get("token-key", {}).get("secret", {}).get("secretName") != planted:
        fail("%s: tokenKey secretName not rendered as a secret volume reference" % env)
    if names["token-key"]["secret"].get("defaultMode") != 0o400:
        fail("%s: token-key volume mode is not 0400" % env)


def test_schema_rejects(env):
    """AC-5: a value outside its declared type or range fails helm template, naming the key."""
    cases = [
        ("server.http.port=ten", "/server/http/port"),
        ("probes.startup.periodSeconds=0", "/probes/startup/periodSeconds"),
        ("server.sim.tick_rate=10", "/server"),        # groomed, not in code: rejected
        ("snapshots.size=20GB", "/snapshots/size"),
        ("nonsense=1", "additional properties"),
    ]
    for setting, needle in cases:
        code, out, err = render(env, "--set", setting)
        if code == 0:
            fail("%s: --set %s rendered; the schema should reject it" % (env, setting))
        elif needle not in err:
            fail("%s: --set %s rejected but the message does not name %r: %s"
                 % (env, setting, needle, err.strip().splitlines()[-1]))


def test_probes(env):
    """Probe contract: /readyz for startup and readiness, /livez for liveness, http port."""
    code, out, err = render(env)
    sts = find(docs(out), "StatefulSet", "andara")
    c = next(c for c in sts["spec"]["template"]["spec"]["containers"] if c["name"] == "server")
    want = {"startupProbe": "/readyz", "readinessProbe": "/readyz", "livenessProbe": "/livez"}
    for probe, path in want.items():
        got = c.get(probe, {}).get("httpGet", {})
        if got.get("path") != path or got.get("port") != "http":
            fail("%s: %s is %r, want path %s on port http" % (env, probe, got, path))
    if c["startupProbe"]["periodSeconds"] * c["startupProbe"]["failureThreshold"] < 600:
        fail("%s: startup budget under 600 s (AC-7)" % env)


def test_ordinal_partitions():
    """AC-9: at replicaCount 2 the init script yields disjoint, complete Partition sets."""
    code, out, err = render("prod", "--set", "replicaCount=2")
    sts = find(docs(out), "StatefulSet", "andara")
    init = next(c for c in sts["spec"]["template"]["spec"]["initContainers"] if c["name"] == "partitions")
    script = init["args"][0]
    sets = []
    for ordinal in (0, 1):
        with tempfile.TemporaryDirectory() as d:
            os.mkdir(os.path.join(d, "shared"))
            env = dict(os.environ, POD_INDEX=str(ordinal), REPLICAS="2")
            p = subprocess.run(["sh", "-ec", script.replace("/shared/", d + "/shared/")],
                               env=env, capture_output=True, text=True)
            if p.returncode:
                return fail("init script failed for ordinal %d: %s" % (ordinal, p.stderr))
            line = open(os.path.join(d, "shared", "partitions.env")).read().strip()
            sets.append({int(x) for x in line.split("=", 1)[1].split(",")})
    if sets[0] & sets[1]:
        fail("ordinal partition sets overlap: %s" % sorted(sets[0] & sets[1]))
    if sets[0] | sets[1] != set(range(64)):
        fail("ordinal partition sets do not cover 0–63")
    if sets[0] != set(range(0, 64, 2)):
        fail("ordinal 0 should own the even Partitions, got %s" % sorted(sets[0]))


def test_measurements():
    """AC-10: prod requests within 2× of the committed measurement; warn if unmeasured."""
    m = yaml.safe_load(open(os.path.join(CHART, "measurements.yaml")))
    if not m.get("measured"):
        print("helm-test: warn: measurements.yaml is a placeholder (measured: false); "
              "run `make measure-tick` once AW-SRV-002 has a tick loop")
    code, out, err = render("prod")
    sts = find(docs(out), "StatefulSet", "andara")
    c = next(c for c in sts["spec"]["template"]["spec"]["containers"] if c["name"] == "server")
    cpu = int(c["resources"]["requests"]["cpu"].rstrip("m"))
    mem = int(c["resources"]["requests"]["memory"].rstrip("Mi"))
    if cpu > 2 * m["server"]["cpu_millicores_p99"] or mem > 2 * m["server"]["memory_mib_p99"]:
        print("helm-test: warn: prod requests (%dm, %dMi) exceed 2× the last measurement "
              "(%dm, %dMi)" % (cpu, mem, m["server"]["cpu_millicores_p99"], m["server"]["memory_mib_p99"]))


def test_edge(env):
    """AW-INF-006: the edge renders as one coherent set — two Ingresses on one host and one
    edge Secret, the Admin one behind the allowlist Middleware, the Service annotated for
    HTTPS through a ServersTransport whose serverName is in the server certificate's SANs,
    both Certificates from ClusterIssuers, and no private key anywhere (AC-5)."""
    code, out, err = render(env)
    if code:
        return fail("%s: render failed: %s" % (env, err.strip()))
    if "PRIVATE KEY" in out:
        fail("%s: a private key is rendered (AC-5)" % env)
    ds = docs(out)
    ns = next((d["metadata"].get("namespace") for d in ds if d["metadata"].get("namespace")), "default")
    host = yaml.safe_load(open(os.path.join(VALUES, env + ".yaml"))).get("host")
    if not host:
        return fail("%s: values file sets no host" % env)

    game, admin = find(ds, "Ingress", "andara"), find(ds, "Ingress", "andara-admin")
    if game is None or admin is None:
        return fail("%s: expected Ingresses andara and andara-admin" % env)
    for ing in (game, admin):
        name = ing["metadata"]["name"]
        if ing["spec"].get("ingressClassName") != "traefik":
            fail("%s: %s ingressClassName is not traefik" % (env, name))
        tls = ing["spec"].get("tls") or []
        if len(tls) != 1 or tls[0].get("hosts") != [host] or tls[0].get("secretName") != "andara-edge-tls":
            fail("%s: %s tls is %r, want host %s from andara-edge-tls" % (env, name, tls, host))
        rules = ing["spec"]["rules"]
        if len(rules) != 1 or rules[0]["host"] != host:
            fail("%s: %s does not serve exactly host %s" % (env, name, host))
        backend = rules[0]["http"]["paths"][0]["backend"]["service"]
        if backend["name"] != "andara" or backend["port"].get("name") != "grpc":
            fail("%s: %s backend is %r, want andara:grpc" % (env, name, backend))
    if game["spec"]["rules"][0]["http"]["paths"][0]["path"] != "/":
        fail("%s: the Game Ingress path is not /" % env)
    if admin["spec"]["rules"][0]["http"]["paths"][0]["path"] != "/andara.admin.v1.Admin/":
        fail("%s: the Admin Ingress path is not the Admin service prefix" % env)
    mw_ref = admin["metadata"]["annotations"].get("traefik.ingress.kubernetes.io/router.middlewares")
    if mw_ref != "%s-andara-admin-allowlist@kubernetescrd" % ns:
        fail("%s: Admin Ingress middleware reference is %r" % (env, mw_ref))
    if "traefik.ingress.kubernetes.io/router.middlewares" in game["metadata"].get("annotations", {}):
        fail("%s: the Game Ingress must not carry the allowlist" % env)

    mw = find(ds, "Middleware", "andara-admin-allowlist")
    if mw is None:
        fail("%s: no allowlist Middleware" % env)
    elif not mw["spec"].get("ipAllowList", {}).get("sourceRange"):
        fail("%s: allowlist Middleware has no sourceRange" % env)

    svc = find(ds, "Service", "andara")
    ann = svc["metadata"].get("annotations") or {}
    if ann.get("traefik.ingress.kubernetes.io/service.serversscheme") != "https":
        fail("%s: Service is not annotated for HTTPS to the pod" % env)
    if ann.get("traefik.ingress.kubernetes.io/service.serverstransport") != "%s-andara@kubernetescrd" % ns:
        fail("%s: Service serverstransport reference is %r" % (env, ann.get("traefik.ingress.kubernetes.io/service.serverstransport")))

    st = find(ds, "ServersTransport", "andara")
    server_cert = find(ds, "Certificate", "andara-server")
    edge_cert = find(ds, "Certificate", "andara-edge")
    if st is None or server_cert is None or edge_cert is None:
        return fail("%s: ServersTransport and both Certificates must render" % env)
    if st["spec"].get("serverName") not in server_cert["spec"]["dnsNames"]:
        fail("%s: ServersTransport serverName %r is not among the server certificate SANs %r"
             % (env, st["spec"].get("serverName"), server_cert["spec"]["dnsNames"]))
    if [r.get("secret") for r in st["spec"].get("rootCAs", [])] != [server_cert["spec"]["secretName"]]:
        fail("%s: ServersTransport rootCAs must read the server certificate's Secret" % env)
    if edge_cert["spec"]["dnsNames"] != [host] or edge_cert["spec"]["secretName"] != "andara-edge-tls":
        fail("%s: edge Certificate is for %r into %r" % (env, edge_cert["spec"]["dnsNames"], edge_cert["spec"]["secretName"]))
    for c in (edge_cert, server_cert):
        if c["spec"]["issuerRef"].get("kind") != "ClusterIssuer":
            fail("%s: %s issuerRef is not a ClusterIssuer" % (env, c["metadata"]["name"]))
        if c["spec"].get("renewBefore") != "240h":
            fail("%s: %s renewBefore is %r, want 240h" % (env, c["metadata"]["name"], c["spec"].get("renewBefore")))
    if server_cert["spec"]["issuerRef"]["name"] != "andara-ca":
        fail("%s: the server certificate must come from the private CA" % env)
    sts = find(ds, "StatefulSet", "andara")
    vols = {v["name"]: v for v in sts["spec"]["template"]["spec"]["volumes"]}
    if vols.get("tls", {}).get("secret", {}).get("secretName") != server_cert["spec"]["secretName"]:
        fail("%s: the pod does not mount the Secret the server Certificate issues into" % env)

    np = find(ds, "NetworkPolicy", "andara")
    if np is None:
        return fail("%s: NetworkPolicy must render (AC-6)" % env)
    grpc_rule = next((r for r in np["spec"]["ingress"] if any(p.get("port") == "grpc" for p in r["ports"])), None)
    admitted = sorted(f["namespaceSelector"]["matchLabels"]["kubernetes.io/metadata.name"] for f in grpc_rule["from"])
    if admitted != ["andara-agents", "traefik"]:
        fail("%s: grpc port admits %r, want the Traefik and agent namespaces only" % (env, admitted))


def test_edge_off():
    """ingress.enabled=false renders none of the edge and the schema rejects a bad host,
    CIDR, or issuer (the values contract)."""
    code, out, err = render("local", "--set", "ingress.enabled=false")
    if code:
        return fail("ingress.enabled=false failed to render: %s" % err.strip())
    ds = docs(out)
    kinds = {d["kind"] for d in ds}
    for k in ("Ingress", "Middleware", "ServersTransport"):
        if k in kinds:
            fail("ingress.enabled=false still renders a %s" % k)
    if find(ds, "Certificate", "andara-edge") is not None:
        fail("ingress.enabled=false still renders the edge Certificate")
    if find(ds, "Certificate", "andara-server") is None:
        fail("ingress.enabled=false dropped the server Certificate; the server has no plaintext mode")
    svc = find(ds, "Service", "andara")
    if "traefik.ingress.kubernetes.io/service.serverstransport" in (svc["metadata"].get("annotations") or {}):
        fail("ingress.enabled=false still points the Service at a ServersTransport")
    for setting, needle in [
        ("host=Andara_Local", "/host"),
        ("admin.allowedCIDRs[0]=everyone", "/admin/allowedCIDRs"),
        ("tls.issuer=some-other-issuer", "/tls/issuer"),
    ]:
        code, out, err = render("local", "--set", setting)
        if code == 0:
            fail("--set %s rendered; the schema should reject it" % setting)
        elif needle not in err:
            fail("--set %s rejected but the message does not name %r" % (setting, needle))


def test_projectors_render_when_enabled():
    code, out, err = render("prod", "--set", "projectors.state.enabled=true")
    if code:
        return fail("projector render failed: %s" % err.strip())
    d = find(docs(out), "Deployment", "andara-projector-state")
    if d is None:
        fail("projectors.state.enabled=true did not render a Deployment")
    elif d["spec"]["strategy"]["type"] != "Recreate":
        fail("projector Deployment must use Recreate (one consumer group holder)")


def main():
    for env in ENVS:
        test_pvc_retained(env)
        test_no_secret_material(env)
        test_schema_rejects(env)
        test_probes(env)
        test_edge(env)
    test_edge_off()
    test_ordinal_partitions()
    test_measurements()
    test_projectors_render_when_enabled()
    if failures:
        print("helm-test: %d failure(s)" % len(failures), file=sys.stderr)
        sys.exit(1)
    print("helm-test: chart assertions hold for %s" % ", ".join(ENVS))


if __name__ == "__main__":
    main()
