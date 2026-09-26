# AW-INF-007 — "the only deploy path" and Argo CD on dev

Spec: `docs/stories/AW-INF-007-deploy-lifecycle-snapshot-and-recovery.md` (`ready`)
Raised: 2026-09-26, PM, grooming `AW-INF-019`.

The story is `ready`, so PM doesn't amend it (lane rule).

## For architecture

`AW-INF-007`'s scope names `make deploy ENV=<env> TAG=<tag>` and `make rollback` "as the only deploy
path". Brian decided on 2026-09-26 that Argo CD keeps `dev` in step with `main` on the box
(`AW-INF-019`, `draft`). As written, the two contradict each other for `dev`.

**Recommendation:** scope the line to `prod`, "the only deploy path for `prod`", and record in the
body that `dev` is deployed by Argo CD from `main` (`AW-INF-019`). Then state which of the
lifecycle's parts `dev` still gets:

- **The pre-stop snapshot and post-start recovery** live in the pod, so `dev` gets them whoever
  triggers the roll. No change is needed.
- **The `andara.core` publish-and-activate step** runs from `make deploy`, outside the pod, so a
  roll that Argo CD triggers skips it. It needs a carrier for `dev`, such as a PreSync hook Job or a
  step `AW-INF-019` adds, or a statement that `dev` activates the core pack some other way.
- **`andara_deploy_interruption_seconds`** is emitted from `ServerStopping` to first `serving`, so it
  would be measured on `dev` rolls too. Say whether they count toward the SLO comparison or are
  labeled `env=dev` and excluded.
