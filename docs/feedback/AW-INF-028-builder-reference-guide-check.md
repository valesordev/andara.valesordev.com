# AW-INF-028: make builder-reference and make guide-check

Story: `AW-INF-028` (`draft`).

## For architecture: SRE observability review, 2026-10-02

No change. "None" holds: `make` targets in CI with no service. They report through exit codes and
their `builder-reference:`/`guide-check:` lines. No metrics, traces, or alerts apply.
