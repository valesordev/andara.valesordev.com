# shellcheck shell=bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# scripts/kube.sh — sourced by every shell script that reaches a cluster (AW-INF-053). The shell
# twin of scripts/kube.py, which holds the check: ANDARA_KUBE_CONTEXT, default kind-andara, is
# passed on every call, and nothing reads or moves the kubeconfig's current-context.
#
# Call kube_require once, in the script's own shell (not inside $(...), where its exit would end
# only the subshell), before the first kube_kubectl or kube_helm. It prints `context: <ctx>` to
# stderr and exits 3 when the kubeconfig has no such context.

ANDARA_KUBE_CONTEXT="${ANDARA_KUBE_CONTEXT:-kind-andara}"
export ANDARA_KUBE_CONTEXT

kube_require() {
  "${PY:-python3}" "$(dirname "${BASH_SOURCE[0]}")/kube.py" require ${1:+"$1"} || exit $?
}

kube_kubectl() {
  "${KUBECTL:-kubectl}" --context "$ANDARA_KUBE_CONTEXT" "$@"
}

# For a call backgrounded with &: bash runs a function there in a subshell, so `$!` would name the
# subshell and `kill $!` would leave kubectl (a port-forward) running. exec makes the subshell
# kubectl. Never call it in the script's own shell, which exec would replace.
kube_kubectl_exec() {
  exec "${KUBECTL:-kubectl}" --context "$ANDARA_KUBE_CONTEXT" "$@"
}

kube_helm() {
  "${HELM:-helm}" --kube-context "$ANDARA_KUBE_CONTEXT" "$@"
}
