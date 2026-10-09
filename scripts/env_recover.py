#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""`make env-recover ENV=<env> CONFIRM=andara-<env>` — the M2 gate on a cluster (AW-INF-034).

    scripts/env_recover.py <env> <confirm>

The cluster counterpart of `make stack-recover` (AW-INF-032): SIGKILL the server while two
players are in the World, and prove it comes back as it was.

The kill comes from the node's PID namespace. The chart execs andara-server as PID 1 in its
container, and the kernel ignores a SIGKILL a namespace's init gets from inside it, so
`kubectl exec … kill -9 1` cannot do it. The PID is read from `crictl inspect` of the `server`
container's ID on andara-0's node (a kind node container: this runs on the box), never matched
by process name, because another environment's server can share the node.

Steps (each printed as `env-recover: <step>`):
  AC-2  RecoveryStateMismatch is loaded in the ruler and not already firing; players A and B
        enter play through the edge, A moves to Room 1; a complete round R newer than the one
        recorded before play; A moves to Room 2, which only the log tail holds.
  AC-3  SIGKILL from the node: restartCount +1, same pod UID, exit code 137, within 10 s.
  AC-4  andara-0 Ready within ENV_RECOVER_RTO of the kill, no further restart.
  AC-5  Grafana Cloud: andara_recovery_round_tick == R and andara_recovery_state_hash_match == 1
        sampled after the kill.
  AC-6  Both `play` clients reconnect; A reads Room 2, B the spawn Room; no already_live, waiting
        line or despawn line between the kill and the reconnect.
  AC-7  RecoveryStateMismatch never fired from the start of the run to its end.

Environment: ANDARA_BOOTSTRAP_OPERATOR (user:password); ENV_RECOVER_RTO (seconds, default 120);
GRAFANA_CLOUD_READ_TOKEN and GRAFANA_CLOUD_PROM_URL / _PROM_USER, as observe_check.py reads them;
MIMIR_ADDRESS, MIMIR_TENANT_ID and MIMIR_API_KEY (the ruler read, AC-2), defaulting from the PROM
pair and GRAFANA_CLOUD_READ_TOKEN (which carries rules:read) when unset. Credentials go in an isolated $XDG_CONFIG_HOME.

Exit codes: 0 the gate held; 1 an assertion failed, a precondition is missing, or the run was
inconclusive; 2 usage or refusal.
"""

import base64
import importlib.util
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

import yaml

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(HERE)
POD = "andara-0"
CONTAINER = "server"
KILL_LANDS_WITHIN = 10
ROUND_WAIT = 120          # snapshot.interval 60 s by default (story assumption) plus margin
PLAY_WAIT = 20
RECONNECT_WAIT = 60
METRIC_WAIT = 120
ALERT_SETTLE = 130        # two of the ruler's default 1 m evaluations after the recovered 1 is scraped
RULE = "RecoveryStateMismatch"
RULER_NAMESPACE = "andara"
PASSWORD = "env-recover-password-1"
ROOM_1 = "Market Plaza"
ROOM_2 = "Town Hall"
SPAWN_ROOM = "Purgatory"
DESPAWN_LINES = ("leaves the world.", "fades from the world.")
ALREADY_LIVE = ("reason=already_live", "Waiting for your previous session to end")
RULER_VARS = ("MIMIR_ADDRESS", "MIMIR_TENANT_ID", "MIMIR_API_KEY")
READ_VARS = ("GRAFANA_CLOUD_READ_TOKEN", "GRAFANA_CLOUD_PROM_URL", "GRAFANA_CLOUD_PROM_USER")


def _load(name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class Failed(Exception):
    """An assertion failed, a precondition is missing, or the run was inconclusive: exit 1."""


def say(msg):
    print("env-recover: " + msg, flush=True)


def refuse(msg):
    print("env-recover: " + msg, file=sys.stderr)
    sys.exit(2)


def check_args(argv):
    if len(argv) != 2 or not argv[0]:
        refuse("usage: make env-recover ENV=dev CONFIRM=andara-dev")
    env, confirm = argv
    if env == "prod":
        refuse("prod is not enabled")
    if env == "local":
        refuse("local is make stack-recover")
    if env != "dev":
        refuse("unknown ENV '%s'; want dev" % env)
    if confirm != "andara-" + env:
        refuse("refusing without CONFIRM=andara-%s; this kills %s's server" % (env, env))
    return env, "andara-" + env


# --- the pod and the kill (AC-3, AC-4) ------------------------------------------------------

def parse_pod(doc):
    """What the kill and its checks need from `kubectl get pod -o json`."""
    st = doc.get("status", {})
    srv = next((c for c in st.get("containerStatuses", []) if c.get("name") == CONTAINER), {})
    ready = [c for c in st.get("conditions", []) if c.get("type") == "Ready"]
    return {
        "uid": doc.get("metadata", {}).get("uid"),
        "node": doc.get("spec", {}).get("nodeName"),
        "container_id": srv.get("containerID", ""),
        "restarts": srv.get("restartCount", 0),
        "exit_code": ((srv.get("lastState") or {}).get("terminated") or {}).get("exitCode"),
        "ready": bool(ready and ready[0].get("status") == "True"),
    }


def resolve_pid(container_id, node, run):
    """The container's init PID in the node's namespace, from crictl on that node. By container
    ID, never a process-name match: another environment's server can share the node."""
    cid = container_id.split("://", 1)[-1]
    if not cid or not node:
        raise Failed("no server containerID or nodeName on %s; cannot find the PID to kill" % POD)
    p = run(["docker", "exec", node, "crictl", "inspect", cid])
    if p.returncode != 0:
        raise Failed("crictl inspect %s on node %s: %s" % (cid[:12], node, (p.stderr or p.stdout).strip()))
    try:
        pid = json.loads(p.stdout)["info"]["pid"]
    except (ValueError, KeyError, TypeError):
        raise Failed("crictl inspect %s on node %s gave no info.pid" % (cid[:12], node))
    if not isinstance(pid, int) or pid <= 1:
        raise Failed("crictl inspect %s on node %s gave pid %r" % (cid[:12], node, pid))
    return pid


