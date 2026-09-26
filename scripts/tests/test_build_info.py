# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The stamps `make up` builds the server with, and the revision it reports (AW-INF-016)."""
import os
import stat
import subprocess
import tempfile
import textwrap
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
MAKEFILE = os.path.join(REPO, "Makefile")
STACK = os.path.join(REPO, "scripts", "stack.sh")


def git(cwd, *args):
    return subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, text=True).stdout.strip()


def build_info(cwd, *overrides):
    out = subprocess.run(["make", "-s", "-f", MAKEFILE, "build-info", *overrides],
                         cwd=cwd, check=True, capture_output=True, text=True).stdout
    return dict(line.split("=", 1) for line in out.splitlines())


class Stamps(unittest.TestCase):
    """COMMIT and REVISION carry `-dirty` exactly when VERSION's `git describe --dirty` would."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = self.tmp.name
        git(self.dir, "init", "-q")
        git(self.dir, "config", "user.email", "t@example.invalid")
        git(self.dir, "config", "user.name", "t")
        git(self.dir, "config", "commit.gpgsign", "false")
        with open(os.path.join(self.dir, "tracked.go"), "w") as f:
            f.write("package main\n")
        git(self.dir, "add", "tracked.go")
        git(self.dir, "commit", "-q", "-m", "one")
        self.head = git(self.dir, "rev-parse", "HEAD")
        self.short = git(self.dir, "rev-parse", "--short", "HEAD")

    def tearDown(self):
        self.tmp.cleanup()

    def test_clean_tree_has_no_suffix(self):
        b = build_info(self.dir)
        self.assertEqual(b["REVISION"], self.head)
        self.assertEqual(b["COMMIT"], self.short)

    def test_a_changed_tracked_file_is_dirty_in_all_three(self):
        with open(os.path.join(self.dir, "tracked.go"), "a") as f:
            f.write("// edit\n")
        b = build_info(self.dir)
        self.assertEqual(b["REVISION"], self.head + "-dirty")
        self.assertEqual(b["COMMIT"], self.short + "-dirty")
        self.assertTrue(b["VERSION"].endswith("-dirty"), b["VERSION"])

    def test_a_staged_change_is_dirty(self):
        with open(os.path.join(self.dir, "tracked.go"), "a") as f:
            f.write("// staged\n")
        git(self.dir, "add", "tracked.go")
        self.assertEqual(build_info(self.dir)["REVISION"], self.head + "-dirty")

    def test_an_untracked_file_is_not_dirty(self):
        with open(os.path.join(self.dir, "untracked.txt"), "w") as f:
            f.write("x\n")
        b = build_info(self.dir)
        self.assertEqual(b["REVISION"], self.head)
        self.assertFalse(b["VERSION"].endswith("-dirty"), b["VERSION"])

    def test_the_command_line_still_wins(self):
        with open(os.path.join(self.dir, "tracked.go"), "a") as f:
            f.write("// edit\n")
        b = build_info(self.dir, "COMMIT=abc", "REVISION=abcdef")
        self.assertEqual((b["COMMIT"], b["REVISION"]), ("abc", "abcdef"))


class SummaryRevision(unittest.TestCase):
    """`stack.sh revision`, which the `make up` summary line prints, reads the container's label."""

    def run_with_fake_docker(self, container_id):
        with tempfile.TemporaryDirectory() as bindir:
            fake = os.path.join(bindir, "docker")
            with open(fake, "w") as f:
                f.write(textwrap.dedent(f"""\
                    #!/usr/bin/env bash
                    case " $* " in
                      *" compose version "*) exit 0 ;;
                      *" ps -q andara-server "*) printf '%s' "{container_id}"; [[ -n "{container_id}" ]] && echo; exit 0 ;;
                      *" inspect "*)
                        [[ "$*" == *'org.opencontainers.image.revision'* && "${{@: -1}}" == "{container_id}" ]] || exit 9
                        echo "0123abcd-dirty" ;;
                      *) exit 7 ;;
                    esac
                    """))
            os.chmod(fake, os.stat(fake).st_mode | stat.S_IEXEC)
            env = dict(os.environ, PATH=bindir + os.pathsep + os.environ["PATH"])
            return subprocess.run([STACK, "revision"], env=env, capture_output=True, text=True)

    def test_prints_the_running_containers_label(self):
        r = self.run_with_fake_docker("c0ffee")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "0123abcd-dirty")

    def test_no_server_container_is_an_error(self):
        r = self.run_with_fake_docker("")
        self.assertEqual(r.returncode, 1)
        self.assertIn("andara-server is not running", r.stderr)


if __name__ == "__main__":
    unittest.main()
