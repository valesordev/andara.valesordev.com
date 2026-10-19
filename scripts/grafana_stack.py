#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""ENV → Grafana Cloud stack, the one mapping (ADR-0012 decision 12; AW-INF-075).

    scripts/grafana_stack.py <ENV>     prints the stack (its gcx context); exit 2 for an unknown ENV

The Makefile and every script that reads Grafana resolve ENV here, then call gcx with the
stack's context (scripts/gcx.py). ENV is the `environment` label value:

  local          solo7dev   environment="local", namespace="andara-local" (the cluster drills)
  dev, staging   solo7dev   environment="dev" / "staging"
  prod           solo7      environment="prod"

require() exits 3 naming GRAFANA_<STACK>_READ_TOKEN when the stack's read token is unset, before
anything calls gcx. Tokens come from the environment, after .local/box.env (scripts/tf.py's
load_box_env: a variable already set wins, a box.env readable by others is refused).
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import tf  # noqa: E402

STACKS = ("solo7dev", "solo7")
ENV_STACK = {"local": "solo7dev", "dev": "solo7dev", "staging": "solo7dev", "prod": "solo7"}


class Exit(Exception):
    def __init__(self, code, msg):
        super().__init__(msg)
        self.code = code


def stack(env):
    """The stack (and gcx context) ENV maps to; Exit 2 for anything outside the table."""
    if env not in ENV_STACK:
        raise Exit(2, "ENV=%s: not one of %s" % (env or "<unset>", ", ".join(ENV_STACK)))
    return ENV_STACK[env]


def selectors(env):
    """The label matchers a drill adds for ENV: environment always, namespace where it tells sources apart."""
    stack(env)
    sel = {"environment": env}
    if env == "local":
        # local has two sources (andara-local, andara-ci); the cluster drills read the kind cluster's.
        sel["namespace"] = "andara-local"
    return sel


def token_var(stack_name):
    return "GRAFANA_%s_READ_TOKEN" % stack_name.upper()


def environ(env=None):
    """The process environment with .local/box.env's values added where unset."""
    try:
        return tf.load_box_env(dict(os.environ if env is None else env))
    except tf.Exit as e:
        raise Exit(3, str(e))


def require(env, environment=None):
    """(stack, token) for ENV; Exit 3 naming the stack's read-token variable when it is unset."""
    s = stack(env)
    e = environ(environment)
    token = (e.get(token_var(s)) or "").strip()
    if not token:
        raise Exit(3, "%s is unset (ENV=%s reads stack %s); set it in the environment or .local/box.env"
                   % (token_var(s), env, s))
    return s, token


def main(argv):
    if len(argv) != 1:
        print("usage: grafana_stack.py <ENV>", file=sys.stderr)
        return 2
    try:
        print(stack(argv[0]))
    except Exit as e:
        print("grafana-stack: %s" % e, file=sys.stderr)
        return e.code
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
