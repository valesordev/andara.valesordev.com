#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Register and verify protobuf subjects against the schema registry (AW-INF-004).

    scripts/schemas.py check                                       # offline; runs inside `make check`
    scripts/schemas.py apply --env <local|dev> [--broker compose|cluster]   # needs a live registry
    scripts/schemas.py diff  --env <local|dev> [--broker compose|cluster]   # needs a live registry
    scripts/schemas.py subjects-for <file>                         # which subjects carry a .proto file

ENVIRONMENTS AND THE SHARED REGISTRY (AW-INF-057, ADR-0013 decision 8). local and dev share one
Karapace in andara-shared, so every registered subject carries the environment's topic prefix
(deploy/kafka/topics.yaml): `dev.andara.commands.v1-value`, and a reference's subject is
`<prefix>.<import path>` with the reference's `name` kept as the import path. The declaration
itself names subjects unprefixed; `apply` and `diff` add the prefix. The registry is
ANDARA_SCHEMA_REGISTRY_URL when set; otherwise local on the compose broker (the default while
deploy/compose/ exists, as for topics.py) is compose's registry on localhost, and everything on
the cluster is reached through `kubectl port-forward` to svc/andara-schema-registry. `apply`
asks the registry about every existing subject before it registers any, so a refused change
registers nothing. Exit codes: 0 ok; 1 an API failure or an incompatible change; 2 usage (ENV
missing or unknown); 3 the cluster or its context missing.

WHICH TARGET ENFORCES WHAT — the division here is deliberate and measured, not stylistic:

  * ADR-0007's additive-only rule is enforced by `buf breaking` in `make proto-check`.
    It is NOT re-checked here. Redpanda's registry was measured on 2026-09-10 and accepts
    both a removed field and a RENUMBERED field as BACKWARD-compatible. Renumbering would
    make replay misread every historical record (ADR-0002), so the registry cannot be the
    detector for it and buf is. The shared Karapace (6.2.4, re-measured 2026-10-24 for
    AW-INF-057) rejects the renumbering but still accepts the removed field, so it remains a
    second opinion, and buf the rule.
  * `check` verifies the declaration itself — offline, no broker, safe inside `make check`.
  * `diff` is the question git cannot answer: does the registry hold what we declared.

