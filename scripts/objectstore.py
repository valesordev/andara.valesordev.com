#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The snapshot object store: the shared gateway in andara-shared for local and dev (AW-INF-058,
ADR-0013 decision 4), and prod's own per-namespace store (AW-INF-025, ADR-0013 decision 10).

    scripts/objectstore.py up                make objectstore-up: the shared gateway, its root pair, andara-it
    scripts/objectstore.py install <env>     make objectstore-install ENV=<local|dev|prod>
    scripts/objectstore.py empty <env>       delete every object in the env's bucket (world-reset)
    scripts/objectstore.py check             make objectstore-check: the isolation test (AC-2, AC-3, AC-6)

`up` (idempotent): the root pair in the Secret andara-objectstore-root in andara-shared, generated
once; deploy/k8s/objectstore/shared.yaml applied there and waited on; the user andara-it (role
`userplus`: it creates andara-test-* buckets) with its pair in andara-shared/andara-objectstore-it.

`install local|dev` (idempotent): the namespace andara-<env> with its andara.valesor/env label;
the user andara-<env> (role `user`: no bucket creation) with its pair generated once in
andara-shared/andara-objectstore-<env>; the bucket andara-snapshots-<env> owned by it; the pair
copied to andara-<env>/andara-snapshot-s3 (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY), the only
Secret the environment holds; then a listing of the bucket with that copy. The access key is the
user's name, so the gateway's access log names the user.

`install prod` is AW-INF-025's path, unchanged: a gateway in andara-prod whose root pair is
andara-snapshot-s3 (deploy/k8s/objectstore/objectstore.yaml).

Every gateway operation runs in a one-shot pod labelled andara-objectstore-tools, which the
NetworkPolicy admits: versitygw's admin CLI in andara-shared with the root Secret, rclone (install,
empty) or curl (check) in the namespace whose Secret it uses. A pair moves only from Secret to Secret, through kubectl's stdin:
never an argument, never printed. Every step prints `objectstore-<cmd>: <step>`.

`check` proves the separation on the live gateway with curl's SigV4 signing, one request per verb:
each environment's credential does all four verbs (write, list, read, delete) on its own bucket
under the snapshot key layout and is refused all four (403) on the other's; andara-it creates,
writes and deletes an andara-test-* bucket, is refused (403) every verb on both environment
buckets including deleting them, and is refused (409) taking an existing name. The gateway's
access log then has to show each refused request arriving and being refused, by user, operation
and bucket, so a refusal the client invented can't pass; and each environment lists its own
bucket last, so a write that landed is caught.

Exit codes: 0 done; 1 a step or an assertion failed, naming it; 2 usage; 3 the cluster context,
kubectl, the namespace andara-shared or the root Secret is missing.
"""

import json
import os
import secrets
import shutil
import string
import subprocess
import sys
import time

import kube

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MANIFEST = os.path.join(REPO, "deploy", "k8s", "objectstore", "objectstore.yaml")
SHARED_MANIFEST = os.path.join(REPO, "deploy", "k8s", "objectstore", "shared.yaml")
SHARED_NS = "andara-shared"
SHARED_ENVS = ("local", "dev")
ENV_LABEL = "andara.valesor/env"
SECRET = "andara-snapshot-s3"
ROOT_SECRET = "andara-objectstore-root"
IT_USER = "andara-it"
IT_SECRET = "andara-objectstore-it"
DEPLOYMENT = "deployment/andara-objectstore"
SERVICE = "andara-objectstore"
SHARED_ENDPOINT = "http://%s.%s.svc:9000" % (SERVICE, SHARED_NS)
TOOLS_LABEL = "andara-objectstore-tools"
RCLONE = "rclone/rclone:1.75.1@sha256:45401ad7410db1d67ffdb58e19059ad20b0d8e0285a60e38bbec55cc1019c7a5"
VERSITYGW = "versity/versitygw:v1.8.0@sha256:30292fc2eeacc67a36993b01f7a7a5e3361a19cced0e80c1d71cfa2a4b0a2499"
READY_TIMEOUT = "120s"
TOOL_TIMEOUT = 180


class Failed(Exception):
    pass


class Missing(Exception):
    """A prerequisite: exit 3."""


class Usage(Exception):
    """Exit 2."""


def say(cmd, msg):
    print("objectstore-%s: %s" % (cmd, msg), flush=True)


def bucket(env):
    return "andara-snapshots-" + env


def user(env):
    return "andara-" + env


def env_ns(env):
    if env in SHARED_ENVS or env == "prod":
        return "andara-" + env
    raise Usage("ENV=%s: not one of local, dev, prod" % (env or "<unset>"))


def user_secret(env):
    """The operator-only source of andara-<env>'s pair, in andara-shared."""
    return "andara-objectstore-" + env


