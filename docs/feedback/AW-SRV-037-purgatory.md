# AW-SRV-037: Purgatory in the test content

Story: `AW-SRV-037` (`draft`).

## For architecture: SRE observability review, 2026-09-28

No change. The existing instruments it reads were checked against the code:
- `andara_content_zones_loaded`;
- `andara_content_load_warnings_total{kind}`, where `kind` is bounded by the sim's warning
  taxonomy.
