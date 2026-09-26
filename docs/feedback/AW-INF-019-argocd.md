# AW-INF-019 — Argo CD for dev: contract questions for review

Spec: `docs/stories/AW-INF-019-argo-cd-deploys-dev-from-main-on-the-box-s-kind-cluster.md` (`draft`)
Raised: 2026-09-26, PM, from Brian's request that `dev` follow `main` on the box. **Argo CD itself
is decided (Brian, 2026-09-26).** These three items are how, and no ADR or spec covers them, so the
story stays `draft` and out of SPRINT-02 until they're answered here and in the story body.

## For architecture

### 1. How a new build reaches the Application

Most merges change `server/` and nothing the chart renders. The Application's git source is the same
before and after, and only ghcr's `:dev` moves. `AW-INF-013` made `helm-install` pin a moving tag to a
digest, for the same reason: a pod spec that doesn't change doesn't roll. The options PM sees:

- **(a) Argo CD Image Updater, `argocd` write-back.** It watches `ghcr.io/valesordev/andara-server`
  for new `sha-` tags (`newest-build`), and sets `image.tag` as an Application parameter. Nothing is
  committed, and CI needs no rights on the box or the repo. The cost: the running tag lives in the
  Application, not in git, and it's one more controller to pin and run.
- **(b) Image Updater, `git` write-back.** The same, but it commits the tag to a file on `main`
  (for example, `deploy/argocd/.argocd-source-andara-dev.yaml`). Git records what `dev` runs, and a
  revert rolls it back. The cost: a bot with push rights to `main`, whose commits land between lane
  PRs, pass through branch protection and signed-commit rules, and show up in `git log`.
- **(c) `publish` commits the tag.** `publish.yaml` writes `sha-<12-hex>` into the values the
  Application reads, after pushing the image. This has (b)'s git record and (b)'s bot commit on
  `main`, with no extra controller. It couples CI to the deploy config.

PM recommends **(a)**. `dev` is a play environment that follows `main` by definition, so the
record of what it runs is `main` itself plus `make argocd-status`. Of the three, it's the only one
that puts no automated writer on `main`. The story's contract is written to hold under any of them.

### 2. `AW-INF-007`'s "only deploy path"

`AW-INF-007` (`ready`) makes `make deploy` / `make rollback` "the only deploy path", and its deploy
activates the `andara.core` pack built with the image before the new pod reports ready. Under this
story, `dev` is deployed by Argo CD. The details are in `docs/feedback/AW-INF-007-deploy-lifecycle.md`.
This story assumes the answer is that `AW-INF-007` governs `prod`, and that `dev` gets the core-pack
step some other way once `AW-INF-007` lands (a PreSync hook Job, say). If architecture decides
instead that `dev` must go through `make deploy`, this story needs rescoping, and Brian should hear
that before it's built.

### 3. The content ConfigMaps under Argo CD

`helm_install.sh` builds `andara-content` and `andara-content-templates` with
`kubectl create configmap --from-file` from `testdata/content/valid` and `content/core/templates`,
which sit outside the chart. A Helm chart's `.Files` can't read them, and a kustomize
`configMapGenerator` needs `LoadRestrictionsNone` to reach them from `deploy/`. The options include a
multi-source Application, a kustomize overlay with that build option in `argocd-cm`, or a generated
copy under the chart that `make check` keeps in step. Pick one and state it in the story's
contract. `AW-SRV-012` serving content from the store would retire this for `dev`. It's recorded
here in case that lands first.