def kubectl(ns, *args, check=True, input=None):
    argv = kube.kubectl(*(("-n", ns) if ns else ()), *args)
    p = subprocess.run(argv, capture_output=True, text=True, input=input)
    if check and p.returncode != 0:
        raise Failed("kubectl %s: %s" % (" ".join(args[:3]), (p.stderr or p.stdout).strip()))
    return p


def exists(ns, kind, name):
    return kubectl(ns, "get", kind, name, "-o", "name", check=False).returncode == 0


def _random(n, alphabet=string.ascii_letters + string.digits):
    return "".join(secrets.choice(alphabet) for _ in range(n))


def ensure_pair(ns, name, access_key=None, keys=("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY")):
    """A Secret holding an access/secret pair, generated the first time and never rewritten.
    access_key None generates one too (the root pair). True when it was created now."""
    if exists(ns, "secret", name):
        return False
    access = access_key or _random(20, string.ascii_uppercase + string.digits)
    doc = {"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
           "metadata": {"name": name, "labels": {"app.kubernetes.io/name": "andara-objectstore"}},
           "stringData": {keys[0]: access, keys[1]: _random(40)}}
    kubectl(ns, "create", "-f", "-", input=json.dumps(doc))
    return True


def copy_pair(src_ns, src, dst_ns, dst):
    """Copy a pair Secret's data to dst, through kubectl's stdin. 'created', 'updated' or 'unchanged'."""
    data = json.loads(kubectl(src_ns, "get", "secret", src, "-o", "json").stdout)["data"]
    cur = kubectl(dst_ns, "get", "secret", dst, "-o", "json", check=False)
    if cur.returncode == 0 and json.loads(cur.stdout).get("data") == data:
        return "unchanged"
    doc = {"apiVersion": "v1", "kind": "Secret", "type": "Opaque",
           "metadata": {"name": dst, "namespace": dst_ns,
                        "labels": {"app.kubernetes.io/name": "andara-objectstore"}},
           "data": data}
    kubectl(dst_ns, "apply", "-f", "-", input=json.dumps(doc))
    return "updated" if cur.returncode == 0 else "created"


# --- one-shot pods ------------------------------------------------------------------------------

