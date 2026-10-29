# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/image_build.sh: local's build is a hash of the two binaries in the image, never pulled,
and no build without both (AW-INF-060)."""
import hashlib
import os
import subprocess
import tempfile
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
SERVER = "52563bd9d68c873c307822da4cabe5aaa981e3ec3d8d8531c92e576135442ce0  /usr/local/bin/andara-server"
PROJECTOR = "272493e7d0efe8f874f1cf47c4e9dd239686e24dd01f66ff88518a5c857d75ba  /usr/local/bin/andara-projector"


def run(stdout_lines, rc=0):
    """image_build.sh on a stub docker that prints `stdout_lines` and exits `rc`, logging its argv."""
    with tempfile.TemporaryDirectory() as d:
        log = os.path.join(d, "argv")
        with open(os.path.join(d, "docker"), "w") as f:
            f.write('#!/bin/sh\necho "$*" > "%s"\nprintf "%%b" "%s"\nexit %d\n'
                    % (log, "".join(l + "\\n" for l in stdout_lines), rc))
        os.chmod(os.path.join(d, "docker"), 0o755)
        r = subprocess.run(["bash", "scripts/image_build.sh", "andara-server:dev"], cwd=REPO, capture_output=True,
                           text=True, env=dict(os.environ, PATH=d + os.pathsep + os.environ["PATH"]))
        argv = open(log).read() if os.path.exists(log) else ""
    return r, argv


class ImageBuild(unittest.TestCase):

    def test_both_binaries_hash_to_the_build(self):
        r, argv = run([SERVER, PROJECTOR])
        self.assertEqual(r.returncode, 0, r.stderr)
        want = "sha256:" + hashlib.sha256((SERVER + "\n" + PROJECTOR + "\n").encode()).hexdigest()
        self.assertEqual(r.stdout.strip(), want)
        # The kind-loaded image, never a registry's: a missing one fails, it isn't pulled.
        self.assertIn("--pull never", argv)
        self.assertIn("andara-server:dev /usr/local/bin/andara-server /usr/local/bin/andara-projector", argv)

    def test_the_same_binaries_are_the_same_build(self):
        self.assertEqual(run([SERVER, PROJECTOR])[0].stdout, run([SERVER, PROJECTOR])[0].stdout)
        self.assertNotEqual(run([SERVER, PROJECTOR])[0].stdout, run([SERVER, SERVER.replace("5256", "5257")])[0].stdout)

    def test_no_build_without_both(self):
        for lines, rc in (([], 125), ([SERVER], 1), ([SERVER, "sha256sum: can't open"], 1), ([], 0)):
            with self.subTest(lines=lines, rc=rc):
                r, _ = run(lines, rc)
                self.assertEqual(r.returncode, 1, r.stdout)
                self.assertEqual(r.stdout, "")
                self.assertIn("make image", r.stderr)


if __name__ == "__main__":
    unittest.main()
