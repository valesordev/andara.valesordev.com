#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""WorldReadOnly's `for:` and ingress.degraded_hold change together (AW-INF-039 AC-5).

The client contract's hold floor assumes the rule's `for:`: a Partition a produce error marked
stays degraded for the hold, and the page must fire inside it and not resolve in the sawtooth's
gap, so `degraded_hold >= 2 * for + 30 s` (docs/specs/kafka/client-contract.md). `check` reads
the rule's `for:` from files/alerts.yaml, the server's DefaultIngressDegradedHold and
MinIngressDegradedHold from server/config/config.go, and keys.yaml's default, and returns one
line per violation. Both server values are held to the floor: the minimum is what an operator
can set, so it must clear it too. `make helm-test` runs it.
"""
import os
import re

import yaml

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
CONFIG_GO = os.path.join(REPO, "server", "config", "config.go")
KEYS = os.path.join(REPO, "deploy", "helm", "andara", "keys.yaml")
RULE = "WorldReadOnly"
KEY = "ingress.degraded_hold"
UNITS = {"ms": 0.001, "s": 1, "m": 60, "h": 3600}


def seconds(d):
    """A Prometheus or Go duration (`30s`, `2m`, `1m30s`) in seconds."""
    parts = re.findall(r"(\d+)(ms|s|m|h)", str(d))
    if not parts or "".join(n + u for n, u in parts) != str(d):
        raise ValueError("not a duration: %r" % d)
    return sum(int(n) * UNITS[u] for n, u in parts)


def rule_for(alerts_text, name=RULE):
    for g in yaml.safe_load(alerts_text)["groups"]:
        for r in g.get("rules", []):
            if r.get("alert") == name:
                return seconds(r.get("for", "0s"))
    raise ValueError("no rule %s in alerts.yaml" % name)


def go_seconds(config_text, const):
    m = re.search(r"\b%s\s*=\s*(\d+)\s*\*\s*time\.(Second|Minute)\b" % const, config_text)
    if not m:
        raise ValueError("no `%s = <n> * time.Second|Minute` in server/config/config.go" % const)
    return int(m.group(1)) * (60 if m.group(2) == "Minute" else 1)


def keys_default(keys_text, key=KEY):
    for k in yaml.safe_load(keys_text)["keys"]:
        if k["key"] == key:
            return seconds(k["default"])
    raise ValueError("no %s in keys.yaml" % key)


def check(alerts_text, config_text, keys_text):
    f = rule_for(alerts_text)
    floor = 2 * f + 30
    errors = []
    for const in ("DefaultIngressDegradedHold", "MinIngressDegradedHold"):
        v = go_seconds(config_text, const)
        if v < floor:
            errors.append("%s is %ds, below 2 x %s's for: (%ds) + 30 = %ds; raise them together "
                          "(docs/specs/kafka/client-contract.md)" % (const, v, RULE, f, floor))
    default = go_seconds(config_text, "DefaultIngressDegradedHold")
    kd = keys_default(keys_text)
    if kd != default:
        errors.append("keys.yaml's %s default is %ds, the server's DefaultIngressDegradedHold %ds"
                      % (KEY, kd, default))
    return errors


def check_repo():
    with open(ALERTS) as a, open(CONFIG_GO) as c, open(KEYS) as k:
        return check(a.read(), c.read(), k.read())
