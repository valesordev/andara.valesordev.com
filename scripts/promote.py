#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""make promote: move `dev` to a chosen build, or back to an earlier promotion (AW-INF-059).

  promote.py dev <sha> "" ""             make promote ENV=dev SHA=<main sha>
  promote.py dev "" dev-<n> [<round>]    make promote ENV=dev TAG=dev-<n> [PIN_ROUND=<round>]

ADR-0013 decision 7. Argo CD's andara-dev Application follows the Git tag `dev`, so what runs is
what the tag points at, and every change to it is a decision with a name in Git:

  SHA   image sha-<12> exists and pulls anonymously (image_check.sh) -> release/dev := main@SHA plus
        exactly one signed commit, whose only change is image.tag: sha-<12>@sha256:<digest> in
        deploy/helm/values/dev.yaml -> annotated signed tags `dev` (moved) and `dev-<n>` (new)
  TAG   a new signed commit on top of dev-<n>'s commit, setting release.rolled_back_to: dev-<n> and,
        with PIN_ROUND, server.recovery.pin_round (0, cleared, without it) -> the same two tags

then one atomic push of the branch and both tags, Argo CD's hard refresh, and a wait until
`make argocd-status`'s three answers agree: the commit `dev` points at, Argo's synced revision,
and the digest andara-0 runs. The tree is built with plumbing in a private index, so the operator's
checkout, index and branches are never touched.

The tag message of dev-<n> is the event record (UTC time, main SHA, digest, who promoted, and for a
rollback rolled_back_to and pin_round): no annotation is written to Grafana (ADR-0012 decision 4).

Brian's only. A role session's push hook sees `git push` typed in a session, not one inside a make
recipe, so this refuses when CLAUDECODE is set. That is a convention, not a control.

Progress lines are `promote: <step> run=<id>`. Exit: 0 ok · 1 an image, push, sync or assertion
failure · 2 usage · 3 a signing key or Git credential missing.
"""
import copy
import datetime
import os
import re
import subprocess
import sys
import tempfile
import time
import uuid

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
VALUES_PATH = "deploy/helm/values/dev.yaml"
BRANCH = "release/dev"
TAG = "dev"
HISTORY = re.compile(r"^dev-([1-9][0-9]*)$")
REMOTE = "origin"
REGISTRY_IMAGE = "ghcr.io/valesordev/andara-server"
# AC-6: a force-moved tag is seen within this, not Argo CD's roughly three-minute ref cache.
REFRESH_DEADLINE = 60
AGREE_DEADLINE = 600
POST_SYNC = "post-sync"
PIN = ["server", "recovery", "pin_round"]

RUN_ID = ""


class Fail(Exception):
    def __init__(self, msg, code=1):
        super().__init__(msg)
        self.code = code


def say(step, detail=""):
    print("promote: %s%s run=%s" % (step, (" " + detail) if detail else "", RUN_ID), flush=True)


def git(*args, check=True, input=None, env=None, raw=False):
    p = subprocess.run(["git", "-C", REPO, *args], capture_output=True, text=True, input=input,
                       env=dict(os.environ, **(env or {})))
    if check and p.returncode != 0:
        raise Fail("git %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p.stdout if raw else p.stdout.strip()


# --- dev.yaml, edited as text --------------------------------------------------------------

def _block_end(lines, start, indent):
    """The index after the last line of the block that opens at lines[start] (a `key:` line at
    `indent`): every following line that is blank, a comment, or indented deeper."""
    end = start + 1
    last = start + 1
    while end < len(lines):
        s = lines[end]
        if s.strip() and not s.lstrip().startswith("#"):
            if len(s) - len(s.lstrip()) <= indent:
                break
            last = end + 1
        end += 1
    return last


def _find_key(lines, lo, hi, indent, key):
    pat = re.compile(r"^%s%s:(\s|$)" % (" " * indent, re.escape(key)))
    for i in range(lo, hi):
        if pat.match(lines[i]):
            return i
    return None


def _scalar(v):
    if isinstance(v, int):
        return str(v)
    return yaml.safe_dump(v, default_style=None).strip().removesuffix("\n...").strip()


def set_value(text, path, value):
    """text with the block-mapping value at path (a list of keys) set to the scalar value,
    changing only the line that carries it. A missing key goes at the end of its parent's block,
    indented two spaces per level. The result is checked by parsing it."""
    lines = text.split("\n")
    lo, hi, indent = 0, len(lines), 0
    for depth, key in enumerate(path):
        i = _find_key(lines, lo, hi, indent, key)
        if i is None:
            new = ["%s%s:" % (" " * (indent + 2 * d), k) for d, k in enumerate(path[depth:-1])]
            new.append("%s%s: %s" % (" " * (indent + 2 * (len(path) - 1 - depth)), path[-1], _scalar(value)))
            at = hi
            if depth == 0:
                new = [""] + new
            while at > lo and not lines[at - 1].strip():
                at -= 1
            lines[at:at] = new
            break
        if depth == len(path) - 1:
            comment = re.search(r"\s+#.*$", lines[i])
            lines[i] = "%s%s: %s%s" % (" " * indent, key, _scalar(value), comment.group(0) if comment else "")
            break
        lo, hi = i + 1, _block_end(lines, i, indent)
        indent += 2
    out = "\n".join(lines)
    want = copy.deepcopy(yaml.safe_load(text) or {})
    node = want
    for key in path[:-1]:
        node = node.setdefault(key, {})
    node[path[-1]] = value
    if (yaml.safe_load(out) or {}) != want:
        raise Fail("editing %s at %s changed more than that value" % (VALUES_PATH, ".".join(path)))
    return out


def get_value(text, path):
    node = yaml.safe_load(text) or {}
    for key in path:
        node = node.get(key) if isinstance(node, dict) else None
    return node


# --- the commit and the tags ----------------------------------------------------------------

def commit_with(base, text, message):
    """A signed commit whose parent is base and whose tree is base's with dev.yaml replaced: built
    in a private index, so nothing in the operator's checkout moves."""
    blob = git("hash-object", "-w", "--stdin", input=text)
    with tempfile.TemporaryDirectory() as d:
        env = {"GIT_INDEX_FILE": os.path.join(d, "index")}
        git("read-tree", base, env=env)
        git("update-index", "--cacheinfo", "100644,%s,%s" % (blob, VALUES_PATH), env=env)
        tree = git("write-tree", env=env)
    return git("commit-tree", "-S", "-p", base, "-m", message, tree)


