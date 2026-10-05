---
id: AW-INF-033
title: The Content Repository's make bootstrap sets up commit signing
epic: EPIC-05
component: infra
type: infra
status: done
size: S
depends_on: [AW-INF-022, AW-INF-023]
blocks: []
lane: sre
risk: low
---

## Context

The Content Repository (`valesordev/andara.solo7.media`, `AW-INF-022`) requires signed commits on
`main`, and nothing sets signing up. In SPRINT-03's demo (the M3 gate), Brian configured SSH signing
by hand, with `git config` lines, `ssh-add`, and a branch re-signed four times. #14 also lists a
GitHub Signing Key and an allowed-signers file, which the transcript doesn't show. The
Builder's Guide now documents those steps by hand (section 4, "Sign your commits"; #330). A
documented sequence with no target is a §9 defect, so the demo marks it `§9 defect → AW-INF-033`.

SRE already wrote the full specification and a draft script on `andara.solo7.media` #14, modelled on
this repo's `scripts/bootstrap.sh` signing block. Brian deferred it on 2026-10-02 because it needs a
session in that repository. This story is its carrier in the sprint plan. The contract below is #14's
spec, lifted. The build happens in the Content Repository.

## User story

As a builder, I want `make bootstrap` in a fresh clone of the Content Repository to set up commit
signing, or tell me exactly what's missing, so that my first pull request is signed without a page of
hand-typed `git config`.

## Scope

### In scope
- In `andara.solo7.media`: `make bootstrap`, which runs `scripts/bootstrap.sh` (bash; macOS's
  Make 3.81 and OpenSSH work). Also a tracked `.github/allowed_signers`, seeded with the Builders'
  keys, a README line ("after `git clone`, run `make bootstrap`, then `make tools`"), and a
  `CLAUDE.md` targets row.
- In this repo: the guide's "Sign your commits" switches to `make bootstrap`, as `AW-INF-023`'s §8
  close records. That's architecture's file (`docs/builders/`), so it's routed to architecture's
  §8 review of this story.

### Out of scope
- Signing for this repo, which `make bootstrap` here already does (CLAUDE.md §4).
- GPG keys. SSH only.

## Acceptance criteria

1. **Given** a fresh clone with `user.name` and `user.email` unset **when** `make bootstrap` runs
   **then** it exits 1 and prints the two `git config --local` lines, plus "verified on GitHub
   (Settings > Emails)".
2. **Given** a name, an email, an SSH key the agent holds, that key's `<email> <key>` line in
   `.github/allowed_signers`, and either no `gh` login or the key registered as a Signing Key
   **when** `make bootstrap` runs **then**
   `git config --local` has `gpg.format ssh`, `user.signingkey <key>`, `commit.gpgsign true`,
   `tag.gpgsign true` and `gpg.ssh.allowedSignersFile .github/allowed_signers`. A following
   `git commit` is signed and trusted: `git log --show-signature -1` reads
   `Good "git" signature for <email>`, and doesn't read `No principal matched`. (A key missing from
   `allowed_signers` still prints `Good "git" signature with …`, so the bare prefix proves nothing.)
   Running it a second time changes nothing and exits 0.
3. **Given** a key that can't sign without a prompt, such as a passphrase-protected key its agent
   doesn't hold, **when** `make bootstrap` runs **then** it exits 1 and prints
   `ssh-add --apple-use-keychain <key>` on Darwin, or `eval "$(ssh-agent -s)" && ssh-add <key>`
   elsewhere. (An unencrypted key signs without an agent, so it passes, as it would for `git`.)
4. **Given** a key that isn't in `.github/allowed_signers` **when** `make bootstrap` runs **then** it
   prints the exact `<email> <keytype> <key>` line to add by pull request, and exits 0.
5. **Given** `gh` logged in and the key not registered as a Signing Key **when** `make bootstrap`
   runs **then** it exits 1 with `gh ssh-key add <key> --type signing`. Without `gh`, or with `gh`
   not logged in, it prints the GitHub settings path and "Key type: Signing Key", and exits 0,
   because it can't check.
6. **Given** `CI` non-empty **when** `make bootstrap` runs **then** it sets only
   `allowedSignersFile`, prints `commit signing skipped (CI doesn't commit)`, and exits 0.
7. **Given** the `check (macOS, make 3.81)` job **when** it runs with `CI` emptied (`CI= make
   bootstrap`) **then** AC-2 holds against a set-up the job makes first: a local `user.name` and
   `user.email`, a throwaway ed25519 key added to a started `ssh-agent`, and that key's line
   appended to the checkout's `.github/allowed_signers`. There's no `gh` login, so AC-5's no-`gh`
   path applies.