def check_restart(before, after):
    """None when `after` is the pod `before` restarted once by SIGKILL; else why not (AC-3)."""
    if after["uid"] != before["uid"]:
        return "pod UID changed (%s → %s): a reschedule, not a restart" % (before["uid"], after["uid"])
    if after["restarts"] != before["restarts"] + 1:
        return "restartCount %d→%d, want exactly +1" % (before["restarts"], after["restarts"])
    if after["exit_code"] != 137:
        return "lastState.terminated.exitCode is %r, want 137" % (after["exit_code"],)
    return None


# --- Grafana Cloud (AC-5, AC-7) -------------------------------------------------------------

def vector(results):
    """[(labels, float value)] of an instant query's result."""
    return [(r["metric"], float(r["value"][1])) for r in results]


def check_recovery(ns, rounds, matches, stamps, round_r, t_kill):
    """None when AC-5 holds, else why not. A restart keeps the pod's name and IP, so each metric
    is one series across both processes: the round tells the new process from the old by value
    (the old one recovered at its own boot, before R existed), the hash match by its timestamp."""
    if not rounds or not matches or not stamps:
        return "no sample yet for namespace=%s (round_tick %d, hash_match %d, timestamp %d series)" % (
            ns, len(rounds), len(matches), len(stamps))
    got = sorted({int(v) for _, v in rounds})
    if got != [round_r]:
        return "andara_recovery_round_tick is %s, want %d" % (got, round_r)
    if any(v != 1 for _, v in matches):
        return "andara_recovery_state_hash_match is %s, want 1" % sorted({v for _, v in matches})
    stale = [v for _, v in stamps if v <= t_kill]
    if stale:
        return "andara_recovery_state_hash_match last sampled at %.0f, not after the kill at %.0f" % (
            max(stale), t_kill)
    return None


def check_rebound(despawned, reconnected):
    """None when the recovered server has despawned no one and rebound both players, else why not.
    An absent despawn series is a counter never incremented: 0."""
    if any(v != 0 for _, v in despawned):
        return "andara_events_emitted_total{type=\"character_despawned\"} is %s after the recovery, want 0" % (
            sorted({v for _, v in despawned}),)
    got = [v for _, v in reconnected]
    if not got or max(got) < 2:
        return "andara_linkdead_outcomes_total{outcome=\"reconnected\"} is %s, want at least 2 (A and B)" % (
            got or "absent")
    return None