def changed_paths(commit):
    return git("diff-tree", "--no-commit-id", "--name-only", "-r", commit + "^", commit).split("\n")


def remote_refs():
    """{ref: sha} for release/dev and the dev tags on origin. A missing credential is exit 3."""
    p = subprocess.run(["git", "-C", REPO, "ls-remote", REMOTE, "refs/heads/" + BRANCH, "refs/tags/dev",
                        "refs/tags/dev-*"], capture_output=True, text=True)
    if p.returncode:
        raise Fail("cannot read %s (a Git credential?): %s" % (REMOTE, p.stderr.strip()), 3)
    refs = {}
    for line in p.stdout.splitlines():
        sha, ref = line.split("\t", 1)
        refs[ref] = sha
    return refs


def next_number(refs):
    ns = [int(m.group(1)) for r in refs
          for m in [HISTORY.match(r.removeprefix("refs/tags/").removesuffix("^{}"))] if m]
    return max(ns, default=0) + 1


def image_digest(sha12):
    tag = "sha-" + sha12
    p = subprocess.run([os.path.join(HERE, "image_check.sh"), "", REGISTRY_IMAGE, tag, "1"],
                       capture_output=True, text=True)
    if p.returncode:
        raise Fail("image %s:%s does not pull anonymously: %s" % (REGISTRY_IMAGE, tag, (p.stderr or p.stdout).strip()))
    p = subprocess.run([os.path.join(HERE, "image_digest.sh"), REGISTRY_IMAGE, tag], capture_output=True, text=True)
    if p.returncode or not p.stdout.strip().startswith("sha256:"):
        raise Fail("no digest for %s:%s: %s" % (REGISTRY_IMAGE, tag, p.stderr.strip()))
    return p.stdout.strip()


def promoter():
    return "%s <%s>" % (git("config", "user.name", check=False) or "?", git("config", "user.email", check=False) or "?")


