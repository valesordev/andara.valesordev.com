---
id: AW-INF-013
title: Publish the server image to ghcr on merge to main
epic: EPIC-01
component: infra
type: infra
status: ready
size: S
depends_on: [AW-INF-003]
blocks: [AW-INF-007]
lane: architecture
risk: low
---

## Context

`values/dev.yaml` and `values/prod.yaml` pull `ghcr.io/valesordev/andara-server`, and nothing
publishes it. No workflow pushes an image, and an anonymous pull is denied. EPIC-01 names "image
publishing and environment promotion" as in scope. `AW-INF-007` lists image build and publish as out
of scope, "`AW-INF-001`'s CI", but `AW-INF-001` never did it. `deploy/compose/Dockerfile.server` says
so itself: "there is no registry and no publish story yet". The `AW-INF-008` review found the
consequence on 2026-09-24. `make helm-install ENV=dev` can't start a pod on the box, so that story's
five backend checks can't run. **Brian decided the same day to publish the image** rather than hand
those checks to `AW-INF-009`.

There is a second defect on the same path. `scripts/helm_install.sh` always passes
`--set image.repository=$IMAGE --set image.tag=$TAG`, where `IMAGE` defaults to the locally built
`andara-server`. `values/dev.yaml`'s registry never takes effect, and with `pullPolicy: Always` the
kubelet looks for `andara-server` on Docker Hub.

## User story

As an operator, I want every merge to `main` to publish a server image the box can pull, so that
`dev` runs what `main` holds and a deploy can name an exact build.

## Scope

