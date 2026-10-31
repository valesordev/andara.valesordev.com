#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
#
# image_build.sh <image:tag>: local's build, `sha256:<hex>`, a hash of the two binaries the image
# carries (AW-INF-060). helm-install ENV=local stamps it on the pod templates (image.build), so a new
# build rolls the server and the projector and an unchanged one doesn't. Not the image's ID, which
# every rebuild changes (its layers' timestamps) when nothing that runs has.
#
# The image is the one `make image` built and `make kind-load` loads, never pulled. Exit 1, naming
# the targets, when it isn't here or doesn't carry both binaries.
set -euo pipefail

REF="${1:?usage: image_build.sh <image:tag>}"

ERR="$(mktemp)"
trap 'rm -f "$ERR"' EXIT
SUMS="$(docker run --rm --pull never --entrypoint sha256sum "$REF" \
  /usr/local/bin/andara-server /usr/local/bin/andara-projector 2>"$ERR" || true)"
if [[ "$(grep -c '^[0-9a-f]\{64\}  /usr/local/bin/andara-' <<<"$SUMS" || true)" != 2 ]]; then
  echo "make: helm-install: cannot hash andara-server and andara-projector in $REF; build and load it first (\`make image kind-load\`)" >&2
  # docker's own reason (no daemon, no permission, no such image), when it gave one.
  sed 's/^/make: helm-install:   docker: /' "$ERR" >&2
  exit 1
fi
echo "sha256:$(sha256sum <<<"$SUMS" | awk '{print $1}')"