def record(n, main_sha, image, rolled_back_to=None, pin_round=None):
    """The annotated message of dev-<n>: the promotion's event record."""
    lines = ["promotion dev-%d" % n, "",
             "time:    %s" % datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
             "main:    %s" % main_sha,
             "image:   %s" % image,
             "by:      %s" % promoter(),
             "run:     %s" % RUN_ID]
    if rolled_back_to:
        lines.append("rolled_back_to: %s" % rolled_back_to)
        lines.append("pin_round: %s" % (pin_round or "none"))
    return "\n".join(lines) + "\n"


def values_at(commit):
    return git("show", "%s:%s" % (commit, VALUES_PATH), raw=True)


def build_promotion(sha, refs):
    """SHA mode: (commit, main sha, image, n, message)."""
    git("fetch", "-q", REMOTE, "main")
    full = git("rev-parse", "--verify", "--quiet", sha + "^{commit}", check=False)
    if not full:
        raise Fail("%s is not a commit; `git fetch` and name a main sha" % sha)
    if subprocess.run(["git", "-C", REPO, "merge-base", "--is-ancestor", full, "%s/main" % REMOTE]).returncode:
        raise Fail("%s is not on %s/main; a promotion names a main commit" % (full[:12], REMOTE))
    sha12 = full[:12]
    say("image-check", "%s:sha-%s" % (REGISTRY_IMAGE, sha12))
    digest = image_digest(sha12)
    image = "sha-%s@%s" % (sha12, digest)
    say("image", "%s:%s" % (REGISTRY_IMAGE, image))
    text = set_value(values_at(full), ["image", "tag"], image)
    n = next_number(refs)
    commit = commit_with(full, text, "promote: dev-%d, image %s\n\nmain %s\nrun %s\n" % (n, image, full, RUN_ID))
    return commit, full, image, n, record(n, full, image)


def build_rollback(tag, pin_round, refs):
    """TAG mode: a new commit on top of the history tag's commit."""
    if not HISTORY.match(tag):
        raise Fail("TAG=%s: a rollback names a history tag dev-<n>" % tag, 2)
    if "refs/tags/" + tag not in refs:
        raise Fail("no tag %s on %s" % (tag, REMOTE))
    git("fetch", "-q", "--no-tags", REMOTE, "+refs/tags/%s:refs/tags/%s" % (tag, tag))
    base = git("rev-parse", tag + "^{commit}")
    text = values_at(base)
    image = ((yaml.safe_load(text) or {}).get("image") or {}).get("tag", "")
    text = set_value(text, ["release", "rolled_back_to"], tag)
    # The lifecycle spec's pin: set by a rollback with a round, cleared (0 is unset) by one without.
    if pin_round:
        text = set_value(text, PIN, int(pin_round))
    elif get_value(text, PIN):
        text = set_value(text, PIN, 0)
    # The main commit the history tag's promotion was cut from, against main as origin has it now.
    git("fetch", "-q", REMOTE, "main")
    main_sha = git("merge-base", base, "%s/main" % REMOTE, check=False) or "?"
    n = next_number(refs)
    msg = "promote: dev-%d, rollback to %s%s\n\nrun %s\n" % (n, tag, ", pin_round %s" % pin_round if pin_round else "", RUN_ID)
    commit = commit_with(base, text, msg)
    return commit, main_sha, image, n, record(n, main_sha, image, tag, pin_round)


def make_tags(commit, n, message):
    git("tag", "-s", "-a", "-f", TAG, "-m", message, commit)
    git("tag", "-s", "-a", "dev-%d" % n, "-m", message, commit)
    for t in (TAG, "dev-%d" % n):
        p = subprocess.run(["git", "-C", REPO, "tag", "-v", t], capture_output=True, text=True)
        if p.returncode:
            raise Fail("`git tag -v %s` does not verify against .github/allowed_signers: %s" % (t, p.stderr.strip()))


def undo_tags(n, before_dev):
    """Put the local tags back as they were before this run: dev-<n> gone, dev at the exact tag
    object it named (or gone). update-ref, never `git tag`, which under tag.gpgsign=true (as
    bootstrap.sh sets it) would make a new signed tag rather than restore the saved one.
    Returns "" when restored, else a note for the caller's message, which stays the real failure."""
    git("update-ref", "-d", "refs/tags/dev-%d" % n, check=False)
    if before_dev:
        git("update-ref", "refs/tags/" + TAG, before_dev, check=False)
    else:
        git("update-ref", "-d", "refs/tags/" + TAG, check=False)
    now = git("rev-parse", "--verify", "--quiet", "refs/tags/" + TAG, check=False)
    if now != before_dev or git("rev-parse", "--verify", "--quiet", "refs/tags/dev-%d" % n, check=False):
        return " (local tags NOT restored: dev is %s, want %s; fix with `git fetch --force origin "
        "'refs/tags/dev*:refs/tags/dev*'`)" % (now[:12] or "absent", before_dev[:12] or "absent")
    return ""


