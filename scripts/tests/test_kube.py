# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-053: every kubectl, helm and kind call names ANDARA_KUBE_CONTEXT's cluster.

The helpers (scripts/kube.py, scripts/kube.sh) against recording stubs; the static check
(scripts/kube_check.py, AC-1) against fixture trees and the repo; and a sweep that runs every
cluster-bound script against the stubs under a kubeconfig whose current-context is some-other
(AC-2, AC-3, AC-4, AC-8)."""
import io
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
from contextlib import redirect_stderr
from unittest import mock

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import kube  # noqa: E402
import kube_check  # noqa: E402

FIXTURE = os.path.join(REPO, "scripts", "tests", "fixtures", "kubeconfig")
STUB = textwrap.dedent("""\
    #!/bin/sh
    echo "$(basename "$0") $*" >> "$STUB_LOG"
    echo "Error from server (NotFound): stub" >&2
    exit 1
    """)


class Workdir(unittest.TestCase):
    """A temp dir with a copy of the fixture kubeconfig and recording kubectl, helm and docker stubs."""

    def setUp(self):
        self.d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.d)
        self.kubeconfig = os.path.join(self.d, "kubeconfig")
        shutil.copy(FIXTURE, self.kubeconfig)
        self.log = os.path.join(self.d, "calls")
        self.bin = os.path.join(self.d, "bin")
        os.mkdir(self.bin)
        # docker succeeds and prints nothing: image-check pulls before it reaches the cluster.
        for tool, body in (("kubectl", STUB), ("helm", STUB), ("docker", "#!/bin/sh\nexit 0\n")):
            path = os.path.join(self.bin, tool)
            with open(path, "w") as f:
                f.write(body)
            os.chmod(path, os.stat(path).st_mode | stat.S_IEXEC)
        kube._checked.clear()
        self.addCleanup(kube._checked.clear)

    def env(self, context=None, **extra):
        env = {k: v for k, v in os.environ.items() if k != kube.ENV}
        env.update(KUBECONFIG=self.kubeconfig, STUB_LOG=self.log, PATH=self.bin + os.pathsep + env["PATH"],
                   KUBECTL=os.path.join(self.bin, "kubectl"), HELM=os.path.join(self.bin, "helm"),
                   DOCKER=os.path.join(self.bin, "docker"),
                   # Never the box's real .local/box.env.
                   ANDARA_BOX_ENV=os.path.join(self.d, "no-box.env"))
        if context is not None:
            env[kube.ENV] = context
        env.update(extra)
        return env

    def calls(self):
        if not os.path.exists(self.log):
            return []
        with open(self.log) as f:
            return [line.split() for line in f.read().splitlines()]


class Helper(Workdir):
    """AC-2 to AC-4 for scripts/kube.py."""

    def patched(self, context=None):
        return mock.patch.dict(os.environ, {k: v for k, v in self.env(context).items()}, clear=True)

    def test_unset_is_kind_andara(self):
        with self.patched(), redirect_stderr(io.StringIO()):
            self.assertEqual(kube.kubectl("get", "pods"), ["kubectl", "--context", "kind-andara", "get", "pods"])
            self.assertEqual(kube.helm("list"), ["helm", "--kube-context", "kind-andara", "list"])

    def test_the_variable_overrides_it(self):
        with self.patched("kind-ci"), redirect_stderr(io.StringIO()):
            self.assertEqual(kube.kubectl("get", "pods", tool="bin/kubectl"),
                             ["bin/kubectl", "--context", "kind-ci", "get", "pods"])
            self.assertEqual(kube.helm("list")[:3], ["helm", "--kube-context", "kind-ci"])

    def test_the_context_line_is_printed_once_to_stderr(self):
        err = io.StringIO()
        with self.patched(), redirect_stderr(err):
            kube.kubectl("get", "pods")
            kube.helm("list")
        self.assertEqual(err.getvalue(), "context: kind-andara\n")

    def test_a_missing_context_exits_3_naming_the_variable_and_the_context(self):
        err = io.StringIO()
        with self.patched("kind-nope"), redirect_stderr(err), self.assertRaises(SystemExit) as e:
            kube.kubectl("get", "pods")
        self.assertEqual(e.exception.code, 3)
        self.assertIn("ANDARA_KUBE_CONTEXT=kind-nope: the kubeconfig has no context kind-nope", err.getvalue())

    def test_require_raises_missing_for_callers_with_their_own_exits(self):
        with self.patched("kind-nope"), self.assertRaises(kube.Missing):
            kube.require()

    def test_a_kubeconfig_list_is_merged(self):
        other = os.path.join(self.d, "other")
        with open(other, "w") as f:
            yaml.safe_dump({"contexts": [{"name": "gke_x", "context": {}}]}, f)
        env = self.env("gke_x", KUBECONFIG=os.pathsep.join([self.kubeconfig, other]))
        with redirect_stderr(io.StringIO()):
            self.assertEqual(kube.require(env), "gke_x")

    def test_kind_name_strips_the_prefix_and_refuses_another_context(self):
        self.assertEqual(kube.kind_name({}), "andara")
        self.assertEqual(kube.kind_name({kube.ENV: "kind-ci"}), "ci")
        for ctx in ("gke_x", "kind-"):
            with self.subTest(ctx=ctx), self.assertRaises(kube.WrongContext):
                kube.kind_name({kube.ENV: ctx})

    def test_box_only_refuses_anything_but_kind_andara(self):
        kube.box_only({})
        kube.box_only({kube.ENV: "kind-andara"})
        with self.assertRaises(kube.WrongContext) as e:
            kube.box_only({kube.ENV: "kind-ci"})
        self.assertIn("ANDARA_KUBE_CONTEXT=kind-ci", str(e.exception))


# A lone & (not &&, >&, <&, &>): something on the line runs in the background.
BACKGROUND = re.compile(r"(?<![&<>])&(?![&>])")
QUOTED = re.compile(r"'[^']*'|\"(?:\\.|[^\"\\])*\"")


def background_findings(name, text):
    """Logical lines (continuations joined, comments and quoted text dropped) that call kube_kubectl
    or kube_helm and background anything, or call kube_kubectl_exec and background nothing. A whole
    line, not a segment: bash's & backgrounds the whole and-or list or pipeline before it, so
    `kube_kubectl … || true &` is a subshell too. That also refuses `kube_kubectl get x; sleep 1 &`,
    which errs toward failing. A line ending in &&, || or | continues the list, as in bash, so it is
    joined with the next. A group spanning lines (`(` … `) &`, a function later backgrounded) is out
    of its reach; no script has one."""
    lines = []
    for n, line in kube_check.logical(text.splitlines()):
        code = QUOTED.sub("''", kube_check.strip_comment(line))
        if lines and re.search(r"(&&|\|\|?)\s*$", lines[-1][1]):
            lines[-1] = (lines[-1][0], lines[-1][1] + " " + code)
        else:
            lines.append((n, code))
    out = []
    for n, code in lines:
        calls = re.findall(r"\b(coproc\s+)?kube_(kubectl_exec|kubectl|helm)\b", code)
        if not calls:
            continue
        bg = bool(BACKGROUND.search(code)) or any(c for c, _ in calls)
        plain = any(t != "kubectl_exec" for _, t in calls)
        if (plain and bg) or (not plain and not bg):
            out.append("%s:%d" % (name, n))
    return out


class ShellHelper(Workdir):
    """scripts/kube.sh, the shell twin."""

    def sh(self, script, context=None):
        return subprocess.run(["bash", "-c", '. scripts/kube.sh\n' + script], cwd=REPO, capture_output=True,
                              text=True, env=self.env(context))

    def test_every_call_carries_the_context(self):
        for ctx, want in ((None, "kind-andara"), ("kind-ci", "kind-ci")):
            with self.subTest(ctx=ctx):
                open(self.log, "w").close()
                p = self.sh("kube_require t\nkube_kubectl get pods || true\nkube_helm list || true\n", ctx)
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertEqual(p.stderr.splitlines()[0], "context: " + want)
                self.assertEqual(self.calls(), [["kubectl", "--context", want, "get", "pods"],
                                                ["helm", "--kube-context", want, "list"]])

    def test_a_backgrounded_call_is_killable_by_its_pid(self):
        """Codex on #605: `kube_kubectl … &` backgrounds a subshell, so `kill $!` left the port-forward
        running. kube_kubectl_exec replaces that subshell with kubectl, so $! is kubectl's PID."""
        pidfile = os.path.join(self.d, "pid")
        sleeper = os.path.join(self.bin, "kubectl")
        with open(sleeper, "w") as f:
            # The sleep holds no pipe of the test's, so a regression fails at once, not after 30 s.
            f.write('#!/bin/sh\necho $$ > "%s"\nexec sleep 30 >/dev/null 2>&1 </dev/null\n' % pidfile)
        p = self.sh('kube_kubectl_exec -n x port-forward pod/a 1:1 &\npf=$!\n'
                    'for _ in $(seq 50); do [ -s "%s" ] && break; sleep 0.1; done\n'
                    'kill "$pf"; echo "$pf"\n' % pidfile)
        self.assertEqual(p.returncode, 0, p.stderr)
        with open(pidfile) as f:
            self.assertEqual(f.read().strip(), p.stdout.strip())

    def test_a_foreground_exec_refuses_and_the_script_goes_on(self):
        p = self.sh("kube_kubectl_exec get pods; echo \"rc=$?\"\necho after\n")
        self.assertEqual(p.stdout.split(), ["rc=2", "after"], p.stderr)
        self.assertIn("not in the script's own shell", p.stderr)
        self.assertEqual(self.calls(), [])

    def test_the_background_guard(self):
        bad = ['kube_kubectl -n x port-forward a 1:1 >/dev/null 2>&1 &',
               'kube_kubectl -n x port-forward a 1:1 2>&1 & pf=$!',
               'kube_kubectl -n x port-forward a 1:1 & # traefik',
               'kube_kubectl -n x \\\n  port-forward a 1:1 &',
               'coproc kube_kubectl -n x port-forward a 1:1',
               'kube_kubectl_exec get pods || true',
               'kube_kubectl -n x port-forward a 1:1 || true &',
               'kube_kubectl -n x port-forward a 1:1 && echo ok &',
               'kube_kubectl -n x port-forward a 1:1 | tee log &',
               '{ kube_kubectl -n x port-forward a 1:1; } &',
               'kube_kubectl get x; sleep 1 &',
               'kube_kubectl -n x port-forward a 1:1 & kube_kubectl_exec get y &',
               'kube_kubectl -n x port-forward a 1:1 &&\n  true &',
               'kube_kubectl -n x port-forward a 1:1 ||\n  true &',
               'kube_kubectl -n x port-forward a 1:1 |\n  tee log &']
        good = ['kube_kubectl get x >/dev/null &&\n  true',
                'kube_kubectl get x >/dev/null 2>&1 || kube_kubectl create x',
                'kube_kubectl_exec -n x port-forward a 1:1 >"$W/pf.log" 2>&1 &',
                'kube_kubectl get x &>/dev/null',
                'kube_kubectl get x  # & later',
                'kube_kubectl get x -l "a&b" -o \'jsonpath={.a}&\'',
                'kube_kubectl apply -f - <&3',
                'kube_kubectl get x -l "a\\"&b"']
        for text in bad:
            with self.subTest(bad=text):
                self.assertTrue(background_findings("t.sh", text), text)
        for text in good:
            with self.subTest(good=text):
                self.assertEqual(background_findings("t.sh", text), [], text)

    def test_no_script_backgrounds_the_function(self):
        sdir = os.path.join(REPO, "scripts")
        found = []
        for name in sorted(os.listdir(sdir)):
            if name.endswith(".sh") and name != "kube.sh":  # kube.sh defines them
                with open(os.path.join(sdir, name)) as f:
                    found += background_findings(name, f.read())
        self.assertEqual(found, [], "background with kube_kubectl_exec, whose $! is kubectl's, and only that")

    def test_a_missing_context_exits_3_before_any_call(self):
        p = self.sh("kube_require t\nkube_kubectl get pods\n", "kind-nope")
        self.assertEqual(p.returncode, 3)
        self.assertIn("t: ANDARA_KUBE_CONTEXT=kind-nope: the kubeconfig has no context kind-nope", p.stderr)
        self.assertEqual(self.calls(), [])


