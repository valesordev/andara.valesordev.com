# AW-INF-033: the Content Repository's make bootstrap sets up commit signing

Story: `AW-INF-033` (`draft`).

## For architecture: SRE observability review, 2026-10-02

No change. "None" holds: a developer-machine target in `andara.solo7.media`. It reports through its
`bootstrap: <step>` lines. No metrics, traces, or alerts apply.

## Architecture: contract review, 2026-10-02

Moved to `ready` unchanged. This repo's `make bootstrap` signing block and #14's spec agree.
- AC-2's "no `No principal matched`" is the check that matters.
- The Builder's Guide section 4 edit is architecture's, at this story's §8.

## SRE: delivered, 2026-10-04

The build merged in `andara.solo7.media` as #24, applied by Brian from SRE's patch (SRE has no role in
that repository). AC-7's macOS job passed there; the record is in the story's "Verification record".

### For architecture: the Builder's Guide edit, for the §8 review
`docs/builders/04-your-first-zone.md` still describes the hand-typed steps. `make guide-check` passes
as it stands, since the section runs no `andara-cli` command. The edit this story's Definition of done
asks for:
- **The clone block** (around line 22) gains `make bootstrap` before `make tools`:
  `git clone … && cd andara.solo7.media && make bootstrap && make tools`.
- **"Sign your commits"**: step 1 (the GitHub Signing Key) stays, because that is the one thing the
  target can only check when `gh` is logged in. Steps 2 and 3 (`git config` lines, and the
  `~/.config/git/allowed_signers` file) become `make bootstrap`. It sets everything with
  `git config --local`, finds the key (`user.signingkey`, then the `identityfile` for github.com, then
  `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub` or `id_rsa.pub`), and trusts the repository's own tracked
  `.github/allowed_signers`, so the hand-made local file goes away.
- **A line for the allowed-signers file:** if `make bootstrap` prints
  `<email> <keytype> <key> … Add this line by pull request`, the Builder adds it to
  `.github/allowed_signers` in their first PR. It isn't fatal: GitHub still verifies the commit.
- **Keep** the passphrase paragraph (`ssh-add`, naming the private key) and the re-sign paragraph.
  `make bootstrap` fails with the exact `ssh-add` command when the agent doesn't hold the key.
- Section 9 and any other page that repeats the signing steps should point at `make bootstrap` too.

