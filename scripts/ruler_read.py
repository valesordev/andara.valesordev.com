#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The legacy ruler's rule config, read for the drills' backend selection (AW-INF-069, until AW-INF-077).

env_recover.py's fetch_ruler, moved here so env_recover.py and observe_unavailable.py share it: the one
direct Grafana call either script still makes, on make gcx-only-check's exception list until AW-INF-077
deletes the ruler, this file and the drills' legacy branch. `gcx alert rules list` cannot see the ruler's
rules, so "the rule is loaded" (AW-INF-034 AC-2) is still this read of namespace `andara`.

Credentials as env_recover.py took them before AW-INF-069: MIMIR_ADDRESS, MIMIR_TENANT_ID and MIMIR_API_KEY,
the first two defaulting from GRAFANA_CLOUD_PROM_URL / _PROM_USER (scripts/alerts_sync.py's resolve_env), the
key from GRAFANA_CLOUD_READ_TOKEN (which carries rules:read). Never printed.

fetch() returns the namespace's YAML; None for a 404 (the namespace is gone, cutover step 4(ii): "not live in
the ruler"); RulerError for any other HTTP status or a transport failure, a timeout included (inconclusive).
"""
import base64
import importlib.util
import os
import urllib.error
import urllib.request

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
NAMESPACE = "andara"
INTERVAL = 60            # the ruler's evaluation interval: every andara group runs at the default 1 m
VARS = ("MIMIR_ADDRESS", "MIMIR_TENANT_ID", "MIMIR_API_KEY")


class RulerError(Exception):
    """The ruler read failed other than with a 404: the run is inconclusive."""


def _sync():
    spec = importlib.util.spec_from_file_location("alerts_sync", os.path.join(HERE, "alerts_sync.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def credentials(environ):
    """(the MIMIR_* variables still unset, the environment with the defaults filled)."""
    sync = _sync()
    env = dict(sync.resolve_env(environ))
    key = env.get("MIMIR_API_KEY", "").strip() or environ.get("GRAFANA_CLOUD_READ_TOKEN", "").strip()
    if key:
        env["MIMIR_API_KEY"] = key
    else:
        env.pop("MIMIR_API_KEY", None)
    return sync.missing(env, VARS), env


def fetch(env, namespace=NAMESPACE, opener=None, timeout=60):
    """The ruler's YAML for namespace; None when it answers 404."""
    url = "%s/prometheus/config/v1/rules/%s" % (env["MIMIR_ADDRESS"].rstrip("/"), namespace)
    req = urllib.request.Request(url)
    token = base64.b64encode(("%s:%s" % (env["MIMIR_TENANT_ID"], env["MIMIR_API_KEY"])).encode()).decode()
    req.add_header("Authorization", "Basic " + token)
    req.add_header("X-Scope-OrgID", env["MIMIR_TENANT_ID"])
    opener = opener or urllib.request.build_opener(_sync().NoRedirect)
    try:
        with opener.open(req, timeout=timeout) as r:
            return r.read().decode()
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return None
        raise RulerError("the ruler refused the rules read for namespace %s: HTTP %d" % (namespace, e.code))
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        raise RulerError("could not reach the ruler (namespace %s): %s" % (namespace, e))


def rule_loaded(ruler_yaml, rule, namespace=NAMESPACE):
    """True when the ruler's namespace body names rule. None (a 404) is not loaded."""
    doc = yaml.safe_load(ruler_yaml or "") or {}
    groups = doc if isinstance(doc, dict) else {}
    return any(r.get("alert") == rule for g in (groups.get(namespace) or []) for r in (g.get("rules") or []))
