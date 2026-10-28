#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 Valesor Development
"""Print one key of a chart's values files merged as helm merges them (AW-INF-060).

    scripts/values_get.py <dotted.key> --values <file> [--values <file> ...]

A later file wins, a mapping merges key by key, and a null deletes the key. Prints the empty
string when the key is unset. scripts/helm_install.sh and scripts/stream_soak.sh read the
environment's values with the overlay `PROFILE=ci` adds (values/ci.yaml on CI's lifecycle
cluster) through this, so neither reads one file where helm installed two.
"""
import sys

import yaml


def merge(a, b):
    for k, v in b.items():
        if v is None:
            a.pop(k, None)
        elif isinstance(v, dict) and isinstance(a.get(k), dict):
            merge(a[k], v)
        else:
            a[k] = v
    return a


def get(key, files):
    v = {}
    for f in files:
        with open(f) as fh:
            merge(v, yaml.safe_load(fh) or {})
    for part in key.split("."):
        v = v.get(part) if isinstance(v, dict) else None
    return "" if v is None else v


def main(argv):
    if len(argv) < 3 or len(argv) % 2 == 0 or any(a != "--values" for a in argv[1::2]):
        print("usage: values_get.py <dotted.key> --values <file> [--values <file> ...]", file=sys.stderr)
        return 2
    print(get(argv[0], argv[2::2]))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
