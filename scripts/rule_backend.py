#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""Which backend evaluates a rule for ENV, and that rule's state read through gcx (AW-INF-069).

env_recover.py and observe_unavailable.py assert on what a rule does. Until cutover step 4(ii-b) the legacy
ruler in `solo7` evaluates dev's rules and the Grafana-managed ones in solo7dev are paused; after it, the
reverse (ADR-0012 decision 7: never both). So each run reads both, before its first poll:

  grafana <stack>   gcx's rule in ENV's stack (scripts/grafana_stack.py) is live: defined, not paused,
                    health ok, last evaluated within twice its group's interval (gcx.rule_check);
  ruler solo7       the ruler's namespace `andara` names the rule (scripts/ruler_read.py). A 404 for the
                    namespace is "not live in the ruler"; any other ruler error is inconclusive.

Live in exactly one is that backend. Live in both, or in neither, is inconclusive (exit 1), naming which
and why. Reads then follow the backend: instance state through `gcx alert instances list` (the ruler's with
--datasource grafanacloud-prom against solo7), series through `gcx metrics query` against its stack. The
Grafana-managed backend filters on environment and namespace; the legacy one on namespace only, because
dev's series and the ruler's alerts in solo7 carry no `environment`, and a filter on it would match nothing.

One vocabulary (gcx.state): firing, pending, normal. A poll's state is firing if any instance is, pending if
any is, normal otherwise; a state outside the vocabulary is recorded as gcx printed it, and fails the run.

