# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""AW-INF-058: objectstore.py's ENV handling (AC-7), its parsing of versitygw's admin tables and
access log, and the isolation check's verdicts, over fakes. The live run is `make objectstore-check`."""
import contextlib
import io
import os
import subprocess
import sys
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import objectstore  # noqa: E402

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


class Env(unittest.TestCase):
    """AC-7: local operates on andara-snapshots-local with andara-local's pair; staging is usage."""

    def test_local_and_dev_use_their_own_bucket_namespace_and_the_shared_gateway(self):
        for env in ("local", "dev"):
            ns, endpoint, check_bucket = objectstore.store_args(env)
            self.assertEqual(ns, "andara-" + env)  # the pod runs where andara-snapshot-s3 is env's pair
            self.assertEqual(objectstore.bucket(env), "andara-snapshots-" + env)
            self.assertEqual(endpoint, "http://andara-objectstore.andara-shared.svc:9000")
            self.assertFalse(check_bucket, "an environment user may not create buckets")

    def test_prod_keeps_its_own_per_namespace_store(self):
        ns, endpoint, check_bucket = objectstore.store_args("prod")
        self.assertEqual((ns, endpoint, check_bucket), ("andara-prod", None, True))

    def test_empty_local_runs_rclone_in_andara_local_with_its_secret(self):
        calls = []

        def fake_run_pod(ns, image, args, env=(), env_from=(), **kw):
            calls.append((ns, args, dict(env), list(env_from)))
            return True, ""
        with mock.patch.object(objectstore, "run_pod", fake_run_pod):
            self.assertEqual(objectstore.empty("local"), "andara-snapshots-local")
        for ns, args, env, env_from in calls:
            self.assertEqual(ns, "andara-local")
            self.assertEqual(env_from, ["andara-snapshot-s3"])
            self.assertEqual(env["RCLONE_CONFIG_S3_ENDPOINT"], "http://andara-objectstore.andara-shared.svc:9000")
            self.assertIn("s3:andara-snapshots-local", args)

    def test_staging_and_unset_exit_2_before_any_cluster_call(self):
        for argv in (["install", "staging"], ["empty", ""], ["install"], ["bogus"]):
            with self.subTest(argv=argv), \
                    mock.patch.object(objectstore, "kubectl", side_effect=AssertionError("cluster")), \
                    contextlib.redirect_stderr(io.StringIO()) as err:
                self.assertEqual(objectstore.main(argv), 2)
            self.assertIn("make: objectstore:", err.getvalue())

    def test_make_objectstore_install_env_staging_exits_2(self):
        # PATH without kubectl: a usage error must come first, so this can't reach a cluster.
        r = subprocess.run(["make", "-s", "-C", REPO, "objectstore-install", "ENV=staging"],
                           capture_output=True, text=True, env=dict(os.environ, PATH="/usr/bin:/bin"))
        self.assertEqual(r.returncode, 2, r.stderr)
        self.assertIn("ENV=staging: not one of local, dev, prod", r.stderr)


class Parsing(unittest.TestCase):
    USERS = """Account       Role      UserID  GroupID  ProjectID
-------       ----      ------  -------  ---------
andara-dev    user      0       0        0
andara-it     userplus  0       0        0
"""
    BUCKETS = """Bucket                  Owner
-------                 ----
andara-snapshots-dev    andara-dev

"""
    # Two access-log lines as v1.8.0 writes them, between banner and debug noise.
    LOG = (
        "initializing internal IAM with \"/data/iam\"\n"
        "andara-local andara-snapshots-local [24/October/2026:18:12:50 +0000] 10.244.2.13 andara-it "
        "GXNN s3_CreateBucket - /andara-snapshots-local 409 BucketAlreadyExists 0 0 0 0 - rclone/v1.75.1 - x SigV4 -\n"
        "18:13:42 | 107.202µs | | PUT |\n"
        "andara-dev andara-snapshots-dev [24/October/2026:18:12:50 +0000] 10.244.2.13 andara-local "
        "6T2K s3_ListObjects - /andara-snapshots-dev?prefix= 403 AccessDenied 0 0 0 0 - rclone/v1.75.1 - y SigV4 -\n")

    def test_admin_tables(self):
        self.assertEqual({r[0]: r[1] for r in objectstore.table(self.USERS)},
                         {"andara-dev": "user", "andara-it": "userplus"})
        self.assertEqual({r[0]: r[1] for r in objectstore.table(self.BUCKETS)},
                         {"andara-snapshots-dev": "andara-dev"})

    def test_access_log_entries_only(self):
        got = objectstore.access_log(self.LOG)
        self.assertEqual([(e["requester"], e["op"], e["bucket"], e["status"], e["error"]) for e in got],
                         [("andara-it", "CreateBucket", "andara-snapshots-local", "409", "BucketAlreadyExists"),
                          ("andara-local", "ListObjects", "andara-snapshots-dev", "403", "AccessDenied")])

    def test_verdicts(self):
        self.assertEqual(objectstore.verdicts("200 write b\n403 list c\nnoise\n000 read d\nok x y\n"
                                              "abc x\n4030 x\ncurl: (7) Failed to connect\n"),
                         {"write b": "200", "list c": "403", "read d": "000"})