def run_pod(ns, image, args, env=(), env_from=(), secret_keys=(), command=None, timeout=TOOL_TIMEOUT):
    """Run a one-shot tools pod; (succeeded, logs). secret_keys: (VAR, secret, key) env from a
    Secret in ns, so a value reaches the pod without passing through this process."""
    name = "%s-%d%s" % (TOOLS_LABEL, int(time.time() * 1000) % 10**9, _random(3, string.ascii_lowercase))
    container = {
        "name": "tool", "image": image, "args": list(args),
        "env": [{"name": k, "value": v} for k, v in env] + [{"name": "HOME", "value": "/tmp"}]
        + [{"name": var, "valueFrom": {"secretKeyRef": {"name": s, "key": k}}} for var, s, k in secret_keys],
        "envFrom": [{"secretRef": {"name": s}} for s in env_from],
        "securityContext": {"allowPrivilegeEscalation": False, "capabilities": {"drop": ["ALL"]}},
        "volumeMounts": [{"name": "tmp", "mountPath": "/tmp"}],
    }
    if command:
        container["command"] = list(command)
    pod = {
        "apiVersion": "v1", "kind": "Pod",
        "metadata": {"name": name, "labels": {"app.kubernetes.io/name": TOOLS_LABEL}},
        "spec": {
            "restartPolicy": "Never",
            "automountServiceAccountToken": False,
            "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001,
                                "seccompProfile": {"type": "RuntimeDefault"}},
            "containers": [container],
            "volumes": [{"name": "tmp", "emptyDir": {}}],
        },
    }
    kubectl(ns, "create", "-f", "-", input=json.dumps(pod))
    try:
        deadline = time.time() + timeout
        while True:
            phase = kubectl(ns, "get", "pod", name, "-o", "jsonpath={.status.phase}", check=False).stdout
            if phase in ("Succeeded", "Failed"):
                break
            if time.time() > deadline:
                raise Failed("%s %s in %s: no result in %ds" % (image.split("@")[0], " ".join(args[:2]), ns, timeout))
            time.sleep(1)
        return phase == "Succeeded", kubectl(ns, "logs", name, check=False).stdout
    finally:
        kubectl(ns, "delete", "pod", name, "--wait=false", check=False)


def rclone_env(endpoint, check_bucket=True):
    env = [("RCLONE_CONFIG_S3_TYPE", "s3"), ("RCLONE_CONFIG_S3_PROVIDER", "Other"),
           ("RCLONE_CONFIG_S3_ENV_AUTH", "true"), ("RCLONE_CONFIG_S3_ENDPOINT", endpoint),
           ("RCLONE_CONFIG", "/tmp/rclone.conf"),
           # Errors only: `kubectl logs` interleaves stderr, and a NOTICE line would read as output.
           ("RCLONE_LOG_LEVEL", "ERROR")]
    if not check_bucket:
        # An environment user may not create buckets, and rclone sends CreateBucket before an
        # upload unless told the bucket is there.
        env.append(("RCLONE_CONFIG_S3_NO_CHECK_BUCKET", "true"))
    return env


def rclone(ns, *args, secret=SECRET, endpoint=None, check_bucket=True):
    """rclone against a store, in a one-shot pod in ns with that namespace's Secret; its stdout."""
    ok, logs = run_pod(ns, RCLONE, args, env=rclone_env(endpoint or "http://%s:9000" % SERVICE, check_bucket),
                       env_from=[secret])
    if not ok:
        raise Failed("rclone %s: %s" % (" ".join(args), logs.strip()[-500:]))
    return logs


def admin(*args, secret_keys=()):
    """versitygw's admin CLI against the shared gateway with the root pair; its stdout. Kubernetes
    expands $(VAR) in args from the pod's env, so a secret is named, never written, here."""
    argv = ["admin", "--access", "$(ROOT_ACCESS_KEY)", "--secret", "$(ROOT_SECRET_KEY)",
            "--endpoint-url", "http://%s:9000" % SERVICE] + list(args)
    ok, logs = run_pod(SHARED_NS, VERSITYGW, argv, env_from=[ROOT_SECRET], secret_keys=secret_keys)
    if not ok:
        raise Failed("versitygw admin %s: %s" % (args[0], logs.strip()[-500:]))
    return logs


def table(text):
    """versitygw admin's table output as rows of fields, header and rule lines dropped."""
    rows = []
    for line in text.splitlines():
        f = line.split()
        if not f or set(line.strip()) <= set("-") or f[0] in ("Account", "Bucket") or f[0].startswith("-"):
            continue
        rows.append(f)
    return rows


# --- the shared gateway -------------------------------------------------------------------------

def require_shared(cmd):
    kube.require()
    if not exists("", "namespace", SHARED_NS):
        raise Missing("namespace %s is missing; `make platform-up` creates it" % SHARED_NS)


