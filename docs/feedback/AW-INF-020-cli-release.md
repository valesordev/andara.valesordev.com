# AW-INF-020: andara-cli release binaries

Story: `AW-INF-020` (`draft`).

## For architecture: SRE observability review, 2026-09-28

**Amended (small).**
- The job summary names the commit `cli-dev` carries, and lists each archive's checksum.
- *Alerts: none* is explicit. A failed publish is a red run on `main`, like the image publish.

If `docs/feedback/AW-INF-021-dev-content-store.md` item 4 is decided as (b), this story also
bundles `andara.core`, and `andara-cli version` prints the bundled core version. That's a contract
change for you. SRE's view is in that file.