AW-INF-077 deletes the ruler branch with ruler_read.py.
"""
import datetime
import os
import re
import sys
import threading
import time

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import gcx  # noqa: E402
import grafana_stack  # noqa: E402
import ruler_read  # noqa: E402

REPO = os.path.dirname(HERE)
ALERTS = os.path.join(REPO, "deploy", "helm", "andara", "files", "alerts.yaml")
LEGACY_STACK = "solo7"
POLL = 10                 # seconds between polls, at most (ADR-0012 decision 2)
GRAFANA, RULER = "grafana", "ruler"


class Inconclusive(Exception):
    """The run cannot prove anything about the rule: exit 1."""


def now_utc():
    return datetime.datetime.now(datetime.timezone.utc)


def stamp(t):
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t))


def alerts_rule(rule, path=None):
    """(group name, the rule's entry) from alerts.yaml."""
    with open(path or ALERTS) as f:
        for g in yaml.safe_load(f)["groups"]:
            for r in g["rules"]:
                if r["alert"] == rule:
                    return g["name"], r
    raise Inconclusive("no %s in %s" % (rule, path or ALERTS))


def credentials(env, environ=None):
    """(ENV's stack, the ruler read's environment, the merged environment) once every credential the drill
    reads is set; grafana_stack.Exit(3) naming the first one unset, before any gcx call."""
    merged = grafana_stack.environ(environ)
    stack, _ = grafana_stack.require(env, merged)
    legacy = grafana_stack.token_var(LEGACY_STACK)
    if not (merged.get(legacy) or "").strip():
        raise grafana_stack.Exit(3, "%s is unset (backend selection reads the legacy ruler's alerts in %s, until "
                                    "AW-INF-077); set it in the environment or .local/box.env" % (legacy, LEGACY_STACK))
    missing, ruler_env = ruler_read.credentials(merged)
    if missing:
        raise grafana_stack.Exit(3, "%s unset (backend selection reads the legacy ruler's rule config, until "
                                    "AW-INF-077)" % ", ".join(missing))
    return stack, ruler_env, merged


class Sample:
    def __init__(self, t, state, raw):
        self.t, self.state, self.raw = t, state, raw

    def __repr__(self):
        return "Sample(%s, %s, %s)" % (stamp(self.t), self.state, self.raw)


def fold(instances):
    """One poll's state over the instances it matched: (mapped, raw as gcx printed it)."""
    states = [i["state"] for i in instances]
    raw = ", ".join(str(i.get("raw_state")) for i in instances) or "—"
    known = {gcx.FIRING, gcx.PENDING, gcx.NORMAL}
    odd = [s for s in states if s not in known]
    if odd:
        return odd[0], raw
    for s in (gcx.FIRING, gcx.PENDING):
        if s in states:
            return s, raw
    return gcx.NORMAL, raw


class Backend:
    """The rule's selected backend: where its state and its series are read."""

    def __init__(self, kind, stack, rule, group, interval, selectors, query=None, ruler_env=None, env=None,
                 fetch=None):
        self.kind, self.stack, self.rule, self.group = kind, stack, rule, group
        self.interval, self.selectors, self.query = int(interval), dict(selectors), query
        self.ruler_env, self.env, self.fetch = ruler_env, env, fetch or ruler_read.fetch

    def __str__(self):
        return "%s %s" % (self.kind, self.stack)

    @property
    def settle(self):
        """ALERT_SETTLE: two of the group's evaluations and one poll."""
        return 2 * self.interval + POLL

    @property
    def matchers(self):
        """The PromQL label matchers series reads add: environment and namespace, or namespace alone."""
        return ",".join('%s="%s"' % (k, v) for k, v in sorted(self.selectors.items()))

    def header(self):
        """What the run reads, printed before its first poll."""
        return ["backend: %s" % self,
                "rule %s (group %s), stack %s, gcx context %s, filter %s, poll %ds, ALERT_SETTLE %ds "
                "(2 × %ds interval + %ds poll)" % (self.rule, self.group, self.stack, self.stack, self.matchers,
                                                  POLL, self.settle, self.interval, POLL)]

    def for_rule(self, rule):
        """The same backend for another rule of alerts.yaml (its group read from the file)."""
        group, _ = alerts_rule(rule)
        return Backend(self.kind, self.stack, rule, group, self.interval, self.selectors, None, self.ruler_env,
                       self.env, self.fetch)

    def instances(self):
        name = "^%s$" % re.escape(self.rule)
        if self.kind == GRAFANA:
            return gcx.instances(self.stack, name, labels=self.selectors, env=self.env)
        return gcx.instances(LEGACY_STACK, name, labels=self.selectors, datasource=gcx.PROM, env=self.env)

    def sample(self, clock=time.time):
        state, raw = fold(self.instances())
        return Sample(clock(), state, raw)

    def instant(self, expr):
        return gcx.metrics_query(self.stack, expr, env=self.env)

    def confirm_exists(self):
        """Before any "none" is accepted: the rule exists in this backend, or an empty read proves nothing."""
        if self.kind == GRAFANA:
            exists = gcx.rule_defined(self.stack, self.group, self.rule, env=self.env) is not None
        else:
            try:
                exists = ruler_read.rule_loaded(self.fetch(self.ruler_env), self.rule)
            except ruler_read.RulerError as e:
                raise Inconclusive("%s: %s" % (self, e))
        if not exists:
            raise Inconclusive("%s is not defined in %s, so an empty read proves nothing" % (self.rule, self))


def select(env, rule, stack, ruler_env, environ=None, now=None, fetch=None):
    """The backend that evaluates rule for ENV; Inconclusive when it is live in both or in neither."""
    fetch = fetch or ruler_read.fetch
    group, _ = alerts_rule(rule)
    sel = dict(grafana_stack.selectors(env), namespace="andara-" + env)
    r = gcx.rule_defined(stack, group, rule, env=environ)
    g_why = gcx.rule_check(r, now or now_utc())
    try:
        text = fetch(ruler_env)
    except ruler_read.RulerError as e:
        raise Inconclusive("%s: backend selection: %s" % (rule, e))
    r_live = ruler_read.rule_loaded(text, rule)
    if g_why is None and r_live:
        raise Inconclusive("%s is live in both backends (grafana %s and ruler %s); decision 7 allows one"
                           % (rule, stack, LEGACY_STACK))
    if g_why is None:
        return Backend(GRAFANA, stack, rule, group, r["interval"], sel, r.get("query"), ruler_env, environ, fetch)
    if r_live:
        return Backend(RULER, LEGACY_STACK, rule, group, ruler_read.INTERVAL, {"namespace": sel["namespace"]},
                       None, ruler_env, environ, fetch)
    r_why = ("namespace %s is gone (404)" % ruler_read.NAMESPACE if text is None
             else "not in namespace %s" % ruler_read.NAMESPACE)
    raise Inconclusive("%s is live in neither backend: grafana %s: %s; ruler %s: %s"
                       % (rule, stack, g_why, LEGACY_STACK, r_why))


def record_line(rule, s):
    return "record %s %s %s (gcx: %s)" % (stamp(s.t), rule, s.state, s.raw)


class Watch(threading.Thread):
    """Polls backend's rule every POLL seconds from start() to stop(), printing and keeping each (time, state);
    a run asserts over this record, never over a reconstructed window."""

    def __init__(self, backend, say, poll=POLL, clock=time.time):
        super().__init__(daemon=True)
        self.backend, self.say, self.poll, self.clock = backend, say, poll, clock
        self.samples, self.errors = [], []
        self.started = self.stopped = None
        self._halt = threading.Event()

    def run(self):
        self.started = self.clock()
        while True:
            try:
                s = self.backend.sample(self.clock)
                self.samples.append(s)
                self.say(record_line(self.backend.rule, s))
            except gcx.GcxError as e:
                self.errors.append((self.clock(), str(e)))
                self.say("record %s %s read failed: %s" % (stamp(self.clock()), self.backend.rule, e))
            if self._halt.wait(self.poll):
                break

    def stop(self):
        self._halt.set()
        self.join()
        self.stopped = self.clock()
        return self

    def problems(self):
        """Why the record cannot show the window it covers: a state outside the vocabulary, or a gap longer
        than the group's interval between the run's ends and its successful polls."""
        out = ["%s at %s: state %r is not firing, pending or normal" % (self.backend.rule, stamp(s.t), s.state)
               for s in self.samples if s.state not in (gcx.FIRING, gcx.PENDING, gcx.NORMAL)]
        times = [self.started] + [s.t for s in self.samples] + [self.stopped]
        gaps = [(a, b) for a, b in zip(times, times[1:]) if b - a > self.backend.interval]
        out += ["no successful poll of %s from %s to %s (%.0fs, over the %ds interval)"
                % (self.backend.rule, stamp(a), stamp(b), b - a, self.backend.interval) for a, b in gaps]
        return out

    def firing(self):
        return [s for s in self.samples if s.state == gcx.FIRING]