def ensure_user(name, secret_name, role, cmd):
    """The gateway user `name` with the pair in andara-shared/secret_name; created, or its secret
    reset when the pair was just generated (a recreated Secret), else left."""
    made = ensure_pair(SHARED_NS, secret_name, access_key=name)
    key = [("USER_SECRET", secret_name, "AWS_SECRET_ACCESS_KEY")]
    users = {r[0]: r[1] for r in table(admin("list-users")) if len(r) >= 2}
    if name not in users:
        admin("create-user", "--access", name, "--secret", "$(USER_SECRET)", "--role", role, secret_keys=key)
        say(cmd, "user %s created (role %s)" % (name, role))
    elif made:
        admin("update-user", "--access", name, "--secret", "$(USER_SECRET)", secret_keys=key)
        say(cmd, "user %s: pair regenerated, gateway updated" % name)
    elif users[name] != role:
        raise Failed("user %s has role %s, not %s; fix it by hand with versitygw admin update-user"
                     % (name, users[name], role))
    else:
        say(cmd, "user %s exists (role %s); unchanged" % (name, role))


def up(cmd="up"):
    require_shared(cmd)
    made = ensure_pair(SHARED_NS, ROOT_SECRET, keys=("ROOT_ACCESS_KEY", "ROOT_SECRET_KEY"))
    say(cmd, "root Secret %s/%s %s" % (SHARED_NS, ROOT_SECRET, "created" if made else "exists; unchanged"))
    kubectl(SHARED_NS, "apply", "-f", SHARED_MANIFEST)
    kubectl(SHARED_NS, "rollout", "status", DEPLOYMENT, "--timeout=" + READY_TIMEOUT)
    say(cmd, "versitygw Ready at %s" % SHARED_ENDPOINT)
    ensure_user(IT_USER, IT_SECRET, "userplus", cmd)


def ensure_namespace(env, cmd):
    ns = env_ns(env)
    if not exists("", "namespace", ns):
        kubectl("", "create", "namespace", ns)
        say(cmd, "namespace %s created" % ns)
    kubectl("", "label", "namespace", ns, "%s=%s" % (ENV_LABEL, env), "--overwrite")


def ensure_bucket(env, cmd):
    owners = {r[0]: r[1] for r in table(admin("list-buckets")) if len(r) >= 2}
    b, u = bucket(env), user(env)
    if b not in owners:
        admin("create-bucket", "--bucket", b, "--owner", u)
        say(cmd, "bucket %s created, owner %s" % (b, u))
    elif owners[b] != u:
        admin("change-bucket-owner", "--bucket", b, "--owner", u)
        say(cmd, "bucket %s owner %s -> %s" % (b, owners[b], u))
    else:
        say(cmd, "bucket %s exists, owner %s; unchanged" % (b, u))


def install_shared(env, cmd="install"):
    require_shared(cmd)
    if not exists(SHARED_NS, "secret", ROOT_SECRET):
        raise Missing("the root Secret %s/%s is missing; run `make objectstore-up` first" % (SHARED_NS, ROOT_SECRET))
    ensure_namespace(env, cmd)
    ensure_user(user(env), user_secret(env), "user", cmd)
    ensure_bucket(env, cmd)
    state = copy_pair(SHARED_NS, user_secret(env), env_ns(env), SECRET)
    say(cmd, "Secret %s/%s %s (andara-%s's pair only)" % (env_ns(env), SECRET, state, env))
    if state == "updated":
        # envFrom is read at container start: a running server or projector keeps the old pair, and
        # its snapshot writes are refused until it restarts.
        if env == "dev":
            how = ("run `make server-stop ENV=dev && make server-start ENV=dev` and "
                   "`make projector-stop ENV=dev && make projector-start ENV=dev`")
        else:
            # server-stop/-start and projector-stop/-start take dev and prod only (AW-INF-037, AW-INF-025).
            how = ("restart them: `kubectl -n andara-local rollout restart statefulset/andara` and "
                   "`kubectl -n andara-local rollout restart deployment -l app.kubernetes.io/name=andara` "
                   "(the projectors; no local target yet)")
        say(cmd, "the pair changed: a running server and projector in %s keep the old one until restarted; %s"
            % (env_ns(env), how))
    rclone(env_ns(env), "lsf", "s3:" + bucket(env), endpoint=SHARED_ENDPOINT, check_bucket=False)
    say(cmd, "bucket %s reachable from %s with its own pair" % (bucket(env), env_ns(env)))