Stdlib only. This validates the stack, so it must not need anything the stack installs.
"""

import argparse
import json
import os
import queue
import re
import subprocess
import sys
import threading
import urllib.error
import urllib.parse
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import topics  # noqa: E402
from topics import parse_declaration as parse_topics  # noqa: E402

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DECL = os.path.join(REPO, "deploy", "kafka", "schemas.yaml")
TOPICS = os.path.join(REPO, "deploy", "kafka", "topics.yaml")
PROTO_DIR = os.path.join(REPO, "docs", "specs", "protocol")
# Stories are GitHub issues since SPRINT-05, so a pending entry's story is checked by form only.
STORY_ID = re.compile(r"^AW-[A-Z]+-\d{3}$")

ACCEPT = "application/vnd.schemaregistry.v1+json"
ENVS = ("local", "dev")
REGISTRY_NS = "andara-shared"
REGISTRY_SERVICE = "svc/andara-schema-registry"
PORT_FORWARD_TIMEOUT = 20
popen = subprocess.Popen
RUN_ID = "%x" % (os.getpid() ^ int.from_bytes(os.urandom(2), "big"))
STRATEGIES = ("TopicNameStrategy", "TopicRecordNameStrategy")


def q(subject):
    """URL-encode a subject for a path segment.

    Reference subjects are import paths — `andara/log/v1/log.proto` — so an unencoded
    subject turns one path segment into four and the registry answers 404.
    """
    return urllib.parse.quote(subject, safe="")


def die(msg, code=1):
    sys.stderr.write("make: schemas: %s\n" % msg)
    sys.exit(code)


def parse_schema_declaration(path):
    """Parse deploy/kafka/schemas.yaml.

    Narrow on purpose, like the topic declaration's parser: it reads this file's shape —
    one top-level scalar and three lists of flat mappings, `why:` block scalars allowed —
    and nothing else. A declaration that needs a real YAML parser is a declaration nobody
    wants to review.
    """
    doc = {"compatibility": None, "references": [], "subjects": [], "pending": []}
    section = None
    item = None
    block_key = None
    block = []

    with open(path, "r", encoding="utf-8") as fh:
        raw_lines = fh.read().split("\n")

    for lineno, raw in enumerate(raw_lines, start=1):
        if block_key is not None:
            # A block scalar continues while indented past its key, or is blank.
            if not raw.strip() or raw.startswith("      "):
                block.append(raw.strip())
                continue
            item[block_key] = "\n".join(block).strip()
            block_key, block = None, []

        line = raw if raw.strip().startswith("#") is False else ""
        line = line.split(" #")[0].rstrip()
        if not line.strip():
            continue

        if not line.startswith(" ") and line.endswith(":"):
            section = line[:-1].strip()
            if section not in doc:
                die("%s:%d: unknown section '%s'" % (path, lineno, section))
            continue
        if not line.startswith(" "):
            key, _, val = line.partition(":")
            doc[key.strip()] = val.strip()
            section = None
            continue

        stripped = line.strip()
        if stripped.startswith("- "):
            item = {}
            doc[section].append(item)
            stripped = stripped[2:]
        if item is None:
            die("%s:%d: value outside a list item" % (path, lineno))
        key, _, val = stripped.partition(":")
        key, val = key.strip(), val.strip()
        if val == "|":
            block_key, block = key, []
            continue
        item[key] = val

    if block_key is not None:
        item[block_key] = "\n".join(block).strip()
    return doc


def descriptor():
    """File descriptors as JSON, from buf. Never hand-parse .proto to answer this."""
    buf = os.path.join(REPO, "bin", "buf")
    exe = buf if os.path.exists(buf) else "buf"
    try:
        out = subprocess.run(
            [exe, "build", PROTO_DIR, "-o", "-#format=json"],
            capture_output=True, check=True,
        ).stdout
    except FileNotFoundError:
        die("buf not found (run `make bootstrap`)")
    except subprocess.CalledProcessError as e:
        die("buf build failed:\n%s" % e.stderr.decode().strip())
    files = {}
    for f in json.loads(out).get("file", []):
        files[f["name"]] = {
            "package": f.get("package", ""),
            "messages": [m["name"] for m in f.get("messageType", [])],
            "deps": f.get("dependency", []),
        }
    return files


def comment_start(line):
    """The index of the first `//` outside a string literal, or -1 (`"http://…"` is not a comment)."""
    quote, i = None, 0
    while i < len(line):
        c = line[i]
        if quote:
            if c == "\\":
                i += 1
            elif c == quote:
                quote = None
        elif c in "\"'":
            quote = c
        elif line.startswith("//", i):
            return i
        i += 1
    return -1


def ascii_comments(text, rel="<source>"):
    """The source with every non-ASCII character in a `//` comment replaced by `?`.

    Karapace 6.2.4 refuses a schema with any non-ASCII character (`ValueError: string argument
    should contain only ASCII characters`, HTTP 500; measured 2026-10-24, AW-INF-057 AC-8), where
    Redpanda accepted it. The protos use em dashes and the like in comments only, and a registry
    stores the schema canonicalised, comments dropped, so this changes nothing a registry keeps.
    A non-ASCII character outside a comment would change the schema, so it is refused here."""
    out = []
    for n, line in enumerate(text.split("\n"), start=1):
        cut = comment_start(line)
        code, sep, comment = (line, "", "") if cut < 0 else (line[:cut], "//", line[cut + 2:])
        if any(ord(c) > 127 for c in code):
            die("%s:%d: a non-ASCII character outside a // comment; Karapace refuses it, and "
                "replacing it would change the schema" % (rel, n))
        out.append(code + sep + "".join(c if ord(c) < 128 else "?" for c in comment))
    return "\n".join(out)


def source_of(rel):
    """The .proto as the registry is sent it (ascii_comments)."""
    with open(os.path.join(PROTO_DIR, rel), "r", encoding="utf-8") as fh:
        return ascii_comments(fh.read(), rel)


def prefix_of(env):
    """The environment's topic prefix, which is also its subject prefix."""
    return topics.environment(TOPICS, env)["prefix"]


