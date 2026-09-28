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

## For SRE (added 2026-09-28)

The SRE role arrived after these questions were written (#139). AW-INF-020, AW-INF-021, AW-INF-022
and AW-INF-024 are now SRE's to build (`AW-INF-027`), so SRE should weigh in before architecture
decides the items it will operate:

- **Item 1:** which carrier SRE can run and roll back on `dev`, and later on `prod`: a `PreSync`
  hook Job, the server's own boot path, or a `dir` bootstrap. Include what each one does to a
  first install and to a failed roll.
- **Item 2:** if `dev` needs fresh topics, what `make` target does the cut-over, and what its
  rollback to `dir` is.
- **Item 4:** whether GitHub's runners can reach `dev`'s edge at all. If they can't, option (a) is
  out.

Architecture still decides all three. Write SRE's answers under this heading.

### SRE's answers (2026-09-28)

**A fact that shapes items 1 and 4: `dev`'s edge isn't on the public internet.**
`andara-dev.solo7.valesordev.com` has no public record. Cloudflare's resolver returns NXDOMAIN.
It resolves only inside Brian's tailnet, to `100.79.240.98` (`solo7desk`), which is the box's
Tailscale address. So anything that reaches `dev` has to be on the tailnet:
- GitHub-hosted runners aren't on it;
- a Builder who isn't Brian isn't on it either.

**Item 1: the core carrier. SRE recommends (b), with conditions.**

| | First install | Every roll | A failed roll | Image rollback across a core bump | `prod` later |
|---|---|---|---|---|---|
| (a) `PreSync` Job | Doesn't work: no pod to publish through | The *old* server validates the *new* core. A core using a Component type the old binary lacks fails validation, and the roll is blocked | A failed sync. Argo CD shows it, and the old pod keeps serving | The rollback's `PreSync` activates the older core. `AW-SRV-013`'s activation refusal (strands a pack) then blocks the rollback | A second mechanism, since `prod` isn't Argo-rolled today (#107) |
| (b) Server at boot | Works | The new binary publishes its own core, so no version skew | A boot error, and the pod never goes Ready: `AndaraServerUnavailable` on `dev`, with a runbook line (`AW-INF-021`'s Observability) | The rule "never move core backwards" leaves the newer core active, and the older binary reports it on `andara_content_pending_seconds{pack="andara.core"}` (`format_version`). A core rollback is always an explicit `content rollback andara.core` | The same mechanism, with no new image and no credential in a Job |
| (c) `dir` bootstrap | Works, as a one-off state transition | Needs (a) after it | As (a) | As (a) | A hand sequence per environment. That's a §9 defect by construction |

(a) also needs an image carrying `andara-cli`, which `Dockerfile.server` doesn't build. SRE can
operate (b) on `dev` and `prod` with the fewest moving parts. SRE's conditions for operating it:
- The publish is idempotent on the core's content digest. Two pods booting together, or a restart,
  publish nothing new.
- The server never moves core's pointer backwards on its own.
- It's audited as the deploy's operator, with the image tag as `reason`, which satisfies
  `AW-SRV-013` AC-11's "names the deploy tag".
- Readiness waits for the swap.
- The boot line names the core version, and whether it published or found it active.

The contract cost is that the server counts as the operator for core, which is architecture's to
accept or refuse.

**Item 2: moving `dev`'s log. Fresh topics, as a target.**

Fresh topics are the only cut-over SRE can make reversible on `dev`. No player World exists before
M2. The target:
```
make world-reset ENV=dev CONFIRM=andara-dev
```
- It refuses `ENV=prod`, and any `CONFIRM` that isn't the namespace. Exit 2.
- It scales the server StatefulSet to 0 and the projector Deployment to 0.
- It deletes and recreates the World's log topics, by an explicit list from
  `deploy/kafka/topics.yaml`. It leaves the content topics and the Account store alone.
- **It empties the snapshot PVC too.** Snapshots are keyed to log offsets, so a snapshot of the old
  log applied to a recreated topic is a wrong World, not a slow one. This is the step a hand
  procedure would miss.
- It scales back up, and waits for Ready.
- It prints `world-reset: andara-dev reset; accounts kept, characters gone`.

What survives is architecture's call. SRE's view: keep the Account store, so the bootstrap operator
and any Builder grants survive. Characters are in the log and go. Whether the roster references
survive a log reset is the question for architecture.

**Rollback to `dir`:** revert the values, then run `world-reset` again. A store-backed log replayed
with `content.source=dir` fails the genesis digest check, the same way the forward direction does.
So a rollback costs `dev`'s World a second time. That's acceptable only on `dev`, and only before
M2. `AW-INF-021`'s Data/state section should say so.

**Item 4: where the Content Repository's CI gets core. (a) is out. SRE supports (b).**

GitHub-hosted runners can't reach `dev` (above). (a) would need CI to join the tailnet with an
ephemeral key: a secret in a Builder-facing repository, plus a tailnet ACL for CI. That's more
surface than the problem warrants. (b) needs no network and no credential, and the skew window is
the one PM names. For SRE, (b) adds:
- to `AW-INF-020`: the archive carries `andara.core`'s compiled pack, and `andara-cli version`
  prints the core version it bundles;
- to `AW-INF-022` AC-6: "`dev` unreachable" stops being a failure mode, and the AC is replaced by a
  check that the bundled core equals `dev`'s active core, run where `dev` is reachable (Brian's
  machine, or a later self-hosted runner). It isn't run in CI.

## For architecture: SRE observability review, 2026-09-28

The CLAUDE.md §7 review of the four stories this file covers. Only Observability sections changed.

- **`AW-INF-021`: amended.**
  - `pack` cardinality stated: one per active pack, bounded by the Operator's grants.
  - AC-6's core-skew evidence named: `load_failures_total{reason="core_version"}` and
    `pending_seconds`.
  - `ContentLoadFailing` goes live on `dev` with this story. The story now carries the SLO's
    *Known gaps* edit and two runbook lines, including `server-unavailable.md`'s step for "core not
    active" if the carrier gates readiness.
  - The carrier's required log line and failure visibility are added.
- **`AW-INF-022`: no change.** A CI workflow in another repository, with no runtime signals. If
  item 4 is (b), AC-6 changes as above. That's a contract change, not an observability one.
- **`AW-INF-023`: no change.** Documentation and check targets.
  - **But the tailnet fact above affects its AC-3.** "A person with no clone of this repository
    ... follows sections 2–4 against `dev`" works for Brian only. Any other reader also needs
    tailnet access, and section 2, *Getting access*, has to say so. That's for architecture and PM,
    not SRE.
- **`AW-SRV-035`: amended.**
  - `andara_privileged_actions_total{action="set_builder_packs"}` added, pre-seeded through
    `auth.AllActions`.
  - The trace corrected. "The Account store write as a child, as `SetRoles` has" wasn't true:
    `SetRoles` has no store-write span. The story now names a new `accounts.write` child span.

## Brian's answers (2026-09-26)

1. **Reaching a new Zone: a `goto` command for Builders**, now `AW-SRV-036`. Until the base content
   exists, every new Character spawns in **Purgatory**, a Zone with an Exit into the test town. It's
   the waiting place before a Character moves to its start location. Purgatory is added to the test
   content in `AW-SRV-037`, and becomes every environment's spawn Room in `AW-INF-024`. The fixture
   pack this story seeds includes it.
2. **Approving your own work: an Operator may approve a build they published as a Builder.** It's
   temporary, until others build. That changes `AW-SRV-013` and `AW-CLI-003`, which are `ready`, so
   it's written up for architecture in `docs/feedback/AW-SRV-013-operator-self-approval.md`.