BUCKETS = ["andara-snapshots-local", "andara-snapshots-dev"]


class GatewayFindings(unittest.TestCase):
    """The access log is the verdict: every refused request must have reached the gateway and been
    refused there, by op, requester and bucket (pre-review: rclone's HEAD-first hid the real verbs)."""

    @staticmethod
    def entry(requester, op, bucket, status, error):
        return {"owner": "x", "bucket": bucket, "requester": requester, "op": op,
                "uri": "/" + bucket, "status": status, "error": error}

    def full(self):
        log = []
        for b in BUCKETS:
            log.append(self.entry("andara-it", "CreateBucket", b, "409", "BucketAlreadyExists"))
            for op in objectstore.REFUSED_OPS_IT:
                log.append(self.entry("andara-it", op, b, "403", "AccessDenied"))
        for env, other in (("andara-local", "andara-snapshots-dev"), ("andara-dev", "andara-snapshots-local")):
            for op in objectstore.REFUSED_OPS_ENV:
                log.append(self.entry(env, op, other, "403", "AccessDenied"))
        return log

    def without(self, requester, op, bucket):
        return [e for e in self.full() if (e["requester"], e["op"], e["bucket"]) != (requester, op, bucket)]

    def test_complete_log_has_no_findings(self):
        self.assertEqual(objectstore.gateway_findings(self.full(), BUCKETS), [])

    def test_every_cross_bucket_verb_must_reach_the_gateway(self):
        for op in objectstore.REFUSED_OPS_ENV:
            with self.subTest(op=op):
                got = objectstore.gateway_findings(self.without("andara-local", op, "andara-snapshots-dev"), BUCKETS)
                self.assertEqual(got, ["andara-local %s on andara-snapshots-dev: no 403 AccessDenied naming them in "
                                       "the gateway's access log" % op])

    def test_andara_it_delete_bucket_must_be_refused_as_access_not_as_not_empty(self):
        log = self.without("andara-it", "DeleteBucket", "andara-snapshots-dev")
        log.append(self.entry("andara-it", "DeleteBucket", "andara-snapshots-dev", "409", "BucketNotEmpty"))
        self.assertEqual(objectstore.gateway_findings(log, BUCKETS),
                         ["andara-it DeleteBucket on andara-snapshots-dev: no 403 AccessDenied naming them in "
                          "the gateway's access log"])

    def test_a_create_that_succeeded_is_a_finding(self):
        log = self.without("andara-it", "CreateBucket", "andara-snapshots-local")
        log.append(self.entry("andara-it", "CreateBucket", "andara-snapshots-local", "200", "-"))
        self.assertEqual(objectstore.gateway_findings(log, BUCKETS),
                         ["andara-it create andara-snapshots-local: no 409 BucketAlreadyExists in the gateway's access log"])

    def test_a_create_with_the_right_error_but_a_wrong_status_is_a_finding(self):
        for status, error in (("200", "BucketAlreadyExists"), ("409", "BucketAlreadyOwnedByYou")):
            with self.subTest(status=status, error=error):
                log = self.without("andara-it", "CreateBucket", "andara-snapshots-dev")
                log.append(self.entry("andara-it", "CreateBucket", "andara-snapshots-dev", status, error))
                self.assertEqual(len(objectstore.gateway_findings(log, BUCKETS)), 1)

    def test_another_users_refusal_does_not_stand_in(self):
        # andara-it is refused on andara-snapshots-local in the same window; that's not andara-dev's.
        got = objectstore.gateway_findings(self.without("andara-dev", "ListObjectsV2", "andara-snapshots-local"), BUCKETS)
        self.assertEqual(got, ["andara-dev ListObjectsV2 on andara-snapshots-local: no 403 AccessDenied naming them in "
                               "the gateway's access log"])

    def test_an_allowed_request_is_not_a_refusal(self):
        for status, error in (("200", "-"), ("403", "SignatureDoesNotMatch"), ("200", "AccessDenied")):
            with self.subTest(status=status, error=error):
                log = self.without("andara-local", "PutObject", "andara-snapshots-dev")
                log.append(self.entry("andara-local", "PutObject", "andara-snapshots-dev", status, error))
                self.assertEqual(len(objectstore.gateway_findings(log, BUCKETS)), 1)


