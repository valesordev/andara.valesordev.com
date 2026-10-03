---
id: AW-INF-033
title: The Content Repository's make bootstrap sets up commit signing
epic: EPIC-05
component: infra
type: infra
status: ready
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
3. **Given** a key the agent doesn't hold **when** `make bootstrap` runs **then** it exits 1 and
   prints `ssh-add --apple-use-keychain <key>` on Darwin, or `eval "$(ssh-agent -s)" && ssh-add <key>`
   elsewhere.
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
  precondition is missing (name or email, a key, the agent, or a Signing Key that a logged-in `gh`
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

- `[ASSUMPTION]` SRE has write access in the Content Repository, as for `andara.solo7.media` #11.
  If that repository's role hook blocks the SRE role there (as #14's comment reports), Brian decides
  which role builds it.