def env_subject(prefix, subject):
    return "%s.%s" % (prefix, subject) if prefix else subject


def port_forward():
    """A port-forward to the shared registry: (url, process). Exit 3 without the cluster."""
    import kube
    try:
        kube.require()
    except kube.Missing as e:
        die(str(e), 3)
    import shutil
    if shutil.which("kubectl") is None:
        die("kubectl not found; the shared registry is on the cluster", 3)
    # stderr merged into stdout: one pipe, drained below, so kubectl's per-connection errors can't fill
    # an undrained pipe and stall the forward.
    proc = popen(kube.kubectl("-n", REGISTRY_NS, "port-forward", REGISTRY_SERVICE, ":8081"),
                 stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    lines = queue.Queue()

    def pump():
        # The first line names the port; kubectl writes one more per connection, so the rest is drained
        # here, or a long run would fill the pipe and stall the forward.
        for line in proc.stdout:
            lines.put(line)
        lines.put(None)  # kubectl ended: the wait below fails at once, not at the deadline
    threading.Thread(target=pump, daemon=True).start()
    seen = []
    try:
        while True:
            line = lines.get(timeout=PORT_FORWARD_TIMEOUT)
            if line is None:
                break
            seen.append(line)
            if "Forwarding from 127.0.0.1:" in line:
                break
    except queue.Empty:
        pass
    m = re.search(r"Forwarding from 127\.0\.0\.1:(\d+)", "".join(seen))
    if not m:
        proc.kill()
        die("no port-forward to %s in %s: %s" % (REGISTRY_SERVICE, REGISTRY_NS,
                                                 "".join(seen).strip()[:300] or "no output within %ds" % PORT_FORWARD_TIMEOUT))
    return "http://127.0.0.1:%s" % m.group(1), proc


def registry_url(env, broker):
    """(url, port-forward process or None) for env's registry."""
    if os.environ.get("ANDARA_SCHEMA_REGISTRY_URL"):
        return os.environ["ANDARA_SCHEMA_REGISTRY_URL"].rstrip("/"), None
    if env == "local" and broker == "compose":
        return "http://localhost:%s" % os.environ.get("ANDARA_SCHEMA_REGISTRY_PORT", "8081"), None
    return port_forward()


class Unreachable(RuntimeError):
    """The registry didn't answer at all. A RuntimeError like an HTTP error, so a run that loses the
    registry part-way still rolls back and names what it registered; main exits 1 on one that escapes."""


def call(url, path, method="GET", payload=None, params=""):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(
        url + path + params, data=data,
        headers={"Content-Type": ACCEPT, "Accept": ACCEPT}, method=method,
    )
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            body = r.read().decode()
        return json.loads(body) if body else {}
    except urllib.error.HTTPError as e:
        raise RuntimeError("%s %s -> HTTP %d %s" % (method, path, e.code, e.read().decode()[:300]))
    except (urllib.error.URLError, OSError) as e:  # refused, reset, or a read timeout
        raise Unreachable("no schema registry at %s (%s). `make up` starts one; override with "
                          "ANDARA_SCHEMA_REGISTRY_URL." % (url, getattr(e, "reason", e)))


# --------------------------------------------------------------------------- check

def check():
    doc = parse_schema_declaration(DECL)
    _, topics, _ = parse_topics(TOPICS)
    declared_topics = [t["name"] for t in topics]
    files = descriptor()
    errors = []

    if doc.get("compatibility") != "BACKWARD":
        errors.append("compatibility is '%s'; ADR-0007 is additive-only, so it must be BACKWARD"
                      % doc.get("compatibility"))

    seen = {}
    for s in doc["subjects"]:
        for key in ("subject", "topic", "strategy", "file", "message"):
            if not s.get(key):
                errors.append("subject entry missing '%s': %r" % (key, s))
        if not s.get("subject"):
            continue
        if s["subject"] in seen:
            errors.append("subject '%s' declared twice" % s["subject"])
        seen[s["subject"]] = s

        if s.get("strategy") not in STRATEGIES:
            errors.append("subject '%s': strategy '%s' is not one of %s"
                          % (s["subject"], s.get("strategy"), ", ".join(STRATEGIES)))
        if s.get("topic") and s["topic"] not in declared_topics:
            errors.append("subject '%s': topic '%s' is not declared in deploy/kafka/topics.yaml"
                          % (s["subject"], s["topic"]))

        # The subject name is derived, not free text. A hand-typed subject that does not
        # match its strategy is a subject producers will not find at runtime.
        if s.get("strategy") == "TopicNameStrategy":
            want = "%s-value" % s.get("topic")
        elif s.get("strategy") == "TopicRecordNameStrategy":
            want = "%s-%s" % (s.get("topic"), s.get("message"))
        else:
            want = None
        if want and s["subject"] != want:
            errors.append("subject '%s': %s requires the subject to be '%s'"
                          % (s["subject"], s["strategy"], want))

        f = files.get(s.get("file"))
        if f is None:
            errors.append("subject '%s': file '%s' is not in the protobuf module"
                          % (s["subject"], s.get("file")))
            continue
        fqn = s.get("message", "")
        prefix = f["package"] + "."
        if not fqn.startswith(prefix) or fqn[len(prefix):] not in f["messages"]:
            errors.append("subject '%s': message '%s' does not exist in %s (package %s has: %s)"
                          % (s["subject"], fqn, s["file"], f["package"], ", ".join(f["messages"])))
        # Every import has to be registerable as a reference, or `apply` cannot resolve it.
        refs = {r.get("file") for r in doc["references"]}
        for dep in f["deps"]:
            if dep not in refs:
                errors.append("subject '%s': '%s' imports '%s', which has no entry under "
                              "`references:` — the registry resolves references by subject "
                              "name and cannot register this schema without it"
                              % (s["subject"], s["file"], dep))

    # A reference nothing imports is config that will rot. The check that adds them is
    # above; this is the one that removes them.
    imported = set()
    for s2 in doc["subjects"]:
        imported |= set(files.get(s2.get("file"), {}).get("deps", []))
    for r in doc["references"]:
        if r.get("file") and r["file"] not in imported:
            errors.append("reference '%s' is not imported by any declared subject's file; "
                          "remove it" % r["file"])
        if not r.get("file") or not r.get("subject"):
            errors.append("reference entry needs both 'file' and 'subject': %r" % r)
            continue
        if files.get(r["file"], {}).get("deps"):
            errors.append("reference '%s' imports %s; a reference must import nothing, so `apply` can "
                          "ask about it before registering anything" % (r["file"], ", ".join(files[r["file"]]["deps"])))
            continue
        if r["file"] not in files:
            errors.append("reference '%s': file is not in the protobuf module" % r["file"])
        if r["subject"] != r["file"]:
            errors.append("reference '%s': the subject must equal the import path ('%s'); apply "
                          "registers it as <prefix>.<import path>, with the reference's name kept as "
                          "the import path" % (r["subject"], r["file"]))

    pending_topics = {}
    for p in doc["pending"]:
        if not p.get("topic") or not p.get("story"):
            errors.append("pending entry needs both 'topic' and 'story': %r" % p)
            continue
        pending_topics[p["topic"]] = p["story"]
        if not STORY_ID.match(p["story"]):
            errors.append("pending topic '%s' names story %r, which is not a story ID (AW-XXX-NNN)"
                          % (p["topic"], p["story"]))

    # The check this file exists for: a topic that is neither mapped nor explicitly pending
    # is indistinguishable from a topic somebody forgot.
    mapped = {s.get("topic") for s in doc["subjects"]}
    for name in declared_topics:
        if name in mapped and name in pending_topics:
            errors.append("topic '%s' is both mapped to a subject and listed as pending" % name)
        elif name not in mapped and name not in pending_topics:
            errors.append("topic '%s' has no subject and is not listed under `pending:` — every "
                          "topic must be accounted for, or a forgotten one looks like a "
                          "deliberate omission" % name)

    if errors:
        for e in errors:
            sys.stderr.write("make: schemas-check: %s\n" % e)
        die("%d problem(s) in deploy/kafka/schemas.yaml" % len(errors))
    print("schemas-check: %d subject(s), %d reference(s), %d topic(s) pending a record type"
          % (len(doc["subjects"]), len(doc["references"]), len(doc["pending"])))
    print("schemas-check: additive-only is enforced by `make proto-check`, not here")


# --------------------------------------------------------------------------- apply

def register(url, subject, source, refs):
    """Register one subject. Returns (id, version created or None). A source already registered under
    the subject, at any version (a reverted file included), is left as it is and creates nothing, so
    a run only ever records versions it added, and a rollback can only remove those."""
    found = lookup(url, subject, source, refs)
    if found is not None:
        return found.get("id"), None
    payload = {"schemaType": "PROTOBUF", "schema": source}
    if refs:
        payload["references"] = refs
    out = call(url, "/subjects/%s/versions" % q(subject), "POST", payload)
    return out.get("id"), call(url, "/subjects/%s/versions/latest" % q(subject))["version"]


def incompatible(url, subject, source, refs, mode):
    """The registry's messages when it refuses this source for an existing subject, else None.
    A second opinion, never the primary one — see the module docstring."""
    payload = {"schemaType": "PROTOBUF", "schema": source}
    if refs:
        payload["references"] = refs
    try:
        call(url, "/subjects/%s/versions/latest" % q(subject))
    except Unreachable:
        raise
    except RuntimeError:
        return None  # a new subject has nothing to be compatible with
    verdict = call(url, "/compatibility/subjects/%s/versions/latest" % q(subject),
                   "POST", payload, "?verbose=true")
    if verdict.get("is_compatible") is False:
        return verdict.get("messages") or ["not %s-compatible" % mode]
    return None


def refs_ready(url, files, rel, prefix):
    """Whether every import of rel is registered under the prefix with the source declared now."""
    for d in files.get(rel, {}).get("deps", []):
        try:
            refs = reference_list(url, files, d, prefix)
            if lookup(url, env_subject(prefix, d), source_of(d), refs) is None:
                return False
        except RuntimeError:
            return False
    return True


def ask(url, subject, rel, files, prefix, mode, undo=None):
    """[subject] when the registry refuses rel for it (its messages on stderr), else []. A failed
    question exits 1 naming the subject, never a traceback; `undo`, when given, is called first and
    what it returns (what it rolled back) goes in the message."""
    try:
        msgs = incompatible(url, subject, source_of(rel), reference_list(url, files, rel, prefix), mode)
    except RuntimeError as e:
        die("asking the registry about %s failed: %s%s" % (subject, e, "; " + undo() if undo else ""))
    for m in msgs or []:
        sys.stderr.write("schemas: error %s refused under compatibility %s: %s run=%s\n" % (subject, mode, m, RUN_ID))
    return [subject] if msgs else []


def reference_list(url, files, rel, prefix=""):
    """Reference entries for a file's imports, with concrete versions.

    Not `version: -1`. Confluent reads -1 as "latest"; Redpanda rejects it — and rejects it
    as `422 parse error at offset 2772`, an offset past the end of the schema, which sends
    you looking at the .proto rather than at the reference. Measured 2026-09-10. Karapace
    accepts -1 (re-measured 2026-10-24); concrete versions work on both, and compose's
    Redpanda is still `make up`'s registry.

    A reference's subject is `<prefix>.<import path>` and its name the import path, so local
    and dev never share one; Karapace resolves it by subject (measured 2026-10-24).
    """
    refs = []
    for d in files.get(rel, {}).get("deps", []):
        subject = env_subject(prefix, d)
        try:
            latest = call(url, "/subjects/%s/versions/latest" % q(subject))
        except Unreachable:
            raise
        except RuntimeError:
            continue  # not registered yet: only the compatibility pass asks before it exists
        refs.append({"name": d, "subject": subject, "version": latest["version"]})
    return refs


def apply_action(env, url):
    doc = parse_schema_declaration(DECL)
    files = descriptor()
    prefix = prefix_of(env)
    mode = doc["compatibility"]
    items = doc["references"] + doc["subjects"]

    # A refused change registers nothing anywhere (AC-4), in phases:
    #   1. every subject whose references are registered as declared is asked;
    #   2. the references are registered (an importer can't be asked until they are: the registry
    #      can't resolve its import), each version this run creates recorded;
    #   3. every importer that had to wait is asked; one refused rolls back the versions of phase 2,
    #      so the run leaves the registry as it found it;
    #   4. only then is any other subject registered.
    refused, deferred = [], set()
    for r in items:
        subject = env_subject(prefix, r["subject"])
        if not refs_ready(url, files, r["file"], prefix):
            deferred.add(subject)
            continue
        refused += ask(url, subject, r["file"], files, prefix, mode)
    if refused:
        die("the registry refuses %s under compatibility %s; nothing was registered"
            % (", ".join(refused), mode))

    made, created_versions = 0, []

    def put(r):
        nonlocal made
        subject, rel = env_subject(prefix, r["subject"]), r["file"]
        try:
            sid, version = register(url, subject, source_of(rel), reference_list(url, files, rel, prefix))
        except RuntimeError as e:
            die("registering %s failed: %s; registered by this run: %s"
                % (subject, e, ", ".join("%s@%s" % sv for sv in created_versions) or "nothing"))
        if version is not None:
            created_versions.append((subject, version))
        made += 1 if version is not None else 0
        print("schemas: %s %s id=%s run=%s" % (subject, "unchanged" if version is None else "registered", sid, RUN_ID))

    # References first: the registry resolves a reference by subject name, so the imported file has
    # to be there before its importer is asked or registered.
    for r in doc["references"]:
        put(r)
    late = [r for r in doc["subjects"] if env_subject(prefix, r["subject"]) in deferred]

    def undo():
        undone, left = rollback(url, created_versions)
        if left:
            return "rollback incomplete: still registered %s%s" % (
                ", ".join(left), "; undone: %s" % ", ".join(undone) if undone else "")
        return "nothing was registered%s" % (" (rolled back %s)" % ", ".join(undone) if undone else "")
    refused = []
    for r in late:
        refused += ask(url, env_subject(prefix, r["subject"]), r["file"], files, prefix, mode, undo)
    if refused:
        die("the registry refuses %s under compatibility %s; %s" % (", ".join(refused), mode, undo()))
    call(url, "/config", "PUT", {"compatibility": mode})
    print("schemas-apply: global compatibility set to %s run=%s" % (mode, RUN_ID))
    for r in doc["references"]:
        call(url, "/config/%s" % q(env_subject(prefix, r["subject"])), "PUT", {"compatibility": mode})
    for r in doc["subjects"]:
        put(r)
        call(url, "/config/%s" % q(env_subject(prefix, r["subject"])), "PUT", {"compatibility": mode})
    print("schemas-apply: %d subject(s), %d newly registered (env=%s, prefix=%s)"
          % (len(items), made, env, prefix or "(none)"))


def rollback(url, created_versions):
    """Delete the versions this run created, newest first, soft then permanently (a Confluent-API
    registry needs both). Returns (undone, left) as subject@version strings: a failed delete is
    reported and the rest still tried, never a traceback."""
    undone, left = [], []
    for subject, version in reversed(created_versions):
        sv = "%s@%s" % (subject, version)
        path = "/subjects/%s/versions/%s" % (q(subject), version)
        try:
            call(url, path, "DELETE")
        except RuntimeError as e:
            sys.stderr.write("schemas: error rolling back %s: %s run=%s\n" % (sv, e, RUN_ID))
            left.append(sv)
            continue
        try:
            call(url, path, "DELETE", params="?permanent=true")
            undone.append(sv)
        except RuntimeError as e:
            # Hidden from readers and lookups, but still holding its id: a different recovery.
            sys.stderr.write("schemas: error purging %s: %s run=%s\n" % (sv, e, RUN_ID))
            left.append(sv + " (soft-deleted, not purged)")
    return undone, left


# --------------------------------------------------------------------------- diff

def lookup(url, subject, source, refs):
    """Ask the registry whether this exact source is already registered under a subject.

    Text comparison cannot work here: the registry canonicalises what it stores — comments
    stripped, type references fully qualified — so the schema it hands back never equals the
    file it came from. `POST /subjects/{subject}` answers the question directly and without
    registering anything, which is what a diff needs.
    """
    payload = {"schemaType": "PROTOBUF", "schema": source}
    if refs:
        payload["references"] = refs
    try:
        return call(url, "/subjects/%s" % q(subject), "POST", payload)
    except RuntimeError as e:
        if "HTTP 404" in str(e):
            return None
        raise


def diff_action(env, url):
    doc = parse_schema_declaration(DECL)
    files = descriptor()
    prefix = prefix_of(env)
    declared = [(env_subject(prefix, r["subject"]), r["file"]) for r in doc["references"] + doc["subjects"]]
    drift = []

    for subject, rel in sorted(declared):
        try:
            latest = call(url, "/subjects/%s/versions/latest" % q(subject))
        except Unreachable:
            raise
        except RuntimeError:
            drift.append("%s: declared but not registered — run `make schemas-apply`" % subject)
            continue
        found = lookup(url, subject, source_of(rel), reference_list(url, files, rel, prefix))
        if found is None:
            drift.append("%s: %s is not registered under this subject at any version — the "
                         "declaration and the registry disagree" % (subject, rel))
        elif found.get("version") != latest.get("version"):
            drift.append("%s: %s is registered as version %s but the latest is %s — something "
                         "else was registered after it" % (subject, rel, found.get("version"),
                                                           latest.get("version")))

    names = {s for s, _ in declared}
    prefixes = {e["prefix"] for e in topics.parse_environments(TOPICS).values() if e["prefix"]}
    for subject in call(url, "/subjects"):
        head = subject.split(".", 1)[0]
        if head not in prefixes:
            # On the shared registry every subject belongs to an environment (AC-2).
            drift.append("%s: carries no environment prefix (%s); on a long-lived compose stack these are "
                         "from before AW-INF-057, cleared by `make down VOLUMES=1`" % (subject, ", ".join(sorted(prefixes))))
        elif head == prefix and subject not in names:
            drift.append("%s: registered but not declared in deploy/kafka/schemas.yaml" % subject)

    cfg = call(url, "/config").get("compatibilityLevel")
    if cfg != doc["compatibility"]:
        drift.append("global compatibility is %s, declared %s" % (cfg, doc["compatibility"]))

    if drift:
        for d in drift:
            sys.stderr.write("make: schemas-diff: %s\n" % d)
        die("%d subject(s) drifted from the declaration" % len(drift))
    print("schemas-diff: %d subject(s) match the registry (env=%s, prefix=%s)" % (len(declared), env, prefix or "(none)"))


# --------------------------------------------------------------------------- lookup

def subjects_for(rel):
    """Which subjects carry a .proto file. Used by proto-check so a breaking change names
    the subject it breaks, not only the file."""
    doc = parse_schema_declaration(DECL)
    # buf reports paths relative to the working directory; the declaration uses paths
    # relative to the module root. Normalise either form.
    rel = os.path.relpath(os.path.abspath(rel), PROTO_DIR)
    hits = [s["subject"] for s in doc["subjects"] if s["file"] == rel]
    for h in hits:
        print(h)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("action", choices=["check", "apply", "diff", "subjects-for"])
    ap.add_argument("file", nargs="?")
    ap.add_argument("--env", default="")
    ap.add_argument("--broker", default="", help="local only: compose or cluster (default %s)"
                    % topics.default_broker())
    args = ap.parse_args()

    if args.action in ("apply", "diff"):
        if not args.env:
            die("ENV is required: ENV=<local|dev>", 2)
        if args.env not in ENVS:
            die("ENV=%s: not one of %s (prod's registry is not on this cluster)" % (args.env, ", ".join(ENVS)), 2)
        if args.broker and args.broker not in topics.BROKERS:
            die("--broker is compose or cluster, not '%s'" % args.broker, 2)
        if args.broker and args.env != "local":
            die("--broker means something only for local; %s is always the cluster's" % args.env, 2)
        broker = args.broker or (topics.default_broker() if args.env == "local" else "cluster")
        url, proc = registry_url(args.env, broker)
        try:
            (apply_action if args.action == "apply" else diff_action)(args.env, url)
        except Unreachable as e:
            die(str(e))
        finally:
            if proc is not None:
                proc.terminate()
    elif args.action == "check":
        check()
    else:
        if not args.file:
            die("subjects-for needs a .proto path")
        subjects_for(args.file)


if __name__ == "__main__":
    main()
