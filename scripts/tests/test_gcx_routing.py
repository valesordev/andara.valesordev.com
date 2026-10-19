# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-075 AC-3: --context routes every adapter class to the stack it names, never to the config's current one.

The pinned bin/gcx (make bootstrap) against two local HTTP stubs standing in for solo7dev and solo7 on
distinct ports, with the config's current-context set to the other stack. No credentials: the stubs answer
anything with an empty success. v0.2.11 ignored --context in some operations; this is the test that would
have caught it.
"""
import json
import os
import re
import shutil
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest import mock

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
sys.path.insert(0, os.path.join(REPO, "scripts"))
import gcx  # noqa: E402

TYPES = {gcx.PROM: "prometheus", gcx.LOGS: "loki", gcx.TRACES: "tempo"}


class Stub(BaseHTTPRequestHandler):
    def _answer(self):
        self.server.seen.append(self.path)
        n = int(self.headers.get("Content-Length") or 0)
        if n:
            self.rfile.read(n)
        path = self.path.split("?")[0]
        if path.startswith("/api/datasources/uid/") and path.count("/") == 4:
            uid = path.rsplit("/", 1)[1]
            body = {"uid": uid, "type": TYPES.get(uid, "prometheus"), "name": uid, "id": 1}
        else:
            body = {"status": "success", "data": {"resultType": "vector", "result": []}}
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    do_GET = do_POST = _answer

    def log_message(self, *args):
        pass


def serve():
    s = ThreadingHTTPServer(("127.0.0.1", 0), Stub)
    s.seen = []
    threading.Thread(target=s.serve_forever, daemon=True).start()
    return s


# One call per adapter class in ADR-0012 decision 2's table.
ADAPTERS = {
    "alert rules": lambda ctx, kw: gcx.rule_groups(ctx, **kw),
    "alert instances": lambda ctx, kw: gcx.instances(ctx, "^AndaraServerUnavailable$", **kw),
    "metrics": lambda ctx, kw: gcx.metrics_query(ctx, 'up{job="andara-server"}', **kw),
    "irm": lambda ctx, kw: gcx.irm_alert_groups(ctx, **kw),
}


@unittest.skipUnless(os.path.exists(gcx.GCX) or os.environ.get("CI"), "bin/gcx absent: run make bootstrap")
class Routing(unittest.TestCase):

    @classmethod
    def setUpClass(cls):
        cls.stubs = {"solo7dev": serve(), "solo7": serve()}
        cls.tmp = tempfile.mkdtemp()
        cls.config = os.path.join(cls.tmp, "gcx.yaml")

    @classmethod
    def tearDownClass(cls):
        for s in cls.stubs.values():
            s.shutdown()
        shutil.rmtree(cls.tmp)

    def write_config(self, current):
        """make gcx-config's own rendering, pointed at the stubs. gcx discovers a *.grafana.net stack's ID
        itself (seen live with this rendering, 2026-10-19: `config check` says "Context type: Grafana Cloud");
        a 127.0.0.1 server has nothing to discover, so the test adds `org-id: 1` and changes nothing else."""
        urls = {name: "http://127.0.0.1:%d" % s.server_address[1] for name, s in self.stubs.items()}
        order = [current] + [n for n in self.stubs if n != current]
        with mock.patch.object(gcx, "grafana_url", lambda stack: urls[stack]):
            text = gcx.render_config({n: "stub" for n in order})
        text = re.sub(r"^(      token: .*)$", r"\1\n      org-id: 1", text, flags=re.M)
        self.assertIn("current-context: %s\n" % current, text)
        with open(self.config, "w") as f:
            f.write(text)

    def test_each_adapter_reaches_only_the_requested_stack(self):
        env = {"PATH": os.environ.get("PATH", ""), "HOME": self.tmp, "GCX_CONFIG": self.config}
        for want, other in (("solo7dev", "solo7"), ("solo7", "solo7dev")):
            self.write_config(current=other)
            for name, call in ADAPTERS.items():
                with self.subTest(context=want, adapter=name):
                    for s in self.stubs.values():
                        s.seen.clear()
                    call(want, {"env": env})
                    self.assertTrue(self.stubs[want].seen, "the %s stub received nothing" % want)
                    self.assertEqual(self.stubs[other].seen, [], "the current-context's stub was called")


if __name__ == "__main__":
    unittest.main()
