# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/builder_guide.py (AW-INF-028): fixture guides and a fake andara-cli.

The fake implements the two things the script asks of the binary: Cobra's hidden `__complete`
(subcommands for '', flags for '-', nothing while a flag value is pending) and `--help` (exit 0 for
anything resolvable, as Cobra does for `content bogus --help`). Run by `make scripts-test`.
"""

import contextlib
import importlib.util
import io
import json
import os
import stat
import tempfile
import textwrap
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    "builder_guide", Path(__file__).resolve().parent.parent / "builder_guide.py")
bg = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bg)

FAKE = r'''#!/usr/bin/env python3
import json, os, sys
TREE = {"content": {"approve": {}, "validate": {}, "reference": {}, "broken": {}},
        "auth": {"login": {}}, "completion": {}}
VALUE_FLAGS = {"-o", "--output", "--path", "--config"}
args = sys.argv[1:]
if args and args[0] == "__complete":
    words, to_complete = args[1:-1], args[-1]
    node, pending, positional = TREE, False, False
    i = 0
    while i < len(words):
        w = words[i]
        if w in VALUE_FLAGS:
            if i + 1 < len(words):
                i += 1
            else:
                pending = True
        elif w.startswith("-"):
            pass
        elif not positional and w in node:
            node = node[w]
        else:
            positional = True
        i += 1
    if pending:
        print(":0")
    elif to_complete.startswith("-"):
        print("--output\toutput format\n-o\toutput format\n--help\thelp\n:4")
    elif node and not positional:
        for name in node:
            print(name + "\tdesc")
        print(":4")
    else:
        print(":0")
    sys.exit(0)
if args[:2] == ["content", "reference"] and "--output" in args and "--help" not in args:
    sys.stdout.write(open("__REF_JSON__").read())
    sys.exit(0)
if "--help" in args:
    sys.exit(1 if "broken" in args else 0)
sys.exit(0)
'''

REF = {
    "format_version": 1,
    "directions": [{"name": "north", "reverse": "south"}, {"name": "south", "reverse": "north"}],
    "component_types": [{"type": "andara.core.Behavior", "fields": [{"name": "name", "kind": "string"}]},
                        {"type": "andara.core.Dark", "fields": []}],
    "core": {"pack": "andara.core", "version": 3,
             "templates": [{"name": "andara.core.Npc", "kind": "entity",
                            "chain": ["andara.core.Entity", "andara.core.Npc"]}]},
    "diagnostics": [{"code": "orphan_room", "severity": "warning", "raised_by": "both"},
                    {"code": "unknown_room", "severity": "error", "raised_by": "both"}],
}

ERRORS_MD = """# Errors

## 2. Shared

| `not_a_code_section` | x |

## 3. The codes

### 3.1 Raised only by the compiler

| Code | Token |
|------|-------|
| `orphan_room` | the room |
| `unknown_room` | the room |

## 4. The sidecar