def install_prod(cmd="install"):
    """AW-INF-025's per-namespace store, unchanged (ADR-0013 decision 10)."""
    ns = env_ns("prod")
    if kubectl(ns, "get", "namespace", ns, check=False).returncode != 0:
        kubectl(ns, "create", "namespace", ns)
    made = ensure_pair(ns, SECRET)
    say(cmd, "Secret %s %s" % (SECRET, "created" if made else "exists; unchanged"))
    kubectl(ns, "apply", "-f", MANIFEST)
    kubectl(ns, "rollout", "status", DEPLOYMENT, "--timeout=" + READY_TIMEOUT)
    say(cmd, "versitygw Ready at http://%s:9000" % SERVICE)
    rclone(ns, "mkdir", "s3:" + bucket("prod"))
    listed = rclone(ns, "lsf", "--dirs-only", "s3:")
    if bucket("prod") + "/" not in listed.split():
        raise Failed("bucket %s not listed after mkdir: %r" % (bucket("prod"), listed))
    say(cmd, "bucket %s in %s ready" % (bucket("prod"), ns))


def install(env, cmd="install"):
    env_ns(env)
    if env == "prod":
        install_prod(cmd)
    else:
        install_shared(env, cmd)


def store_args(env):
    """(namespace, endpoint, check_bucket) for rclone on env's bucket with env's own pair."""
    ns = env_ns(env)
    if env in SHARED_ENVS:
        return ns, SHARED_ENDPOINT, False
    return ns, None, True


def empty(env):
    ns, endpoint, check_bucket = store_args(env)
    rclone(ns, "delete", "s3:" + bucket(env), endpoint=endpoint, check_bucket=check_bucket)
    left = rclone(ns, "lsf", "-R", "--files-only", "s3:" + bucket(env),
                  endpoint=endpoint, check_bucket=check_bucket).split()
    if left:
        raise Failed("bucket %s still holds %d object(s) after delete" % (bucket(env), len(left)))
    return bucket(env)


# --- check: the isolation test (AC-2, AC-3, AC-6) ----------------------------------------------

CURL = "curlimages/curl:8.10.1@sha256:d9b4541e214bcd85196d6e92e2753ac6d0ea699f0af5741f8c6cccbfcf00ef4b"

# The probes speak S3 with curl's SigV4 signing: exactly one request per verb, as the gateway logs
# it. (rclone checks before it acts, a HEAD before a read, a write or a delete, so a 403 on the
# HEAD failed it without the verb ever reaching the gateway: pre-review round 1.) Each line is
# "<http status> <label>"; 000 is a request that didn't complete. The pair comes from the pod's
# envFrom, so it is never in this process.
S3_FN = r'''
echo hello > /tmp/o
s3() { label="$1"; shift
  CODE=$(curl -sS -o /tmp/body -w '%{http_code}' --aws-sigv4 "aws:amz:us-east-1:s3" \
    --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" -H "x-amz-content-sha256: UNSIGNED-PAYLOAD" "$@") || CODE=000
  echo "$CODE $label"; }
has() { [ "$CODE" = 200 ] && grep -q "<Key>$1</Key>" /tmp/body; }
'''