## Interface contract

- `make bootstrap`: `## bootstrap: set this clone up to sign its commits (per repository, never
  global)`.
- Output: `bootstrap: <step>` lines; on failure, one `bootstrap: <what's missing>` line to stderr,
  plus the command that fixes it.
- Exit codes: `0` signing set up, or CI, or the GitHub check couldn't run (no `gh` login); `1` a
  precondition is missing (name or email, a key, a key that can't sign unattended such as a passphrase key its agent doesn't hold, or a Signing Key that a logged-in `gh`
  shows isn't registered).
- Key choice, in order: `user.signingkey` if set; the `identityfile` that `ssh -G github.com`
  reports (its `.pub`); then `~/.ssh/id_ed25519.pub`, `id_ecdsa.pub` or `id_rsa.pub`. With none, it
  prints `ssh-keygen -t ed25519 -C <email>`.
- Every write is `git config --local`. Nothing global.

## Data / state impact

None in the code repository. `.github/allowed_signers` is new and tracked in the Content Repository.

## Observability requirements

- **Metrics / Traces / Alerts:** none. It's a developer-machine target.
- **Logs:** the `bootstrap: <step>` lines above.

## Test plan

- **Unit:** none.
- **Integration:** the Content Repository's macOS CI job, as AC-7.
- **Manual/operator:** in a fresh clone, with your key's line in `.github/allowed_signers` (AC-4
  prints it if not), run `make bootstrap`, then `git commit --allow-empty -m t`, then
  `git log --show-signature -1`, which reads `Good "git" signature for <your email>`.

## Definition of done

CLAUDE.md §8, plus: the Builder's Guide section 4 says `make bootstrap` in place of the hand-typed
steps, and `andara.solo7.media` #14 is closed by the merging PR.

## Open questions