# (name, argv from the repo root). Every script that reaches a cluster, at its cheapest entry point.
# The stubs fail every call, so each stops at its first; the sweep checks that call and that there was one.
PY = sys.executable
SWEEP = [
    ("helm_install.sh", ["bash", "scripts/helm_install.sh", "local", "andara-server", "dev"]),
    ("kafka.sh install", ["bash", "scripts/kafka.sh", "install", "dev"]),
    ("kafka.sh up", ["bash", "scripts/kafka.sh", "up"]),
    ("shared.py check", [PY, "scripts/shared.py", "check"]),
    ("kafka.sh bounce", ["bash", "scripts/kafka.sh", "bounce", "dev"]),
    ("stream_soak.sh", ["bash", "scripts/stream_soak.sh", "dev", "1s"]),
    ("image_check.sh", ["bash", "scripts/image_check.sh", "dev", "ghcr.io/valesordev/andara-server", "t"]),
    ("platform_up.py", [PY, "scripts/platform_up.py", "up", "ci"]),
    ("cluster_secrets.py", [PY, "scripts/cluster_secrets.py", "export"]),
    ("argocd.py", [PY, "scripts/argocd.py", "status", "dev"]),
    ("world_reset.py", [PY, "scripts/world_reset.py", "dev", "andara-dev"]),
    ("env_destroy.py", [PY, "scripts/env_destroy.py", "dev", "andara-dev"]),
    ("server_ctl.py", [PY, "scripts/server_ctl.py", "stop", "dev"]),
    ("projector.py", [PY, "scripts/projector.py", "stop", "dev"]),
    ("objectstore.py", [PY, "scripts/objectstore.py", "install", "dev"]),
    ("topics.py", [PY, "scripts/topics.py", "diff", "--env", "dev"]),
]