# `sh -c PROBE probe <phase> <own bucket> <other bucket> <run> <endpoint>`. Keys follow the snapshot
# layout {zone}/{tick}/{state_version}/{offset}, so AC-6's put and get are the server's shape.
PROBE = S3_FN + r'''
phase="$1"; own="$2"; other="$3"; run="$4"; H="$5"
seed="check-$run/1/1/0"; mine="check-$run/1/1/1"; stray="check-$run/1/1/9"
case "$phase" in
seed)  s3 "seed $own" -X PUT --data-binary @/tmp/o "$H/$own/$seed" ;;
test)  s3 "write $own" -X PUT --data-binary @/tmp/o "$H/$own/$mine"
       s3 "list $own" "$H/$own?list-type=2&prefix=check-$run/"
       if has "$mine"; then echo "200 listed $own"; else echo "000 listed $own"; fi
       s3 "read $own" "$H/$own/$mine"
       if [ "$CODE" = 200 ] && grep -qx hello /tmp/body; then echo "200 content $own"; else echo "000 content $own"; fi
       s3 "delete $own" -X DELETE "$H/$own/$mine"
       s3 "list $other" "$H/$other?list-type=2"
       s3 "read $other" "$H/$other/$seed"
       s3 "write $other" -X PUT --data-binary @/tmp/o "$H/$other/$stray"
       s3 "delete $other" -X DELETE "$H/$other/$seed" ;;
final) # Prefixed listings: a bucket holding a thousand snapshot keys can't push these off the page.
       s3 "list run in $own" "$H/$own?list-type=2&prefix=check-$run/"; c1=$CODE; cp /tmp/body /tmp/l1
       s3 "list it in $own" "$H/$own?list-type=2&prefix=it-$run"; c2=$CODE
       # A listing that failed proves nothing: both verdicts need its 200.
       if [ "$c1" = 200 ] && [ "$c2" = 200 ] && ! grep -q "<Key>$stray</Key>" /tmp/l1 \
          && ! grep -q "<Key>it-$run</Key>" /tmp/body; then
         echo "200 no stray in $own"; else echo "000 no stray in $own"; fi
       if [ "$c1" = 200 ] && grep -q "<Key>$seed</Key>" /tmp/l1; then
         echo "200 seed kept in $own"; else echo "000 seed kept in $own"; fi
       s3 "cleanup $own" -X DELETE "$H/$own/$seed" ;;
cleanup) # Best effort after a failure: everything a run can leave in this bucket.
       for k in "$seed" "$mine" "$stray" "it-$run"; do s3 "cleanup $k" -X DELETE "$H/$own/$k"; done ;;
esac
'''

# andara-it, in andara-shared with its own pair: `sh -c IT_PROBE probe <run> <endpoint> <bucket>...`.
IT_PROBE = S3_FN + r'''
run="$1"; H="$2"; shift 2; seed="check-$run/1/1/0"; tb="andara-test-$run"
s3 "create $tb" -X PUT "$H/$tb"
s3 "write $tb" -X PUT --data-binary @/tmp/o "$H/$tb/o"
s3 "delete $tb" -X DELETE "$H/$tb/o"
s3 "delete-bucket $tb" -X DELETE "$H/$tb"
for b in "$@"; do
  s3 "create $b" -X PUT "$H/$b"
  s3 "list $b" "$H/$b?list-type=2"
  s3 "read $b" "$H/$b/$seed"
  s3 "write $b" -X PUT --data-binary @/tmp/o "$H/$b/it-$run"
  s3 "delete $b" -X DELETE "$H/$b/$seed"
  s3 "delete-bucket $b" -X DELETE "$H/$b"
done
'''

# andara-it's best-effort cleanup: `sh -c IT_CLEANUP probe <run> <endpoint>`.
IT_CLEANUP = S3_FN + r'''
run="$1"; H="$2"; tb="andara-test-$run"
s3 "cleanup $tb/o" -X DELETE "$H/$tb/o"
s3 "cleanup $tb" -X DELETE "$H/$tb"
'''

VERBS_OWN = ("write", "list", "listed", "read", "content", "delete")
VERBS_OTHER = ("list", "read", "write", "delete")
VERBS_IT = ("list", "read", "write", "delete", "delete-bucket")
# The S3 operations each refused verb reaches the gateway as (its access-log names).
REFUSED_OPS_ENV = ("ListObjectsV2", "GetObject", "PutObject", "DeleteObject")
REFUSED_OPS_IT = REFUSED_OPS_ENV + ("DeleteBucket",)
OK, DENIED, EXISTS = "2xx", "403", "409"