def push(commit, n, before_dev):
    """One atomic push, so the branch and both tags move together or not at all. On failure the
    local tags this run made are undone and a local release/dev, if any, is reset to origin's."""
    refspecs = ["+%s:refs/heads/%s" % (commit, BRANCH), "+refs/tags/%s:refs/tags/%s" % (TAG, TAG),
                "refs/tags/dev-%d:refs/tags/dev-%d" % (n, n)]
    p = subprocess.run(["git", "-C", REPO, "push", "--atomic", REMOTE, *refspecs], capture_output=True, text=True)
    if p.returncode == 0:
        return time.monotonic()
    note = undo_tags(n, before_dev)
    if git("rev-parse", "--verify", "--quiet", "refs/heads/" + BRANCH, check=False):
        remote = git("rev-parse", "--verify", "--quiet", "refs/remotes/%s/%s" % (REMOTE, BRANCH), check=False)
        if remote:
            git("update-ref", "refs/heads/" + BRANCH, remote, check=False)
    raise Fail("push refused, nothing promoted%s: %s"
               % (note or " (local dev, dev-%d and %s restored)" % (n, BRANCH), (p.stderr or p.stdout).strip()))


# --- Argo CD --------------------------------------------------------------------------------

def refresh_and_wait(commit, pushed_at):
    """argocd.py raises RuntimeError on a failed call; here that is a promote failure (exit 1)."""
    try:
        return _refresh_and_wait(commit, pushed_at)
    except RuntimeError as e:
        raise Fail("pushed %s, then Argo CD: %s" % (commit[:12], e))


def _refresh_and_wait(commit, pushed_at):
    import argocd  # noqa: E402  (scripts/, beside this file)
    import kube  # noqa: E402
    name = "andara-dev"
    if argocd.kubectl_json("-n", argocd.NS, "get", "application", name) is None:
        raise Fail("pushed, but there is no application %s; `make argocd-install ENV=dev` applies it, "
                   "then `make argocd-status`" % name)
    argocd.run(kube.kubectl("-n", argocd.NS, "annotate", "application", name,
                            "argocd.argoproj.io/refresh=hard", "--overwrite"))
    say("refresh", "hard refresh requested on application %s" % name)
    say("sync", "Argo CD syncs by itself; to start one now: kubectl --context %s -n %s patch application %s "
        "--type merge -p '{\"operation\":{\"sync\":{\"revision\":\"%s\"}}}'" % (kube.context(), argocd.NS, name, commit))
    seen = None
    end = time.monotonic() + AGREE_DEADLINE
    last = None
    while time.monotonic() < end:
        sync, health, rev, _ = argocd.app_state(name)
        if seen is None and rev == commit:
            seen = time.monotonic() - pushed_at
            say("refreshed", "Argo CD resolved dev to %s %.0fs after the push%s"
                % (commit[:12], seen, "" if seen <= REFRESH_DEADLINE else " (over %ds, AC-6)" % REFRESH_DEADLINE))
        a = argocd.agreement("andara-dev")
        state = (sync, health, tuple(a["mismatch"]))
        if state != last:
            say("waiting", "%s / %s%s" % (sync, health, "; " + "; ".join(a["mismatch"]) if a["mismatch"] else ""))
            last = state
        if sync == "Synced" and not a["mismatch"] and a["tag"] == commit and (
                health == "Healthy" or argocd.content_waiting("andara-dev")):
            return seen, a
        time.sleep(5)
    sync, health, _, app = argocd.app_state(name)
    conds = "; ".join(c.get("message", "") for c in (app.get("status") or {}).get("conditions") or [])
    raise Fail("application %s is %s / %s and its answers disagree after %ds%s; see make argocd-status"
               % (name, sync, health, AGREE_DEADLINE, (": " + conds) if conds else ""))


