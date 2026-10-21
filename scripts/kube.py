#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""scripts/kube.py — every kubectl, helm and kind call names its cluster (AW-INF-053).

ADR-0013 decision 1: the context comes from one variable, ANDARA_KUBE_CONTEXT, default kind-andara,
and every call passes it: --context to kubectl, --kube-context to helm, and --name to kind, the
context without its kind- prefix. Nothing reads or moves the kubeconfig's current-context.

kubectl(...) and helm(...) return an argv. The first one a process builds checks that the context is
in the kubeconfig and prints `context: <ctx>` to stderr, never stdout, which several callers parse.
A missing context exits 3, naming the variable and the context, before anything reaches a cluster.
A script with its own error handling calls require() up front instead and gets Missing.
scripts/kube.sh is the shell twin.

  require [PROG]  check the context and print its line, as kube.sh's kube_require; a missing one
                  exits 3 with PROG's name on the message
  kind-name       print the kind cluster ANDARA_KUBE_CONTEXT names (make kind-load)

Exit: 0 ok · 2 usage, or kind-name given a context that doesn't start with kind- · 3 the context
isn't in the kubeconfig.
"""
import os
import sys

import yaml

ENV = "ANDARA_KUBE_CONTEXT"
DEFAULT = "kind-andara"
KIND_PREFIX = "kind-"
_checked = set()


class Missing(Exception):
    """The context isn't in the kubeconfig: a missing prerequisite, exit 3."""
    code = 3


class WrongContext(Exception):
    """A context this target can't act on: exit 2."""
    code = 2


def context(env=None):
    return (os.environ if env is None else env).get(ENV) or DEFAULT


def kubeconfig_paths(env=None):
    env = os.environ if env is None else env
    if env.get("KUBECONFIG"):
        return [p for p in env["KUBECONFIG"].split(os.pathsep) if p]
    return [os.path.join(os.path.expanduser("~"), ".kube", "config")]


def contexts(env=None):
    """Every context name in the kubeconfig, merged as kubectl merges KUBECONFIG's list. A file
    that is missing or unreadable contributes none."""
    names = set()
    for path in kubeconfig_paths(env):
        try:
            with open(path) as f:
                cfg = yaml.safe_load(f) or {}
        except (OSError, yaml.YAMLError):
            continue
        names.update(c.get("name") for c in cfg.get("contexts") or [] if isinstance(c, dict))
    return names


def require(env=None, ctx=None):
    """The context (ctx when a caller already resolved one, else the variable's), checked once per
    process and kubeconfig. Raises Missing when the kubeconfig doesn't have it."""
    ctx = ctx or context(env)
    key = (ctx, tuple(kubeconfig_paths(env)))
    if key in _checked:
        return ctx
    if ctx not in contexts(env):
        raise Missing("%s=%s: the kubeconfig has no context %s; `make cluster-up` creates kind-andara, "
                      "or set %s to a context it has" % (ENV, ctx, ctx, ENV))
    _checked.add(key)
    print("context: %s" % ctx, file=sys.stderr, flush=True)
    return ctx


def _ensure(env, ctx=None, prog=None):
    try:
        return require(env, ctx)
    except Missing as e:
        print("%s: %s" % (prog or os.path.basename(sys.argv[0] or "kube"), e), file=sys.stderr, flush=True)
        raise SystemExit(e.code)


def kubectl(*args, tool="kubectl", env=None, ctx=None):
    """argv for kubectl against the context."""
    return [tool, "--context", _ensure(env, ctx), *args]


def helm(*args, tool="helm", env=None, ctx=None):
    """argv for helm against the context."""
    return [tool, "--kube-context", _ensure(env, ctx), *args]


def kind_name(env=None):
    """The kind cluster the context names: the context without its kind- prefix."""
    ctx = context(env)
    if not ctx.startswith(KIND_PREFIX) or ctx == KIND_PREFIX:
        raise WrongContext("%s=%s is not a kind context (kind-<cluster>); kind targets act on kind "
                           "clusters only" % (ENV, ctx))
    return ctx[len(KIND_PREFIX):]


def box_only(env=None):
    """cluster-up, cluster-down and cluster-rebuild act on the box's cluster and no other."""
    ctx = context(env)
    if ctx != DEFAULT:
        raise WrongContext("%s=%s: this target creates and deletes the box's cluster, %s, only; unset "
                           "%s or set it to %s" % (ENV, ctx, DEFAULT, ENV, DEFAULT))


def main(argv):
    if argv[:1] == ["require"] and len(argv) <= 2:
        _ensure(None, prog=argv[1] if len(argv) == 2 else None)
        return 0
    if argv != ["kind-name"]:
        print("usage: scripts/kube.py require [PROG] | kind-name", file=sys.stderr)
        return 2
    try:
        print(kind_name())
    except WrongContext as e:
        print("kind: %s" % e, file=sys.stderr)
        return e.code
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
