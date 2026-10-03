# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make core-versions-check` (AW-INF-029): content/core/VERSIONS is append-only.

Each case builds a throwaway repository whose `origin/main` holds a base VERSIONS, then changes
the file on a branch and runs scripts/core_versions_check.sh in it directly, since the ACs pin
the script's exit codes (through make, every failure is make's 2).
"""
import os
import subprocess
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SCRIPT = os.path.join(REPO, "scripts", "core_versions_check.sh")
PATH = "content/core/VERSIONS"

D1 = "1 90e5f780d7bf7b5b31bd868980a3e9f40bde934a9629241e7a4dfeb8fbcd1918"
D2 = "2 1111111111111111111111111111111111111111111111111111111111111111"
D3 = "3 2222222222222222222222222222222222222222222222222222222222222222"


def clean_env(extra=None):
    """The environment without anything that points git at another repository (a hook sets
    GIT_INDEX_FILE, for one) and without an inherited BASE_REF."""
    e = {k: v for k, v in os.environ.items() if not k.startswith("GIT_") and k != "BASE_REF"}
    e.update(extra or {})
    return e


def git(cwd, *args):
    return subprocess.run(["git", "-c", "commit.gpgsign=false", *args], cwd=cwd, check=True,
                          capture_output=True, text=True, env=clean_env()).stdout


class Fixture:
    """A repository with an `origin` whose main holds `base`, cloned and checked out on a branch."""

    def __init__(self, base_lines, fetch=True, depth=None):
        self.tmp = tempfile.TemporaryDirectory()
        root = self.tmp.name
        seed = os.path.join(root, "seed")
        self.origin = os.path.join(root, "origin.git")
        self.work = os.path.join(root, "work")
        os.makedirs(seed)
        git(seed, "init", "-q", "-b", "main")
        self._identity(seed)
        if base_lines is None:
            with open(os.path.join(seed, "README"), "w") as f:
                f.write("no VERSIONS yet\n")
            git(seed, "add", "README")
        else:
            self._write(seed, base_lines)
            git(seed, "add", PATH)
        git(seed, "commit", "-q", "-m", "base")
        # Bare, so the AC-5 cases can push to its main.
        git(root, "clone", "-q", "--bare", seed, self.origin)
        if depth:
            # file:// so --depth is honoured (a local path clone ignores it).
            git(root, "clone", "-q", "--depth", str(depth), "file://" + self.origin, self.work)
        else:
            git(root, "clone", "-q", self.origin, self.work)
        if not fetch:
            # No remote at all: no origin/main to compare with.
            git(self.work, "remote", "remove", "origin")
        self._identity(self.work)
        git(self.work, "switch", "-q", "-c", "change")

    @staticmethod
    def _identity(repo):
        git(repo, "config", "user.email", "t@example.invalid")
        git(repo, "config", "user.name", "t")

    @staticmethod
    def _write(repo, lines):
        os.makedirs(os.path.join(repo, os.path.dirname(PATH)), exist_ok=True)
        with open(os.path.join(repo, PATH), "w") as f:
            f.write("".join(l + "\n" for l in lines))

    def change(self, lines, commit=True):
        self._write(self.work, lines)
        if commit:
            git(self.work, "add", PATH)
            git(self.work, "commit", "-q", "-m", "change")

    def run(self, env=None):
        p = subprocess.run(["bash", SCRIPT], cwd=self.work, capture_output=True, text=True,
                           env=clean_env(env))
        return p.returncode, p.stdout + p.stderr

    def close(self):
        self.tmp.cleanup()


class AppendOnly(unittest.TestCase):

    def check(self, base, new, commit=True):
        fx = Fixture(base)
        self.addCleanup(fx.close)
        if new is not None:
            fx.change(new, commit=commit)
        return fx.run()

    def test_appending_the_next_version_passes(self):  # AC-1
        code, out = self.check([D1], [D1, D2])
        self.assertEqual(code, 0, out)

    def test_appending_two_in_sequence_passes(self):
        code, out = self.check([D1], [D1, D2, D3])
        self.assertEqual(code, 0, out)

    def test_editing_an_existing_line_fails_naming_it(self):  # AC-2
        code, out = self.check([D1, D2], [D1, "2 " + "f" * 64])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 2 changed; VERSIONS is append-only", out)

    def test_deleting_the_last_line_fails(self):  # AC-2
        code, out = self.check([D1, D2], [D1])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 2 changed; VERSIONS is append-only", out)

    def test_deleting_a_middle_line_fails_at_it(self):  # AC-2
        code, out = self.check([D1, D2, D3], [D1, D3])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 2 changed; VERSIONS is append-only", out)

    def test_reordering_fails(self):  # AC-2
        code, out = self.check([D1, D2], [D2, D1])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 1 changed; VERSIONS is append-only", out)

    def test_inserting_before_the_end_fails(self):  # AC-2: an insert shifts an existing line
        code, out = self.check([D1, D3], [D1, D2, D3])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 2 changed; VERSIONS is append-only", out)

    def test_a_skipped_version_fails_naming_the_line(self):  # AC-3
        code, out = self.check([D1], [D1, D3])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 2 is version 3; expected 2", out)

    def test_a_repeated_version_fails(self):  # AC-3
        code, out = self.check([D1], [D1, "1 " + "a" * 64])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 2 is version 1; expected 2", out)

    def test_the_second_appended_line_is_checked_too(self):  # AC-3
        code, out = self.check([D1], [D1, D2, "4 " + "b" * 64])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 3 is version 4; expected 3", out)

    def test_introducing_the_file_starts_at_version_1(self):
        code, out = self.check(None, [D1])
        self.assertEqual(code, 0, out)

    def test_introducing_the_file_at_version_2_fails(self):
        code, out = self.check(None, ["2 " + "a" * 64])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 1 is version 2; expected 1", out)

    def test_a_leading_zero_base_version_is_decimal(self):  # 08 isn't octal
        code, out = self.check(["1 " + "a" * 64, "08 " + "b" * 64],
                               ["1 " + "a" * 64, "08 " + "b" * 64, "99 " + "c" * 64])
        self.assertEqual(code, 1, out)
        self.assertIn("core-versions-check: line 3 is version 99; expected 9", out)
        code, out = self.check(["1 " + "a" * 64, "08 " + "b" * 64],
                               ["1 " + "a" * 64, "08 " + "b" * 64, "9 " + "c" * 64])
        self.assertEqual(code, 0, out)

    def test_a_tab_separated_line_is_read_as_go_reads_it(self):  # strings.Fields
        code, out = self.check([D1], [D1, "2\t" + "c" * 64])
        self.assertEqual(code, 0, out)

    def test_no_change_passes(self):  # AC-4
        code, out = self.check([D1, D2], None)
        self.assertEqual(code, 0, out)

    def test_an_uncommitted_edit_is_caught(self):  # the working tree, not just HEAD
        code, out = self.check([D1, D2], [D1, "2 " + "c" * 64], commit=False)
        self.assertEqual(code, 1, out)
        self.assertIn("line 2 changed", out)


class Base(unittest.TestCase):

    def test_no_base_exits_2(self):  # AC-6
        fx = Fixture([D1], fetch=False)
        self.addCleanup(fx.close)
        code, out = fx.run()
        self.assertEqual(code, 2, out)
        self.assertIn("core-versions-check: no merge base; fetch origin", out)

    def test_a_shallow_clone_refuses_rather_than_passing(self):
        # A bad edit on main, in a clone too shallow to hold HEAD's parent: the script can't
        # tell "no parent" from "parent not fetched", so it must not compare HEAD with itself.
        fx = Fixture([D1, D2])
        self.addCleanup(fx.close)
        fx.change([D1, "2 " + "e" * 64])
        git(fx.work, "push", "-q", "origin", "HEAD:main")
        shallow = Fixture.__new__(Fixture)
        shallow.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(shallow.tmp.cleanup)
        shallow.work = os.path.join(shallow.tmp.name, "work")
        git(shallow.tmp.name, "clone", "-q", "--depth", "1", "file://" + fx.origin, shallow.work)
        code, out = shallow.run()
        self.assertEqual(code, 2, out)
        self.assertIn("core-versions-check: shallow clone; fetch full history", out)

    def test_base_ref_overrides_origin_main(self):
        fx = Fixture([D1])
        self.addCleanup(fx.close)
        fx.change([D1, D2])
        git(fx.work, "branch", "pinned", "HEAD")
        fx.change([D1, "2 " + "d" * 64])
        # Against origin/main ([D1]) line 2 is a good append; against `pinned` it's an edit.
        self.assertEqual(fx.run()[0], 0)
        code, out = fx.run({"BASE_REF": "pinned"})
        self.assertEqual(code, 1, out)
        self.assertIn("line 2 changed", out)
        code, out = fx.run({"BASE_REF": "no-such-ref"})
        self.assertEqual(code, 2, out)
        self.assertIn("no merge base", out)

    def test_on_main_itself_it_compares_with_the_first_parent(self):  # AC-5
        fx = Fixture([D1, D2])
        self.addCleanup(fx.close)
        # A push to main: HEAD is origin/main, so the merge base is HEAD itself, and the
        # check must look at what HEAD's own commit did.
        fx.change([D1, "2 " + "e" * 64])
        git(fx.work, "push", "-q", "origin", "HEAD:main")
        git(fx.work, "fetch", "-q", "origin")
        code, out = fx.run()
        self.assertEqual(code, 1, out)
        self.assertIn("line 2 changed", out)

    def test_on_main_with_a_good_append_passes(self):  # AC-5
        fx = Fixture([D1])
        self.addCleanup(fx.close)
        fx.change([D1, D2])
        git(fx.work, "push", "-q", "origin", "HEAD:main")
        git(fx.work, "fetch", "-q", "origin")
        code, out = fx.run()
        self.assertEqual(code, 0, out)


if __name__ == "__main__":
    unittest.main()
