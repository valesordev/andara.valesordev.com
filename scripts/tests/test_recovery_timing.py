# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/recovery_timing.py (AW-SRV-007 AC-7): the job summary's table and the previous-run fetch.
Run by `make scripts-test`."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import recovery_timing as rt  # noqa: E402

SCRIPT = Path(__file__).resolve().parent.parent / "recovery_timing.py"


def sample(replay=45.98, total=46.3, tail=602):
    return {"round_tick": 777, "tail_ticks": tail, "entities": 25000, "rooms": 2000, "zones": 16,
            "characters": 500, "peak_rss_bytes": 97_000_000, "head_tick": 1379,
            "phases_seconds": {"load": 0.24, "seek": 0.05, "replay": replay, "verify": 0.07, "total": total}}


class Render(unittest.TestCase):
    def test_a_first_run_says_so_and_lists_every_phase(self):
        out = rt.render(sample())
        self.assertIn("No previous run to compare with.", out)
        for p in rt.PHASES:
            self.assertIn("| %s |" % p if p != "replay" else "| **replay** |", out)
        self.assertIn("| **replay** | 45.98 |", out)
        self.assertIn("a tail of 602 ticks", out)
        self.assertIn("peak RSS 97 MB", out)

    def test_a_comparison_shows_the_replay_delta_and_percentage(self):
        out = rt.render(sample(replay=50.0, total=50.4), sample(replay=40.0, total=40.4))
        self.assertIn("| **replay** | 50.00 | 40.00 | +10.00 s (+25%) |", out)
        self.assertIn("| total | 50.40 | 40.40 | +10.00 s (+25%) |", out)

    def test_an_improvement_is_negative(self):
        out = rt.render(sample(replay=30.0), sample(replay=40.0))
        self.assertIn("-10.00 s (-25%)", out)

    def test_a_zero_previous_phase_has_no_percentage(self):
        prev = sample()
        prev["phases_seconds"]["verify"] = 0
        self.assertIn("| verify | 0.07 | 0.00 | +0.07 s (-) |", rt.render(sample(), prev))

    def test_different_tails_are_called_out(self):
        out = rt.render(sample(tail=650), sample(tail=602))
        self.assertIn("The tails differ (650 now, 602 before)", out)
        self.assertNotIn("The tails differ", rt.render(sample(), sample()))

    def test_a_few_ticks_of_tail_is_the_same_tail(self):
        self.assertNotIn("The tails differ", rt.render(sample(tail=603), sample(tail=601)))
        self.assertNotIn("The tails differ", rt.render(sample(tail=601), sample(tail=603)))

    def test_five_percent_is_the_edge_of_the_same_tail(self):
        self.assertNotIn("The tails differ", rt.render(sample(tail=630), sample(tail=600)))
        self.assertIn("The tails differ", rt.render(sample(tail=631), sample(tail=600)))

    def test_a_different_fixture_is_called_out(self):
        prev = sample()
        prev["entities"] = 20000
        prev["zones"] = 8
        out = rt.render(sample(), prev)
        self.assertIn("The fixture differs (entities 25000 now, 20000 before, zones 16 now, 8 before)", out)
        self.assertNotIn("The fixture differs", rt.render(sample(), sample()))

    def test_a_phase_the_previous_run_lacks_is_dashed(self):
        prev = sample()
        del prev["phases_seconds"]["seek"]
        self.assertIn("| seek | 0.05 | - | - |", rt.render(sample(), prev))


class Load(unittest.TestCase):
    def write(self, body):
        d = tempfile.mkdtemp()
        self.addCleanup(lambda: __import__("shutil").rmtree(d, ignore_errors=True))
        p = Path(d) / "t.json"
        p.write_text(body if isinstance(body, str) else json.dumps(body))
        return str(p)

    def test_the_real_shape_loads(self):
        self.assertEqual(rt.load(self.write(sample()))["tail_ticks"], 602)

    def test_a_missing_file_is_a_value_error_naming_it(self):
        with self.assertRaisesRegex(ValueError, "nope.json"):
            rt.load("/nonexistent/nope.json")

    def test_not_json_and_not_an_object_are_refused(self):
        with self.assertRaisesRegex(ValueError, "not JSON"):
            rt.load(self.write("{"))
        with self.assertRaisesRegex(ValueError, "JSON object"):
            rt.load(self.write("[1]"))

    def test_a_missing_key_is_named(self):
        bad = sample()
        del bad["tail_ticks"]
        with self.assertRaisesRegex(ValueError, "missing tail_ticks"):
            rt.load(self.write(bad))

    def test_phases_without_replay_or_total_are_refused(self):
        bad = sample()
        del bad["phases_seconds"]["replay"]
        with self.assertRaisesRegex(ValueError, "replay and total"):
            rt.load(self.write(bad))