def post_sync():
    # The named hook AW-INF-041 fills with scripts/deploy.sh's steps (the andara.core step, the
    # measured interruption against the RTO). Until then it runs nothing.
    say("%s skipped (AW-INF-041)" % POST_SYNC)


# --- main -----------------------------------------------------------------------------------

def preflight():
    if os.environ.get("CLAUDECODE"):
        raise Fail("CLAUDECODE is set: a promotion is Brian's, run from his own shell, never a Claude Code session")
    if not git("config", "user.signingkey", check=False):
        raise Fail("no user.signingkey: the promotion commit and tags must be signed; run `make bootstrap`", 3)


def usage(msg):
    raise Fail("%s\nusage: make promote ENV=dev SHA=<main sha> | make promote ENV=dev TAG=dev-<n> [PIN_ROUND=<round>]" % msg, 2)


def promote(env, sha, tag, pin_round):
    if env == "prod":
        usage("ENV=prod: prod is `make deploy` (ADR-0013 decision 10)")
    if env != "dev":
        usage("ENV=%s: only dev is promoted" % (env or "<unset>"))
    if bool(sha) == bool(tag):
        usage("give exactly one of SHA and TAG")
    if pin_round and not tag:
        usage("PIN_ROUND goes with a rollback (TAG=dev-<n>)")
    if pin_round and not re.fullmatch(r"[1-9][0-9]*", pin_round):
        usage("PIN_ROUND=%s is not a round number" % pin_round)
    preflight()
    refs = remote_refs()
    before_dev = git("rev-parse", "--verify", "--quiet", "refs/tags/" + TAG, check=False)
    say("start", "ENV=dev %s" % ("SHA=" + sha if sha else "TAG=%s%s" % (tag, " PIN_ROUND=" + pin_round if pin_round else "")))
    if sha:
        commit, main_sha, image, n, message = build_promotion(sha, refs)
    else:
        commit, main_sha, image, n, message = build_rollback(tag, pin_round, refs)
    paths = changed_paths(commit)
    if paths != [VALUES_PATH]:
        raise Fail("the promotion commit changes %s, not only %s" % (paths, VALUES_PATH))
    say("commit", "%s on %s (%s)" % (commit[:12], commit + "^", VALUES_PATH))
    try:
        make_tags(commit, n, message)
    except Fail as e:
        raise Fail(str(e) + undo_tags(n, before_dev), e.code)
    say("tags", "dev and dev-%d at %s, signed" % (n, commit[:12]))
    pushed_at = push(commit, n, before_dev)
    say("push", "%s, dev, dev-%d" % (BRANCH, n))
    seen, a = refresh_and_wait(commit, pushed_at)
    say("agree", "tag %s = argo %s, running %s" % (a["tag"][:12], a["argo"][:12], a["running"]))
    post_sync()
    say("done", "commit=%s tag=dev-%d digest=%s refresh=%s interruption=unmeasured (AW-INF-041)"
        % (commit[:12], n, image.split("@", 1)[-1], "%.0fs" % seen if seen is not None else "?"))


def log(count=10):
    """make promote-log: the newest promotions' event records, from origin's dev-<n> tags."""
    git("fetch", "-q", "--no-tags", REMOTE, "+refs/tags/dev-*:refs/tags/dev-*")
    names = sorted((t for t in git("tag", "-l", "dev-*").split() if HISTORY.match(t)),
                   key=lambda t: int(t[4:]), reverse=True)[:count]
    if not names:
        print("promote-log: no dev-<n> tags on %s; nothing has been promoted" % REMOTE)
    for t in names:
        # subject and body, not %(contents), which carries the tag's signature block.
        print(git("tag", "-l", "--format=%(refname:short)  -> %(*objectname:short)%0a%(contents:subject)%0a%0a"
                  "%(contents:body)", t, raw=True))


def main():
    global RUN_ID
    RUN_ID = uuid.uuid4().hex[:8]
    if sys.argv[1:2] == ["log"]:
        try:
            log()
        except Fail as e:
            print("make: promote-log: %s" % e, file=sys.stderr)
            sys.exit(e.code)
        return
    args = (sys.argv[1:] + ["", "", "", ""])[:4]
    try:
        promote(*args)
    except Fail as e:
        print("make: promote: %s run=%s" % (e, RUN_ID), file=sys.stderr)
        sys.exit(e.code)


if __name__ == "__main__":
    main()
