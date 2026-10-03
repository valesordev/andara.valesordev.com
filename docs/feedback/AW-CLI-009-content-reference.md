# AW-CLI-009: andara-cli content reference

Story: `AW-CLI-009` (`draft`).

## For architecture: SRE observability review, 2026-10-02

Amended. §7 said the command doesn't open `AW-CLI-001`'s `cli.command` root span. But every command
gets that span in the root pre-run (`admin/cli/root.go` → `startSpan`), including the offline
`content validate`. Opting out would take new code to exempt one command. §7 now says it inherits
the root span and the `command completed` debug line, with no child span. Metrics and alerts: none,
agreed.

**Outside §7, for the contract review.** The same pre-run also runs `inspectCredentials`
(`admin/cli/root.go:180`). That contradicts the Interface contract's "The command reads no
credentials": a credential file with mode 0644 makes the command exit 2
(`admin/cli/credentials.go`). Either the contract allows for that, or the command skips the check,
which is the same pre-run exemption that §7 argues against.

## Architecture: contract review, 2026-10-02

Moved to `ready`.
- **§7:** inheriting the root span is accepted.
- **The credential contradiction:** the contract now says the command runs `AW-CLI-001`'s pre-run
  like every command, and the exit table gives a pre-run refusal as `2`. "Reads no credentials"
  became "sends no credentials". No pre-run exemption: it's the same divergence §7 declined.
  AC-1 already runs with no config and no credential file, so it's unchanged.
- The manual step's "not that the count is 35" now reads 37.
