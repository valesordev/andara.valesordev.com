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
        self.assertEqual(objectstore.verdicts("ok write b\nrefused list c\nnoise\ntried create b\n"),
                         {"write b": "ok", "list c": "refused"})


class GatewayFindings(unittest.TestCase):
    def entry(self, requester, op, bucket, status, error):
        return {"owner": "x", "bucket": bucket, "requester": requester, "op": op,
                "uri": "/" + bucket, "status": status, "error": error}

    def full(self):
        return [self.entry("andara-it", "CreateBucket", "andara-snapshots-local", "409", "BucketAlreadyExists"),
                self.entry("andara-it", "CreateBucket", "andara-snapshots-dev", "409", "BucketAlreadyExists"),
                self.entry("andara-local", "ListObjects", "andara-snapshots-dev", "403", "AccessDenied"),
                self.entry("andara-dev", "ListObjects", "andara-snapshots-local", "403", "AccessDenied")]

    def test_complete_log_has_no_findings(self):
        self.assertEqual(objectstore.gateway_findings(self.full(), ["andara-snapshots-local", "andara-snapshots-dev"]), [])

    def test_a_create_that_succeeded_is_a_finding(self):
        log = self.full()
        log[0] = self.entry("andara-it", "CreateBucket", "andara-snapshots-local", "200", "-")
        self.assertEqual(objectstore.gateway_findings(log, ["andara-snapshots-local", "andara-snapshots-dev"]),
                         ["andara-it create andara-snapshots-local: no 409 BucketAlreadyExists in the gateway's access log"])

    def test_a_refusal_not_logged_against_the_user_is_a_finding(self):
        log = self.full()[:3]
        self.assertEqual(objectstore.gateway_findings(log, ["andara-snapshots-local", "andara-snapshots-dev"]),
                         ["andara-dev on andara-snapshots-local: no AccessDenied naming them in the gateway's access log"])


class Check(unittest.TestCase):
    """The isolation check over a fake gateway: probe outputs per (namespace, phase) and an access log."""

    def run_check(self, override=None, log=None):
        override = override or {}
        run = "r1"

        def env_out(env, phase):
            own, other = "andara-snapshots-" + env, "andara-snapshots-" + ("dev" if env == "local" else "local")
            lines = {"seed": ["ok seed " + own],
                     "test": ["ok %s %s" % (v, own) for v in objectstore.VERBS_OWN]
                     + ["refused %s %s" % (v, other) for v in objectstore.VERBS_OTHER],
                     "final": ["ok no stray in " + own, "ok seed kept in " + own, "ok cleanup " + own]}[phase]
            return "\n".join(override.get((env, phase), lines))

        def fake_run_pod(ns, image, args, env=(), env_from=(), command=None, **kw):
            if env_from == ["andara-objectstore-it"]:
                lines = ["ok create andara-test-r1", "ok write andara-test-r1", "ok delete-bucket andara-test-r1"]
                for b in ("andara-snapshots-local", "andara-snapshots-dev"):
                    lines += ["refused %s %s" % (v, b) for v in objectstore.VERBS_IT]
                return True, "\n".join(override.get("it", lines))
            return True, env_out(ns.replace("andara-", ""), args[1])

        default_log = GatewayFindings().full()
        out = io.StringIO()
        with mock.patch.object(objectstore, "require_shared", lambda cmd: None), \
                mock.patch.object(objectstore, "exists", lambda ns, kind, name: True), \
                mock.patch.object(objectstore, "run_pod", fake_run_pod), \
                mock.patch.object(objectstore, "kubectl", lambda *a, **k: mock.Mock(stdout="")), \
                mock.patch.object(objectstore, "access_log", lambda text: default_log if log is None else log), \
                contextlib.redirect_stdout(out):
            try:
                objectstore.check(run=run)
                return None, out.getvalue()
            except objectstore.Failed as e:
                return str(e), out.getvalue()

    def test_separated_stores_pass(self):
        err, out = self.run_check()
        self.assertIsNone(err, err)
        self.assertIn("objectstore-check: ok:", out)

    def test_a_cross_environment_read_that_succeeds_fails(self):
        lines = (["ok %s andara-snapshots-local" % v for v in objectstore.VERBS_OWN]
                 + ["refused list andara-snapshots-dev", "ok read andara-snapshots-dev",
                    "refused write andara-snapshots-dev", "refused delete andara-snapshots-dev"])
        err, _ = self.run_check({("local", "test"): lines})
        self.assertIn("andara-local read andara-snapshots-dev: got ok, want refused", err)

    def test_an_own_verb_that_is_refused_fails(self):
        lines = (["ok write andara-snapshots-dev", "ok list andara-snapshots-dev", "ok read andara-snapshots-dev",
                  "refused delete andara-snapshots-dev"]
                 + ["refused %s andara-snapshots-local" % v for v in objectstore.VERBS_OTHER])
        err, _ = self.run_check({("dev", "test"): lines})
        self.assertIn("andara-dev delete andara-snapshots-dev: got refused, want ok", err)

    def test_a_stray_write_that_landed_fails_even_if_the_client_said_refused(self):
        err, _ = self.run_check({("dev", "final"): ["refused no stray in andara-snapshots-dev",
                                                   "ok seed kept in andara-snapshots-dev",
                                                   "ok cleanup andara-snapshots-dev"]})
        self.assertIn("andara-dev no stray in andara-snapshots-dev: got refused, want ok", err)

    def test_andara_it_deleting_an_environment_bucket_fails(self):
        lines = ["ok create andara-test-r1", "ok write andara-test-r1", "ok delete-bucket andara-test-r1"]
        for b in ("andara-snapshots-local", "andara-snapshots-dev"):
            lines += ["refused %s %s" % (v, b) for v in objectstore.VERBS_IT if v != "delete-bucket"]
            lines += ["ok delete-bucket %s" % b if b.endswith("dev") else "refused delete-bucket %s" % b]
        err, _ = self.run_check({"it": lines})
        self.assertIn("andara-it delete-bucket andara-snapshots-dev: got ok, want refused", err)

    def test_a_missing_probe_line_is_a_failure_not_a_pass(self):
        lines = ["ok %s andara-snapshots-local" % v for v in objectstore.VERBS_OWN]  # no cross lines at all
        err, _ = self.run_check({("local", "test"): lines})
        self.assertIn("andara-local list andara-snapshots-dev: got missing, want refused", err)

    def test_a_failed_seed_stops_before_the_tests(self):
        err, out = self.run_check({("local", "seed"): ["refused seed andara-snapshots-local"]})
        self.assertIn("seeding failed", err)
        self.assertNotIn("four verbs", out)

    def test_the_gateway_log_is_part_of_the_verdict(self):
        err, _ = self.run_check(log=GatewayFindings().full()[1:])
        self.assertIn("andara-it create andara-snapshots-local: no 409 BucketAlreadyExists", err)


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
