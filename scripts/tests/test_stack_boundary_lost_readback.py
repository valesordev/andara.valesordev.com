# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The read-back in scripts/stack_boundary_lost.sh (AW-SRV-026 AC-4, AW-SRV-007).

After the lost-boundary recovery, the script restarts the server once more and reads the
`recovered from the log` line to decide whether the topic was read back across the loss. The
decision is a few lines of arithmetic that would pass silently if it were edited wrongly, so this
runs the real block (the lines between the `readback:begin` and `readback:end` markers) with a stub
`field`, `fail` and `last_delivered`, and checks every outcome. Run by `make scripts-test`.
"""

import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent.parent / "stack_boundary_lost.sh"
LAST_DELIVERED = 2814410  # the loss line's last_delivered_tick in a real run

HARNESS = r"""#!/usr/bin/env bash
set -uo pipefail
RECOVERED_LINE='recovered from the log'
fail() { echo "FAIL: $*"; exit 1; }
last_delivered=%d
field() { case "$2" in tick) echo "${T:-}";; ticks_replayed) echo "${R:-}";; round_tick) echo "${RT:-}";; esac; }
source "$1"
echo "PASS"
""" % LAST_DELIVERED


def block():
    text = SCRIPT.read_text()
    m = re.search(r"^# readback:begin[^\n]*\n(.*?)^# readback:end\n", text, re.S | re.M)
    assert m, "the readback markers are missing from stack_boundary_lost.sh"
    return m.group(1)


class ReadBack(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.TemporaryDirectory()
        d = Path(cls._tmp.name)
        (d / "block.sh").write_text(block())
        (d / "harness.sh").write_text(HARNESS)
        cls.dir = d

    @classmethod
    def tearDownClass(cls):
        cls._tmp.cleanup()

    def run_with(self, tick="", replayed="", round_tick=None):
        env = {"PATH": os.environ["PATH"], "T": str(tick), "R": str(replayed)}
        if round_tick is not None:
            env["RT"] = str(round_tick)
        r = subprocess.run(["bash", str(self.dir / "harness.sh"), str(self.dir / "block.sh")],
                           capture_output=True, text=True, env=env)
        return r.stdout.strip()

    def test_a_snapshot_recovery_whose_tail_crosses_the_loss_passes(self):
        # The live run: the round at 2813786, 654 ticks replayed, to 2814440, past the loss at 2814411.
        self.assertEqual(self.run_with(2814440, 654, 2813786), "PASS")

    def test_a_full_replay_passes_with_or_without_a_round_tick(self):
        self.assertEqual(self.run_with(2814440, 2814440), "PASS")
        self.assertEqual(self.run_with(2814440, 2814440, 0), "PASS")

    def test_a_line_whose_numbers_do_not_add_up_fails(self):
        out = self.run_with(2814440, 600, 2813786)
        self.assertTrue(out.startswith("FAIL:") and "inconsistent" in out, out)

    def test_a_partial_replay_with_no_round_fails_as_inconsistent(self):
        out = self.run_with(2814440, 100)
        self.assertTrue(out.startswith("FAIL:") and "inconsistent" in out, out)

    def test_a_round_cut_after_the_loss_fails_loudly_instead_of_passing(self):
        out = self.run_with(2814440, 20, 2814420)
        self.assertTrue(out.startswith("FAIL:") and "doesn't cross it" in out, out)
        self.assertIn("2814420", out)
        self.assertIn(str(LAST_DELIVERED), out)

    def test_a_round_exactly_at_the_last_delivered_tick_fails(self):
        # (round_tick, tick] starts after the last delivered tick, so it doesn't cover it.
        out = self.run_with(2814440, 30, LAST_DELIVERED)
        self.assertTrue(out.startswith("FAIL:") and "doesn't cross it" in out, out)

    def test_a_round_one_tick_before_the_last_delivered_tick_passes(self):
        self.assertEqual(self.run_with(2814440, 2814440 - (LAST_DELIVERED - 1), LAST_DELIVERED - 1), "PASS")

    def test_a_recovery_that_did_not_get_past_the_loss_fails(self):
        out = self.run_with(LAST_DELIVERED, 624, 2813786)
        self.assertTrue(out.startswith("FAIL:") and "not past the loss" in out, out)

    def test_no_recovered_line_fails(self):
        out = self.run_with("", "")
        self.assertTrue(out.startswith("FAIL:") and "logged no" in out, out)


if __name__ == "__main__":
    unittest.main()