class Check(unittest.TestCase):
    """The isolation check over a fake gateway: probe outputs per (namespace, phase) and an access log."""

    @staticmethod
    def env_lines(env, phase):
        own, other = "andara-snapshots-" + env, "andara-snapshots-" + ("dev" if env == "local" else "local")
        return {"seed": ["200 seed " + own],
                "test": ["200 %s %s" % (v, own) for v in objectstore.VERBS_OWN]
                + ["403 %s %s" % (v, other) for v in objectstore.VERBS_OTHER],
                "final": ["200 no stray in " + own, "200 seed kept in " + own, "204 cleanup " + own]}[phase]

    @staticmethod
    def it_lines():
        lines = ["200 create andara-test-r1", "200 write andara-test-r1", "204 delete andara-test-r1",
                 "204 delete-bucket andara-test-r1"]
        for b in BUCKETS:
            lines += ["409 create " + b] + ["403 %s %s" % (v, b) for v in objectstore.VERBS_IT]
        return lines

    def run_check(self, override=None, log=None):
        override = override or {}
        calls = []

        def fake_run_pod(ns, image, args, env=(), env_from=(), command=None, **kw):
            calls.append((ns, image, list(env_from), command, list(args)))
            if env_from == ["andara-objectstore-it"]:
                return True, "\n".join(override.get("it", self.it_lines()))
            env_ = ns.replace("andara-", "")
            return True, "\n".join(override.get((env_, args[1]), self.env_lines(env_, args[1])))

        out = io.StringIO()
        with mock.patch.object(objectstore, "require_shared", lambda cmd: None), \
                mock.patch.object(objectstore, "exists", lambda ns, kind, name: True), \
                mock.patch.object(objectstore, "run_pod", fake_run_pod), \
                mock.patch.object(objectstore, "kubectl", lambda *a, **k: mock.Mock(stdout="")), \
                mock.patch.object(objectstore, "access_log",
                                  lambda text: GatewayFindings().full() if log is None else log), \
                contextlib.redirect_stdout(out):
            try:
                objectstore.check(run="r1")
                return None, out.getvalue(), calls
            except objectstore.Failed as e:
                return str(e), out.getvalue(), calls

    def test_separated_stores_pass(self):
        err, out, calls = self.run_check()
        self.assertIsNone(err, err)
        self.assertIn("objectstore-check: ok:", out)
        # Every probe is curl in the namespace whose own Secret it uses.
        self.assertEqual({(c[0], tuple(c[2])) for c in calls},
                         {("andara-local", ("andara-snapshot-s3",)), ("andara-dev", ("andara-snapshot-s3",)),
                          ("andara-shared", ("andara-objectstore-it",))})
        self.assertEqual({c[1] for c in calls}, {objectstore.CURL})
        self.assertFalse([c for c in calls if "cleanup" in c[4] or c[3][2] == objectstore.IT_CLEANUP],
                         "a passing run needs no cleanup")

    def test_a_cross_environment_read_that_succeeds_fails(self):
        lines = [l.replace("403 read", "200 read") for l in self.env_lines("local", "test")]
        err, _, _ = self.run_check({("local", "test"): lines})
        self.assertIn("andara-local read andara-snapshots-dev: got 200, want 403", err)

    def test_a_refusal_for_another_reason_is_not_a_refusal(self):
        lines = [l.replace("403 write", "404 write") for l in self.env_lines("dev", "test")]
        err, _, _ = self.run_check({("dev", "test"): lines})
        self.assertIn("andara-dev write andara-snapshots-local: got 404, want 403", err)

    def test_an_own_verb_that_is_refused_fails(self):
        lines = [l.replace("200 delete", "403 delete") for l in self.env_lines("dev", "test")]
        err, _, _ = self.run_check({("dev", "test"): lines})
        self.assertIn("andara-dev delete andara-snapshots-dev: got 403, want 2xx", err)

    def test_a_listing_without_the_written_key_fails(self):
        lines = [l.replace("200 listed", "000 listed") for l in self.env_lines("local", "test")]
        err, _, _ = self.run_check({("local", "test"): lines})
        self.assertIn("andara-local listed andara-snapshots-local: got 000, want 2xx", err)

    def test_a_stray_write_that_landed_fails(self):
        lines = [l.replace("200 no stray", "000 no stray") for l in self.env_lines("dev", "final")]
        err, _, _ = self.run_check({("dev", "final"): lines})
        self.assertIn("andara-dev no stray in andara-snapshots-dev: got 000, want 2xx", err)

    def test_andara_it_deleting_an_environment_bucket_fails(self):
        lines = [l.replace("403 delete-bucket andara-snapshots-dev", "204 delete-bucket andara-snapshots-dev")
                 for l in self.it_lines()]
        err, _, _ = self.run_check({"it": lines})
        self.assertIn("andara-it delete-bucket andara-snapshots-dev: got 204, want 403", err)

    def test_andara_it_taking_an_existing_name_fails(self):
        lines = [l.replace("409 create andara-snapshots-local", "200 create andara-snapshots-local")
                 for l in self.it_lines()]
        err, _, _ = self.run_check({"it": lines})
        self.assertIn("andara-it create andara-snapshots-local: got 200, want 409", err)

    def test_a_missing_probe_line_is_a_failure_not_a_pass(self):
        lines = [l for l in self.env_lines("local", "test") if "andara-snapshots-dev" not in l]
        err, _, _ = self.run_check({("local", "test"): lines})
        self.assertIn("andara-local list andara-snapshots-dev: got missing, want 403", err)

    def test_a_failed_seed_stops_before_the_tests(self):
        err, out, _ = self.run_check({("local", "seed"): ["403 seed andara-snapshots-local"]})
        self.assertIn("seeding failed", err)
        self.assertNotIn("four verbs", out)

    def test_the_gateway_log_is_part_of_the_verdict(self):
        log = GatewayFindings().without("andara-local", "DeleteObject", "andara-snapshots-dev")
        err, _, _ = self.run_check(log=log)
        self.assertIn("andara-local DeleteObject on andara-snapshots-dev: no 403 AccessDenied", err)


