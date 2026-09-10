#!/usr/bin/env python3
"""Register and verify protobuf subjects against the schema registry (AW-INF-004).

    scripts/schemas.py check                 # offline; runs inside `make check`
    scripts/schemas.py apply [--env local]   # needs a live registry
    scripts/schemas.py diff  [--env local]   # needs a live registry
    scripts/schemas.py subjects-for <file>   # which subjects carry a .proto file

WHICH TARGET ENFORCES WHAT — the division here is deliberate and measured, not stylistic:

  * ADR-0007's additive-only rule is enforced by `buf breaking` in `make proto-check`.
    It is NOT re-checked here. Redpanda's registry was measured on 2026-09-10 and accepts
    both a removed field and a RENUMBERED field as BACKWARD-compatible. Renumbering would
    make replay misread every historical record (ADR-0002), so the registry cannot be the
    detector for it and buf is.
  * `check` verifies the declaration itself — offline, no broker, safe inside `make check`.
  * `diff` is the question git cannot answer: does the registry hold what we declared.

Stdlib only. This validates the stack, so it must not need anything the stack installs.
"""

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from topics import parse_declaration as parse_topics  # noqa: E402

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DECL = os.path.join(REPO, "deploy", "kafka", "schemas.yaml")
TOPICS = os.path.join(REPO, "deploy", "kafka", "topics.yaml")
PROTO_DIR = os.path.join(REPO, "docs", "specs", "protocol")
STORIES = os.path.join(REPO, "docs", "stories")

ACCEPT = "application/vnd.schemaregistry.v1+json"
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


def source_of(rel):
    with open(os.path.join(PROTO_DIR, rel), "r", encoding="utf-8") as fh:
        return fh.read()


def registry_url(env):
    if os.environ.get("ANDARA_SCHEMA_REGISTRY_URL"):
        return os.environ["ANDARA_SCHEMA_REGISTRY_URL"].rstrip("/")
    port = os.environ.get("ANDARA_SCHEMA_REGISTRY_PORT", "8081")
    return "http://localhost:%s" % port


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
    except urllib.error.URLError as e:
        die("no schema registry at %s (%s). `make up` starts one; override with "
            "ANDARA_SCHEMA_REGISTRY_URL." % (url, e.reason))


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
        if r["file"] not in files:
            errors.append("reference '%s': file is not in the protobuf module" % r["file"])
        if r["subject"] != r["file"]:
            errors.append("reference '%s': the subject must equal the import path ('%s'), "
                          "because that is how the registry resolves it" % (r["subject"], r["file"]))

    pending_topics = {}
    for p in doc["pending"]:
        if not p.get("topic") or not p.get("story"):
            errors.append("pending entry needs both 'topic' and 'story': %r" % p)
            continue
        pending_topics[p["topic"]] = p["story"]
        hits = [n for n in os.listdir(STORIES) if n.startswith(p["story"] + "-")]
        if not hits:
            errors.append("pending topic '%s' names story %s, which does not exist in docs/stories/"
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

def register(url, subject, source, refs, second_opinion=True):
    """Register one subject. Returns (id, created). Idempotent: an identical schema
    returns its existing id and adds no version."""
    payload = {"schemaType": "PROTOBUF", "schema": source}
    if refs:
        payload["references"] = refs
    existing = None
    try:
        existing = call(url, "/subjects/%s/versions/latest" % q(subject))
    except RuntimeError:
        pass  # no such subject yet

    if existing:
        # A second opinion, never the primary one — see the module docstring.
        verdict = call(url, "/compatibility/subjects/%s/versions/latest" % q(subject),
                       "POST", payload, "?verbose=true")
        if second_opinion and verdict.get("is_compatible") is False:
            for m in verdict.get("messages", []):
                sys.stderr.write("make: schemas-apply: %s: %s\n" % (subject, m))
            die("registry rejected '%s' as backward-incompatible" % subject)

    out = call(url, "/subjects/%s/versions" % q(subject), "POST", payload)
    created = not existing or existing.get("id") != out.get("id")
    return out.get("id"), created


def reference_list(url, files, rel):
    """Reference entries for a file's imports, with concrete versions.

    Not `version: -1`. Confluent reads -1 as "latest"; Redpanda rejects it — and rejects it
    as `422 parse error at offset 2772`, an offset past the end of the schema, which sends
    you looking at the .proto rather than at the reference. Measured 2026-09-10.
    """
    refs = []
    for d in files.get(rel, {}).get("deps", []):
        latest = call(url, "/subjects/%s/versions/latest" % q(d))
        refs.append({"name": d, "subject": d, "version": latest["version"]})
    return refs


def apply_action(env):
    doc = parse_schema_declaration(DECL)
    files = descriptor()
    url = registry_url(env)

    call(url, "/config", "PUT", {"compatibility": doc["compatibility"]})
    print("schemas-apply: global compatibility set to %s" % doc["compatibility"])

    made = 0
    # References first: the registry resolves a reference by subject name, so the imported
    # file has to already be there when the importer is registered.
    for r in doc["references"] + doc["subjects"]:
        subject, rel = r["subject"], r["file"]
        sid, created = register(url, subject, source_of(rel), reference_list(url, files, rel))
        call(url, "/config/%s" % q(subject), "PUT", {"compatibility": doc["compatibility"]})
        made += 1 if created else 0
        print("  %-46s id=%-4s %s" % (subject, sid, "registered" if created else "unchanged"))
    print("schemas-apply: %d subject(s), %d newly registered (env=%s)"
          % (len(doc["references"]) + len(doc["subjects"]), made, env))


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


def diff_action(env):
    doc = parse_schema_declaration(DECL)
    files = descriptor()
    url = registry_url(env)
    declared = [(r["subject"], r["file"]) for r in doc["references"] + doc["subjects"]]
    drift = []

    for subject, rel in sorted(declared):
        try:
            latest = call(url, "/subjects/%s/versions/latest" % q(subject))
        except RuntimeError:
            drift.append("%s: declared but not registered — run `make schemas-apply`" % subject)
            continue
        found = lookup(url, subject, source_of(rel), reference_list(url, files, rel))
        if found is None:
            drift.append("%s: %s is not registered under this subject at any version — the "
                         "declaration and the registry disagree" % (subject, rel))
        elif found.get("version") != latest.get("version"):
            drift.append("%s: %s is registered as version %s but the latest is %s — something "
                         "else was registered after it" % (subject, rel, found.get("version"),
                                                           latest.get("version")))

    names = {s for s, _ in declared}
    for subject in call(url, "/subjects"):
        if subject not in names:
            drift.append("%s: registered but not declared in deploy/kafka/schemas.yaml" % subject)

    cfg = call(url, "/config").get("compatibilityLevel")
    if cfg != doc["compatibility"]:
        drift.append("global compatibility is %s, declared %s" % (cfg, doc["compatibility"]))

    if drift:
        for d in drift:
            sys.stderr.write("make: schemas-diff: %s\n" % d)
        die("%d subject(s) drifted from the declaration" % len(drift))
    print("schemas-diff: %d subject(s) match the registry (env=%s)" % (len(declared), env))


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
    ap.add_argument("--env", default=os.environ.get("ANDARA_ENV", "local"))
    args = ap.parse_args()

    if args.action == "check":
        check()
    elif args.action == "apply":
        apply_action(args.env)
    elif args.action == "diff":
        diff_action(args.env)
    else:
        if not args.file:
            die("subjects-for needs a .proto path")
        subjects_for(args.file)


if __name__ == "__main__":
    main()