| `after_section_3` | x |
"""


def ref_md(codes=("orphan_room", "unknown_room"), linked=True):
    def row(c):
        cell = "[`%s`](../specs/content-language/v1/errors.md#3-the-codes)" % c if linked else "`%s`" % c
        return "| %s | error | both |" % cell
    # A code-looking row outside Diagnostics, which must not count as a documented code.
    other = "## Component types\n\n| Type | Field | Kind |\n|---|---|---|\n| `not_a_diagnostic` | none | none |\n\n"
    return ("<!-- GENERATED -->\n\n# Reference\n\n" + other + "## Diagnostics\n\n"
            "| Code | Severity | Raised by |\n|---|---|---|\n" + "\n".join(row(c) for c in codes) + "\n")


class Fixture(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.root = Path(self._tmp.name)
        self.cli = self.root / "fake-andara-cli"
        self.ref_json = self.root / "ref.json"
        self.ref_json.write_text(json.dumps(REF))
        self.cli.write_text(FAKE.replace("__REF_JSON__", str(self.ref_json)))
        self.cli.chmod(self.cli.stat().st_mode | stat.S_IEXEC)
        (self.root / "docs/builders").mkdir(parents=True)
        errors = self.root / bg.ERRORS
        errors.parent.mkdir(parents=True)
        errors.write_text(ERRORS_MD)

    def guide(self, name, text):
        (self.root / "docs/builders" / name).write_text(textwrap.dedent(text))

    def run_bg(self, action):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = bg.main([action, "--root", str(self.root), "--cli", str(self.cli)])
        return rc, out.getvalue(), err.getvalue()


class GuideFixture(Fixture):
    def setUp(self):
        super().setUp()
        (self.root / bg.REFERENCE).write_text(ref_md(linked=False))


class GuideCommands(GuideFixture):
    def commands(self, *lines):
        self.guide("a.md", "# A\n\n```\n" + "\n".join(lines) + "\n```\n")
        return self.run_bg("guide-check")

    def test_a_group_followed_by_an_unknown_word_is_a_typo(self):
        rc, _, err = self.commands("andara-cli content bogus")
        self.assertEqual(rc, 1)
        self.assertIn('guide-check: docs/builders/a.md:4: no command "andara-cli content bogus"', err)

    def test_an_unknown_root_command_is_a_typo(self):
        rc, _, err = self.commands("andara-cli frobnicate")
        self.assertEqual(rc, 1)
        self.assertIn('no command "andara-cli frobnicate"', err)

    def test_a_leaf_with_positionals_passes(self):
        rc, out, err = self.commands("andara-cli content approve brian 1")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("1 commands", out)

    def test_a_global_flag_before_the_command_passes(self):
        rc, _, err = self.commands("andara-cli -o json content validate --path .")
        self.assertEqual((rc, err), (0, ""), err)

    def test_a_typo_after_a_global_flag_and_its_value_is_still_a_typo(self):
        # Without telling a flag's value from a subcommand, `json` ends the path at the root,
        # which isn't asked about its leftovers, and the typo passes.
        rc, _, err = self.commands("andara-cli -o json content bogus")
        self.assertEqual(rc, 1)
        self.assertIn('no command "andara-cli content bogus"', err)

    def test_a_flag_with_an_equals_value_is_skipped_too(self):
        rc, _, err = self.commands("andara-cli --output=json content bogus")
        self.assertEqual(rc, 1)
        self.assertIn('no command "andara-cli content bogus"', err)

    def test_a_prompt_and_a_placeholder_pass(self):
        rc, out, err = self.commands("$ andara-cli content approve <pack> <version>")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("1 commands", out)  # the prompt didn't hide the line from the check

    def test_a_prompt_does_not_hide_a_typo(self):
        rc, _, err = self.commands("$ andara-cli content bogus")
        self.assertEqual(rc, 1)
        self.assertIn('no command "andara-cli content bogus"', err)

    def test_a_command_whose_help_fails_is_reported(self):
        rc, _, err = self.commands("andara-cli content broken")
        self.assertEqual(rc, 1)
        self.assertIn('no command "andara-cli content broken"', err)

    def test_a_continued_line_is_one_command(self):
        rc, out, err = self.commands("andara-cli content validate \\", "  --path .")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("1 commands", out)

    def test_a_continuation_carries_the_typo_on_its_second_line(self):
        rc, _, err = self.commands("andara-cli content \\", "  bogus")
        self.assertEqual(rc, 1)
        self.assertIn('docs/builders/a.md:4: no command "andara-cli content bogus"', err)

    def test_a_shell_operator_ends_the_command(self):
        # On a group, the words after the operator would be leftovers, and fail, if they counted.
        for line in ("andara-cli content | grep x", "andara-cli content > out.txt",
                     "andara-cli content && echo done", "andara-cli content; echo done",
                     "andara-cli content # a comment"):
            rc, out, err = self.commands(line)
            self.assertEqual((rc, err), (0, ""), "%s: %s" % (line, err))
            self.assertIn("1 commands", out)

    def test_prose_and_other_programs_are_not_commands(self):
        self.guide("a.md", "# A\n\nRun andara-cli content bogus.\n\n```\nls andara-cli\ngo test\n```\n")
        rc, out, _ = self.run_bg("guide-check")
        self.assertEqual(rc, 0)
        self.assertIn("0 commands", out)


class GuideFailsClosed(GuideFixture):
    """When the binary can't answer, a line is unverifiable, not passed."""

    def fake(self, completion_body):
        text = FAKE.replace("__REF_JSON__", str(self.ref_json)).replace(
            'if args and args[0] == "__complete":', 'if args and args[0] == "__complete":\n' + completion_body, 1)
        self.cli.write_text(text)

    def test_a_completion_that_exits_non_zero_is_unverifiable(self):
        self.fake("    sys.exit(3)")
        self.guide("a.md", "# A\n\n```\nandara-cli content bogus\n```\n")
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, out), (1, ""))
        self.assertIn('docs/builders/a.md:4: cannot verify "andara-cli content bogus"', err)
        self.assertIn("`andara-cli __complete` exited 3", err)

    def test_a_completion_with_no_directive_is_unverifiable(self):
        self.fake("    sys.exit(0)")
        self.guide("a.md", "# A\n\n```\nandara-cli content approve x\n```\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("printed no completion directive", err)

    def test_one_bad_line_does_not_hide_the_others(self):
        self.fake("    if any(w.startswith('-o') and len(w) > 2 for w in args[1:]):\n        sys.exit(2)")
        self.guide("a.md", "# A\n\n```\nandara-cli -ojson content approve x\nandara-cli content bogus\nandara-cli content approve y\n```\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:4: cannot verify "andara-cli -ojson content approve x"', err)
        self.assertIn('a.md:5: no command "andara-cli content bogus"', err)
        self.assertEqual(len(err.strip().splitlines()), 2, err)


class GuideIndentedCode(GuideFixture):
    def test_an_indented_block_is_checked(self):
        self.guide("a.md", "# A\n\nRun:\n\n    andara-cli content bogus\n    andara-cli content approve x\n")
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:5: no command "andara-cli content bogus"', err)
        self.assertEqual(len(err.strip().splitlines()), 1, err)

    def test_a_tab_indented_block_is_checked(self):
        self.guide("a.md", "# A\n\n\tandara-cli content bogus\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:3: no command "andara-cli content bogus"', err)

    def test_an_indented_line_continuing_a_paragraph_is_not_code(self):
        self.guide("a.md", "# A\n\nA paragraph\n    andara-cli content bogus\n")
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("0 commands", out)

    def test_list_content_is_not_code_until_it_is_indented_further(self):
        self.guide("a.md", "# A\n\n- an item\n\n    andara-cli is the program\n\n- another\n\n        andara-cli content bogus\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:9: no command "andara-cli content bogus"', err)
        self.assertNotIn(":5:", err)

    def test_links_inside_an_indented_block_are_not_checked(self):
        self.guide("a.md", "# A\n\n    [x](nope.md)\n")
        rc, out, _ = self.run_bg("guide-check")
        self.assertEqual(rc, 0)
        self.assertIn("0 links", out)


class GuideIndentedCodeBoundaries(GuideFixture):
    def lines(self, text):
        code, prose, _ = bg.split_blocks(text)
        return [n for n, _ in code], [n for n, _ in prose]

    def test_three_spaces_is_not_code_and_four_is(self):
        self.assertEqual(self.lines("p\n\n   x\n\n    y\n")[0], [5])

    def test_consecutive_indented_lines_are_one_block(self):
        self.assertEqual(self.lines("p\n\n    a\n    b\n    c\n")[0], [3, 4, 5])

    def test_a_bogus_command_on_the_second_indented_line_is_found(self):
        (self.root / bg.REFERENCE).write_text(ref_md(linked=False))
        self.guide("a.md", "# A\n\n    andara-cli content approve x\n    andara-cli content bogus\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:4: no command "andara-cli content bogus"', err)

    def test_code_then_blank_then_code(self):
        self.assertEqual(self.lines("p\n\n    a\n\n    b\n")[0], [3, 5])

    def test_an_indented_line_after_prose_that_follows_code_is_a_continuation(self):
        self.assertEqual(self.lines("p\n\n    a\nprose\n    b\n")[0], [3])

    def test_a_list_then_a_paragraph_then_indented_code(self):
        self.assertEqual(self.lines("- item\n\npara\n\n    code\n")[0], [5])

    def test_code_may_follow_a_heading_or_a_fence_directly(self):
        self.assertEqual(self.lines("# H\n    after heading\n")[0], [2])
        self.assertEqual(self.lines("```\nx\n```\n    after fence\n")[0], [2, 4])

    def test_in_a_bullet_list_code_needs_the_content_offset_plus_four(self):
        # `- item` has content offset 2: five spaces is item content, six is code.
        self.assertEqual(self.lines("- item\n\n     five\n\n      six\n")[0], [5])

    def test_in_a_numbered_list_the_offset_is_the_marker_width(self):
        self.assertEqual(self.lines("1. item\n\n      six\n\n       seven\n")[0], [5])

    def test_five_spaces_after_a_marker_is_a_marker_and_one_space(self):
        # `-      item` (six spaces) has content offset 2, not 7. Eight spaces is code under offset 2
        # and only item content under offset 7, which a dedent can't mask the way it does at six.
        self.assertEqual(self.lines("-      item\n\n        y\n")[0], [3])

    def test_an_item_marker_indented_past_the_offset_is_code(self):
        self.assertEqual(self.lines("- a\n\n      - b\n")[0], [3])
        self.assertEqual(self.lines("- a\n\n    - b\n")[0], [])

    def test_a_paragraph_under_a_nested_item_is_not_code(self):
        self.assertEqual(self.lines("- a\n\n    - b\n\n        para of b\n")[0], [])

    def test_a_third_level_item_is_prose_so_its_links_are_checked(self):
        (self.root / bg.REFERENCE).write_text(ref_md(linked=False))
        self.guide("a.md", "# A\n\n- a\n\n    - b\n\n        - c [x](nope.md)\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:7: broken link nope.md", err)

    def test_a_fence_outside_the_item_ends_the_list(self):
        self.assertEqual(self.lines("- a\n\n```\nx\n```\n\n    code\n")[0], [4, 7])

    def test_a_thematic_break_ends_the_list(self):
        self.assertEqual(self.lines("- a\n\n* * *\n\n    code\n")[0], [5])

    def test_a_lazy_continuation_keeps_the_item_open(self):
        self.assertEqual(self.lines("- item\nlazy\n\n    more\n")[0], [])

    def test_a_dedented_line_after_a_blank_ends_the_list(self):
        self.assertEqual(self.lines("- a\n\npara\n\n    code\n")[0], [5])


class GuideParagraphBoundaries(GuideFixture):
    def check(self, text):
        self.guide("a.md", text)
        return self.run_bg("guide-check")

    def test_a_stray_backtick_in_one_list_item_does_not_hide_the_next_item_s_link(self):
        rc, _, err = self.check("# A\n\n- the ` key\n- see [l](missing.md) and `x`\n")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:4: broken link missing.md", err)

    def test_a_stray_backtick_in_a_table_row_does_not_hide_the_next_row(self):
        rc, _, err = self.check("# A\n\n| a ` b |\n|---|\n| [l](missing.md) | `x` |\n")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:5: broken link missing.md", err)

    def test_a_stray_backtick_in_a_heading_does_not_hide_the_next_line(self):
        rc, _, err = self.check("# A\n\n## Heading ` tick\n[l](missing.md) then `x`\n")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:4: broken link missing.md", err)

    def test_a_heading_cannot_hold_the_start_of_a_link(self):
        rc, out, _ = self.check("# A\n\n## [H\ntext](missing.md)\n")
        self.assertEqual(rc, 0)
        self.assertIn("0 links", out)

    def test_a_blockquote_starts_a_new_paragraph(self):
        rc, _, err = self.check("# A\n\ntext ` here\n> [l](missing.md) `x`\n")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:4: broken link missing.md", err)

    def test_line_numbers_are_exact_after_a_multi_line_code_span(self):
        text = ("# A\n\n"
                "A long first line of the paragraph that goes on for a while and a while longer yet.\n"
                "Then `a code span that begins here and\n"
                "runs across two more lines of text\n"
                "before it ends` and some words after it.\n"
                "Final line with [the link](missing.md) on it.\n")
        rc, _, err = self.check(text)
        self.assertEqual(rc, 1)
        self.assertIn("a.md:7: broken link missing.md", err)

    def test_findings_print_in_line_order_within_a_file(self):
        # One paragraph: the inline link on line 4 is found before the definitions on 3 and 5.
        rc, _, err = self.check("# A\n\n[a]: first-missing.md\ntext [c](second-missing.md)\n[b]: third-missing.md\n")
        self.assertEqual(rc, 1)
        order = [l.split(":")[2] for l in err.strip().splitlines()]
        self.assertEqual(order, ["3", "4", "5"])

    def test_a_link_at_either_edge_of_a_line_deep_in_a_paragraph_maps_to_its_own_line(self):
        # Twelve short lines: any drift in the running offsets moves a link at a line's edge
        # to its neighbour.
        lines = ["a"] * 10 + ["see [x](m.md)", "[y](n.md) tail"]
        rc, _, err = self.check("# A\n\n" + "\n".join(lines) + "\n")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:13: broken link m.md", err)
        self.assertIn("a.md:14: broken link n.md", err)


class GuideTimeout(GuideFixture):
    def test_a_binary_that_hangs_is_unverifiable_not_a_hang(self):
        text = FAKE.replace("__REF_JSON__", str(self.ref_json)).replace(
            'if args and args[0] == "__complete":', 'if args and args[0] == "__complete":\n    __import__("time").sleep(10)', 1)
        self.cli.write_text(text)
        self.guide("a.md", "# A\n\n```\nandara-cli content approve x\n```\n")
        old, bg.TIMEOUT_S = bg.TIMEOUT_S, 1
        try:
            rc, _, err = self.run_bg("guide-check")
        finally:
            bg.TIMEOUT_S = old
        self.assertEqual(rc, 1)
        self.assertIn("`andara-cli __complete` exited 124", err)


class GuideFences(GuideFixture):
    def test_a_tilde_fence_is_a_code_block(self):
        self.guide("a.md", "# A\n\n~~~\nandara-cli content bogus\n~~~\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:4: no command "andara-cli content bogus"', err)

    def test_a_longer_fence_may_contain_a_shorter_one(self):
        self.guide("a.md", "# A\n\n````md\n```\nandara-cli content bogus\n```\n````\n\n[x](nope.md)\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:5: no command "andara-cli content bogus"', err)
        self.assertIn("a.md:9: broken link nope.md", err)

    def test_a_fence_with_an_info_string_does_not_close_a_fence(self):
        self.guide("a.md", "# A\n\n```\n```bash\nandara-cli content bogus\n```\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn('a.md:5: no command "andara-cli content bogus"', err)

    def test_a_backtick_run_with_a_backtick_in_its_info_string_is_not_a_fence(self):
        self.guide("a.md", "# A\n\n```inline``` is prose\n\n[x](nope.md)\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:5: broken link nope.md", err)
        self.assertNotIn("unclosed", err)

    def test_an_unclosed_fence_is_a_finding(self):
        self.guide("a.md", "# A\n\n```\nandara-cli content approve x\n\n[x](nope.md)\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("docs/builders/a.md:3: unclosed code fence", err)


class GuideCodes(Fixture):
    def test_a_code_in_errors_but_not_the_reference(self):
        (self.root / bg.REFERENCE).write_text(ref_md(("orphan_room",)))
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("guide-check: code unknown_room is in errors.md but not the reference", err)

    def test_a_code_in_the_reference_but_not_errors(self):
        (self.root / bg.REFERENCE).write_text(ref_md(("orphan_room", "unknown_room", "invented")))
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("guide-check: code invented is in the reference but not errors.md", err)

    def test_only_section_3_counts(self):
        (self.root / bg.REFERENCE).write_text(ref_md())
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("2 codes", out)


class GuideLinks(GuideFixture):
    def test_a_missing_target(self):
        self.guide("a.md", "# A\n\nSee [b](nope.md).\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("guide-check: docs/builders/a.md:3: broken link nope.md", err)

    def test_a_missing_anchor(self):
        self.guide("a.md", "# A\n\nSee [b](b.md#nowhere).\n")
        self.guide("b.md", "# B\n\n## Somewhere\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("broken link b.md#nowhere", err)

    def test_heading_slugs_and_explicit_anchors_resolve(self):
        self.guide("a.md", "# A\n\n[1](b.md#some-heading--here) [2](b.md#core_code) [3](#a) [4](b.md#dup-1)\n")
        self.guide("b.md", '# B\n\n## Some `heading` & here\n\n### <a id="core_code"></a>`core_code`\n\n## dup\n\n## dup\n')
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)

    def test_an_explicit_anchor_and_an_underscore_are_anchors_a_heading_slug_isnt(self):
        self.guide("a.md", "# A\n\n[1](b.md#x_y) [2](b.md#a_b) [3](b.md#A_B)\n")
        self.guide("b.md", '# B\n\n## <a id="x_y"></a>Other text\n\n## A_B\n')
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)

    def test_setext_headings_are_anchors(self):
        self.guide("a.md", "# A\n\n[1](b.md#second-title)\n")
        self.guide("b.md", "First\n=====\n\nSecond title\n------------\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)

    def test_a_heading_with_a_link_slugs_to_its_text(self):
        self.guide("a.md", "# A\n\n[1](b.md#link-text)\n")
        self.guide("b.md", "# B\n\n## [Link text](http://x.y/z)\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)

    def test_a_fragment_on_a_file_that_isnt_markdown_is_broken(self):
        self.guide("a.md", "# A\n\n[x](notes.txt#frag)\n")
        self.guide("notes.txt", "# frag\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("broken link notes.txt#frag", err)

    def test_valid_link_forms_resolve(self):
        self.guide("a.md", textwrap.dedent("""\
            # A

            [angle](<b.md>) [query](b.md?plain=1) [pct](my%20file.md) [titled](b.md "A title")
            [escaped](b\\_c.md) [root](/docs/builders/b.md) [nested [brackets] ok](b.md)
            ![image](pic.png) <a href="b.md">html</a>

            [ref]: b.md
            """))
        self.guide("b.md", "# B\n")
        self.guide("my file.md", "# M\n")
        self.guide("b_c.md", "# BC\n")
        (self.root / "docs/builders/pic.png").write_bytes(b"x")
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("10 links", out)

    def test_link_forms_that_used_to_pass_unchecked_are_checked(self):
        self.guide("a.md", textwrap.dedent("""\
            # A

            [angle](<missing one.md>)
            ![image](missing.png)
            <a href="missing-html.md">html</a>
            [nested [brackets] x](missing-nested.md)
            [parens](missing(1).md)
            [dir](../builders#nope)
            [up](../../../../etc/passwd)

            [ref]: missing-ref.md
            """))
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        for t in ("<missing one.md>", "missing.png", "missing-html.md", "missing-nested.md",
                  "missing(1).md", "../builders#nope", "../../../../etc/passwd", "missing-ref.md"):
            self.assertIn("broken link " + t, err)

    def test_external_links_and_links_in_code_are_not_checked(self):
        self.guide("a.md", "# A\n\n[x](https://example.com/404) `[y](gone.md)`\n\n```\n[z](gone.md)\n```\n")
        rc, out, _ = self.run_bg("guide-check")
        self.assertEqual(rc, 0)
        self.assertIn("0 links", out)

    def test_a_link_split_across_lines_is_checked_and_reported_where_it_starts(self):
        self.guide("a.md", "# A\n\nSee [details](\nmissing.md\n) and [two\nlines](also-missing.md).\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("a.md:3: broken link missing.md", err)
        self.assertIn("a.md:5: broken link also-missing.md", err)

    def test_a_link_split_across_a_blank_line_is_not_a_link(self):
        self.guide("a.md", "# A\n\n[text\n\n](missing.md)\n")
        rc, out, _ = self.run_bg("guide-check")
        self.assertEqual(rc, 0)
        self.assertIn("0 links", out)

    def test_a_fenced_block_ends_a_paragraph(self):
        self.guide("a.md", "# A\n\n[text\n```\ncode\n```\n](missing.md)\n")
        rc, out, _ = self.run_bg("guide-check")
        self.assertEqual(rc, 0)
        self.assertIn("0 links", out)

    def test_inline_code_spanning_lines_hides_link_syntax(self):
        self.guide("a.md", "# A\n\nUse `[x](\nmissing.md)` here, and [ok](b.md).\n")
        self.guide("b.md", "# B\n")
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertIn("1 links", out)

    def test_a_guide_page_in_a_subdirectory_is_checked(self):
        (self.root / "docs/builders/sub").mkdir()
        (self.root / "docs/builders/sub/c.md").write_text("# C\n\n[x](nope.md)\n\n```\nandara-cli content bogus\n```\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 1)
        self.assertIn("docs/builders/sub/c.md:3: broken link nope.md", err)
        self.assertIn('docs/builders/sub/c.md:6: no command "andara-cli content bogus"', err)

    def test_a_link_to_a_directory_or_a_sibling_resolves(self):
        self.guide("a.md", "# A\n\n[up](../builders/b.md) [spec](../specs/content-language/v1/errors.md#3-the-codes)\n")
        self.guide("b.md", "# B\n")
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)


class GuideCheckOverall(Fixture):
    def test_every_failure_is_reported_not_only_the_first(self):
        self.guide("a.md", "# A\n\n[b](nope.md)\n\n```\nandara-cli content bogus\n```\n")
        (self.root / bg.REFERENCE).write_text(ref_md(("orphan_room",)))
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, out), (1, ""))
        self.assertEqual(len(err.strip().splitlines()), 3, err)

    def test_a_guide_with_only_the_reference_passes(self):
        (self.root / bg.REFERENCE).write_text(ref_md())
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, err), (0, ""), err)
        self.assertEqual(out.strip(), "guide-check: 0 commands, 2 codes, 2 links ok")

    def test_a_guide_with_pages_and_no_reference_does_not_go_quiet(self):
        self.guide("a.md", "# A\n")
        rc, out, err = self.run_bg("guide-check")
        self.assertEqual((rc, out), (1, ""))
        self.assertIn("guide-check: docs/builders/reference.md not found; run make builder-reference", err)

    def test_an_empty_guide_is_not_a_failure(self):
        rc, out, _ = self.run_bg("guide-check")
        self.assertEqual(rc, 0)
        self.assertEqual(out.strip(), "guide-check: 0 commands, 0 codes, 0 links ok")

    def test_no_guide_directory_exits_two(self):
        (self.root / "docs/builders").rmdir()
        rc, _, err = self.run_bg("guide-check")
        self.assertEqual(rc, 2)
        self.assertIn("docs/builders doesn't exist", err)


class FailureLinesArePrefixedEveryLine(Fixture):
    """The contract: one finding per line, every line prefixed. A failure that embeds a tool's own
    multi-line output must not leave its later lines bare (issue #401)."""

    def run_with(self, action, **env):
        saved = {k: os.environ.get(k) for k in env}
        os.environ.update(env)
        try:
            out, err = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                rc = bg.main([action, "--root", str(self.root)] + (["--cli", str(self.cli)] if "GO" not in env else []))
            return rc, out.getvalue(), err.getvalue()
        finally:
            for k, v in saved.items():
                if v is None:
                    os.environ.pop(k, None)
                else:
                    os.environ[k] = v

    def failing_go(self):
        go = self.root / "failing-go"
        go.write_text("#!/bin/sh\necho '# example.com/cmd/andara-cli' >&2\necho 'cmd/andara-cli/x.go:2:14: syntax error' >&2\nexit 1\n")
        go.chmod(go.stat().st_mode | stat.S_IEXEC)
        return str(go)

    def assert_every_line_prefixed(self, err, prefix, expect_lines):
        lines = err.rstrip("\n").split("\n")
        self.assertEqual(len(lines), expect_lines, err)
        for line in lines:
            self.assertTrue(line.startswith(prefix + ": "), "unprefixed line %r in %r" % (line, err))

    def test_a_failing_build_under_reference_check(self):
        rc, out, err = self.run_with("reference-check", GO=self.failing_go())
        self.assertEqual((rc, out), (1, ""))
        self.assert_every_line_prefixed(err, "builder-reference", 2)
        self.assertIn("go build ./cmd/andara-cli failed: # example.com/cmd/andara-cli", err)
        self.assertIn("builder-reference: cmd/andara-cli/x.go:2:14: syntax error", err)

    def test_a_failing_build_under_reference(self):
        rc, out, err = self.run_with("reference", GO=self.failing_go())
        self.assertEqual((rc, out), (1, ""))
        self.assert_every_line_prefixed(err, "builder-reference", 2)

    def test_a_failing_build_under_guide_check(self):
        self.guide("a.md", "# A\n\n```\nandara-cli content approve x\n```\n")
        rc, out, err = self.run_with("guide-check", GO=self.failing_go())
        self.assertEqual((rc, out), (1, ""))
        self.assert_every_line_prefixed(err, "guide-check", 2)

    def test_a_failing_content_reference_under_reference_check(self):
        self.cli.write_text("#!/bin/sh\necho 'first line of the CLI error' >&2\necho 'second line of it' >&2\nexit 7\n")
        rc, out, err = self.run_with("reference-check")
        self.assertEqual((rc, out), (1, ""))
        self.assert_every_line_prefixed(err, "builder-reference", 2)
        self.assertIn("exited 7: first line of the CLI error", err)
        self.assertIn("builder-reference: second line of it", err)

    def test_say_err_prefixes_each_line_and_keeps_an_empty_message_visible(self):
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            bg.say_err("p", "one\ntwo\nthree")
            bg.say_err("p", "")
        self.assertEqual(err.getvalue(), "p: one\np: two\np: three\np: \n")

    def test_single_line_findings_are_unchanged(self):
        self.ref_json.write_text(json.dumps(dict(REF, format_version=2)))
        rc, _, err = self.run_with("reference")
        self.assertEqual(err, "builder-reference: unsupported format_version 2\n")


class Reference(Fixture):
    def test_writes_the_four_sections_in_order(self):
        rc, out, _ = self.run_bg("reference")
        self.assertEqual(rc, 0)
        self.assertEqual(out.strip(), "builder-reference: wrote docs/builders/reference.md")
        text = (self.root / bg.REFERENCE).read_text()
        self.assertTrue(text.startswith("<!-- GENERATED by make builder-reference"))
        heads = [l for l in text.splitlines() if l.startswith("## ")]
        self.assertEqual(heads, ["## Directions", "## Component types", "## `andara.core@3` Templates", "## Diagnostics"])
        self.assertIn("| `andara.core.Behavior` | `name` | string |", text)
        self.assertIn("| `andara.core.Dark` | none | none |", text)
        self.assertIn("| `andara.core.Npc` | entity | `andara.core.Entity` › `andara.core.Npc` |", text)
        self.assertIn("| [`orphan_room`](../specs/content-language/v1/errors.md#3-the-codes) | warning | both |", text)

    def test_a_second_run_is_byte_identical(self):
        self.run_bg("reference")
        first = (self.root / bg.REFERENCE).read_bytes()
        self.run_bg("reference")
        self.assertEqual((self.root / bg.REFERENCE).read_bytes(), first)

    def test_an_added_key_or_field_changes_nothing(self):
        self.run_bg("reference")
        first = (self.root / bg.REFERENCE).read_text()
        more = json.loads(json.dumps(REF))
        more["extra_section"] = [1]
        more["directions"][0]["note"] = "x"
        more["diagnostics"][0]["since"] = 4
        self.ref_json.write_text(json.dumps(more))
        rc, _, _ = self.run_bg("reference")
        self.assertEqual(rc, 0)
        self.assertEqual((self.root / bg.REFERENCE).read_text(), first)

    def test_an_unsupported_format_version_exits_one(self):
        self.ref_json.write_text(json.dumps(dict(REF, format_version=2)))
        rc, _, err = self.run_bg("reference")
        self.assertEqual(rc, 1)
        self.assertIn("builder-reference: unsupported format_version 2", err)
        self.assertFalse((self.root / bg.REFERENCE).exists())

    def test_a_payload_that_isnt_the_contract_exits_one_with_a_message(self):
        for payload, want in (("not json", "printed no JSON object"), ("[]", "isn't an object"),
                              (json.dumps({"format_version": True}), "unsupported format_version true"),
                              (json.dumps({"format_version": "1"}), 'unsupported format_version "1"'),
                              (json.dumps({k: v for k, v in REF.items() if k != "core"}), "has no 'core'")):
            self.ref_json.write_text(payload)
            rc, _, err = self.run_bg("reference")
            self.assertEqual(rc, 1, payload)
            self.assertIn(want, err)
            self.assertNotIn("Traceback", err)

    def test_check_passes_when_current_and_names_a_stale_or_missing_file(self):
        rc, _, err = self.run_bg("reference-check")
        self.assertEqual(rc, 1, "a missing file is stale")
        self.assertIn("builder-reference: stale; run make builder-reference", err)
        self.run_bg("reference")
        self.assertEqual(self.run_bg("reference-check")[0], 0)
        path = self.root / bg.REFERENCE
        path.write_text(path.read_text() + "hand edit\n")
        rc, _, err = self.run_bg("reference-check")
        self.assertEqual(rc, 1)
        self.assertIn("builder-reference: stale; run make builder-reference", err)

    def test_check_writes_nothing(self):
        self.run_bg("reference-check")
        self.assertFalse((self.root / bg.REFERENCE).exists())

    def test_a_failing_build_exits_one(self):
        os.environ["GO"] = "/bin/false"
        self.addCleanup(os.environ.pop, "GO", None)
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = bg.main(["reference", "--root", str(self.root)])
        self.assertEqual(rc, 1)
        self.assertIn("builder-reference: go build ./cmd/andara-cli failed", err.getvalue())


if __name__ == "__main__":
    unittest.main()