class CheckAndaraItPositive(Check):
    """AC-3's positive half: andara-it creates its andara-test-* bucket and can delete it again."""

    def test_andara_it_refused_its_own_test_bucket_fails(self):
        lines = [l.replace("200 create andara-test-r1", "403 create andara-test-r1") for l in self.it_lines()]
        err, _, _ = self.run_check({"it": lines})
        self.assertIn("andara-it create andara-test-r1: got 403, want 2xx", err)

    def test_andara_it_unable_to_delete_its_test_bucket_fails(self):
        lines = [l.replace("204 delete-bucket andara-test-r1", "409 delete-bucket andara-test-r1") for l in self.it_lines()]
        err, _, _ = self.run_check({"it": lines})
        self.assertIn("andara-it delete-bucket andara-test-r1: got 409, want 2xx", err)

    def test_a_failed_run_cleans_up_what_it_left(self):
        lines = [l.replace("403 read", "200 read") for l in self.env_lines("local", "test")]
        err, _, calls = self.run_check({("local", "test"): lines})
        self.assertIsNotNone(err)
        # cleanup runs in both namespaces (phase "cleanup") and for andara-it's test bucket
        env_cleanups = sorted(c[0] for c in calls if c[4][:2] == ["probe", "cleanup"])
        self.assertEqual(env_cleanups, ["andara-dev", "andara-local"])
        it_cleanups = [c for c in calls if c[2] == ["andara-objectstore-it"] and c[3][2] == objectstore.IT_CLEANUP]
        self.assertEqual(len(it_cleanups), 1)


# A stand-in for curl that the probe scripts call: it answers listings with $FAKE_LIST (and the code in
# $FAKE_LIST_CODE), a GET of an object with "hello", and anything else with 204, writing -o's file.
FAKE_CURL = r'''#!/bin/sh
out=""; url=""; meth=GET
while [ $# -gt 0 ]; do case "$1" in
  -o) out="$2"; shift 2;; -w|--aws-sigv4|--user|-H|--data-binary) shift 2;; -X) meth="$2"; shift 2;;
  -sS) shift;; *) url="$1"; shift;; esac; done
case "$meth $url" in
  *"list-type=2&prefix=it-"*) printf '%s' "$FAKE_IT_LIST" > "$out"; printf '%s' "${FAKE_LIST_CODE:-200}";;
  *list-type=2*) printf '%s' "$FAKE_LIST" > "$out"; printf '%s' "${FAKE_LIST_CODE:-200}";;
  "GET "*) echo hello > "$out"; printf 200;;
  *) : > "$out"; printf 204;;
esac
'''


