# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make cli-release-check` against a release laid out on disk (AW-INF-020, AC-3 and AC-4)."""
import hashlib
import os
import platform
import stat
import subprocess
import tarfile
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
CHECK = os.path.join(REPO, "scripts", "cli_release_check.sh")
MAKEFILE = os.path.join(REPO, "Makefile")

OS = {"Linux": "linux", "Darwin": "darwin"}.get(platform.system())
ARCH = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())

# Stands in for the binary: prints what `andara-cli version` prints.
FAKE_CLI = "#!/bin/sh\nprintf 'version:  v0.1.0-3-gabc1234\\ncommit:   abc1234\\nbuilt_at: 2026-09-29T00:00:00Z\\n'\n"


@unittest.skipUnless(OS and ARCH, "no published archive for this platform")
class Check(unittest.TestCase):

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.rel = self.tmp.name
        self.archive = "andara-cli_v0.1.0-3-gabc1234_%s_%s.tar.gz" % (OS, ARCH)
        exe = os.path.join(self.rel, "andara-cli")
        with open(exe, "w") as f:
            f.write(FAKE_CLI)
        os.chmod(exe, os.stat(exe).st_mode | stat.S_IXUSR)
        with tarfile.open(os.path.join(self.rel, self.archive), "w:gz") as t:
            t.add(exe, arcname="andara-cli")
        os.remove(exe)
        with open(os.path.join(self.rel, self.archive), "rb") as f:
            digest = hashlib.sha256(f.read()).hexdigest()
        # A second platform's line, so the check has to pick its own.
        other = "andara-cli_v0.1.0-3-gabc1234_windows_amd64.zip"
        with open(os.path.join(self.rel, "SHA256SUMS"), "w") as f:
            f.write("%s  %s\n%s  %s\n" % ("0" * 64, other, digest, self.archive))

    def tearDown(self):
        self.tmp.cleanup()

    def run_check(self, *args):
        return subprocess.run([CHECK, *args], capture_output=True, text=True)

    def test_a_good_release_prints_version_and_commit(self):
        r = self.run_check("cli-dev", "file://" + self.rel)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stdout.strip(), "cli-release-check: andara-cli v0.1.0-3-gabc1234 (abc1234) ok")

    def test_a_changed_archive_is_a_checksum_mismatch(self):
        with open(os.path.join(self.rel, self.archive), "ab") as f:
            f.write(b"tampered")
        r = self.run_check("cli-dev", "file://" + self.rel)
        self.assertEqual(r.returncode, 1)
        self.assertEqual(r.stderr.strip(), "cli-release-check: checksum mismatch for " + self.archive)

    def test_a_missing_release_fails_naming_the_url(self):
        r = self.run_check("cli-dev", "file://" + self.rel + "/nope")
        self.assertEqual(r.returncode, 1)
        self.assertIn("could not download", r.stderr)

    def test_no_tag_is_usage(self):
        self.assertEqual(self.run_check().returncode, 2)


class Targets(unittest.TestCase):
    """TAG defaults to `dev` for images; the CLI targets default to `cli-dev`."""

    def dry(self, *args):
        return subprocess.run(["make", "-n", "-f", MAKEFILE, *args], cwd=REPO,
                              check=True, capture_output=True, text=True).stdout

    def test_check_defaults_to_cli_dev(self):
        self.assertIn('cli_release_check.sh "cli-dev"', self.dry("cli-release-check"))

    def test_check_takes_a_tag_from_the_command_line(self):
        self.assertIn('cli_release_check.sh "v0.1.0"', self.dry("cli-release-check", "TAG=v0.1.0"))

    def test_version_ignores_the_cli_dev_tag(self):
        with tempfile.TemporaryDirectory() as d:
            def git(*a):
                return subprocess.run(["git", *a], cwd=d, check=True, capture_output=True, text=True).stdout.strip()
            git("init", "-q")
            git("-c", "user.email=t@example.invalid", "-c", "user.name=t", "-c", "commit.gpgsign=false",
                "commit", "-q", "--allow-empty", "-m", "one")
            git("-c", "tag.gpgsign=false", "tag", "cli-dev")
            out = subprocess.run(["make", "-s", "-f", MAKEFILE, "build-info"], cwd=d,
                                 check=True, capture_output=True, text=True).stdout
            self.assertIn("VERSION=%s\n" % git("rev-parse", "--short", "HEAD"), out)


if __name__ == "__main__":
    unittest.main()
