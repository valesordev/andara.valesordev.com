# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The story tooling knows three lanes, and routes `review` by who acts (AW-INF-027)."""
import contextlib
import io
import os
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import andara_docs  # noqa: E402
import gen_status  # noqa: E402
import new_story  # noqa: E402
import validate_stories  # noqa: E402

COMP = {"SRV": "server", "CLI": "cli", "INF": "infra"}


def story(sid, lane, status="ready", slug="a-story"):
    return sid + "-" + slug + ".md", "\n".join([
        "---",
        "id: " + sid,
        "title: " + slug.replace("-", " "),
        "epic: EPIC-01",
        "component: " + COMP[sid.split("-")[1]],
        "type: chore",
        "status: " + status,
        "size: S",
        "depends_on: []",
        "blocks: []",
        "lane: " + lane,
        "risk: low",
        "---",
        "",
        "## Context",
        "",
    ])


class Fixture(unittest.TestCase):
    """A throwaway docs tree the tools read in place of the repository's."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.stories = os.path.join(self.tmp.name, "stories")
        self.epics = os.path.join(self.tmp.name, "epics")
        self.adrs = os.path.join(self.tmp.name, "adr")
        for d in (self.stories, self.epics, self.adrs):
            os.mkdir(d)
        with open(os.path.join(self.epics, "EPIC-01-x.md"), "w") as f:
            f.write("---\nid: EPIC-01\ntitle: x\n---\n")
        for p in (mock.patch.object(andara_docs, "STORIES_DIR", self.stories),
                  mock.patch.object(validate_stories, "EPICS_DIR", self.epics),
                  mock.patch.object(validate_stories, "ADR_DIR", self.adrs),
                  mock.patch.object(gen_status, "ADR_DIR", self.adrs)):
            p.start()
            self.addCleanup(p.stop)
        self.addCleanup(self.tmp.cleanup)

    def add(self, *args, **kw):
        name, text = story(*args, **kw)
        with open(os.path.join(self.stories, name), "w") as f:
            f.write(text)
        return name

    def validate(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
            code = validate_stories.main()
        return code, err.getvalue()

    def section(self, heading):
        text = gen_status.render()
        start = text.index("## %s lane" % heading)
        end = text.index("\n## ", start + 1)
        return text[start:end]


class Validator(Fixture):
    def test_each_lane_is_accepted(self):  # AC-1
        for i, lane in enumerate(("architecture", "sre", "implementation")):
            self.add("AW-INF-%03d" % (i + 1), lane)
        self.assertEqual(self.validate(), (0, ""))

    def test_any_other_lane_is_refused_naming_the_file_and_the_three(self):  # AC-2
        for lane in ("pm", "ops"):
            with self.subTest(lane=lane):
                for n in os.listdir(self.stories):
                    os.remove(os.path.join(self.stories, n))
                name = self.add("AW-INF-001", lane)
                code, err = self.validate()
                self.assertEqual(code, 1)
                self.assertIn(name, err)
                self.assertIn("key 'lane' has value '%s'; permitted values: "
                              "architecture, sre, implementation" % lane, err)


class Scaffold(Fixture):
    def test_a_new_story_names_all_three_lanes(self):  # AC-7
        with mock.patch.object(new_story, "STORIES_DIR", self.stories), \
                mock.patch.object(sys, "argv", ["new_story.py", "INF", "x"]), \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(new_story.main(), 0)
        [name] = os.listdir(self.stories)
        with open(os.path.join(self.stories, name)) as f:
            [line] = [l for l in f if l.startswith("lane:")]
        for lane in andara_docs.LANES:
            self.assertIn(lane, line.split("#", 1)[1])


class Status(Fixture):
    def test_three_sections_in_charter_order(self):  # AC-3
        self.add("AW-INF-001", "sre")
        text = gen_status.render()
        headings = [l for l in text.splitlines() if l.endswith(tuple(v[2] for v in gen_status.LANE_VIEWS))]
        self.assertEqual(headings, [
            "## Architecture lane — contracts, specs, ADRs, the §8 review",
            "## SRE lane — build, ship, operate, observe",
            "## Implementation lane — server and cli source, tests",
        ])

    def test_now_line_carries_the_lanes_branch_prefix(self):  # AC-4
        for sid, lane, heading, prefix in (("AW-INF-001", "architecture", "Architecture", "arch/"),
                                           ("AW-INF-002", "sre", "SRE", "sre/"),
                                           ("AW-SRV-003", "implementation", "Implementation", "impl/")):
            self.add(sid, lane, status="in-progress", slug="the-work")
            with self.subTest(lane=lane):
                self.assertIn("branch %s%s-the-work" % (prefix, sid.lower()), self.section(heading))

    def test_review_is_routed_by_who_acts(self):  # AC-6
        self.add("AW-INF-001", "architecture", status="review")
        self.add("AW-INF-002", "sre", status="review")
        self.add("AW-SRV-003", "implementation", status="review")
        review = {h: [l for l in self.section(h).splitlines() if l.startswith("  review")]
                  for h in ("Architecture", "SRE", "Implementation")}
        all3 = "AW-INF-001, AW-INF-002, AW-SRV-003"
        self.assertEqual(review["Architecture"],
                         ["  review %s — run the §8 checklist, then flip to done" % all3])
        self.assertEqual(review["SRE"],
                         ["  review %s — verify §7 instrumentation, record it in the §8 record" % all3])
        self.assertEqual(review["Implementation"], ["  review AW-SRV-003 — awaiting §8"])

    def test_review_prompt_is_never_cut(self):
        # Four stories at review cut SRE's prompt to "record it in t…" (PR #146 review).
        for i in range(1, 7):
            self.add("AW-INF-%03d" % i, "sre", status="review")
        line = [l for l in self.section("SRE").splitlines() if l.startswith("  review")][0]
        self.assertTrue(line.endswith("— verify §7 instrumentation, record it in the §8 record"))
        self.assertLessEqual(len(line), gen_status.MAX_COLS)
        shown = line.split(" — ")[0].split(", ")
        self.assertEqual(shown[-1], "+%d more" % (6 - len(shown) + 1))

    def test_sre_review_line_is_the_section_7_scope(self):
        # A client story has no §7 section, so SRE has nothing to verify on it.
        with mock.patch.dict(COMP, {"CLT": "client"}):
            self.add("AW-CLT-001", "implementation", status="review")
        self.assertIn("  review AW-CLT-001 — run the §8", self.section("Architecture"))
        self.assertNotIn("  review", self.section("SRE"))

    def test_a_permitted_lane_with_no_view_fails_the_generator(self):
        self.add("AW-INF-001", "sre")
        with mock.patch.object(gen_status, "LANES", andara_docs.LANES + ["ops"]), \
                contextlib.redirect_stderr(io.StringIO()) as err, \
                self.assertRaises(SystemExit) as exit_:
            gen_status.render()
        self.assertEqual(exit_.exception.code, 1)
        self.assertIn("lane 'ops'", err.getvalue())


if __name__ == "__main__":
    unittest.main()
