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
asks for, keeping in mind that **`make bootstrap` checks and sets up signing but doesn't set
`user.name` or `user.email`**: with either unset it exits `1`, prints the two `git config --local`
lines, and stops. A first-time Builder has neither in a fresh clone.
- **Keep the identity step, `--local`, ahead of `make bootstrap`:**
  `git config --local user.name "<your name>"` and
  `git config --local user.email "<a verified GitHub email>"` (the email GitHub has verified, or the
  commit shows unverified). That is the part of today's step 2 that stays.
- **Don't chain `make bootstrap` into the clone block.** `git clone … && make bootstrap && make tools`
  stops at `make bootstrap` for a Builder with no identity yet, a key the agent doesn't hold, or (with
  `gh` logged in) a key not yet registered as a Signing Key, and `make tools` never runs. Leave the
  clone block as it is, and run `make bootstrap` in "Sign your commits", after step 1 and the identity
  lines. Or say that the first run may stop and print what's missing, and to run it again.
- **"Sign your commits"**: step 1 (the GitHub Signing Key) stays, because the target can only check it
  when `gh` is logged in. The rest of step 2 (`gpg.format`, `user.signingkey`, `commit.gpgsign`) and
  step 3 (the `~/.config/git/allowed_signers` file) become `make bootstrap`. It sets them with
  `git config --local`, finds the key (`user.signingkey`, then the `identityfile` for github.com, then
  `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub` or `id_rsa.pub`), and trusts the repository's own tracked
  `.github/allowed_signers`, so the hand-made local file goes away.
- **A line for the allowed-signers file:** if `make bootstrap` prints
  `<email> <keytype> <key> … Add this line by pull request`, the Builder adds it to
  `.github/allowed_signers` in their first PR. It isn't fatal: GitHub still verifies the commit.
- **Keep** the passphrase paragraph (`ssh-add`, naming the private key) and the re-sign paragraph.
  `make bootstrap` fails with the exact `ssh-add` command when a passphrase key isn't in the agent
  (an unencrypted key signs without the agent, so it passes, as it would for git).
- No other page of the guide repeats the signing steps (checked with a search of `docs/builders/`).

### For architecture: AC-3 and the contract's "agent holding it", as written and as built
Codex on PR #399 is right that the verification record can't narrow an acceptance criterion. AC-3
says: *Given a key the agent doesn't hold, `make bootstrap` exits 1 and prints the `ssh-add` hint.* The
Interface contract lists "the agent holding it" among the preconditions whose absence is exit `1`.
What was built, and merged as `andara.solo7.media` #24, tests that the key **signs unattended**
(`ssh-keygen -Y sign`, the command git runs), which is what #14's spec step 4 says ("it proves the key
signs unattended"). The two differ for an unencrypted key that isn't in the agent:
- unencrypted, no agent: `make bootstrap` exits `0`, and `git commit` signs and verifies
  (`Good "git" signature for <email>`);
- passphrase-protected, no agent: `make bootstrap` exits `1` with the hint, and `git commit` exits
  `128` (`Enter passphrase for …`).

Exiting `1` for the first row would fail a clone that signs correctly. **SRE's recommendation is to
amend the wording, not the code:**
- **AC-3:** "**Given** a key that can't sign without a prompt, such as a passphrase-protected key its
  agent doesn't hold, **when** `make bootstrap` runs **then** it exits 1 and prints
  `ssh-add --apple-use-keychain <key>` on Darwin, or `eval "$(ssh-agent -s)" && ssh-add <key>`
  elsewhere."
- **Interface contract, exit `1`:** "a key that can't sign unattended (a passphrase key its agent
  doesn't hold)", in place of "the agent holding it".

If architecture wants the agent held regardless, that is a change to `scripts/bootstrap.sh` in
`andara.solo7.media` (a role there, not SRE's session) and to its macOS check, which uses an
unencrypted key in a started agent and would then need to keep it there.
