# Builder content on dev: contract questions before review

Stories:
- `AW-INF-021`: `dev` serves its content from the content store
- `AW-INF-022`: the content repository
- `AW-INF-023`: the Builder's Guide
- `AW-SRV-035`: an Operator grants a Builder their packs

All four are at `draft`.

Raised 2026-09-26 by PM, from Brian's request to start building content for `dev` once SPRINT-02 has
it running. They're groomed for SPRINT-03's contract review.

Brian's decisions, 2026-09-26:
- **A separate, private Content Repository** holds Builders' `.aw` source. The store stays
  authoritative for what's live (ADR-0004).
- **Architecture writes the Builder's Guide** from `AW-INF-023`, as the commands land.
- **The dev fixture stays**, published as pack `town`, so `dev` is playable before any Builder
  content exists.

## For architecture

### 1. How `andara.core` becomes active in `dev`'s store on every roll

`AW-INF-021` AC-6 needs the build's `andara.core` active before a pod from that build reports
ready. `AW-SRV-013` makes core an operator-only publish with no approver, and calls it a deploy step
(`AW-INF-007`). #107 scopes `AW-INF-007`'s `make deploy` to `prod` and `local`, and `dev` is rolled
by Argo CD. So `dev` needs its own carrier.

There's also a bootstrap problem. Publishing goes through the server's `Admin` RPCs, and a server
with `content.source=kafka` and nothing to load exits 1 (`AW-SRV-012`: "a boot with no loadable
version exits `1`"). On a fresh namespace, nothing can publish the first core.

Options:
- **(a) An Argo CD `PreSync` hook Job** that runs `andara-cli content publish` and `activate` for
  core, against the running pod, as the operator. The server image doesn't ship `andara-cli`
  (`Dockerfile.server` copies only `andara-server` and `andara-projector`), so the Job needs an
  image with it. It doesn't cover a first install, which has no running pod.
- **(b) The server publishes its own embedded core at boot**, when the store's active core is
  absent or older. That covers the first install. But it puts a store write in the server's boot
  path, and it contradicts `AW-SRV-013`'s "operator only" unless the server counts as the operator.
- **(c) A `dir`-mode bootstrap.** The first install boots with `content.source=dir`, seeds, then
  flips to `kafka`. Every later roll uses (a). This is two modes to get right.

PM has no recommendation between (a) and (b). (b) is the smaller operational surface and the bigger
contract change. Whichever is chosen should serve `prod` later, and `AW-INF-007`'s step should be
the same mechanism.

### 2. Moving `dev`'s existing log from `dir` to the store

`dev`'s log has genesis swaps with `pack="dir"`, version 0. `AW-SRV-012` rebuilds content only from
replayed swaps, so a store-backed server replaying that log would need the `dir` content. Questions:
- Does the switch need fresh topics for `dev`? `AW-SRV-012`'s recovery for a pre-rule log is
  "fresh topics for `dev`".
- If it does, what survives? Accounts are in the Account store on the same broker, and Characters
  are in the log. Losing Brian's `dev` Characters is acceptable, since no player World exists
  before M2. Losing the bootstrap operator and any Builder Accounts means re-creating them, which
  the guide would then have to say.
- What's the rollback path back to `dir`?

### 3. Where the fixture's source lives

`AW-INF-021` assumes the seed publishes the spec corpus's `valid/town/`, pack `town`, whose
`expected/` is the dev fixture. Since Brian's Purgatory decision (below), the fixture also needs a
`purgatory` Zone that the corpus case doesn't have. The spec corpus is architecture's, and a conformance case changing
would then change `dev`'s content. Is that acceptable, or should the seed have its own copy that
`make check` holds equal to `testdata/content/valid`?

### 4. Where the Content Repository's CI gets `andara.core`

`AW-INF-022`'s `check` runs `content validate`, which needs a cached core (`AW-CLI-002` AC-5).
Options:
- **(a) `content fetch-core` from `dev`** with a CI credential stored as a secret in the Content
  Repository. It needs `dev`'s edge reachable from GitHub's runners, and an identity allowed to read
  core. `AW-SRV-013`'s matrix gives `GetVersion` only to a Builder holding the pack, or to the
  operator.
- **(b) The `andara-cli` release bundles the core pack it was built with** (`AW-INF-020`), so CI and
  Builders validate offline against the core of the commit `dev` runs. This adds scope to
  `AW-INF-020`, and skew is possible in the minutes between a merge and `dev`'s roll.
- **(c) Commit the core cache into the Content Repository.** It goes stale silently, so PM doesn't
  recommend it.

PM leans to (b): no network or credential in a Builder's CI. The skew window is the same one a
Builder already has between downloading `cli-dev` and `dev` rolling.

### 5. Where the Builder's Guide lives, and who owns the path

`AW-INF-023` assumes `docs/builders/` in this repository:
- it's public, so a Builder can read it without access;
- it's next to the specs it links to;
- `make check` can hold it to the commands.

No lane owns `docs/builders/` in CLAUDE.md §2, and PM doesn't edit the charter's table. The
alternative is the Content Repository, but then this repository's `make check` can't keep the
guide in step with `andara-cli`. Please confirm the location and add the path to §2's
architecture row.

## Brian's answers (2026-09-26)

1. **Reaching a new Zone: a `goto` command for Builders**, now `AW-SRV-036`. Until the base content
   exists, every new Character spawns in **Purgatory**, a Zone with an Exit into the test town. It's
   the waiting place before a Character moves to its start location. Purgatory is added to the test
   content in `AW-SRV-037`, and becomes every environment's spawn Room in `AW-INF-024`. The fixture
   pack this story seeds includes it.
2. **Approving your own work: an Operator may approve a build they published as a Builder.** It's
   temporary, until others build. That changes `AW-SRV-013` and `AW-CLI-003`, which are `ready`, so
   it's written up for architecture in `docs/feedback/AW-SRV-013-operator-self-approval.md`.
