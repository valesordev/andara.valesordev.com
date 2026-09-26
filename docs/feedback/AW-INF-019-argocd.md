# AW-INF-019 — Argo CD for dev: contract questions for review

Spec: `docs/stories/AW-INF-019-argo-cd-deploys-dev-from-main-on-the-box-s-kind-cluster.md` (`draft`)
Raised: 2026-09-26, PM, from Brian's request that `dev` follow `main` on the box. **Argo CD itself
is decided (Brian, 2026-09-26).** These items are how, and no ADR or spec covers them, so the
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
  PRs, pass through branch protection and signed-commit rules, and show up in `git log`. **It
  also loops:** `publish` runs on every push to `main` with no path filter, so the write-back
  commit builds a new `sha-` image, Image Updater writes that tag back, and that commit publishes
  again, without end. Choosing (b) needs `publish` to ignore the write-back file (a `paths-ignore`
  entry), plus a check that it never deploys an image built from its own commit.
- **(c) `publish` commits the tag.** `publish.yaml` writes `sha-<12-hex>` into the values the
  Application reads, after pushing the image. This has (b)'s git record and (b)'s bot commit on
  `main`, with no extra controller. It couples CI to the deploy config, and it has (b)'s loop:
  the tag commit is a push to `main` that `publish` builds and tags again. It needs the same
  `paths-ignore`, or the tag written somewhere that isn't `main`, such as a deploy branch the
  Application tracks.

PM recommends **(a)**. `dev` is a play environment that follows `main` by definition, so the
record of what it runs is `main` itself plus `make argocd-status`. Of the three, it's the only one
that puts no automated writer on `main`, and so the only one with no publish loop to guard
against. The story's contract is written to hold under any of them.

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

### 4. Does `publish` build merges that can't change the image? (`AW-INF-013`)

`publish.yaml` runs on every push to `main`. A merge that touches only `docs/` still builds and
tags a new `sha-` image, and it moves `:dev`, because the commit is stamped into the binary. Under
this story, `dev` then rolls. That means a World restart for a story file. `AW-INF-019` AC-5
accepts that as written. A `paths-ignore` (for `docs/**`, `*.md` and `BACKLOG.md`, say) would stop
it, but it's a change to `AW-INF-013`'s contract, which is at `review`, so it's architecture's
call. The cost: a skipped merge gets no `sha-` tag, so `make deploy TAG=` can't name it. That
matters little for a commit that changed nothing the image holds.

### 5. Recovery from a build that never becomes Ready

The chart's StatefulSet is `OrderedReady`. When a published build never becomes Ready, the rolling
update stalls on that pod, and a later good template doesn't replace it. Kubernetes documents this
as *Forced rollback*: the pod already attempted on the bad revision has to be deleted. The story now
makes that deletion a make target, `make argocd-recover ENV=dev` (AC-8), so it's one operator
command rather than a hand-typed `kubectl`. If architecture wants it automated instead (an Argo CD
`SyncFail` or `PostSync` hook, or a small controller), the contract should say which, and AC-8
becomes "no hand step" again.

## Architecture's answers (2026-09-26, contract review)

Recorded in the story body under "Contract review". The story is `ready`.

1. **(a), tracking `:dev` by digest.** Image Updater with `argocd` write-back, but with the `digest`
   strategy on `:dev` rather than `newest-build` over `sha-` tags. `newest-build` sorts by image
   creation time, and `publish` doesn't build in commit order. `:dev` is the one tag
   `image_publish.sh` keeps in commit order.
2. **`AW-INF-007` governs `prod` and `local`.** The line is scoped in its body. `dev` needs no
   core-pack step while it reads content from ConfigMaps. See the answer in
   `AW-INF-007-deploy-lifecycle.md`.
3. **The chart renders both ConfigMaps**, from relative symlinks under
   `deploy/helm/andara/files/`, with a `checksum/content` roll. `helm_install.sh` stops creating
   them. The fallback, if Argo CD refuses the links, is a generated copy that `make check` keeps
   identical.
4. **No `paths-ignore`.** A skipped docs merge right after a code merge leaves `:dev` a build behind.
   The code merge's run sees it isn't `main`'s head and defers to a run that never happens. AC-5
   stands.
5. **Manual.** `make argocd-recover` stays an operator command.

### Found while checking item 1: a defect in `AW-INF-013`

The chart writes `image.tag` into the `app.kubernetes.io/version` label verbatim. `helm_install.sh`
pins `:dev` to `dev@sha256:<digest>`, which the API server rejects on every object carrying the
label:
- `@` and `:` aren't valid label characters;
- the value is over 63 bytes.

Checked with a server-side dry run on the box's cluster. So `make helm-install ENV=dev` fails at
apply as `AW-INF-013` left it. Filed as #106. It blocks the box session's `dev` install, so
architecture fixes it before that session runs.
