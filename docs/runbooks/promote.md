# Promoting and rolling back `dev`

Procedure, not an alert (`AW-INF-059`, `ADR-0013` decision 7). `dev` runs what the Git tag `dev`
points at. A merge to `main` publishes `ghcr.io/valesordev/andara-server:sha-<12 hex>` and runs
nothing; `make promote` is the only way `dev` changes.

## Read first

```
make promote-log              # the newest promotions: time, main SHA, image digest, who, rollback markers
make argocd-status ENV=dev    # the three answers: tag dev's commit, Argo's synced revision, the running digest
```

(`make promote-log` is `git tag -n20 'dev-*'` over origin's tags, newest first.)

`dev-<n>`'s annotated message is the event record: when an SLI moved, find the promotion whose
`time:` precedes it. No annotation is written to Grafana.

## Promote a build

From your own shell, never a Claude Code session (it refuses when `CLAUDECODE` is set; that is a
convention, not a control: the role push hook can't see a push inside a `make` recipe):

```
make promote ENV=dev SHA=<main sha>
```

It checks that `sha-<12>` pulls anonymously, resets `release/dev` to `main@SHA` plus one signed
commit that changes only `image.tag` in `deploy/helm/values/dev.yaml` (to `sha-<12>@sha256:<digest>`),
moves the signed tag `dev` there, adds `dev-<n>`, pushes all three atomically, asks Argo CD for a
hard refresh, and waits up to 10 minutes for `argocd-status`'s three answers to agree. Every line is
`promote: <step> … run=<id>`; the last carries the commit, the tag, the digest and the refresh time.
The `promote: post-sync skipped (AW-INF-041)` line is expected until `AW-INF-041` fills the hook
with `make deploy`'s pre- and post-steps (the `andara.core` step, the measured interruption).

Exit: `0` promoted and agreeing · `1` image, push, sync or assertion failure · `2` usage · `3` no
signing key (`make bootstrap`) or no Git credential.

## Roll back

```
make promote ENV=dev TAG=dev-<n>                    # back to that promotion's image
make promote ENV=dev TAG=dev-<n> PIN_ROUND=<round>  # and pin recovery to a snapshot round
```

A new commit goes on top of `dev-<n>`'s commit (so the tag history stays linear), setting
`release.rolled_back_to: dev-<n>` and `server.recovery.pin_round` (a rollback without `PIN_ROUND`
sets it to `0`, clearing an earlier pin). `dev` moves to it and a new `dev-<n'>` is created. The next
`make promote SHA=…` starts from `main` again, so both markers clear.

## It didn't converge

| `promote:` says | Meaning | Do |
|---|---|---|
| `does not pull anonymously` | `publish` hasn't run for that commit, failed, or the package is private | `make image-check REGISTRY_ONLY=1 TAG=sha-<12>`; re-run `publish`, or `make image-publish` on that commit |
| `is not on origin/main` | the SHA isn't a `main` commit | promote a merged commit |
| `push refused, nothing promoted` | a ruleset or credential refused the push; the local `dev`, `dev-<n>` and `release/dev` are restored | read the refusal; `release/dev` and `dev*` tags are outside `main`'s protection, so a ruleset refusal is a regression in the repository settings |
| `pushed, but there is no application andara-dev` | the first promotion on a rebuilt cluster | `make argocd-install ENV=dev`, then `make argocd-status ENV=dev` |
| `… answers disagree after 600s` | Argo didn't sync it, or the pod isn't running the pinned digest | `make argocd-status ENV=dev` names which answer differs and Argo's condition. A pod stuck on a build that never became Ready is `make argocd-recover ENV=dev` once a good build has synced (`server-crashlooping.md`) |
| `refreshed … (over 60s, AC-6)` | Argo noticed the moved tag late | not a failure; note it on `AW-INF-059`'s §8 record |

A bad image that does sync shows as `AndaraServerUnavailable` or `AndaraServerCrashLooping` on
`environment="dev"`; roll back as above.

## A rebuilt cluster

`make argocd-install ENV=dev` refuses until the tag `dev` exists. On a cluster rebuilt from nothing:
`make promote ENV=dev SHA=<main sha>` first (it pushes, then stops at "no application andara-dev"),
then `make argocd-install ENV=dev`, then `make argocd-status ENV=dev`.
