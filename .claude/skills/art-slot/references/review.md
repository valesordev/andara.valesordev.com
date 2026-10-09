# Candidate review

Review the **treated** candidates, composited over each theme's background token, at the sizes in the spec.
Open each file and look at it; never review from the file name or the prompt.

| Criterion | Question | Fails when |
|---|---|---|
| Brief | Does it show what the brief says, with every must-keep? | A must-keep is missing or contradicted |
| Direction | Is it on the approved illustration system (rendering, value, line, light)? | It drifts to a style the system or `negative-examples.md` excludes |
| Treatment | Does it survive treatment and both themes? | Tone turns to mush, edges fringe, or the dark theme loses the subject |
| Composition | Is the empty zone empty, and is every crop in the spec a complete picture? | Type would sit on detail, or a crop cuts the subject |
| Scale | Does it read at its smallest rendered size (600 px wide for heroes, 16 px for marks)? | It becomes a smear or blob |
| Tells | Any generator artefacts: pseudo-text, signatures, warped geometry, melted details, duplicated objects? | Any visible at 1× |
| Distinct | Does it stay clear of the sibling imprints' languages and of existing logos? | It reads as a sibling's, or resembles a known mark |

Table in the slot file:

```markdown
| | Brief | Direction | Treatment | Composition | Scale | Tells | Distinct | Notes |
|---|---|---|---|---|---|---|---|---|
| A | ✓ | ✓ | ~ | ✓ | ✓ | ✓ | ✓ | moon rim fringes after threshold; fixable with --gamma |
```

`✓` pass, `~` fixable in treatment or cleanup (say how), `✗` fail.

Recommend one, with the reason in one line. A `~` that cleanup fixes is fine. A `✗` on Brief, Tells, or
Distinct rules a candidate out. If none passes, write revised prompts for the closest candidate and say
what changed.