class Grafana:
    def __init__(self):
        self.oc = _load("observe_check")
        self.prom = self.oc.Backend("PROM", os.environ["GRAFANA_CLOUD_READ_TOKEN"].strip())

    def instant(self, q):
        return self.oc.instant(self.prom, q)

    def firing_between(self, ns, start, end, state="firing"):
        """Samples of ALERTS{alertname=RULE, namespace, alertstate=state} over [start, end]."""
        q = 'ALERTS{alertname="%s",namespace="%s",alertstate="%s"}' % (RULE, ns, state)
        body = self.prom.get("/api/v1/query_range", {"query": q, "start": "%.0f" % start,
                                                     "end": "%.0f" % end, "step": "15"})
        if body.get("status") != "success":
            raise Failed("prom query_range %s: %s" % (q, body.get("error")))
        return body["data"]["result"]

    def observed_states(self, ns, start, end):
        q = 'ALERTS{alertname="AndaraServerUnavailable",namespace="%s"}' % ns
        body = self.prom.get("/api/v1/query_range", {"query": q, "start": "%.0f" % start,
                                                     "end": "%.0f" % end, "step": "15"})
        return sorted({r["metric"].get("alertstate", "?") for r in body.get("data", {}).get("result", [])})


def missing_credentials(environ):
    """The variables AC-2 and AC-5 need that aren't set, ruler defaults filled as alerts_sync has them."""
    sync = _load("alerts_sync")
    env = dict(sync.resolve_env(environ))
    key = env.get("MIMIR_API_KEY", "").strip() or environ.get("GRAFANA_CLOUD_READ_TOKEN", "").strip()
    if key:
        env["MIMIR_API_KEY"] = key
    else:
        env.pop("MIMIR_API_KEY", None)
    return [k for k in READ_VARS if not environ.get(k, "").strip()] + sync.missing(env, RULER_VARS), env


def rule_loaded(ruler_yaml):
    """True when the ruler's namespace body names RecoveryStateMismatch."""
    doc = yaml.safe_load(ruler_yaml) or {}
    groups = doc if isinstance(doc, dict) else {}
    return any(r.get("alert") == RULE for g in (groups.get(RULER_NAMESPACE) or []) for r in (g.get("rules") or []))


def fetch_ruler(env):
    sync = _load("alerts_sync")
    url = "%s/prometheus/config/v1/rules/%s" % (env["MIMIR_ADDRESS"].rstrip("/"), RULER_NAMESPACE)
    req = urllib.request.Request(url)
    token = base64.b64encode(("%s:%s" % (env["MIMIR_TENANT_ID"], env["MIMIR_API_KEY"])).encode()).decode()
    req.add_header("Authorization", "Basic " + token)
    req.add_header("X-Scope-OrgID", env["MIMIR_TENANT_ID"])
    try:
        with urllib.request.build_opener(sync.NoRedirect).open(req, timeout=60) as r:
            return r.read().decode()
    except urllib.error.HTTPError as e:
        raise Failed("the ruler refused the rules read: HTTP %d" % e.code)
    except (urllib.error.URLError, TimeoutError) as e:
        raise Failed("could not reach the ruler: %s" % e)


# --- the CLI and the players (AC-2, AC-6) ---------------------------------------------------

def newest_round(table):
    """The tick of the newest round `snapshot list` calls complete, or 0: the first column of the
    first row whose fourth is `true`."""
    for line in table.splitlines()[1:]:
        f = line.split()
        if len(f) >= 4 and f[3] == "true":
            return int(f[0])
    return 0


class Player:
    """One `andara-cli play --show-protocol` on a held-open stdin, its transcript in a file."""

    def __init__(self, label, cli, creds, character, work, env):
        self.label, self.character = label, character
        self.path = os.path.join(work, label + "-out.txt")
        self.out = open(self.path, "w+")
        self.proc = subprocess.Popen([cli, "--credentials", creds, "play", "--character", character,
                                      "--show-protocol"], stdin=subprocess.PIPE, stdout=self.out,
                                     stderr=subprocess.STDOUT, text=True, env=env)

    def lines(self):
        self.out.flush()
        with open(self.path) as f:
            return f.read().splitlines()

    def send(self, text):
        self.proc.stdin.write(text)
        self.proc.stdin.flush()

    def wait_for(self, pattern, after=0, secs=PLAY_WAIT):
        """Poll to a deadline for a line matching `pattern` after line `after`."""
        end = time.monotonic() + secs
        rx = re.compile(pattern)
        while True:
            if any(rx.search(l) for l in self.lines()[after:]):
                return True
            if time.monotonic() > end:
                return False
            time.sleep(0.5)

    def stop(self):
        try:
            self.proc.stdin.close()
        except OSError:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            self.proc.wait()
        self.out.close()


