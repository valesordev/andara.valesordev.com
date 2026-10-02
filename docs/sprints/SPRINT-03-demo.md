# SPRINT-03 demo — Builder content on `dev`

**Goal (from `SPRINT-03.md`):** Brian starts with no clone of the code repository, only the Content
Repository and the Builder's Guide (`docs/builders/`). He writes a Zone in his own pack and has
`check` pass on its pull request. He publishes it to `dev` with `andara-cli`, approves it with his
Operator identity, and activates it. Then he spawns in Purgatory, uses `goto` to reach the Zone in
`andara-cli play`, changes it, publishes again, and rolls it back. `dev` does all of this with no
deploy. This is `docs/roadmap.md`'s **M3 gate**, and Phase 1 exit criterion 4.

**Result: passed, with one §9 defect.** Step 3, signing commits for the Content Repository, is a
hand-typed `git config` sequence: `§9 defect → AW-INF-033`, in SPRINT-04.
- **Steps 1–11:** run by Brian on 2026-10-01 and 2026-10-02, as `AW-INF-023`'s AC-3
  walk-through. The run was on `dev` rebuilt from a deleted namespace, with `andara-cli 1a99cfc` on
  macOS. The transcript, redacted, is `docs/feedback/AW-INF-023-walkthrough-transcript.md`. It
  starts partway through step 2 (after `git clone` and `make tools`), so step 1 is recorded by
  `AW-INF-023`'s "AC-3 walk-through" section, not the transcript.
- **Steps 2 and 4's validation:** re-run by PM at the close-out on 2026-10-02, on a fresh clone of
  the Content Repository with `andara-cli ba7e4a4` (`origin/main`).
- **PM didn't re-run steps 1, 3 and 5–11**, against the PM charter's "run every step". Each needs a
  Builder or Operator login or a signing key, and the close-out session holds none of them. **Brian
  waived the re-run in the close-out session on 2026-10-02** (his answer "1a": accept the
  walk-through as the run of the `dev` steps). `dev` was then rebuilt again for `AW-INF-021`'s AC-7
  confirming run, so `glade` is no longer on it.
- Run output below is quoted from the transcript or PM's run. `…` marks lines left out.

## Preconditions

- An Operator Account on `dev`, and a device on the tailnet, or the box itself (guide section 2).
  `dev`'s edge is `andara-dev.solo7.valesordev.com:443`.
- Access to the Content Repository, `valesordev/andara.solo7.media`, and an SSH key.
- `make` and `git`. No Go toolchain, and no clone of the code repository.
- The Builder's Guide: `docs/builders/README.md`, sections 2–4.

## Steps

Each step names the command, what you should see, and what the run saw. The section numbers are the
Builder's Guide's.

1. **Get a Builder Account and a pack** (section 2). As the Operator:
   `andara-cli account create --username <you> --role builder --role player`, then
   `andara-cli account set-packs <account-id> --pack glade`.
   - **Expect:** the new Account's ID, then its packs listed as `glade`.
   - **Run:** completed in Brian's walk-through. The transcript starts after it.