def verdicts(logs):
    """{label: http status} from a probe's output."""
    out = {}
    for line in logs.splitlines():
        code, _, label = line.strip().partition(" ")
        if len(code) == 3 and code.isdigit() and label:
            out[label] = code
    return out


def access_log(lines):
    """versitygw access-log entries as dicts: owner, bucket, requester, op, uri, status, error.
    Other lines (its startup banner, debug output) are skipped."""
    out = []
    for line in lines.splitlines():
        f = line.split()
        if len(f) < 12 or not f[2].startswith("[") or not f[7].startswith("s3_"):
            continue
        out.append({"owner": f[0], "bucket": f[1], "requester": f[5], "op": f[7][3:],
                    "uri": f[9], "status": f[10], "error": f[11]})
    return out


def _matches(code, want):
    return code.startswith("2") if want == OK else code == want


def expect(got, want, who):
    """'who label: got X, want Y' for every label in want whose status isn't the one wanted."""
    return ["%s %s: got %s, want %s" % (who, label, got.get(label, "missing"), w)
            for label, w in want.items() if not _matches(got.get(label, "missing"), w)]


def other_env(env):
    return [e for e in SHARED_ENVS if e != env][0]


def env_probe(env, phase, run):
    ns = env_ns(env)
    ok, logs = run_pod(ns, CURL, ["probe", phase, bucket(env), bucket(other_env(env)), run, SHARED_ENDPOINT],
                       env_from=[SECRET], command=["sh", "-c", PROBE])
    if not ok:
        raise Failed("probe %s %s in %s did not run: %s" % (phase, env, ns, logs.strip()[-300:]))
    return verdicts(logs)


def _refused(entries, requester, op, b):
    return any(e["requester"] == requester and e["op"] == op and e["bucket"] == b
               and e["status"] == "403" and e["error"] == "AccessDenied" for e in entries)


def gateway_findings(entries, env_buckets):
    """What the gateway's access log must show, request by request, so a refusal the client invented
    (or a request the client never sent) can't pass: each environment refused 403 AccessDenied on
    every verb against the other's bucket; andara-it refused every verb on both environment buckets,
    deleting them included, and 409 BucketAlreadyExists on taking either name."""
    bad = []
    for env in SHARED_ENVS:
        other = bucket(other_env(env))
        for op in REFUSED_OPS_ENV:
            if not _refused(entries, user(env), op, other):
                bad.append("%s %s on %s: no 403 AccessDenied naming them in the gateway's access log"
                           % (user(env), op, other))
    for b in env_buckets:
        if not any(e["requester"] == IT_USER and e["op"] == "CreateBucket" and e["uri"] == "/" + b
                   and e["status"] == "409" and e["error"] == "BucketAlreadyExists" for e in entries):
            bad.append("andara-it create %s: no 409 BucketAlreadyExists in the gateway's access log" % b)
        for op in REFUSED_OPS_IT:
            if not _refused(entries, IT_USER, op, b):
                bad.append("andara-it %s on %s: no 403 AccessDenied naming them in the gateway's access log" % (op, b))
    return bad


def check(cmd="check", run=None, now=time.time):
    require_shared(cmd)
    for name in (ROOT_SECRET, IT_SECRET):
        if not exists(SHARED_NS, "secret", name):
            raise Missing("%s/%s is missing; run `make objectstore-up` first" % (SHARED_NS, name))
    for env in SHARED_ENVS:
        if not exists(env_ns(env), "secret", SECRET):
            raise Missing("%s/%s is missing; run `make objectstore-install ENV=%s` first" % (env_ns(env), SECRET, env))
    run = run or "%x" % int(now())
    since = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(now() - 5))
    try:
        _run_check(cmd, run, since)
    except Exception:
        _cleanup(run)
        raise


