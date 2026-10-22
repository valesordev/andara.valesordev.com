# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-055 AC-6: no script in scripts/ names a topic bare.

On the shared broker every environment's topics are `<prefix>.<name>` (`dev.andara.commands.v1`),
and scripts/topics.py is the one place that builds the name. A bare `andara.commands.v1` in a
script reaches no environment's topic on the shared broker, or, with an empty prefix, prod's.
So a declared topic name may appear in scripts/ only in a comment or a docstring, or on a line
ALLOW names with its reason. The names are deploy/kafka/topics.yaml's, so a topic declared later
is covered, and a protobuf package such as `andara.game.v1` isn't mistaken for one.
"""
import io
import os
import re
import sys
import tempfile
import tokenize
import unittest

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import topics  # noqa: E402

# (path, text on the line, reason). Each entry must still match a line, or it's a finding.
ALLOW = [
    ("scripts/world_reset.py", "RESET_TOPICS = [",
     "the names within an environment; reset_names puts the environment's prefix on them"),
]

SKIP_DIRS = {"tests", "__pycache__"}


def declared(decl=topics.DECL):
    return [t["name"] for t in topics.parse_declaration(decl)[1]]


def pattern(names):
    # Not after a name character or a dot, so `dev.andara.commands.v1` and `{p}.andara…` aren't bare.
    return re.compile(r"(?<![A-Za-z0-9_.{}-])(%s)(?![A-Za-z0-9_])" % "|".join(re.escape(n) for n in names))


def python_strings(text):
    """(line, string) of every string literal that is not a docstring. Comments aren't tokens
    we look at."""
    out = []
    toks = [t for t in tokenize.generate_tokens(io.StringIO(text).readline)
            if t.type not in (tokenize.NL, tokenize.COMMENT)]
    starts = (tokenize.NEWLINE, tokenize.INDENT, tokenize.DEDENT, tokenize.ENCODING)
    for i, t in enumerate(toks):
        name = tokenize.tok_name[t.type]
        if t.type != tokenize.STRING and name not in ("FSTRING_MIDDLE", "TSTRING_MIDDLE"):
            continue
        docstring = (t.type == tokenize.STRING and (i == 0 or toks[i - 1].type in starts)
                     and i + 1 < len(toks) and toks[i + 1].type in (tokenize.NEWLINE, tokenize.ENDMARKER))
        if not docstring:
            out.append((t.start[0], t.string))
    return out


def shell_code(text):
    """(line, text) with each line's comment removed: a `#` at the start or after whitespace."""
    return [(n, re.split(r"(?:^|\s)#", line, maxsplit=1)[0]) for n, line in enumerate(text.splitlines(), 1)]


def findings(root, names, allow=ALLOW, rel_to=REPO):
    pat = pattern(names)
    found, used = [], set()
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for fn in sorted(filenames):
            path = os.path.join(dirpath, fn)
            rel = os.path.relpath(path, rel_to)
            if fn.endswith(".py"):
                with open(path) as f:
                    text = f.read()
                pieces = python_strings(text)
            elif fn.endswith(".sh"):
                with open(path) as f:
                    text = f.read()
                pieces = shell_code(text)
            else:
                continue
            lines = text.splitlines()
            for n, piece in pieces:
                m = pat.search(piece)
                if not m:
                    continue
                hit = [a for a in allow if a[0] == rel and a[1] in lines[n - 1]]
                if hit:
                    used.add(hit[0][:2])
                    continue
                found.append("%s:%d: bare topic %s; build it with topics.topic_name(<prefix>, …)" % (rel, n, m.group(1)))
    for a in allow:
        if a[:2] not in used:
            found.append("%s: ALLOW entry %r matches no line any more; remove it" % (a[0], a[1]))
    return found


class Scripts(unittest.TestCase):
    def test_no_script_names_a_topic_bare(self):
        self.assertEqual(findings(os.path.join(REPO, "scripts"), declared()), [])


class Fixtures(unittest.TestCase):
    NAMES = ["andara.commands.v1", "andara.content.active.v1"]

    def scan(self, files, allow=()):
        with tempfile.TemporaryDirectory() as d:
            for name, text in files.items():
                with open(os.path.join(d, name), "w") as f:
                    f.write(text)
            return findings(d, self.NAMES, allow=list(allow), rel_to=d)

    def test_a_bare_literal_is_named_with_file_and_line(self):
        got = self.scan({"a.py": 'x = 1\nTOPIC = "andara.commands.v1"\n',
                         "b.sh": 'echo hi\nrpk topic describe andara.content.active.v1\n'})
        self.assertEqual(got, ["a.py:2: bare topic andara.commands.v1; build it with topics.topic_name(<prefix>, …)",
                               "b.sh:2: bare topic andara.content.active.v1; build it with topics.topic_name(<prefix>, …)"])

    def test_comments_docstrings_and_prefixed_names_pass(self):
        got = self.scan({"a.py": '"""Reads andara.commands.v1."""\n# andara.commands.v1\n'
                                 'def f(p):\n    """andara.commands.v1 again."""\n'
                                 '    return "%s.andara.commands.v1" % p, f"{p}.andara.commands.v1", "dev.andara.commands.v1"\n',
                         "b.sh": '# andara.commands.v1\nrpk topic list  # andara.commands.v1\n'
                                 'rpk topic describe "$P.andara.commands.v1"\n'})
        self.assertEqual(got, [])

    def test_protobuf_packages_and_longer_names_are_not_topics(self):
        got = self.scan({"a.py": 'S = "andara.game.v1.Game/OpenSession"\nT = "andara.commands.v10"\n'})
        self.assertEqual(got, [])

    def test_an_fstring_body_is_scanned(self):
        got = self.scan({"a.py": 'def f(x):\n    return f"{x} andara.commands.v1"\n'})
        self.assertEqual(len(got), 1, got)

    def test_allow_is_by_file_and_line_and_must_still_match(self):
        files = {"a.py": 'RESET = ["andara.commands.v1"]\n'}
        self.assertEqual(self.scan(files, [("a.py", "RESET = [", "why")]), [])
        self.assertEqual(len(self.scan(files, [("b.py", "RESET = [", "why")])), 2)
        self.assertEqual(self.scan({"a.py": "x = 1\n"}, [("a.py", "RESET = [", "why")]),
                         ["a.py: ALLOW entry 'RESET = [' matches no line any more; remove it"])


if __name__ == "__main__":
    unittest.main()