def transcript_problems(lines, who):
    """Absence checks over the lines between the kill and the reconnect (AC-6)."""
    out = []
    if any(s in l for l in lines for s in ALREADY_LIVE):
        out.append("%s's reconnect was refused already_live" % who)
    if any(s in l for l in lines for s in DESPAWN_LINES):
        out.append("%s read a despawn line between the kill and the reconnect" % who)
    return out


def check_round_unchanged(latest, r):
    if latest != r:
        raise Failed("inconclusive: a round completed after the tail move (newest %d, R was %d); rerun"
                     % (latest, r))


# --- the run --------------------------------------------------------------------------------

class Run:
    def __init__(self, env, ns, environ):
        self.env, self.ns, self.environ = env, ns, environ
        self.work = tempfile.mkdtemp(prefix="env-recover.")
        self.cli = os.environ.get("ANDARA_CLI", os.path.join(REPO, "bin", "andara-cli"))
        self.players = {}
        self.account_ids = {}
        self.rto = environ.get("ENV_RECOVER_RTO", "120")
        self.cli_env = dict(os.environ)
        self.uid = None

    # shell-outs
    def sh(self, args, **kw):
        return subprocess.run(args, capture_output=True, text=True, **kw)

    def kubectl(self, *args):
        p = self.sh(["kubectl", "-n", self.ns, *args])
        if p.returncode != 0:
            raise Failed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
        return p.stdout

    def pod(self):
        return parse_pod(json.loads(self.kubectl("get", "pod", POD, "-o", "json")))

    def cli_run(self, *args, stdin=None, creds=None, check=True):
        cmd = [self.cli] + (["--credentials", creds] if creds else []) + list(args)
        p = subprocess.run(cmd, input=stdin, capture_output=True, text=True, env=self.cli_env)
        if check and p.returncode != 0:
            raise Failed("andara-cli %s: %s" % (" ".join(args[:2]), (p.stderr or p.stdout).strip()))
        return p.stdout

    def cred(self, who):
        return os.path.join(self.work, "cred-%s.yaml" % who)

    def setup_cli(self):
        values = yaml.safe_load(open(os.path.join(REPO, "deploy", "helm", "values", "%s.yaml" % self.env)))
        self.address = "%s:443" % values["host"]
        cfg = os.path.join(self.work, "cli.yaml")
        with open(cfg, "w") as f:
            f.write("server:\n  address: %s\n" % self.address)
        self.cli_env = dict(os.environ, ANDARA_CONFIG=cfg, XDG_CONFIG_HOME=os.path.join(self.work, "config"),
                            XDG_STATE_HOME=os.path.join(self.work, "state"))
        op = self.environ.get("ANDARA_BOOTSTRAP_OPERATOR", "")
        if ":" not in op:
            raise Failed("ANDARA_BOOTSTRAP_OPERATOR is not set (user:password)")
        user, pw = op.split(":", 1)
        say("logging in to %s as %s" % (self.address, user))
        self.cli_run("auth", "login", "--username", user, "--password-stdin", stdin=pw + "\n")

    def newest_round(self):
        return newest_round(self.cli_run("snapshot", "list"))

    def make_player(self, who, hex_, prefix):
        user = "recover-%s-%s" % (who, hex_)
        made = self.cli_run("--output", "json", "account", "create", "--username", user, "--password-stdin",
                            stdin=PASSWORD + "\n")
        # Recorded at once, so a later failure still gets this Account disabled.
        self.account_ids[who] = json.loads(made)["account_id"]
        self.cli_run("auth", "login", "--username", user, "--password-stdin", stdin=PASSWORD + "\n",
                     creds=self.cred(who))
        name = prefix + "".join(chr(97 + b % 26) for b in os.urandom(8))
        self.cli_run("character", "create", name, creds=self.cred(who))
        return user, name

    def start_player(self, who, user, name):
        p = Player(who.upper(), self.cli, self.cred(who), name, self.work, self.cli_env)
        self.players[who] = p
        if not p.wait_for(r"^-- Connected to [^ ]+ as %s, playing %s " % (re.escape(user), re.escape(name))):
            raise Failed("%s never connected playing %s" % (who.upper(), name))
        if not p.wait_for("^%s$" % SPAWN_ROOM):
            raise Failed("%s never read %s from its automatic look" % (who.upper(), SPAWN_ROOM))
        return p

    # the steps
    def preflight(self, g, ruler_env):
        if not shutil.which("kubectl") or not shutil.which("docker"):
            raise Failed("kubectl and docker are needed here: this runs on the box, where the nodes are")
        if not os.access(self.cli, os.X_OK):
            raise Failed("no %s; run `make build` first" % self.cli)
        if not rule_loaded(fetch_ruler(ruler_env)):
            raise Failed("inconclusive: %s is not loaded in the tenant's ruler (namespace %s); "
                         "`make alerts-sync` delivers it" % (RULE, RULER_NAMESPACE))
        say("%s is loaded in the ruler" % RULE)
        if g.firing_between(self.ns, time.time() - 120, time.time()):
            raise Failed("inconclusive: %s is already firing for %s" % (RULE, self.ns))
        if not self.pod()["ready"]:
            raise Failed("%s is not Ready; nothing to kill" % POD)

    def play_until_round(self):
        hex_ = os.urandom(4).hex()
        a_user, a_name = self.make_player("a", hex_, "Recovered")
        b_user, b_name = self.make_player("b", hex_, "Bystander")
        round_before = self.newest_round()
        say("newest complete round before play: %s" % (round_before or "none"))
        a = self.start_player("a", a_user, a_name)
        self.start_player("b", b_user, b_name)
        a.send("out\nlook\n")
        if not a.wait_for("^%s$" % ROOM_1):
            raise Failed("A never read %s from its look after out" % ROOM_1)
        say("waiting up to %ds for a complete round past %d" % (ROUND_WAIT, round_before))
        end, r = time.monotonic() + ROUND_WAIT, 0
        while time.monotonic() < end:
            n = self.newest_round()
            if n > round_before:
                r = n
                break
            time.sleep(2)
        if not r:
            raise Failed("no complete snapshot round newer than %d within %ds" % (round_before, ROUND_WAIT))
        say("round R = %d" % r)
        a.send("north\nlook\n")
        if not a.wait_for("^%s$" % ROOM_2):
            raise Failed("A never read %s from its look after north" % ROOM_2)
        check_round_unchanged(self.newest_round(), r)
        return r

    def kill(self):
        before = self.pod()
        self.uid = before["uid"]
        pid = resolve_pid(before["container_id"], before["node"], self.sh)
        say("SIGKILL to pid %d (%s's server container) from node %s" % (pid, POD, before["node"]))
        t_kill = time.time()
        p = self.sh(["docker", "exec", before["node"], "kill", "-9", str(pid)])
        if p.returncode != 0:
            raise Failed("kill -9 %d on %s: %s" % (pid, before["node"], (p.stderr or p.stdout).strip()))
        end, after = time.monotonic() + KILL_LANDS_WITHIN, before
        while time.monotonic() < end:
            after = self.pod()
            if after["restarts"] != before["restarts"]:
                break
            time.sleep(0.5)
        if after["restarts"] == before["restarts"]:
            raise Failed("SIGKILL didn't land; restartCount unchanged")
        problem = check_restart(before, after)
        if problem:
            raise Failed(problem)
        end = t_kill + self.rto
        while True:
            now = self.pod()
            if now["restarts"] != after["restarts"]:
                raise Failed("a further restart (restartCount %d→%d) inside the RTO" % (after["restarts"], now["restarts"]))
            if now["ready"]:
                break
            if time.time() > end:
                raise Failed("%s not Ready within %ds of the kill" % (POD, self.rto))
            time.sleep(0.5)
        return t_kill, time.time() - t_kill, before["restarts"], now["restarts"]

    def recovered_metrics(self, g, r, t_kill):
        sel = '{namespace="%s"}' % self.ns
        end, why = time.monotonic() + METRIC_WAIT, ""
        while time.monotonic() < end:
            why = check_recovery(
                self.ns,
                vector(g.instant("andara_recovery_round_tick" + sel)),
                vector(g.instant("andara_recovery_state_hash_match" + sel)),
                vector(g.instant("timestamp(andara_recovery_state_hash_match%s)" % sel)), r, t_kill)
            if not why:
                return
            time.sleep(5)
        raise Failed("within %ds: %s" % (METRIC_WAIT, why))

    def rebound_counters(self, g):
        """The check no transcript can make: B sits in the Purgatory and A in the Town Hall, so
        neither witnesses the other's despawn, and a despawn emitted before a client resubscribes
        never reaches it. The recovered process began its counters at 0 (AC-6). The sums are
        namespace-wide: run it alone on dev, and not within the 180 s linkdead grace of a prior run,
        whose players the recovered process would re-mark linkdead and then despawn."""
        sel = '{namespace="%s"}' % self.ns
        end, why = time.monotonic() + METRIC_WAIT, ""
        while time.monotonic() < end:
            why = check_rebound(
                vector(g.instant('sum(andara_events_emitted_total{type="character_despawned",namespace="%s"})' % self.ns)),
                vector(g.instant('sum(andara_linkdead_outcomes_total{outcome="reconnected",namespace="%s"})' % self.ns)))
            if not why:
                return
            time.sleep(5)
        raise Failed("within %ds: %s" % (METRIC_WAIT, why))

    def assert_no_further_restart(self, expected):
        """AC-4's "no further restart", held to the end of the run and not only to first Ready."""
        now = self.pod()
        if not self.uid:
            raise Failed("no pod UID was recorded at the kill; cannot tell a restart from a reschedule")
        if now["uid"] != self.uid:
            raise Failed("pod UID changed to %s at the end of the run: rescheduled after the recovery" % now["uid"])
        if now["restarts"] != expected:
            raise Failed("restartCount is %d at the end of the run, was %d at Ready: the server restarted again"
                         % (now["restarts"], expected))

    def trace_id(self, since):
        p = self.sh(["kubectl", "-n", self.ns, "logs", POD, "-c", CONTAINER, "--since-time=" + since])
        tid = ""
        for line in p.stdout.splitlines():
            try:
                o = json.loads(line)
            except ValueError:
                continue
            if o.get("msg") == "recovery complete":
                tid = o.get("trace_id", tid)
        return tid

    def rebind(self, marks, t_ready):
        """Both players reconnect within RECONNECT_WAIT of Ready: one deadline, shared."""
        a, b = self.players["a"], self.players["b"]
        for who, p in (("A", a), ("B", b)):
            left = max(0.0, t_ready + RECONNECT_WAIT - time.time())
            if not p.wait_for(r"^-- Connected to ", after=marks[who], secs=left):
                raise Failed("%s's play did not reconnect within %ds of the server being Ready" % (who, RECONNECT_WAIT))
        am, bm = len(a.lines()), len(b.lines())
        a.send("look\n")
        b.send("look\n")
        if not a.wait_for("^%s$" % ROOM_2, after=am):
            raise Failed("A's look after the recovery does not read %s: the tail move was not replayed" % ROOM_2)
        if not b.wait_for("^%s$" % SPAWN_ROOM, after=bm):
            raise Failed("B's look after the recovery does not read %s" % SPAWN_ROOM)
        problems = transcript_problems(a.lines()[marks["A"]:], "A") + transcript_problems(b.lines()[marks["B"]:], "B")
        if problems:
            raise Failed("; ".join(problems))

    def dump(self):
        for who, p in self.players.items():
            print("--- %s:" % who.upper(), file=sys.stderr)
            for l in p.lines():
                print("  %s| %s" % (who.upper(), l), file=sys.stderr)
        for flag, title in (("--previous", "previous"), ("", "current")):
            p = self.sh(["kubectl", "-n", self.ns, "logs", POD, "-c", CONTAINER, "--tail", "50"] + ([flag] if flag else []))
            print("--- %s %s, last 50 lines:" % (CONTAINER, title), file=sys.stderr)
            print(p.stdout or p.stderr, file=sys.stderr)

    def cleanup(self):
        for p in self.players.values():
            try:
                p.stop()
            except Exception:
                pass
        for who in ("a", "b"):
            aid = self.account_ids.get(who)
            if not aid:
                if self.account_ids:
                    say("could not disable player %s: its Account was never created" % who.upper())
                continue
            try:
                p = subprocess.run([self.cli, "account", "set-status", aid, "disabled"], capture_output=True,
                                   text=True, env=self.cli_env, timeout=60)
            except (subprocess.TimeoutExpired, OSError) as e:
                say("could not disable player %s's Account %s: %s" % (who.upper(), aid, e))
                continue
            if p.returncode != 0:
                say("could not disable player %s's Account %s: %s" % (who.upper(), aid, (p.stderr or p.stdout).strip()))
        shutil.rmtree(self.work, ignore_errors=True)