### In scope
- `.github/workflows/publish.yaml`: on push to `main`, build `deploy/compose/Dockerfile.server` for
  `linux/amd64` (the box's nodes) and push it with two tags. `permissions: contents: read,
  packages: write`. Authenticate with `GITHUB_TOKEN`; no stored secret.
- `Dockerfile.server`:
  - the header comment stops saying no publish story exists;
  - the runtime stage gains OCI labels `org.opencontainers.image.source`
    (`https://github.com/valesordev/andara.valesordev.com`, which links the package to the repo),
    `.revision` (the full commit) and `.version` (`VERSION`).
- `scripts/helm_install.sh` and the `helm-install` target:
  - for `local`, the kind-loaded `IMAGE:TAG` overrides the values file, as today;
  - for any other environment, the values file names the image, and only a `TAG=` given on the
    command line overrides its tag.
- `make image-check ENV=<env> [TAG=]`: proves a tag is pullable anonymously and pullable from the
  cluster (AC-2, AC-3).
- `deploy/helm/andara/README.md`: the Environments paragraph says where `dev`'s image comes from and
  how to pin one.

### Out of scope
- **Running `dev` to Ready.** `dev` inherits `content.source`, `sim.source` and `auth.store` of
  `kafka`, and `kafka.brokers` is empty. No broker runs on the box, and none is planned outside
  `AW-INF-014` (draft). This story ends at a pulled image and a started container. `AW-INF-008`'s
  backend checks need both stories.
- Release tags (`v*` → `:<semver>`) and promotion to `prod`. `AW-INF-007`'s `make deploy TAG=` can
  name a `sha-` tag, which is immutable, and that is enough for its rollback. A release scheme is
  its own decision when `prod` has users.
- Multi-arch images, image signing and SBOMs. None of them is needed to pull on one amd64 box. Each
  is a line in this workflow when it is wanted.
- The agent image (`AW-SRV-016`'s `Dockerfile.agent`), which follows this story's pattern when that
  story lands.
- The CI `kind` job. It builds and `kind load`s its own image for `local`, and still does.

## Acceptance criteria

1. **Given** a merge to `main` **when** `publish` runs **then** `ghcr.io/valesordev/andara-server`
   carries `:sha-<12-hex>` for that commit and `:dev` points at the same digest. The image's
   `org.opencontainers.image.revision` label is that commit.
2. **Given** no credentials **when** `make image-check ENV=dev TAG=sha-<12-hex>` runs **then** an
   anonymous manifest fetch of the tag succeeds and its revision label matches the tag. (If GitHub
   created the package private, this fails until Brian sets it public once, in the package's
   settings. It is a one-time change to account settings, so it stays Brian's.)
3. **Given** the box **when** `make image-check ENV=dev` runs **then** a throwaway pod in
   `andara-dev` (created if absent), with image `:dev` and command `/bin/true`, pulls, runs, and
   exits `0`, then is deleted. That shows the cluster can pull the image, with no broker involved.
4. **Given** `make helm-install ENV=dev` **when** it renders **then** the StatefulSet's image is
   `ghcr.io/valesordev/andara-server:dev`. With `TAG=sha-<12-hex>` it is that tag. **Given**
   `ENV=local` **then** it is `andara-server:dev`, as today. `make helm-test` asserts all three
   through the same argument handling the script uses.
5. **Given** a pull request **when** CI runs **then** `publish` does not run and nothing is pushed:
   a PR from a fork cannot write packages, and one from a branch must not.
6. **Given** two merges in quick succession **when** both `publish` runs finish **then** `:dev` names
   the later commit. The workflow's concurrency group is `publish-main` with
   `cancel-in-progress: false`, so runs finish in order rather than race. GitHub keeps only one
   pending run per group, so a commit whose run is replaced while queued gets no `sha-` tag. That
   is accepted: `dev` still converges, and a deploy names a commit that has a tag.

## Interface contract

### Tags

| Tag | Mutable | Pushed | Used by |
|-----|---------|--------|---------|
| `sha-<first 12 hex of the commit>` | no | every push to `main` whose run is not replaced while queued (AC-6) | `make deploy TAG=` (`AW-INF-007`), rollback, `make helm-install ENV=dev TAG=` |
| `dev` | yes, moves to the latest `main` | every push to `main` | `values/dev.yaml` with `pullPolicy: Always` |

`VERSION` is built in as `git describe --tags --always` of the pushed commit, and `COMMIT` as the
short sha, exactly as `make image` does. `andara_build_info` therefore names the running build.

### Make targets

| Target | Does | Exit |
|--------|------|------|
| `make image-check ENV=<env> [TAG=dev]` | anonymous `docker manifest inspect` of `ghcr.io/valesordev/andara-server:$TAG`; compares the revision label against `sha-` tags; then `kubectl -n andara-$ENV run` a `/bin/true` pod with that image, waits for `Succeeded`, and deletes it | `0` pulled both ways · `1` a pull failed or the label disagrees · `3` no docker or no kubectl |
| `make helm-install ENV=<env> [TAG=]` | unchanged, except where the image comes from (Scope) | unchanged |

The workflow, sketched:

```yaml
# CONTRACT SKETCH — not an implementation
on: { push: { branches: [main] } }
concurrency: { group: publish-main, cancel-in-progress: false }
permissions: { contents: read, packages: write }
# build deploy/compose/Dockerfile.server --platform linux/amd64
#   --build-arg VERSION=$(git describe --tags --always) --build-arg COMMIT=<short sha>
# push :sha-<12 hex>, then :dev (in that order, so :dev never names an unpushed digest)
```

## Data / state impact

None. The registry holds images only.

## Observability requirements

- **Logs, metrics, traces:** none new. The running build is `andara_build_info`, as today.
- **Alerts:** none. A failed `publish` is a red workflow on `main`, and the next merge retries.

## Test plan

- **Unit (`make helm-test`):** AC-4, rendering the StatefulSet image for `local`, `dev`, and `dev`
  with a tag.
- **CI:** AC-1, AC-5, AC-6 by the workflow itself. The PR that lands this shows the first run on
  `main` in its verification record.
- **Operator (box, recorded):** AC-2 and AC-3, `make image-check ENV=dev` and again with the
  `sha-` tag from AC-1's run.

## Definition of done

CLAUDE.md §8, plus: `AW-INF-008`'s record names the first `sha-` tag the box pulled.
`AW-INF-007`'s Out of scope stops attributing publishing to `AW-INF-001`.

## Open questions

- **Resolved 2026-09-24 (Brian):** publish the image, instead of handing `AW-INF-008`'s box checks
  to `AW-INF-009`.
- **Package visibility.** The repo is public, so the package should be too, which means no pull
  secret on the box. If GitHub creates it private on the first push, Brian flips it; AC-2 says so.
