#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Direct Grafana Cloud HTTP reads, for env_recover.py and observe_unavailable.py only (AW-INF-075).

observe_check.py's helpers before it moved to gcx (scripts/gcx.py), unchanged, so `make env-recover`
and `make observe-unavailable` behave as they did until AW-INF-069 moves them to the wrapper and
deletes this module. It is on make gcx-only-check's exception list (scripts/gcx_only_exceptions.txt)
until then; nothing new may import it.

Credentials as before: GRAFANA_CLOUD_READ_TOKEN and GRAFANA_CLOUD_<KIND>_URL / _<KIND>_USER from the
environment, never printed.
"""
import base64
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")


class BackendError(Exception):
    pass


def say(msg):
    print("observe-check: " + msg)


def need(name):
    v = os.environ.get(name, "").strip()
    if not v:
        say("no %s; cannot verify" % name)
        sys.exit(3)
    return v


class Backend:
    def __init__(self, kind, token):
        self.url = need("GRAFANA_CLOUD_%s_URL" % kind).rstrip("/")
        user = need("GRAFANA_CLOUD_%s_USER" % kind)
        self.auth = "Basic " + base64.b64encode(("%s:%s" % (user, token)).encode()).decode()
        self.kind = kind.lower()

    def get(self, path, params=None, allow_404=False):
        url = self.url + path + ("?" + urllib.parse.urlencode(params) if params else "")
        req = urllib.request.Request(url, headers={"Authorization": self.auth, "Accept": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=30) as r:
                return json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            if allow_404 and e.code == 404:
                return None
            # The body, not the request: the request carries the token.
            raise BackendError("%s %s: HTTP %d %s" % (self.kind, path, e.code, e.read()[:200].decode(errors="replace")))
        except (urllib.error.URLError, TimeoutError) as e:
            raise BackendError("%s %s: %s" % (self.kind, path, e))


def instant(prom, q):
    body = prom.get("/api/v1/query", {"query": q})
    if body.get("status") != "success":
        raise BackendError("prom query %r: %s" % (q, body.get("error")))
    return body["data"]["result"]
