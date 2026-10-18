#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/cluster_secrets.py export|restore — keep the two certificate Secrets across a rebuild (AW-INF-052).

A rebuild deletes the cluster `andara` and everything on it. Two Secrets are worth carrying over
(ADR-0013 decision 11, "Rebuilds and Let's Encrypt"):

  andara-dev/andara-edge-tls    the dev edge certificate (andara.edgeSecretName); reissuing it
                                spends Let's Encrypt's weekly limit
  cert-manager/andara-ca-tls    cert-manager's private CA root; a new one invalidates every client
                                that trusts the old one

`export` writes each one that exists to .local/tls/<secret>.yaml, mode 0600, through a temporary file
in the same directory and a rename, so a failed write never truncates the previous export. A Secret
the cluster doesn't have is reported `absent` and leaves any earlier export of it in place. `make
clean` keeps .local/tls/.

`restore` reapplies them, dropping the fields the old cluster assigned (resourceVersion, uid,
creationTimestamp, managedFields, ownerReferences) and keeping the annotations, so cert-manager's
issuer-name, issuer-kind and certificate-name survive. Every target namespace is checked before
anything is applied. Where restore sits in the platform's order is AW-INF-054's call.

Both print each Secret's file mode and, for a certificate, its serial and notBefore: public values,
never the key. AW-INF-066 records them before and after a rebuild.

Exit: 0 ok (including nothing to export or restore) · 1 a kubectl read, a write or an apply failed,
or a target namespace is missing · 2 usage · 3 kubectl is missing (run `make bootstrap`).
"""
import base64
import os
import subprocess
import sys
import tempfile

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CONTEXT = "kind-andara"
TLS_DIR = os.path.join(REPO, ".local", "tls")
# (namespace, name), in the order restore applies them: the CA root before the certificate it may
# have issued.
SECRETS = [("cert-manager", "andara-ca-tls"), ("andara-dev", "andara-edge-tls")]
DROP = ("resourceVersion", "uid", "creationTimestamp", "managedFields", "ownerReferences",
        "generation", "selfLink")
LAST_APPLIED = "kubectl.kubernetes.io/last-applied-configuration"


class Failed(Exception):
    """A step that failed. The message names the Secret or namespace and what kubectl said."""


class Missing(Exception):
    """A prerequisite isn't installed."""


def default_say(msg):
    print("cluster-secrets: " + msg, flush=True)


def kubectl_path():
    """bin/kubectl, the version bootstrap pins; KUBECTL overrides it (the tests' stub)."""
    path = os.environ.get("KUBECTL") or os.path.join(REPO, "bin", "kubectl")
    if not os.access(path, os.X_OK):
        raise Missing("%s not found; run `make bootstrap`" % path)
    return path


def kubectl(context, *args, stdin=None):
    return subprocess.run([kubectl_path(), "--context", context, *args], input=stdin,
                          capture_output=True, text=True)


def not_found(p):
    """The API server's NotFound, and nothing else: a missing credential plugin says "not found" too,
    and counting that as absent would let a rebuild delete a Secret it never exported."""
    return p.returncode != 0 and "Error from server (NotFound)" in p.stderr


def cert_facts(secret):
    """`serial=… notBefore=…` from the Secret's tls.crt, or '' when there's no certificate. openssl
    reads only the certificate; the key is never decoded."""
    crt = (secret.get("data") or {}).get("tls.crt")
    if not crt:
        return ""
    try:
        pem = base64.b64decode(crt)
    except ValueError:
        return "serial=unreadable"
    try:
        p = subprocess.run(["openssl", "x509", "-noout", "-serial", "-startdate"], input=pem,
                           capture_output=True)
    except FileNotFoundError:
        return "serial=unknown (openssl not found)"
    if p.returncode != 0:
        return "serial=unreadable"
    facts = dict(line.split("=", 1) for line in p.stdout.decode().splitlines() if "=" in line)
    return "serial=%s notBefore=%s" % (facts.get("serial", "?"), facts.get("notBefore", "?"))


def file_for(name, tls_dir):
    return os.path.join(tls_dir, name + ".yaml")


def write_atomic(path, text):
    """Write text to path, mode 0600, without ever leaving a truncated file at path."""
    fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path), prefix="." + os.path.basename(path) + ".")
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except BaseException:
        try:
            os.unlink(tmp)
        except FileNotFoundError:
            pass
        raise


def export(context=CONTEXT, tls_dir=TLS_DIR, say=default_say):
    """Export every Secret that exists. Returns {"<ns>/<name>": "written" | "absent"}. Raises Failed
    on the first Secret it can't read or write; earlier files are already complete by then."""
    os.makedirs(tls_dir, mode=0o700, exist_ok=True)
    result = {}
    for ns, name in SECRETS:
        key = "%s/%s" % (ns, name)
        path = file_for(name, tls_dir)
        p = kubectl(context, "-n", ns, "get", "secret", name, "-o", "yaml")
        if not_found(p):
            kept = " (earlier export kept: %s)" % os.path.relpath(path, REPO) if os.path.exists(path) else ""
            say("%s absent%s" % (key, kept))
            result[key] = "absent"
            continue
        if p.returncode != 0:
            raise Failed("read %s (only NotFound counts as absent): %s" % (key, p.stderr.strip()))
        secret = yaml.safe_load(p.stdout)
        try:
            write_atomic(path, p.stdout)
        except OSError as e:
            raise Failed("write %s to %s: %s" % (key, os.path.relpath(path, REPO), e))
        mode = oct(os.stat(path).st_mode & 0o777)[2:]
        facts = cert_facts(secret)
        say("%s written %s mode=%s%s" % (key, os.path.relpath(path, REPO), mode, " " + facts if facts else ""))
        result[key] = "written"
    return result


def clean(secret):
    """The Secret as it should be applied to a new cluster: the old cluster's bookkeeping dropped,
    name, namespace, labels and annotations (cert-manager's among them) kept."""
    meta = dict(secret.get("metadata") or {})
    for f in DROP:
        meta.pop(f, None)
    annotations = dict(meta.get("annotations") or {})
    annotations.pop(LAST_APPLIED, None)
    if annotations:
        meta["annotations"] = annotations
    else:
        meta.pop("annotations", None)
    out = {k: v for k, v in secret.items() if k not in ("metadata", "status")}
    out["metadata"] = meta
    return out


def restore(context=CONTEXT, tls_dir=TLS_DIR, say=default_say):
    """Reapply every exported Secret. Returns the number restored."""
    found = []
    for ns, name in SECRETS:
        path = file_for(name, tls_dir)
        if os.path.exists(path):
            with open(path) as f:
                found.append((ns, name, clean(yaml.safe_load(f))))
    if not found:
        say("nothing to restore: no export in %s" % os.path.relpath(tls_dir, REPO))
        return 0
    for ns in sorted({ns for ns, _, _ in found}):
        p = kubectl(context, "get", "namespace", ns, "-o", "name")
        if not_found(p):
            raise Failed("target namespace %s is missing; create it before restoring" % ns)
        if p.returncode != 0:
            raise Failed("read namespace %s: %s" % (ns, p.stderr.strip()))
    for ns, name, secret in found:
        key = "%s/%s" % (ns, name)
        if (secret["metadata"].get("namespace") or ns) != ns or secret["metadata"].get("name") != name:
            raise Failed("%s holds %s/%s, not %s" % (file_for(name, tls_dir), secret["metadata"].get("namespace"),
                                                       secret["metadata"].get("name"), key))
        p = kubectl(context, "apply", "-f", "-", stdin=yaml.safe_dump(secret))
        if p.returncode != 0:
            raise Failed("apply %s: %s" % (key, p.stderr.strip()))
        back = kubectl(context, "-n", ns, "get", "secret", name, "-o", "yaml")
        if back.returncode != 0:
            raise Failed("read back %s: %s" % (key, back.stderr.strip()))
        facts = cert_facts(yaml.safe_load(back.stdout))
        say("%s restored%s" % (key, " " + facts if facts else ""))
    return len(found)


def main(argv):
    if len(argv) != 1 or argv[0] not in ("export", "restore"):
        print("usage: scripts/cluster_secrets.py export|restore", file=sys.stderr)
        return 2
    try:
        if argv[0] == "export":
            export()
        else:
            restore()
    except Missing as e:
        print("cluster-secrets: " + str(e), file=sys.stderr)
        return 3
    except Failed as e:
        print("cluster-secrets: " + str(e), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