def _cleanup(run):
    """Best effort after a failed run: the seeds, any stray, andara-it's object and its test bucket."""
    for env in SHARED_ENVS:
        try:
            env_probe(env, "cleanup", run)
        except Exception:  # noqa: BLE001 -- cleanup must not hide the failure that called it
            pass
    try:
        run_pod(SHARED_NS, CURL, ["probe", run, "http://%s:9000" % SERVICE], env_from=[IT_SECRET],
                command=["sh", "-c", IT_CLEANUP])
    except Exception:  # noqa: BLE001
        pass


def _run_check(cmd, run, since):
    bad = []
    for env in SHARED_ENVS:
        bad += expect(env_probe(env, "seed", run), {"seed " + bucket(env): OK}, user(env))
    if bad:
        raise Failed("seeding failed, so nothing after it would mean anything: " + "; ".join(bad))
    for env in SHARED_ENVS:
        own, other = bucket(env), bucket(other_env(env))
        want = {"%s %s" % (v, own): OK for v in VERBS_OWN}
        want.update({"%s %s" % (v, other): DENIED for v in VERBS_OTHER})
        wrong = expect(env_probe(env, "test", run), want, user(env))
        bad += wrong
        say(cmd, "%s: four verbs on %s, all four refused on %s: %s"
            % (user(env), own, other, "ok" if not wrong else "FAIL (%d)" % len(wrong)))
    env_buckets = [bucket(e) for e in SHARED_ENVS]
    ok, logs = run_pod(SHARED_NS, CURL, ["probe", run, "http://%s:9000" % SERVICE] + env_buckets,
                       env_from=[IT_SECRET], command=["sh", "-c", IT_PROBE])
    if not ok:
        raise Failed("andara-it's probe did not run: %s" % logs.strip()[-300:])
    tb = "andara-test-" + run
    want = {"create " + tb: OK, "write " + tb: OK, "delete " + tb: OK, "delete-bucket " + tb: OK}
    for b in env_buckets:
        want["create " + b] = EXISTS
        want.update({"%s %s" % (v, b): DENIED for v in VERBS_IT})
    wrong = expect(verdicts(logs), want, IT_USER)
    bad += wrong
    say(cmd, "andara-it: %s created, written and deleted; both existing names 409, every verb on %s 403: %s"
        % (tb, " and ".join(env_buckets), "ok" if not wrong else "FAIL (%d)" % len(wrong)))
    for env in SHARED_ENVS:
        bad += expect(env_probe(env, "final", run),
                      {"no stray in " + bucket(env): OK, "seed kept in " + bucket(env): OK,
                       "cleanup " + bucket(env): OK}, user(env))
    entries = access_log(kubectl(SHARED_NS, "logs", DEPLOYMENT, "--since-time=" + since, check=False).stdout)
    wrong = gateway_findings(entries, env_buckets)
    bad += wrong
    say(cmd, "gateway access log: every refused request reached it and was refused, by user and bucket: %s"
        % ("ok" if not wrong else "FAIL (%d)" % len(wrong)))
    if bad:
        raise Failed("the object store does not separate the environments as AW-INF-058 requires: "
                     + "; ".join(bad))
    say(cmd, "ok: each environment reaches only its own bucket; andara-it only its own andara-test-* bucket")


def main(argv):
    cmd = argv[0] if argv else ""
    try:
        if cmd in ("up", "check") and len(argv) == 1:
            if shutil.which("kubectl") is None:
                raise Missing("kubectl not found")
            (up if cmd == "up" else check)()
        elif cmd in ("install", "empty") and len(argv) == 2:
            env_ns(argv[1])  # usage before any tool or cluster check
            if shutil.which("kubectl") is None:
                raise Missing("kubectl not found")
            if cmd == "install":
                install(argv[1])
            else:
                say("empty", "emptied %s" % empty(argv[1]))
        else:
            raise Usage("usage: objectstore.py <up|check> | <install|empty> <local|dev|prod>")
    except Usage as e:
        print("make: objectstore: %s" % e, file=sys.stderr)
        return 2
    except (Missing, kube.Missing) as e:
        print("make: objectstore-%s: %s" % (cmd, e), file=sys.stderr)
        return 3
    except Failed as e:
        print("make: objectstore-%s: %s" % (cmd, e), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
