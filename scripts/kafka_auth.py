#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development

"""The operator's credential on the shared broker (AW-INF-056; ADR-0011 as ADR-0013 amends it).

The broker authenticates every client with SCRAM-SHA-512, so every operator tool that talks to it
authenticates as the one superuser, andara-operator: the rpk toolbox (scripts/topics.py and what
calls it: topics-apply, world-reset, projector-stop/-rebuild, env-destroy) and the Kafka CLI tools
run inside a broker pod (`make shared-check`'s kafka-log-dirs, `make kafka-broker-bounce`'s
kafka-topics).

The password is read from the Secret the Strimzi User Operator writes for andara-operator in the
broker's namespace, with the operator's own kubeconfig, never through a workload. It reaches the
pod on the exec's stdin, never on a command line or in a pod spec, so it is in no process list and
no manifest:

  - rpk reads it into RPK_PASS (with RPK_USER and RPK_SASL_MECHANISM) and execs;
  - a broker-pod tool gets a client properties file in the pod's /tmp, removed when the tool exits.

    scripts/kafka_auth.py broker-exec <pod> -- <tool> <args…>    for kafka.sh: run one Kafka CLI
                                                                 tool in <pod>'s kafka container

Exit codes: the tool's own; 2 usage; 3 the operator's Secret, the cluster or kubectl missing.
"""

import base64
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import kube  # noqa: E402

OPERATOR = "andara-operator"
MECHANISM = "SCRAM-SHA-512"

# rpk: the first stdin line is the password; everything after it is rpk's own stdin.
RPK_AS = 'IFS= read -r RPK_PASS && export RPK_PASS RPK_USER=%s RPK_SASL_MECHANISM=%s && exec %s "$@"'
RPK_SCRIPT = RPK_AS % (OPERATOR, MECHANISM, "rpk")

# A Kafka CLI tool in a broker pod: a properties file in /tmp (the broker pod's writable emptyDir),
# passed with --command-config, removed on exit. Not exec'd, so the trap runs.
BROKER_SCRIPT = (
    'IFS= read -r p || exit 3; f="$(mktemp)"; trap \'rm -f "$f"\' EXIT; '
    'printf "security.protocol=SASL_PLAINTEXT\\nsasl.mechanism=%s\\n'
    'sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required '
    'username=\\"%s\\" password=\\"%%s\\";\\n" "$p" > "$f"; '
    '"$@" --command-config "$f"' % (MECHANISM, OPERATOR))


def die(msg, code=1):
    sys.stderr.write("make: kafka-auth: %s\n" % msg)
    sys.exit(code)


def namespace():
    return os.environ.get("ANDARA_KAFKA_NAMESPACE", "andara-shared")


def operator_password(ns=None, run=subprocess.run, principal=OPERATOR):
    """A principal's password (the operator's by default), from the User Operator's Secret in the
    broker's namespace; exit 3 naming it when absent."""
    ns = ns or namespace()
    p = run(kube.kubectl("-n", ns, "get", "secret", principal, "-o", "jsonpath={.data.password}"),
            capture_output=True, text=True)
    if p.returncode != 0 or not p.stdout.strip():
        die("no Secret %s/%s holding %s's password: the shared broker authenticates every "
            "client (AW-INF-056), and the User Operator writes it once `make shared-up` has applied the "
            "KafkaUsers" % (ns, principal, principal), 3)
    password = base64.b64decode(p.stdout.strip()).decode()
    if any(c in password for c in '"\\\n'):
        # The JAAS line quotes it; the User Operator generates alphanumeric passwords.
        die("the password in %s/%s holds a quote, backslash or newline, which the JAAS line can't carry"
            % (ns, principal), 3)
    return password


def rpk_argv(ns, toolbox, brokers, principal=OPERATOR, pod_timeout=None):
    """The prefix that runs rpk in the toolbox as principal (the operator by default); the password
    goes on stdin. pod_timeout ends rpk inside the pod: a killed `kubectl exec` leaves it running."""
    cmd = "timeout %d rpk" % pod_timeout if pod_timeout else "rpk"
    return kube.kubectl("-n", ns, "exec", "-i", toolbox, "--", "bash", "-c", RPK_AS % (principal, MECHANISM, cmd),
                        "rpk", "-X", "brokers=" + brokers)


def broker_argv(ns, pod, tool_args):
    """kubectl exec of one Kafka CLI tool in a broker pod as the operator; the password goes on stdin."""
    return kube.kubectl("-n", ns, "exec", "-i", pod, "-c", "kafka", "--", "bash", "-c", BROKER_SCRIPT,
                        "sh", *tool_args)


def main(argv):
    if len(argv) < 4 or argv[0] != "broker-exec" or argv[2] != "--":
        sys.stderr.write("usage: scripts/kafka_auth.py broker-exec <pod> -- <tool> <args…>\n")
        return 2
    ns = namespace()
    password = operator_password(ns)
    return subprocess.run(broker_argv(ns, argv[1], argv[3:]), input=password + "\n", text=True).returncode


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