def main(argv):
    env, ns = check_args(argv)
    run = Run(env, ns, os.environ)
    t_start = time.time()
    try:
        absent, ruler_env = missing_credentials(os.environ)
        if absent:
            raise Failed("%s unset; cannot verify" % ", ".join(absent))
        if not re.fullmatch(r"[0-9]+", run.rto) or int(run.rto) <= 0:
            raise Failed("ENV_RECOVER_RTO=%s is not a whole number of seconds above 0" % run.rto)
        run.rto = int(run.rto)
        g = Grafana()
        run.preflight(g, ruler_env)
        run.setup_cli()
        r = run.play_until_round()
        # Before the kill: the absence window is the kill to the reconnect, not Ready to it.
        marks = {"A": len(run.players["a"].lines()), "B": len(run.players["b"].lines())}
        t_kill, kill_to_ready, a_restarts, b_restarts = run.kill()
        since = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(t_kill))
        say("Ready %.0fs after the kill (RTO %ds), restartCount %d→%d, round %d" %
            (kill_to_ready, run.rto, a_restarts, b_restarts, r))
        # The reconnect deadline runs from Ready, so it is awaited first; the metrics poll can take
        # minutes and a late reconnect would otherwise already be in the transcript.
        run.rebind(marks, t_kill + kill_to_ready)
        say("both rebound; A reads %s (the tail move), B reads %s" % (ROOM_2, SPAWN_ROOM))
        run.recovered_metrics(g, r, t_kill)
        say("hash match; recovery.run trace %s" % (run.trace_id(since) or "n/a"))
        run.rebound_counters(g)
        say("the server's own counters: no despawn since the recovery, both players reconnected")
        say("waiting %ds for the ruler to judge the recovered 1" % ALERT_SETTLE)
        time.sleep(ALERT_SETTLE)
        run.rebound_counters(g)   # sampled again: a despawn after the first sample is still the recovery's
        now = time.time()
        if g.firing_between(ns, t_start, now):
            raise Failed("%s fired during the run" % RULE)
        say("%s did not fire; AndaraServerUnavailable observed as %s" %
            (RULE, ", ".join(g.observed_states(ns, t_start, now)) or "inactive"))
        # Last, after every blocking check: a second crash or a reschedule at any point since
        # Ready would still pass each check above.
        run.assert_no_further_restart(b_restarts)
        say("Ready %.0fs after the kill (RTO %ds), restartCount %d→%d, round %d, hash match" %
            (kill_to_ready, run.rto, a_restarts, b_restarts, r))
        status = 0
    except Failed as e:
        print("env-recover: %s" % e, file=sys.stderr)
        try:
            run.dump()
        except Exception as d:
            print("env-recover: could not collect logs: %s" % d, file=sys.stderr)
        status = 1
    except Exception as e:  # a backend or tool failure is still a failed run, and still cleans up
        print("env-recover: %s: %s" % (type(e).__name__, e), file=sys.stderr)
        try:
            run.dump()
        except Exception as d:
            print("env-recover: could not collect logs: %s" % d, file=sys.stderr)
        status = 1
    finally:
        run.cleanup()
    if status == 0:
        say("M2 gate on %s — killed, recovered from a snapshot, hash matched, both rebound — passes" % env)
    return status


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(1))
    sys.exit(main(sys.argv[1:]))