class Sweep(Workdir):
    """AC-2, AC-3 and AC-8: every cluster-bound script, its context unset and then kind-ci, against the
    stubs; AC-4: a context the kubeconfig lacks exits 3 before any call."""

    def run_one(self, argv, context=None):
        open(self.log, "w").close()
        return subprocess.run(argv, cwd=REPO, capture_output=True, text=True, env=self.env(context), timeout=120)

    def assert_named(self, name, p, want):
        calls = [c for c in self.calls() if c[0] in ("kubectl", "helm")]
        self.assertTrue(calls, "%s made no kubectl or helm call: %s" % (name, p.stderr))
        # The stubs never write the kubeconfig, so a move through them shows only in the call.
        moves = [c for c in calls if {"use-context", "current-context"} & set(c)
                 or ("set-context" in c and "--current" in c)]
        self.assertEqual(moves, [], name)
        for c in calls:
            flag = "--context" if c[0] == "kubectl" else "--kube-context"
            self.assertEqual(c[1:3], [flag, want], "%s: %s" % (name, " ".join(c)))
        self.assertIn("context: " + want, p.stderr.splitlines(), name)

    def test_every_script_names_the_context(self):
        with open(self.kubeconfig, "rb") as f:
            before = f.read()
        for ctx, want in ((None, "kind-andara"), ("kind-ci", "kind-ci")):
            for name, argv in SWEEP:
                with self.subTest(script=name, context=want):
                    self.assert_named(name, self.run_one(argv, ctx), want)
        with open(self.kubeconfig, "rb") as f:
            after = f.read()
        self.assertEqual(after, before, "a script wrote the kubeconfig")
        self.assertEqual(yaml.safe_load(after)["current-context"], "some-other")

    def test_a_missing_context_exits_3_before_any_call(self):
        for name, argv in SWEEP:
            with self.subTest(script=name):
                p = self.run_one(argv, "kind-nope")
                self.assertEqual(p.returncode, 3, "%s: %s" % (name, p.stderr))
                self.assertIn("ANDARA_KUBE_CONTEXT=kind-nope", p.stderr, name)
                self.assertEqual([c for c in self.calls() if c[0] in ("kubectl", "helm")], [], name)

    def test_the_sweep_covers_every_script_that_calls_kubectl_or_helm(self):
        """AC-6: the grep's files, less the helpers, cluster.py (fixed to andara and tested in
        test_cluster.py), the offline helm template callers and the scripts that need Grafana Cloud,
        a running server, or a signed push to origin (promote.py) before their first call (each
        one's kubectl goes through kube.py, which kube_check.py enforces)."""
        offline = {"k8s_dry.sh", "helm_test.py", "values_schema.py", "helm_image_args.sh", "bootstrap.sh",
                   "measure_tick.sh"}
        elsewhere = {"cluster.py", "content_seed.sh", "env_recover.py", "observe_check.py",
                     "observe_unavailable.py", "promote.py"}
        helpers = {"kube.py", "kube.sh", "kube_check.py"}
        swept = {name.split()[0] for name, _ in SWEEP}
        sdir = os.path.join(REPO, "scripts")
        calling = set()
        for f in os.listdir(sdir):
            path = os.path.join(sdir, f)
            if os.path.isfile(path) and f.endswith((".py", ".sh")):
                with open(path) as fh:
                    text = fh.read()
                if "kube.kubectl(" in text or "kube.helm(" in text or "kube_kubectl" in text or "kube_helm" in text:
                    calling.add(f)
        self.assertEqual(calling - helpers - swept - offline - elsewhere, set())


