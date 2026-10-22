# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-059's unit half: scripts/promote.py's commits and tags over a temporary repository and a
bare `origin`, signed with a throwaway SSH key. The registry and Argo CD are stubbed: the image
check and digest, and the refresh-and-wait, are the box's half (§8 record)."""
import importlib.util
import io
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPTS = os.path.dirname(HERE)
REPO = os.path.dirname(SCRIPTS)
sys.path.insert(0, SCRIPTS)
DEV_YAML = os.path.join(REPO, "deploy", "helm", "values", "dev.yaml")
DIGEST = "sha256:" + "ab" * 32


def load():
    spec = importlib.util.spec_from_file_location("promote", os.path.join(SCRIPTS, "promote.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def sh(cwd, *args, check=True):
    p = subprocess.run(list(args), cwd=cwd, capture_output=True, text=True)
    if check and p.returncode:
        raise AssertionError("%s: %s" % (" ".join(args), p.stderr))
    return p.stdout.strip()


@unittest.skipUnless(shutil.which("ssh-keygen") and shutil.which("git"), "needs git and ssh-keygen")
class Promote(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)
        self.origin = os.path.join(self.tmp, "origin.git")
        self.work = os.path.join(self.tmp, "work")
        key = os.path.join(self.tmp, "key")
        sh(self.tmp, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "promote-test", "-f", key)
        signers = os.path.join(self.tmp, "allowed_signers")
        with open(key + ".pub") as f, open(signers, "w") as out:
            out.write("brian@example.invalid namespaces=\"git\" %s\n" % " ".join(f.read().split()[:2]))
        sh(self.tmp, "git", "init", "-q", "--bare", "-b", "main", self.origin)
        sh(self.tmp, "git", "init", "-q", "-b", "main", self.work)
        for k, v in (("user.name", "Brian"), ("user.email", "brian@example.invalid"), ("gpg.format", "ssh"),
                     ("user.signingkey", key + ".pub"), ("gpg.ssh.allowedSignersFile", signers),
                     ("commit.gpgsign", "true"), ("tag.gpgsign", "false")):
            sh(self.work, "git", "config", k, v)
        sh(self.work, "git", "remote", "add", "origin", self.origin)
        os.makedirs(os.path.join(self.work, "deploy", "helm", "values"))
        shutil.copy(DEV_YAML, os.path.join(self.work, "deploy", "helm", "values", "dev.yaml"))
        with open(os.path.join(self.work, "README"), "w") as f:
            f.write("x\n")
        sh(self.work, "git", "add", ".")
        sh(self.work, "git", "commit", "-q", "-m", "one")
        self.main1 = sh(self.work, "git", "rev-parse", "HEAD")
        with open(os.path.join(self.work, "README"), "a") as f:
            f.write("y\n")
        sh(self.work, "git", "commit", "-q", "-am", "two")
        self.main2 = sh(self.work, "git", "rev-parse", "HEAD")
        sh(self.work, "git", "push", "-q", "origin", "main")
        self.env = mock.patch.dict(os.environ, {k: v for k, v in os.environ.items() if k != "CLAUDECODE"}, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)
        self.digest_calls = []
        self.waited = []

    def mod(self, digest_fails=False):
        mod = load()
        mod.REPO = self.work

        def image_digest(sha12):
            self.digest_calls.append(sha12)
            if digest_fails:
                raise mod.Fail("image ghcr.io/valesordev/andara-server:sha-%s does not pull anonymously" % sha12)
            return DIGEST

        def refresh_and_wait(commit, pushed_at):
            self.waited.append(commit)
            return 2.0, {"tag": commit, "argo": commit, "running": DIGEST, "mismatch": []}
        mod.image_digest = image_digest
        mod.real_refresh_and_wait = mod.refresh_and_wait
        mod.refresh_and_wait = refresh_and_wait
        return mod

    def run_promote(self, *args, mod=None):
        mod = mod or self.mod()
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(sys, "argv", ["promote.py", *args]), redirect_stdout(out), redirect_stderr(err):
            try:
                mod.main()
                code = 0
            except SystemExit as e:
                code = e.code
        return code, out.getvalue(), err.getvalue()

    def remote(self, ref):
        return sh(self.origin, "git", "rev-parse", "--verify", "--quiet", ref, check=False)

    def values_at(self, commit):
        return yaml.safe_load(sh(self.origin, "git", "show", "%s:deploy/helm/values/dev.yaml" % commit))

    # --- AC-1 -----------------------------------------------------------------------------

    def test_a_promotion_is_one_signed_commit_on_main_at_sha_and_two_tags(self):
        code, out, err = self.run_promote("dev", self.main1, "", "")
        self.assertEqual(code, 0, err)
        branch = self.remote("refs/heads/release/dev")
        self.assertEqual(sh(self.origin, "git", "rev-parse", branch + "^"), self.main1)
        self.assertEqual(sh(self.origin, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", branch + "^", branch),
                         "deploy/helm/values/dev.yaml")
        # Only image.tag: one line out, one line in, and nothing else in the parsed values.
        numstat = sh(self.origin, "git", "diff", "--numstat", branch + "^", branch)
        self.assertTrue(numstat.startswith("1\t1\t"), numstat)
        before, after = self.values_at(self.main1), self.values_at(branch)
        self.assertEqual(after["image"]["tag"], "sha-%s@%s" % (self.main1[:12], DIGEST))
        after["image"]["tag"] = before["image"]["tag"]
        self.assertEqual(after, before)
        self.assertEqual(self.remote("refs/tags/dev^{commit}"), branch)
        self.assertEqual(self.remote("refs/tags/dev-1^{commit}"), branch)
        self.assertEqual(self.remote("refs/tags/dev^{tag}") != "", True)  # annotated
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        for t in ("dev", "dev-1"):
            sh(self.work, "git", "tag", "-v", t)  # AC-8, against allowed_signers
        sh(self.work, "git", "verify-commit", branch)
        self.assertEqual(self.digest_calls, [self.main1[:12]])
        self.assertEqual(self.waited, [branch])

    def test_the_tag_message_is_the_event_record(self):
        self.run_promote("dev", self.main1, "", "")
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        msg = sh(self.work, "git", "tag", "-l", "--format=%(contents)", "dev-1")
        for want in ("promotion dev-1", "main:    " + self.main1, "image:   sha-%s@%s" % (self.main1[:12], DIGEST),
                     "by:      Brian <brian@example.invalid>"):
            self.assertIn(want, msg)
        self.assertRegex(msg, r"time:    \d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ")

    def test_a_second_promotion_resets_the_branch_to_main_and_numbers_on(self):
        self.run_promote("dev", self.main1, "", "")
        code, _, err = self.run_promote("dev", self.main2, "", "")
        self.assertEqual(code, 0, err)
        branch = self.remote("refs/heads/release/dev")
        self.assertEqual(sh(self.origin, "git", "rev-parse", branch + "^"), self.main2)
        self.assertEqual(self.remote("refs/tags/dev-2^{commit}"), branch)
        self.assertEqual(self.remote("refs/tags/dev^{commit}"), branch)
        self.assertNotEqual(self.remote("refs/tags/dev-1^{commit}"), branch)

    def test_the_progress_lines_carry_the_run_id_and_the_hook(self):
        code, out, _ = self.run_promote("dev", self.main1, "", "")
        lines = out.strip().splitlines()
        run = re.search(r"run=([0-9a-f]{8})$", lines[0]).group(1)
        self.assertTrue(all(l.startswith("promote: ") and l.endswith("run=" + run) for l in lines), lines)
        self.assertIn("promote: post-sync skipped (AW-INF-041) run=" + run, lines)  # AC-5
        self.assertRegex(lines[-1], r"^promote: done commit=[0-9a-f]{12} tag=dev-1 digest=%s " % DIGEST)

    def test_promote_log_lists_the_records_newest_first(self):
        code, out, _ = self.run_promote("log")
        self.assertEqual(code, 0)
        self.assertIn("nothing has been promoted", out)
        self.run_promote("dev", self.main1, "", "")
        self.run_promote("dev", self.main2, "", "")
        sh(self.work, "git", "tag", "-d", "dev-1", "dev-2")  # read from origin, not the local tags
        code, out, _ = self.run_promote("log")
        self.assertEqual(code, 0)
        self.assertLess(out.index("promotion dev-2"), out.index("promotion dev-1"))
        self.assertIn("main:    " + self.main2, out)
        self.assertNotIn("BEGIN SSH SIGNATURE", out)

    def test_an_argo_call_failure_after_the_push_is_exit_1_with_the_run_id(self):
        mod = self.mod()

        def boom(commit, pushed_at):
            raise RuntimeError("kubectl annotate: connection refused")
        # The module's own wrapper, around an Argo step that raises as argocd.run does.
        mod.refresh_and_wait = mod.real_refresh_and_wait
        mod._refresh_and_wait = boom
        code, _, err = self.run_promote("dev", self.main1, "", "", mod=mod)
        self.assertEqual(code, 1, err)
        self.assertRegex(err, r"pushed [0-9a-f]{12}, then Argo CD: kubectl annotate: connection refused run=[0-9a-f]{8}")

    # --- AC-2 -----------------------------------------------------------------------------

    def test_an_image_that_does_not_pull_changes_nothing(self):
        code, _, err = self.run_promote("dev", self.main1, "", "", mod=self.mod(digest_fails=True))
        self.assertEqual(code, 1)
        self.assertIn("does not pull anonymously", err)
        self.assertEqual(sh(self.origin, "git", "for-each-ref", "refs/tags", "refs/heads/release"), "")
        self.assertEqual(sh(self.work, "git", "tag", "-l"), "")

    def test_a_commit_not_on_main_is_refused(self):
        sh(self.work, "git", "checkout", "-q", "-b", "side")
        with open(os.path.join(self.work, "README"), "a") as f:
            f.write("z\n")
        sh(self.work, "git", "commit", "-q", "-am", "side")
        code, _, err = self.run_promote("dev", sh(self.work, "git", "rev-parse", "HEAD"), "", "")
        self.assertEqual(code, 1)
        self.assertIn("is not on origin/main", err)
        self.assertEqual(self.digest_calls, [])

    # --- AC-4 -----------------------------------------------------------------------------

    def test_a_rollback_is_a_new_commit_on_the_history_tag_with_the_markers(self):
        self.run_promote("dev", self.main1, "", "")
        self.run_promote("dev", self.main2, "", "")
        one = self.remote("refs/tags/dev-1^{commit}")
        code, _, err = self.run_promote("dev", "", "dev-1", "42")
        self.assertEqual(code, 0, err)
        head = self.remote("refs/heads/release/dev")
        self.assertEqual(sh(self.origin, "git", "rev-parse", head + "^"), one)
        v = self.values_at(head)
        self.assertEqual(v["release"]["rolled_back_to"], "dev-1")
        self.assertEqual(v["server"]["recovery"]["pin_round"], 42)
        self.assertEqual(v["image"]["tag"], self.values_at(one)["image"]["tag"])
        self.assertEqual(self.remote("refs/tags/dev^{commit}"), head)
        self.assertEqual(self.remote("refs/tags/dev-3^{commit}"), head)
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        msg = sh(self.work, "git", "tag", "-l", "--format=%(contents)", "dev-3")
        self.assertIn("rolled_back_to: dev-1", msg)
        self.assertIn("pin_round: 42", msg)

    def test_a_rollback_without_a_round_clears_an_earlier_pin(self):
        self.run_promote("dev", self.main1, "", "")
        self.run_promote("dev", "", "dev-1", "7")
        code, _, err = self.run_promote("dev", "", "dev-2", "")
        self.assertEqual(code, 0, err)
        v = self.values_at(self.remote("refs/heads/release/dev"))
        self.assertEqual(v["server"]["recovery"]["pin_round"], 0)
        self.assertEqual(v["release"]["rolled_back_to"], "dev-2")

    def test_a_rollback_to_a_tag_that_is_not_there(self):
        self.run_promote("dev", self.main1, "", "")
        code, _, err = self.run_promote("dev", "", "dev-9", "")
        self.assertEqual(code, 1)
        self.assertIn("no tag dev-9", err)

    # --- AC-10 ----------------------------------------------------------------------------

    def test_claudecode_refuses_before_anything(self):
        with mock.patch.dict(os.environ, {"CLAUDECODE": "1"}):
            code, _, err = self.run_promote("dev", self.main1, "", "")
        self.assertEqual(code, 1)
        self.assertIn("CLAUDECODE", err)
        self.assertEqual(self.digest_calls, [])
        self.assertEqual(sh(self.origin, "git", "for-each-ref", "refs/tags", "refs/heads/release"), "")

    def test_a_refused_push_leaves_nothing_half_promoted(self):
        self.run_promote("dev", self.main1, "", "")
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        before = sh(self.work, "git", "rev-parse", "dev")
        hook = os.path.join(self.origin, "hooks", "pre-receive")
        with open(hook, "w") as f:
            f.write("#!/bin/sh\necho refused by the test >&2\nexit 1\n")
        os.chmod(hook, 0o755)
        code, _, err = self.run_promote("dev", self.main2, "", "")
        self.assertEqual(code, 1)
        self.assertIn("push refused, nothing promoted", err)
        self.assertEqual(sh(self.work, "git", "tag", "-l", "dev-2"), "")
        self.assertEqual(sh(self.work, "git", "rev-parse", "dev"), before)
        self.assertEqual(self.remote("refs/tags/dev-2"), "")
        self.assertEqual(self.remote("refs/tags/dev"), before)

    def test_a_refused_push_restores_dev_exactly_under_tag_gpgsign(self):
        """bootstrap.sh sets tag.gpgsign=true, where `git tag -f dev <old>` makes a new signed tag
        (or opens an editor) instead of restoring the saved one (Codex on #619). The saved tag
        object comes back as it was."""
        self.run_promote("dev", self.main1, "", "")
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        before = sh(self.work, "git", "rev-parse", "refs/tags/dev")  # the tag object, not its commit
        sh(self.work, "git", "config", "tag.gpgsign", "true")
        hook = os.path.join(self.origin, "hooks", "pre-receive")
        with open(hook, "w") as f:
            f.write("#!/bin/sh\nexit 1\n")
        os.chmod(hook, 0o755)
        with mock.patch.dict(os.environ, {"GIT_EDITOR": "false"}):
            code, _, err = self.run_promote("dev", self.main2, "", "")
        self.assertEqual(code, 1, err)
        self.assertEqual(sh(self.work, "git", "rev-parse", "refs/tags/dev"), before)
        self.assertEqual(sh(self.work, "git", "tag", "-l", "dev-2"), "")

    def test_a_tag_that_does_not_verify_is_undone(self):
        """make_tags made both tags, then `git tag -v` failed (the operator's key isn't in the
        allowed signers): both are undone locally, and nothing reached origin."""
        self.run_promote("dev", self.main1, "", "")
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        before, origin_dev = sh(self.work, "git", "rev-parse", "refs/tags/dev"), self.remote("refs/tags/dev")
        empty = os.path.join(self.tmp, "no_signers")
        open(empty, "w").close()
        sh(self.work, "git", "config", "gpg.ssh.allowedSignersFile", empty)
        code, _, err = self.run_promote("dev", self.main2, "", "")
        self.assertEqual(code, 1, err)
        self.assertIn("does not verify", err)
        self.assertNotIn("NOT restored", err)
        self.assertEqual(sh(self.work, "git", "rev-parse", "refs/tags/dev"), before)
        self.assertEqual(sh(self.work, "git", "tag", "-l", "dev-2"), "")
        self.assertEqual(self.remote("refs/tags/dev"), origin_dev)
        self.assertEqual(self.remote("refs/tags/dev-2"), "")

    def refuse_pushes(self):
        hook = os.path.join(self.origin, "hooks", "pre-receive")
        with open(hook, "w") as f:
            f.write("#!/bin/sh\necho refused by the test >&2\nexit 1\n")
        os.chmod(hook, 0o755)

    def mod_failing(self, *match):
        """A promote module whose git() leaves out the update-ref calls matching match, as a
        lock file or a pruned object would make them fail."""
        mod = self.mod()
        real = mod.git

        def git(*args, **kw):
            if args[:1] == ("update-ref",) and all(m in args for m in match):
                return ""
            return real(*args, **kw)
        mod.git = git
        return mod

    def test_a_failed_dev_restore_says_so_with_the_fix(self):
        self.run_promote("dev", self.main1, "", "")
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        before = sh(self.work, "git", "rev-parse", "refs/tags/dev")
        self.refuse_pushes()
        mod = self.mod_failing("refs/tags/dev", before)
        code, _, err = self.run_promote("dev", self.main2, "", "", mod=mod)
        self.assertEqual(code, 1, err)
        self.assertIn("push refused, nothing promoted", err)
        self.assertIn("refused by the test", err)
        self.assertNotIn("%s", err)
        self.assertNotIn("restored)", err)
        self.assertRegex(err, r"local tags NOT restored: dev is [0-9a-f]{12}, want %s" % before[:12])
        self.assertIn("git update-ref refs/tags/dev %s" % before, err)

    def test_a_leftover_history_tag_says_so_with_the_fix(self):
        self.refuse_pushes()
        mod = self.mod_failing("-d", "refs/tags/dev-1")
        code, _, err = self.run_promote("dev", self.main1, "", "", mod=mod)
        self.assertEqual(code, 1, err)
        self.assertIn("local tags NOT restored", err)
        self.assertIn("git update-ref -d refs/tags/dev-1", err)
        self.assertNotIn("%s", err)

    def test_a_local_release_branch_is_reset_and_named_only_then(self):
        self.run_promote("dev", self.main1, "", "")
        sh(self.work, "git", "fetch", "-q", "origin", "release/dev")
        origin_branch = sh(self.work, "git", "rev-parse", "refs/remotes/origin/release/dev")
        sh(self.work, "git", "update-ref", "refs/heads/release/dev", self.main2)  # an operator's stray local branch
        self.refuse_pushes()
        code, _, err = self.run_promote("dev", self.main2, "", "")
        self.assertEqual(code, 1, err)
        self.assertIn("(local dev, dev-2 and release/dev restored)", err)
        self.assertEqual(sh(self.work, "git", "rev-parse", "refs/heads/release/dev"), origin_branch)

    def test_a_release_branch_that_does_not_reset_says_so_with_the_fix(self):
        self.run_promote("dev", self.main1, "", "")
        origin_branch = self.remote("refs/heads/release/dev")
        sh(self.work, "git", "update-ref", "refs/heads/release/dev", self.main2)
        self.refuse_pushes()
        code, _, err = self.run_promote("dev", self.main2, "", "", mod=self.mod_failing("refs/heads/release/dev"))
        self.assertEqual(code, 1, err)
        self.assertIn("(local dev and dev-2 restored)", err)
        self.assertIn("local release/dev NOT reset; fix with `git update-ref refs/heads/release/dev %s`" % origin_branch, err)
        self.assertNotIn("release/dev restored", err)

    def test_a_failed_first_promotion_delete_says_so_with_the_fix(self):
        self.refuse_pushes()
        code, _, err = self.run_promote("dev", self.main1, "", "", mod=self.mod_failing("-d", "refs/tags/dev"))
        self.assertEqual(code, 1, err)
        self.assertRegex(err, r"dev is [0-9a-f]{12}, want absent")
        self.assertIn("git update-ref -d refs/tags/dev`", err)

    def test_a_refused_first_promotion_leaves_no_local_tags(self):
        hook = os.path.join(self.origin, "hooks", "pre-receive")
        with open(hook, "w") as f:
            f.write("#!/bin/sh\nexit 1\n")
        os.chmod(hook, 0o755)
        code, _, err = self.run_promote("dev", self.main1, "", "")
        self.assertEqual(code, 1, err)
        self.assertIn("(local dev and dev-1 restored)", err)
        self.assertEqual(sh(self.work, "git", "tag", "-l", "dev", "dev-1"), "")

    def test_a_push_refused_for_one_ref_moves_none(self):
        """The push is atomic: origin refusing only the history tag (a concurrent promotion took
        dev-<n>, or a ruleset on tags) must not leave release/dev and dev moved without it."""
        self.run_promote("dev", self.main1, "", "")
        branch, dev = self.remote("refs/heads/release/dev"), self.remote("refs/tags/dev")
        hook = os.path.join(self.origin, "hooks", "update")
        with open(hook, "w") as f:
            f.write('#!/bin/sh\ncase "$1" in refs/tags/dev-*) echo "no history tags" >&2; exit 1;; esac\nexit 0\n')
        os.chmod(hook, 0o755)
        code, _, err = self.run_promote("dev", self.main2, "", "")
        self.assertEqual(code, 1, err)
        self.assertEqual(self.remote("refs/heads/release/dev"), branch)
        self.assertEqual(self.remote("refs/tags/dev"), dev)
        self.assertEqual(self.remote("refs/tags/dev-2"), "")

    def test_a_rollback_records_main_from_origin_not_a_stale_ref(self):
        self.run_promote("dev", self.main1, "", "")
        self.run_promote("dev", self.main2, "", "")
        sh(self.work, "git", "update-ref", "refs/remotes/origin/main", self.main1)
        code, _, err = self.run_promote("dev", "", "dev-2", "")
        self.assertEqual(code, 0, err)
        sh(self.work, "git", "fetch", "-q", "--force", "origin", "+refs/tags/*:refs/tags/*")
        msg = sh(self.work, "git", "tag", "-l", "--format=%(contents)", "dev-3")
        self.assertIn("main:    " + self.main2, msg)
        self.assertIn("pin_round: none", msg)

    def test_no_signing_key_is_exit_3(self):
        sh(self.work, "git", "config", "--unset", "user.signingkey")
        with mock.patch.dict(os.environ, {"GIT_CONFIG_GLOBAL": os.devnull, "GIT_CONFIG_NOSYSTEM": "1"}):
            code, _, err = self.run_promote("dev", self.main1, "", "")
        self.assertEqual(code, 3)
        self.assertIn("user.signingkey", err)

    # --- usage ----------------------------------------------------------------------------

    def test_usage(self):
        for args, want in ((("prod", self.main1, "", ""), "prod is `make deploy`"),
                           (("staging", self.main1, "", ""), "only dev is promoted"),
                           (("", self.main1, "", ""), "only dev is promoted"),
                           (("dev", self.main1, "dev-1", ""), "exactly one of SHA and TAG"),
                           (("dev", "", "", ""), "exactly one of SHA and TAG"),
                           (("dev", self.main1, "", "3"), "PIN_ROUND goes with a rollback"),
                           (("dev", "", "dev-1", "x"), "not a round number"),
                           (("dev", "", "v1.0", ""), "history tag dev-<n>")):
            code, _, err = self.run_promote(*args)
            self.assertEqual(code, 2, (args, err))
            self.assertIn(want, err, args)
        self.assertEqual(self.digest_calls, [])


class SetValue(unittest.TestCase):
    """dev.yaml is edited as text, so its comments survive and a promotion's diff is one line."""

    def setUp(self):
        self.mod = load()
        with open(DEV_YAML) as f:
            self.text = f.read()

    def changed(self, a, b):
        return [(x, y) for x, y in zip(a.split("\n"), b.split("\n")) if x != y]

    def test_image_tag_is_one_line(self):
        out = self.mod.set_value(self.text, ["image", "tag"], "sha-0123456789ab@" + DIGEST)
        self.assertEqual(len(out.split("\n")), len(self.text.split("\n")))
        self.assertEqual(self.changed(self.text, out), [("  tag: dev", "  tag: sha-0123456789ab@" + DIGEST)])

    def test_a_missing_nested_key_is_added_under_its_parent(self):
        out = self.mod.set_value(self.text, ["server", "recovery", "pin_round"], 42)
        self.assertEqual(yaml.safe_load(out)["server"]["recovery"], {"pin_round": 42})
        self.assertEqual(len(out.split("\n")), len(self.text.split("\n")) + 2)

    def test_a_missing_top_level_key_is_appended(self):
        out = self.mod.set_value(self.text, ["release", "rolled_back_to"], "dev-3")
        self.assertEqual(yaml.safe_load(out)["release"], {"rolled_back_to": "dev-3"})
        self.assertTrue(out.startswith(self.text.rstrip("\n")))

    def test_an_existing_value_keeps_its_comment(self):
        out = self.mod.set_value("a:\n  b: 1  # why\n  c: 2\n", ["a", "b"], 5)
        self.assertEqual(out, "a:\n  b: 5  # why\n  c: 2\n")


class NoImageUpdater(unittest.TestCase):
    """AC-9: Image Updater and the moving :dev are gone outside the archives."""

    def test_grep(self):
        # The patterns are built, so the AC's literal grep doesn't find this file either.
        p = subprocess.run(["git", "-C", REPO, "grep", "-n", "-e", "image-" + "updater", "-e", "already installed " + "== 2",
                            "-e", "image.tag=" + "dev@", "--", ".", ":!docs/sprints", ":!docs/feedback", ":!docs/adr"],
                           capture_output=True, text=True)
        self.assertEqual(p.stdout, "")

    def test_image_publish_does_not_move_dev(self):
        with open(os.path.join(SCRIPTS, "image_publish.sh")) as f:
            body = "\n".join(l for l in f.read().splitlines() if not l.lstrip().startswith("#"))
        self.assertNotRegex(body, r":dev\b")


if __name__ == "__main__":
    unittest.main()