@unittest.skipUnless(os.path.exists("/bin/sh"), "needs a POSIX sh")
class ProbeShell(unittest.TestCase):
    """The probe scripts themselves, run by sh against a fake curl (pre-review round 2: the stray and
    listing guards live in the shell, which the Python verdict tests never ran)."""

    def run_probe(self, phase, listing="", it_listing="", list_code="200"):
        import tempfile
        d = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, d)
        with open(os.path.join(d, "curl"), "w") as f:
            f.write(FAKE_CURL)
        os.chmod(os.path.join(d, "curl"), 0o755)
        env = dict(os.environ, PATH=d + ":" + os.environ["PATH"], FAKE_LIST=listing, FAKE_IT_LIST=it_listing,
                   FAKE_LIST_CODE=list_code, AWS_ACCESS_KEY_ID="a", AWS_SECRET_ACCESS_KEY="b")
        script = objectstore.PROBE.replace("/tmp/", d + "/")
        r = subprocess.run(["sh", "-c", script, "probe", phase, "own", "other", "r1", "http://h"],
                           env=env, capture_output=True, text=True)
        return objectstore.verdicts(r.stdout)

    def test_final_clean(self):
        got = self.run_probe("final", "<Key>check-r1/1/1/0</Key>")
        self.assertEqual((got["no stray in own"], got["seed kept in own"]), ("200", "200"))

    def test_final_a_landed_cross_write_is_a_stray(self):
        got = self.run_probe("final", "<Key>check-r1/1/1/0</Key><Key>check-r1/1/1/9</Key>")
        self.assertEqual(got["no stray in own"], "000")

    def test_final_andara_its_landed_write_is_a_stray(self):
        got = self.run_probe("final", "<Key>check-r1/1/1/0</Key>", it_listing="<Key>it-r1</Key>")
        self.assertEqual(got["no stray in own"], "000")
        # it-r10 is another run's key, not this one's
        self.assertEqual(self.run_probe("final", "<Key>check-r1/1/1/0</Key>",
                                        it_listing="<Key>it-r10</Key>")["no stray in own"], "200")

    def test_final_a_failed_listing_proves_nothing(self):
        got = self.run_probe("final", "<Key>check-r1/1/1/0</Key>", list_code="403")
        self.assertEqual((got["no stray in own"], got["seed kept in own"]), ("000", "000"))

    def test_final_a_missing_seed_is_reported(self):
        self.assertEqual(self.run_probe("final", "")["seed kept in own"], "000")

    def test_test_phase_listed_and_content(self):
        got = self.run_probe("test", "<Key>check-r1/1/1/1</Key>")
        self.assertEqual((got["listed own"], got["content own"]), ("200", "200"))
        self.assertEqual(self.run_probe("test", "<Key>check-r1/1/1/2</Key>")["listed own"], "000")


class Install(unittest.TestCase):
    """The user step: create when absent, reset when the pair was regenerated, else leave it."""

    def run_ensure(self, users_table, made):
        admin_calls = []

        def fake_admin(*args, secret_keys=()):
            admin_calls.append((args, list(secret_keys)))
            return users_table if args[0] == "list-users" else ""
        with mock.patch.object(objectstore, "ensure_pair", lambda *a, **k: made), \
                mock.patch.object(objectstore, "admin", fake_admin), \
                contextlib.redirect_stdout(io.StringIO()):
            objectstore.ensure_user("andara-dev", "andara-objectstore-dev", "user", "install")
        return [a for a, _ in admin_calls], [k for _, k in admin_calls]

    def test_absent_user_is_created_with_the_secret_by_reference(self):
        calls, keys = self.run_ensure(Parsing.USERS.replace("andara-dev    user      0       0        0\n", ""), True)
        self.assertEqual(calls[1][:6], ("create-user", "--access", "andara-dev", "--secret", "$(USER_SECRET)", "--role"))
        self.assertEqual(keys[1], [("USER_SECRET", "andara-objectstore-dev", "AWS_SECRET_ACCESS_KEY")])

    def test_existing_user_with_a_kept_pair_is_left(self):
        calls, _ = self.run_ensure(Parsing.USERS, False)
        self.assertEqual([c[0] for c in calls], ["list-users"])

    def test_existing_user_with_a_regenerated_pair_is_updated(self):
        calls, _ = self.run_ensure(Parsing.USERS, True)
        self.assertEqual(calls[1][0], "update-user")

    def test_a_user_with_the_wrong_role_fails(self):
        with self.assertRaisesRegex(objectstore.Failed, "has role userplus, not user"):
            self.run_ensure(Parsing.USERS.replace("andara-dev    user ", "andara-dev    userplus "), False)