2. **Install the pinned `andara-cli` and check the repository** (section 4). In a fresh clone of the
   Content Repository: `make tools`, then `make check`.
   - **Expect:** `andara-cli <sha> from release cli-dev (…, sha256 ok)`. Then each pack validated
     against `core andara.core@1`, ending `ok  content/<pack>` for every pack.
   - **Run (PM, 2026-10-02):**
     ```
     andara-cli ba7e4a4 from release cli-dev (andara-cli_ba7e4a4_linux_amd64.tar.gz, sha256 ok)
     spec: .tools/spec/content-language/
     …
     lore-check: 4 pages ok
     1 zones, 2 rooms, 0 templates, core andara.core@1
     validate content/example: ok
     1 zones, 2 rooms, 0 templates, core andara.core@1
     validate content/glade: ok
     1 zones, 2 rooms, 0 templates
     ok  content/example
     1 zones, 2 rooms, 0 templates
     ok  content/glade
     ```
     Brian's run on macOS saw the same against `1a99cfc`. Its `make tools` defect on Make 3.81 is
     fixed (`andara.solo7.media` #11, with a macOS CI job).

3. **Sign your commits** (section 4, "Sign your commits"). `§9 defect → AW-INF-033`. The guide's
   steps are a GitHub Signing Key, then five `git config` lines, an allowed-signers file, and
   `ssh-add` for a key with a passphrase. No target does them.
   - **Expect:** `git log --show-signature -1` reads `Good "git" signature for <your email>`.
   - **Run:** Brian's first commit went up unsigned. He then ran the sequence by hand, and re-signed
     the branch four times: the first commit had gone up unsigned, `ssh-add` was first given the
     `.pub` (so each signature asked for the passphrase), the per-repo name and email were missing,
     and the author needed `--reset-author`. The transcript doesn't show a verification;
     `git log --show-signature -1` printed nothing. The guide now covers each of those. `make bootstrap` in
     the Content Repository replaces the sequence (`AW-INF-033`, from `andara.solo7.media` #14).

4. **Write the Zone, then format and validate it** (section 4, "Write the pack", "Format and
   validate"). Write `content/glade/pack.aw` and `glade.aw`, then run
   `andara-cli content fmt --path content/glade` and
   `andara-cli content validate --path content/glade`.
   - **Expect:** `1 zones, 2 rooms, 0 templates, core andara.core@1`.
   - **Run:** as expected for Brian. PM's step 2 validates the same pack on the repository's `main`.

5. **Open a pull request; `check` passes on it** (section 4, "Open a pull request"). Commit signed,
   push, and open the PR.
   - **Expect:** the repository's `check` workflow green, and the PR merges.
   - **Run:** `glade-first-zone` merged to the Content Repository's `main`. Opening and merging a PR
     is a GitHub action with no command, accepted at `AW-INF-023`'s §8 as within AC-4.

6. **Publish** (section 4, "Publish"). As the Builder:
   `andara-cli auth login --username <you>`, then `andara-cli content publish --path content/glade`.
   - **Expect:** `glade@1 published (first version), awaiting approval`.
   - **Run:** as expected. Publish also printed the fixture's `missing_reverse_exit` warning for
     `purgatory/start` as if it were glade's. That's #312, and the guide tells Builders to ignore it
     until it's fixed.

7. **Approve it with the Operator Account** (section 4, "Approve it"):
   `andara-cli --credentials ~/.config/andara/operator.yaml content approve glade 1`.
   - **Expect:** `glade@1 approved by <operator>`.
   - **Run:** `glade@1 approved by operator`. The publisher was Brian's Builder Account, `solo7`, so
     this wasn't a self-approval, and `content.operator_self_approval` didn't apply. Brian ran
     `auth login` between the two Accounts instead of using `--credentials`. The guide now shows `--credentials` ("Two Accounts, one person").

8. **Activate it, and see it named** (section 4, "Activate it"):
   `andara-cli content activate glade 1`, answer `y`, then `andara-cli server info`.
   - **Expect:** `glade@1 active (nothing was active)`. `server info` lists `content glade@1` beside
     `andara.core@1` and `town@1`, with a new `content_digest`.
   - **Run:** as expected. There was no deploy. `version` stayed `1a99cfc` before and after.

9. **Spawn in Purgatory and walk the Zone** (section 4, "Walk it"):
   `andara-cli character create Wren`, then `andara-cli play --character Wren`, then
   `goto glade/clearing`, `north`, `south`.
   - **Expect:** `Purgatory` first. Then `A Clearing` after `goto`, and `By the Stream` after `north`.
   - **Run:**
     ```
     -- Connected to andara-dev.solo7.valesordev.com:443 as solo7, playing Wren (session …, protocol 1).
     Wren arrives.
     Purgatory
     Placeholder: Purgatory's description is Brian's to write.
     Exits: out
     > goto glade/clearing
     Wren leaves.
     Wren arrives.
     A Clearing
     Tall grass, and a ring of birches.
     Exits: north
     > north
     Wren leaves north.
     Wren arrives from the south.
     By the Stream
     Cold water runs over flat stones.
     Exits: south
     ```

10. **Change it and publish again** (section 4, "Change it"). Edit, `fmt`, `validate`, then PR and
   merge, then `content publish`, `approve glade 2`, `activate glade 2`, `server info`, and `play`.
   - **Expect:** `glade@2 published (parent 1)`, then `glade@2 active (was glade@1)`. `server info`
     lists `glade@2`, and the Room shows the new text.
   - **Run:** as expected. `By the Stream` read `Cold water runs over flat stones with a calming
     sound.` One false step: Brian ran `approve glade 2` before publishing it, and got
     `glade@2 is not published`, which is the refusal working.

11. **Roll it back** (section 4, "Roll it back"): `andara-cli content rollback glade`, then `y`.
    - **Expect:** `glade@1 active (rolled back from glade@2)`.
    - **Run:** as expected. The transcript ends there, without a final `server info`. The rollback's
      own line names the version in effect.

## What the run found

The nine points where Brian needed something the guide didn't say are listed, each with what closed
it, in `AW-INF-023`'s "AC-3 walk-through" record:
- the guide covers points 1, 4 and 6–9 (#330);
- #328 covers point 2, Admin from the box;
- `andara.solo7.media` #11 covers point 3, `make tools` on macOS;
- #329 covers point 5.

Apart from step 3, none is a §9 defect. Two things a Builder sees are still open:
- #312, publish showing another pack's findings. It's in SPRINT-04.
- Purgatory's placeholder description, which is Brian's to write.
