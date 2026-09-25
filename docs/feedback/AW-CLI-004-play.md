# Feedback — AW-CLI-004 `andara-cli play`

Spec: `docs/stories/AW-CLI-004-andara-cli-play-text-interface.md`
Raised: 2026-09-24, architecture, at the §8 review on `arch/sprint-01-batch-1-review`.

## 1. §8: the scripted M1 gate — owed by `AW-CLI-007` (no action for implementation here)

This story's Definition of done asks for the scripted M1 gate to run in CI. On `origin/main`
`033f2c6`, `stack_play.sh` runs `play` only up to the refusal, because `play` has no way to select a
body. That is `AW-CLI-007`'s work, and its Definition of done (PR #68) carries the gate and each live
`play` observation. Every other §8 item holds. This story stays at `review` until `AW-CLI-007`'s
§8, and then moves to `done` in the same pass.

## 2. For PM: nobody owns the `perceived_from` renderer row

This story's Open questions say `AW-SRV-029` "adds the field and the row to `renderEvent` and the
golden recording in the same pass". `AW-SRV-029` (`ready`) says the opposite. Its scope puts
rendering out and says "`AW-CLI-004` carries the rule as an inherited item". Once this story is
`done`, no open story renders `perceived_from`.

Recommendation: amend `AW-SRV-029`'s scope to include the `renderEvent` row and the golden
recording's new case. The field and its only renderer land together, and 029 is the first story
that can produce the field. It is a scope change to a story outside the active sprint, so it is
yours to make, not architecture's.