class Check(unittest.TestCase):
    """AC-1: kube_check.py names the file and line of every bare call, and passes the repo."""

    def tree(self, files):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        for rel, text in files.items():
            path = os.path.join(d, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, "w") as f:
                f.write(textwrap.dedent(text))
        return d

    def findings(self, files, allow=()):
        return kube_check.check(self.tree(files), list(allow))

    def test_the_repo_is_clean(self):
        self.assertEqual(kube_check.check(), [])

    def test_shell(self):
        out = self.findings({"scripts/a.sh": """\
            #!/bin/sh
            kubectl get pods
            kube_kubectl get pods
            x="$(helm -n ns get values andara)"
            helm template andara deploy/helm/andara
            kubectl --context "$ANDARA_KUBE_CONTEXT" get ns
            echo "run kubectl -n x describe pod"
            # kubectl get nodes, in a comment
            kubectl -n x \\
              --context "$ANDARA_KUBE_CONTEXT" get pods
            kind load docker-image x
            "$BIN/kubectl" version --client
            kubectl config get-contexts kind-andara
            kubectl config use-context kind-andara
            env kubectl get pods
            timeout 5 kubectl get pods
            sudo helm list
            kube_kubectl config set current-context kind-ci
            """})
        self.assertEqual(out, [
            "scripts/a.sh:2: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/a.sh:4: helm without --kube-context \"$ANDARA_KUBE_CONTEXT\" (use kube_helm or kube.helm)",
            "scripts/a.sh:11: kind without --name from ANDARA_KUBE_CONTEXT (scripts/kube.py kind-name)",
            "scripts/a.sh:14: moves the kubeconfig's current-context",
            "scripts/a.sh:14: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/a.sh:15: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/a.sh:16: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/a.sh:17: helm without --kube-context \"$ANDARA_KUBE_CONTEXT\" (use kube_helm or kube.helm)",
            "scripts/a.sh:18: moves the kubeconfig's current-context",
        ])

    def test_set_context_current_is_refused_but_set_is_not(self):
        out = self.findings({"scripts/b.sh": """\
            kubectl config set-context --current --namespace=x
            kubectl config set clusters.kind-andara.server https://x
            """})
        self.assertEqual([o.split(": ")[0] + ": " + o.split(": ")[1].split()[0] for o in out],
                         ["scripts/b.sh:1: moves", "scripts/b.sh:1: kubectl", "scripts/b.sh:2: kubectl"])

    def test_python(self):
        out = self.findings({"scripts/c.py": """\
            import subprocess
            import kube
            subprocess.run(["kubectl", "-n", "x", "get", "pods"])
            subprocess.run(kube.kubectl("-n", "x", "get", "pods"))
            subprocess.run(["helm", "template", "andara", "chart"])
            run(kubectl_path(), "get", "nodes")
            run(self.helm_path, "list")
            KINDS = ("kind",)
            p = shutil.which("kubectl")
            subprocess.run(["kubectl", "config", "use-context", "x"])
            merge(kubectl_bin, path)
            subprocess.run(["helm", *args])
            subprocess.run(kube.kubectl("config", "set", "current-context", "x"))
            """})
        self.assertEqual(out, [
            "scripts/c.py:3: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/c.py:6: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/c.py:7: helm without --kube-context \"$ANDARA_KUBE_CONTEXT\" (use kube_helm or kube.helm)",
            "scripts/c.py:10: moves the kubeconfig's current-context",
            "scripts/c.py:10: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "scripts/c.py:12: helm without --kube-context \"$ANDARA_KUBE_CONTEXT\" (use kube_helm or kube.helm)",
            "scripts/c.py:13: moves the kubeconfig's current-context",
        ])

    def test_makefile_recipes_and_workflow_run_steps(self):
        out = self.findings({
            "Makefile": "x:\n\tkubectl get pods\n\t@echo kubectl\ny := kubectl get pods\nz:\n\t$(KUBECTL) get x\n"
                        "\t@kubectl get pods\n\t@-helm list\nw := $(shell kubectl get ns)\n",
            ".github/workflows/w.yaml": textwrap.dedent("""\
                jobs:
                  j:
                    steps:
                      - uses: helm/kind-action@v1
                      - run: helm list -A
                      - name: block
                        run: |
                          set -e
                          kubectl -n x get pods
                          kubectl --context "$ANDARA_KUBE_CONTEXT" -n x get pods
                """),
        })
        self.assertEqual(out, [
            "Makefile:2: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "Makefile:6: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "Makefile:7: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            "Makefile:8: helm without --kube-context \"$ANDARA_KUBE_CONTEXT\" (use kube_helm or kube.helm)",
            "Makefile:9: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
            ".github/workflows/w.yaml:5: helm without --kube-context \"$ANDARA_KUBE_CONTEXT\" (use kube_helm or kube.helm)",
            ".github/workflows/w.yaml:9: kubectl without --context \"$ANDARA_KUBE_CONTEXT\" (use kube_kubectl or kube.kubectl)",
        ])

    def test_an_allowed_call_passes_and_an_unused_entry_is_a_finding(self):
        files = {"scripts/d.sh": "kind create cluster --name andara\n"}
        self.assertEqual(self.findings(files, [("scripts/d.sh", "kind create", "why")]), [])
        self.assertEqual(self.findings(files, [("scripts/d.sh", "kind create", "why"), ("scripts/d.sh", "gone", "why")]),
                         ["scripts/d.sh: allow-list entry 'gone' matches no call; remove it"])

    def test_scripts_tests_are_not_scanned(self):
        self.assertEqual(self.findings({"scripts/tests/t.sh": "kubectl get pods\n"}), [])


if __name__ == "__main__":
    unittest.main()