class Cli(unittest.TestCase):
    def setUp(self):
        self.d = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: __import__("shutil").rmtree(self.d, ignore_errors=True))
        self.cur = self.d / "cur.json"
        self.cur.write_text(json.dumps(sample()))

    def run_cli(self, *args, **env):
        e = {k: v for k, v in os.environ.items() if k != "GITHUB_STEP_SUMMARY"}
        e.update(env)
        return subprocess.run([sys.executable, str(SCRIPT), *args], capture_output=True, text=True, env=e)

    def test_without_a_summary_file_it_prints(self):
        r = self.run_cli("summary", str(self.cur))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("Recovery timing", r.stdout)

    def test_with_a_summary_file_it_appends_and_prints_nothing(self):
        summ = self.d / "summary.md"
        summ.write_text("earlier\n")
        r = self.run_cli("summary", str(self.cur), GITHUB_STEP_SUMMARY=str(summ))
        self.assertEqual((r.returncode, r.stdout), (0, ""))
        text = summ.read_text()
        self.assertTrue(text.startswith("earlier\n") and "Recovery timing" in text)

    def test_a_missing_current_exits_1_naming_it(self):
        r = self.run_cli("summary", str(self.d / "none.json"))
        self.assertEqual(r.returncode, 1)
        self.assertIn("none.json", r.stderr)

    def test_a_missing_previous_is_the_first_run(self):
        r = self.run_cli("summary", str(self.cur), str(self.d / "gone.json"))
        self.assertEqual(r.returncode, 0)
        self.assertIn("No previous run", r.stdout)

    def test_a_broken_previous_is_ignored_with_a_warning(self):
        prev = self.d / "prev.json"
        prev.write_text("{")
        r = self.run_cli("summary", str(self.cur), str(prev))
        self.assertEqual(r.returncode, 0)
        self.assertIn("ignoring the previous run", r.stderr)
        self.assertIn("No previous run", r.stdout)


class Previous(unittest.TestCase):
    def args(self):
        return SimpleNamespace(out_dir="/o", workflow="recovery-timing.yaml", branch="main")

    @staticmethod
    def fake(listing, download=(0, "")):
        calls = []

        def run(*argv):
            calls.append(argv)
            if argv[1] == "list":
                return SimpleNamespace(returncode=listing[0], stdout=listing[1], stderr=listing[2])
            return SimpleNamespace(returncode=download[0], stdout="", stderr=download[1])
        return run, calls

    def test_the_newest_successful_run_on_main_is_downloaded(self):
        run, calls = self.fake((0, "123\n", ""))
        self.assertEqual(rt.previous(self.args(), run), 0)
        listing, download = calls
        self.assertIn("success", listing)
        self.assertEqual(listing[listing.index("--branch") + 1], "main")
        self.assertEqual(download[:3], ("run", "download", "123"))
        self.assertEqual(download[download.index("--name") + 1], "recovery-timing")

    def test_no_run_yet_is_not_an_error_and_downloads_nothing(self):
        run, calls = self.fake((0, "\n", ""))
        self.assertEqual(rt.previous(self.args(), run), 0)
        self.assertEqual(len(calls), 1)

    def test_an_expired_artifact_is_not_an_error(self):
        run, _ = self.fake((0, "9", ""), download=(1, "no artifact"))
        self.assertEqual(rt.previous(self.args(), run), 0)

    def test_a_failing_gh_list_is_not_an_error_either(self):
        run, calls = self.fake((1, "", "HTTP 403"))
        self.assertEqual(rt.previous(self.args(), run), 0)
        self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()