- ~~`[ASSUMPTION]` SRE has write access in the Content Repository~~ Resolved 2026-10-04: it doesn't
  (the hook blocked it, as #14's comment reported). SRE wrote the change as a patch and Brian applied it as
  `andara.solo7.media` #24.

## Verification record — 2026-10-04 (SRE; `review` until the §8 checklist passes)

The build is in `andara.solo7.media`, where SRE has no role (its roles are `content`, `producer`,
`world-builder`, `concept-art` and `writing-assistant`, so the Open question's case applied). SRE
wrote the change as a patch, tested it in a scratch repo, and Brian applied it by hand and merged
it as `andara.solo7.media` **#24** (merged 2026-10-04T15:53Z, a signed commit, `4044237`). Its six
files are byte-identical to the tested patch: `scripts/bootstrap.sh` (mode `100755`), `Makefile`,
`README.md`, `CLAUDE.md`, `.github/allowed_signers` and `.github/workflows/check.yaml`.

| AC | Shown by |
|----|----------|
| 1 | The patch's acceptance harness: with no name or email it exits `1`, prints both `git config --local` lines and "verified on GitHub (Settings > Emails)", and writes no signing config |
| 2 | The harness: the five `git config --local` values are set, a following commit reads `Good "git" signature for <email>` and not `No principal matched`, and a second run changes nothing and exits `0`. Also on the real macOS runner (AC-7 below) |
| 3 | **Met for a passphrase-protected key, and a deviation from the AC's wording otherwise; put to architecture (below).** The harness, with a passphrase key the agent doesn't hold: exit `1` and `eval "$(ssh-agent -s)" && ssh-add <key>`; with `uname` stubbed to Darwin, `ssh-add --apple-use-keychain <key>`; a passphrase key the agent does hold passes. An **unencrypted key the agent doesn't hold exits `0`**, where the AC as worded ("a key the agent doesn't hold") says `1`: git signs with that key without an agent, so `1` would reject a setup that works |
| 4 | The harness: a key absent from `.github/allowed_signers`, or present for another key under the same email, prints `<email> <keytype> <key>` and exits `0` |
| 5 | The harness, with `gh` stubbed: logged in and key unregistered is exit `1` with `gh ssh-key add <key> --type signing`. No `gh`, `gh` not logged in, or a `gh api` failure (a token without `admin:ssh_signing_key`) is exit `0` with the settings path and "Key type: Signing Key" |
| 6 | The harness: `CI=true` sets only `gpg.ssh.allowedSignersFile`, prints `commit signing skipped (CI doesn't commit)`, and exits `0` |
| 7 | **On `andara.solo7.media` #24, the `check (macOS, make 3.81)` job passed** (GNU Make 3.81, run `37214652163`). Its new step emptied `CI`, made a local identity, a throwaway key in `~/.ssh` and a started agent, appended the key's line to `allowed_signers`, ran `CI= /usr/bin/make bootstrap` twice with an unchanged config, and its commit read `Good "git" signature for ci-builder@example.com with ED25519 key SHA256:9efaoW…` |

The harness (42 checks, and 14 mutants of the script, each caught when run; the mutant list was
inline and wasn't kept) lived in SRE's scratchpad and isn't committed anywhere: that repository's Test plan has no unit tests, and AC-7's job is the standing
test. It ran on Linux with OpenSSH 10.5 and bash 5.3, so the macOS job is also the only run on
bash 3.2 and Make 3.81.

**Where the script improves on #14's draft:** a failing `gh api` is "can't check", not "key not
registered"; the sign test can't wait on a passphrase prompt (`SSH_ASKPASS` is an always-failing
program); a private `user.signingkey` resolves to its `.pub`; and `grep` isn't `-q` under `pipefail`,
so a SIGPIPE can't read as "not registered".

**AC-3's wording, for architecture's ruling.** `make bootstrap` tests what git needs, which is that the
key signs with nobody at the keyboard, not whether `ssh-agent` holds it. With no agent in the
environment, run against the merged script (2026-10-04):

| Key, no agent | `make bootstrap` | `git commit` |
|---------------|------------------|--------------|
| unencrypted | exit `0` ("the key signs unattended") | exit `0`; `git log --show-signature` reads `Good "git" signature for <email>` |
| passphrase-protected | exit `1`, with the `ssh-agent` + `ssh-add` hint | exit `128`: `Enter passphrase for …` |

So the script agrees with git in both rows. AC-3 and the Interface contract's exit-`1` list ("the agent
holding it") name the agent as the condition. SRE has not changed either: the contract is
architecture's, and the amendment is in `docs/feedback/AW-INF-033-content-repo-bootstrap-signing.md`.
The story's §8 item "every acceptance criterion demonstrably passes" depends on that ruling.

**Definition of done:**
- **Builder's Guide section 4.** It's architecture's file (`docs/builders/04-your-first-zone.md`,
  "Sign your commits"). The edit is routed to architecture's §8 review in
  `docs/feedback/AW-INF-033-content-repo-bootstrap-signing.md`.
- **`andara.solo7.media` #14 is still open.** #24's commit message and body don't close it
  (`closingIssuesReferences` is empty). It needs closing by hand, citing #24.

**Not run by SRE:** the Manual/operator step in a fresh clone against a real GitHub account. #24's
CI has no `gh` login and no GitHub Signing Key, so the "GitHub has this key" path and AC-5's
`gh ssh-key add` path ran only against the `gh` stub.

## Architecture: §8 review, 2026-10-04 (stays at `review`)

- **AC-3 and the contract, ruled.** SRE's wording stands. The build tests that the key signs unattended
  (`ssh-keygen -Y sign`, what `git` runs), and the verification record's table shows the script and `git`
  agree in both rows. Exiting `1` for an unencrypted key outside the agent would reject a clone that signs.
  AC-3 and the contract's exit-`1` list are amended above, and the code stays as merged. This is a contract
  change after `ready`, recorded here.
- **The guide edit is done.** `docs/builders/04-your-first-zone.md`, "Sign your commits": step 1 (the
  Signing Key) stays; the `--local` identity lines come first; `make bootstrap` replaces the hand-typed
  `gpg.format`, `user.signingkey`, `commit.gpgsign` and trust-file steps; it says the target can stop and
  print what's missing and is safe to re-run, and what to do with the `allowed_signers` line it prints. The
  clone block is unchanged, as SRE asked. `make guide-check` passes.
- **Open before `done`:** (1) the Manual/operator step, `make bootstrap` in a fresh clone against a real GitHub
  account, which SRE couldn't run and which is the only run of the real `gh` paths; (2) the Definition of
  done's `andara.solo7.media` #14, still open: #24 didn't close it. Both are Brian's. When they're done, this
  story needs only its move to `done`.

## §8 review (architecture, 2026-10-05, second pass): done

Both open items are closed, on Brian's word and the repository: (1) Brian ran `make bootstrap` in a fresh
clone against his real GitHub account, the one run of the real `gh` paths; (2) `valesordev/andara.solo7.media`
#14 is closed. The rest held in the first pass (the contract rulings, the Builder's Guide edit, `make guide-check`)
and SRE's verification record above. The story moves from `review` to `done`.