class PairChanged(unittest.TestCase):
    """A changed pair reaches the gateway and the env Secret, but a running pod read it at start: the
    install says which workloads to restart and how (pre-review round 1, #4)."""

    def install(self, env, state):
        out = io.StringIO()
        with mock.patch.object(objectstore, "require_shared", lambda cmd: None), \
                mock.patch.object(objectstore, "exists", lambda ns, kind, name: True), \
                mock.patch.object(objectstore, "ensure_namespace", lambda env, cmd: None), \
                mock.patch.object(objectstore, "ensure_user", lambda *a: None), \
                mock.patch.object(objectstore, "ensure_bucket", lambda *a: None), \
                mock.patch.object(objectstore, "copy_pair", lambda *a: state), \
                mock.patch.object(objectstore, "rclone", lambda *a, **k: ""), \
                contextlib.redirect_stdout(out):
            objectstore.install_shared(env)
        return out.getvalue()

    def test_unchanged_says_nothing_about_restarts(self):
        for state in ("unchanged", "created"):
            with self.subTest(state=state):
                self.assertNotIn("the pair changed", self.install("dev", state))

    def test_dev_names_the_server_and_projector_targets(self):
        out = self.install("dev", "updated")
        self.assertIn("the pair changed", out)
        self.assertIn("make server-stop ENV=dev && make server-start ENV=dev", out)
        self.assertIn("make projector-stop ENV=dev && make projector-start ENV=dev", out)

    def test_local_restarts_the_server_and_the_projectors(self):
        out = self.install("local", "updated")
        self.assertIn("rollout restart statefulset/andara", out)
        self.assertIn("rollout restart deployment -l app.kubernetes.io/name=andara", out)
        self.assertNotIn("ENV=local", out)  # those targets don't take local


class Manifest(unittest.TestCase):
    """The shared gateway's manifest: the IAM directory, the access log, 10Gi, and the peer rule."""

    def setUp(self):
        import yaml
        with open(objectstore.SHARED_MANIFEST) as f:
            self.docs = {d["kind"]: d for d in yaml.safe_load_all(f) if d}

    def test_iam_dir_access_log_and_root_secret(self):
        c = self.docs["Deployment"]["spec"]["template"]["spec"]["containers"][0]
        self.assertIn("--iam-dir=/data/iam", c["args"])
        self.assertIn("--access-log=stdout", c["args"])
        self.assertNotIn("--quiet", c["args"])
        self.assertEqual(c["image"], objectstore.VERSITYGW)
        self.assertEqual(c["envFrom"], [{"secretRef": {"name": "andara-objectstore-root"}}])

    def test_pvc_is_10gi(self):
        self.assertEqual(self.docs["PersistentVolumeClaim"]["spec"]["resources"]["requests"]["storage"], "10Gi")

    def test_network_policy_admits_only_labelled_environments_and_tools(self):
        rule = self.docs["NetworkPolicy"]["spec"]["ingress"][0]
        peer = rule["from"][0]
        self.assertEqual(peer["namespaceSelector"]["matchExpressions"],
                         [{"key": "andara.valesor/env", "operator": "In", "values": ["local", "dev"]}])
        self.assertEqual(peer["podSelector"]["matchExpressions"][0]["values"], ["andara", "andara-objectstore-tools"])
        self.assertEqual(rule["from"][1], {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "andara-objectstore-tools"}}})
        self.assertEqual(len(rule["from"]), 2)


class ProdPath(unittest.TestCase):
    """AC-10: prod's path is unchanged (ADR-0013 decision 10)."""

    def test_prod_install_still_applies_the_per_namespace_manifest(self):
        with open(objectstore.MANIFEST) as f:
            text = f.read()
        self.assertIn("name: andara-snapshot-s3", text)  # its root pair is the env Secret, as before
        self.assertNotIn("--iam-dir", text)


if __name__ == "__main__":
    unittest.main()
