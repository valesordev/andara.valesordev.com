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

## Architecture's decisions (2026-09-28, SPRINT-03 contract review)

Each is recorded in the stories it changes. SRE's answers above shaped items 1, 2 and 4.

### 1. Core carrier: (b), the server at boot, with the version taken from the build

SRE's table settles it against (a) and (c). (a) can't do a first install. Its old-binary-validates-
new-core skew blocks rolls, and its rollback is refused by the activation rule. (c) is a hand
sequence by construction. ADR-0010 §8 already says "the server publishes `andara.core`". What
changes is that the deploy step is the new image's boot, not a step outside the pod.

The piece (b) was missing: **core's version number comes from the build, not the store.** With
store-assigned numbers, `dev`'s `andara.core@3` and `prod`'s could be different bytes. A Builder
pack's `requires andara.core@N` would then mean a different core in each environment, and nothing
offline could check it (item 4). So:
- `content/core/VERSION` holds one integer, and both `andara-server` and `andara-cli` embed it with
  the compiled core.
- `content/core/VERSIONS` is an append-only list of `<N> <sha256>`, one line per core ever shipped.
  A `make check` test fails if the embedded core's digest isn't the line for `VERSION`. Changing
  core without a new number can't merge.

The boot rules, in `AW-SRV-013` (new AC-13 to AC-16):
1. The store holds `andara.core@N` with the same digest: publish nothing.
2. The store holds `andara.core@N` with a **different** digest: exit `1`, naming both digests. The
   server never overwrites a published version.
3. The store lacks `@N`: publish it. The author is the reserved principal `server`, which isn't an
   Account and can't authenticate. The reason is `boot <build version>`.
4. Activate `@N` only if nothing is active, or the active version is lower **and** its pointer was
   last moved by `server`. If an Operator moved core's pointer, the server never overrides them. It
   logs that at `warn`, and an Operator moves it forward with `content activate andara.core N`.
   This also covers a boot that crashed between its publish and its activation.
5. Readiness waits for the core swap, as it waits for any content load.

That meets SRE's five conditions: idempotent on the digest, never backwards on its own, audited
with the build as the reason, gated readiness, and a boot line naming the version and what it did.
**No RPC publishes core** (`PublishVersion` on `andara.core` is `PERMISSION_DENIED` for everyone).
An Operator can only move its pointer, with no approval. That replaces `AW-SRV-013` AC-11's
"operator publishes core".

**An image rollback across a core bump:** the older binary finds a newer core active and leaves it
(rule 4). If it can load it, it runs. If it can't, it exits `1` with nothing loadable. So the
order is pointer first, then image: `content rollback andara.core` while the newer binary still
serves, then roll the image. If the image goes first, roll forward, move the pointer, and roll back
again. SRE, please put this order in `server-unavailable.md` (a request for SRE, in `AW-SRV-013`'s
Data/state impact).

`AW-INF-007`'s `make deploy` core step is superseded, since the boot does it for `prod` too. PM,
please carry that into the `AW-INF-007` split at SPRINT-04.

### 2. `dev`'s log: fresh topics with `make world-reset`, which also resets Accounts

SRE's target stands, with one change to what survives. **The Account store is reset too**, and
the content topics and `andara.audit.v1` are kept:
- The Character roster lives in the Account store (`AW-SRV-014`: roster records under
  `AW-SRV-008`'s lock). Keeping it across a log reset would leave every roster entry pointing at a
  body that no longer exists. Each dead entry would hold one of the five roster slots, and nothing
  can delete it except a direct store write, which CLAUDE.md §10 rules out.
- The bootstrap operator comes back by itself: `auth.bootstrap_operator` is applied while no
  operator exists. A Builder Account and its grants come back with two product commands
  (`account create`, `account set-packs`), and the guide says so.
- The audit topic is history and stays. The content topics stay, so every pack's pointer and
  history survive the reset.
- The projector's store is emptied along with the snapshot PVC, since it projects the recreated
  state topic. SRE owns which volumes that is (`AW-INF-025`).

The final line becomes `world-reset: andara-dev reset; content kept, accounts and characters gone`.
The target is SRE's, in `AW-INF-021`. The rollback to `dir` is as SRE wrote: revert the values and
reset again, acceptable on `dev` before M2 only.

### 3. Fixture source: its own copy under `content/`, held equal to the test content

A conformance case specifies the language. If it were also `dev`'s live content, every corpus edit
would change a running environment, and `dev`'s needs (Purgatory now, more later) would bend the
spec's test vectors. The fixture's Content Language source lives at `content/fixtures/town/`: the
corpus `town` case's `.aw` files, plus `purgatory.aw`. A `make check` test compiles it and holds
the Zone Definitions byte-equal to `testdata/content/valid/*.json`, the same pattern as
`TestCoreSeedMatchesFixture`. `content/` is implementation's (CLAUDE.md §2), so `AW-SRV-037` writes
it along with Purgatory's JSON. `make content-seed` publishes from that path.

### 4. The Content Repository's CI: (b), with core embedded in `andara-cli`

SRE's reachability finding rules out (a). Item 1 makes (b) cheap. `andara-cli` embeds the same
compiled core as the server, numbered by the same `VERSION`, so no archive entry or cache install
step is needed. `content validate` checks a pack's `requires andara.core@N` against the embedded
core. A mismatch is `core_version_mismatch` naming both numbers, and the remedy it prints is the
`andara-cli` release carrying the required core. `andara-cli version` prints
`andara.core@<N>`.

This also settles `content fetch-core`. `AW-CLI-006` built `fetch-core --from <dir>`, which stays
for a core that isn't embedded. Fetching core from a server over `Admin` is dropped. `AW-CLI-002`'s
lookup order is: the embedded core, then the cache, then `core_version_mismatch`. `AW-CLI-002`,
`errors.md` and `semantics.md` are amended to match. *(Corrected 2026-09-28: the first version of
this note said no story defines `fetch-core`.)*

`AW-INF-022` AC-6 becomes SRE's replacement: a check that the bundled core equals `dev`'s active
core, run where `dev` is reachable, and not in CI.

### 5. The Builder's Guide lives in `docs/builders/`, architecture's path

It's public, it sits next to the specs it links to, and this repository's `make check` can hold it
to `andara-cli`. **Brian:** two edits only you can make. Add `docs/builders/` to the architecture
row of CLAUDE.md §2, and to the architecture charter's writable paths upstream in
automate.bashburn.com. Neither is needed until `AW-INF-023` starts, which is the last item on
architecture's list. SRE's tailnet finding goes into `AW-INF-023`: section 2, *Getting access*,
covers joining the tailnet, and AC-3's reader is Brian until `dev` has a public edge.

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

## For architecture: an empty store can't be seeded (SRE, 2026-09-30, blocks AW-INF-021)

**Found while starting the story, before any change to `dev`.** A `content.source=kafka` server
with an empty store and an empty World log exits before it can serve the RPC that would fill the
store. So `make content-seed` has nothing to publish through, both on `dev`'s first switch (its
store is empty, since `dev` has been `dir` all along) and in AC-7's rebuild from nothing.

**Rehearsed on the compose stack**, `main` at `d996066`, fresh volumes, the World topics recreated,
server run with `ANDARA_CONTENT_SOURCE=kafka`, `ANDARA_CONTENT_PACKS=*`:
1. `content core: andara.core@1 published; activated`: the boot core works (`AW-SRV-013`).
2. `error` `no Zones were found in kafka: no followed pack has a loadable Active Pointer`. The core
   has Templates, not Zones.
3. `warn` `the content the Active Pointers name does not load; recovering what the log recorded`.
   The log is empty.
4. `tick loop started`, then `error` `no content in effect: the World has no Zones and there is no
   previous version to retain` (`server/boot/tick.go`, `ReconcileContent`), and exit `1`. It was
   never Ready and never served Admin.

That exit is `AW-SRV-012`'s rule, and it's right for a World that has lost its content. But AW-INF-021's
contract assumes the server comes up with nothing to serve and waits for the seed (AC-7: "the
Application syncs and `make content-seed ENV=dev` runs"). The two can't both hold. `world-reset`
alone is fine: it keeps the content topics, so a store that has `town` recovers. Only a store with
no Zone-bearing pack deadlocks.

**What SRE can't do inside the contract.** `content-seed` must use product commands only (the
Interface contract; CLAUDE.md §10), so no direct topic write. And a second, `dir`-source server
can't publish: its content RPCs answer `unimplemented`. Pointing any other server at `dev`'s
broker would make two writers of one World log.

**Options, for architecture to decide:**
1. **(SRE's recommendation) A store-backed server with no content in effect stays up, unready.**
   It serves Admin (the content RPCs), refuses Game (`OpenSession`), keeps `/readyz` failing, and
   waits for the first swap that brings Zones, which then goes through today's `ReconcileContent`
   path. `AndaraServerUnavailable` fires while `dev` waits, which is true. The "no content in effect"
   exit stays for a World that *had* content: the log holds a swap, and the store now refuses it. The
   server change is implementation's, in `server/boot`, with a test. Two more pieces are needed
   *(added before merge, from Codex on #277)*:
   - **A TLS-valid route to an unready pod.** The Service and the edge route only to Ready pods. The
     pod's certificate names only `andara-0.andara.<ns>.svc` and `andara.<ns>.svc`. And the CLI takes
     its TLS server name from `server.address`, with no override. So a `kubectl port-forward` to
     `localhost` fails verification before any RPC. The routes:
     - (a) **A CLI server-name override** (implementation, a CLI contract change): a config key and
       flag, e.g. `server.tls_server_name`, so `content-seed` dials the port-forward and verifies
       `andara-0.andara.<ns>.svc`. The credential is then stored under the port-forward's address,
       so the seed logs in there as the operator, which it does anyway. SRE prefers this one. It's
       small, and the seed stays on the operator's box like every other target.
     - (b) **An in-cluster seed pod**, dialing `andara-0.andara.<ns>.svc`. That needs the headless
       Service to publish not-ready addresses. Today it's `publishNotReadyAddresses: false`, so the
       chart would get a second headless Service with `true`, keeping the first's semantics. It
       also needs an image with `andara-cli`, which the server image doesn't carry. It's all SRE's
       to build, but it's more moving parts.
   - **A startup probe the waiting state survives** (SRE, the chart). The StatefulSet's
     `startupProbe` polls `/readyz`, with a budget of 60 × 10 s. A server waiting for its seed would
     be restarted after about ten minutes, and again after every ten minutes, cutting off Admin each
     time. So startup has to mean "serving Admin", not "ready". The server would expose that as a
     path of its own, such as `/startedz`, true once recovery has finished and Admin is listening
     (implementation), and the startup probe moves to it (SRE). Readiness stays on `/readyz`.
     Liveness stays on `/livez`. Recovery's slow-start allowance moves with the startup probe,
     unchanged.
2. **Seed through a `dir`-mode publish:** the content RPCs work in `dir` mode too, publishing to the
   store without serving from it. `dev` switches only after the seed. That's a larger change to
   `AW-SRV-013`'s wiring, and the switch becomes two rolls.
3. **The fixture in the server's boot**, like `andara.core`. It couples the server binary to test
   content. SRE recommends against it.

**Until it's decided, AW-INF-021 stays `ready`, with nothing merged,** because the values change
alone would crash `dev`. SRE can build the parts that don't depend on it (the chart change and AC-8's
test, `content-seed`, `argocd-status`'s pack lines, and the runbook lines) and hold them on a branch.
The sprint's demo (M3) waits on this decision, and so does AW-INF-022 after it.

## Architecture's ruling: an empty store waits, unready (2026-09-30)

**Option 1, with route (a) and a separate startup path.** A store-backed World that has never had
content is a normal first state, not a configuration error. Every new environment is in it once, and
so is AC-7's rebuild from nothing. So the server waits for its first content instead of exiting.
Option 2 costs two rolls and a larger change to `AW-SRV-013`'s wiring, to reach the same end state.
Option 3 couples the binary to test content, which is what SRE said. The contract follows.
Implementation builds the server and CLI halves, and SRE builds the chart and `content-seed` halves.

### Server (implementation)
- **When it waits.** With `content.source=kafka`, the server is **waiting for content** when both of
  these hold:
  - the World recovered from the log has never had Zones in effect: no Content Swap with Zones has
    *applied*;
  - no followed pack's Active Pointer loads Zones.

  A swap the sim refused (`stale_base` and the rest) is a deterministic no-op, so it doesn't count,
  even though it's in the log. **How recovery knows** (decided 2026-10-01 on Codex's P1 on #292): `ContentSwap`
  gains `uint32 zone_count = 5`, the number of Zones in the whole World after the swap, the same
  scope as `world_digest`. The Loader sets it. "Has had Zones" means some swap in the recovered log
  that *applied* has `zone_count > 0`. Whether a swap applied is decided from its digests alone,
  so this is readable even when the store refuses the version's content. A swap written before the
  field reads 0. `dev`'s switch runs `world-reset` in the same roll, so its log carries the field
  from genesis. It's pinned in `log.proto` at `AW-SRV-042`'s contract review. Today's exit stays for every other case:
  - a World in which a swap with Zones has applied, and whose store now refuses its content, still
    exits `1` with "no content in effect" (`AW-SRV-012`);
  - `content.source=dir` with no Zones still exits `1` (`AW-SRV-001` AC-9). An empty directory is a
    configuration error.
- **While waiting:**
  - Recovery has finished, `andara.core` is in effect (`AW-SRV-013` AC-15), and the gRPC listener
    serves.
  - **Admin is fully served**: the content RPCs, accounts, and `server info` (which lists no Zone
    content).
  - **Game refuses `OpenSession`** with `UNAVAILABLE`, `ErrorInfo` domain `andara.game`, reason
    `no_content_in_effect`. It's retryable.
  - No Zone ticks, because there's no Zone.
- **It leaves waiting** when the first swap with Zones applies, after an Active Pointer move.
  That goes through today's `FollowContent` → `ReconcileContent` path, unchanged, and the server
  becomes Ready. It never goes back to waiting: once a swap with Zones has applied, the World has
  had content, after a restart too. If that swap is refused, the server is still waiting, live and
  after a restart.
- **The first content must hold the spawn Room.** While the server waits, `ActivateVersion` refuses
  a version whose resulting World lacks `character.spawn_room`, with `FAILED_PRECONDITION` and
  reason `spawn_room_removed`, naming the Room (`AW-SRV-013` AC-14). That reason's meaning widens
  from "removes the spawn Room the World in effect has" to "the World after this move would lack
  `character.spawn_room`". Once the World is Ready the two are the same, because boot's
  `CheckSpawnInEffect` already holds it. The reason set doesn't change. Without this, the first
  content could make the server Ready with nowhere to create a Character, and the next boot's
  `CheckSpawnInEffect` would exit `1` in a loop. When the spawn Room is in another pack, the Builder
  activates that pack first. On `dev`, the fixture pack `town` holds `purgatory/start`.
- **Health endpoints**, on `http.port`:

  | Path | 200 when |
  |------|----------|
  | `/livez` | unchanged |
  | `/startedz` | **new.** Recovery has finished and the gRPC listener is serving, waiting or not. It stays 200 until exit. Only `/readyz` reflects drain |
  | `/readyz` | unchanged in meaning, plus: 503 while waiting for content |

- **Logs:**
  - `warn` `waiting for content: no Zones in effect; publish and activate a pack`, once on entering
    the state, with `content_source`, `packs` and `core_version`;
  - `info` `content in effect: leaving the wait` when the first Zones apply, with `zones` and the
    triggering `pack@version`.
- **Metrics:** none new by architecture. `andara_content_zones_loaded` is already 0 while waiting.
  SRE says in AW-INF-021's Observability section whether it wants an explicit gauge.

### CLI (implementation)
- **`server.tls_server_name`**: flag `--tls-server-name`, env `ANDARA_TLS_SERVER_NAME`, config key
  `server.tls_server_name`. The default is empty, and empty means "from `server.address`" (today's
  behaviour). The precedence is `AW-CLI-001`'s, the same as `server.tls_ca`.
- It sets only the TLS verification name, and it's verified against `server.tls_ca` as usual. It
  doesn't change where the CLI dials, or the key credentials are stored under (`server.address`).
- `andara-cli config show` lists it with its source.

### Chart and seed (SRE, AW-INF-021)
- The `startupProbe` moves to `/startedz`, with today's budget. Readiness stays on `/readyz`, and
  liveness on `/livez`.
- `make content-seed` dials a `kubectl port-forward` to `andara-0` with
  `--tls-server-name andara-0.andara.<ns>.svc`. It stays product commands only.
- `AndaraServerUnavailable` fires while a fresh `dev` waits. That's true, and the runbook line says
  the first fix is `make content-seed ENV=<env>`.

### Placement (PM)
The server and CLI halves are one implementation story, size S–M, **on SPRINT-03's critical path**,
ahead of AW-INF-021's merge. Its ACs:
1. A kafka server with an empty store and an empty log stays up. `/startedz` returns 200,
   `/readyz` 503, `OpenSession` gets `UNAVAILABLE` `no_content_in_effect`, Admin's `ListVersions`
   succeeds, and the `warn` line appears once.
2. Publish, approve and activate a Zone-bearing pack over Admin, and the server becomes Ready with
   no restart, logging the `info` line. `OpenSession` then succeeds.
3. A World in which a swap with Zones has applied, whose store now refuses every version, still
   exits `1` (unchanged). A log whose only swap with Zones was *refused* (e.g. `stale_base`) waits,
   live and after a restart.
4. `dir` mode with no Zones still exits `1` (unchanged).
5. `--tls-server-name` verifies against the named host. A port-forward to a pod whose certificate
   names `andara-0.andara.<ns>.svc` succeeds with it, and fails verification without it.
6. Restarting a waiting server waits again, with no exit loop. Restarting after the first swap
   recovers Ready.
7. While waiting, activating a Zone-bearing version that lacks `character.spawn_room` is refused
   `spawn_room_removed`, naming the Room, and the server stays waiting. Activating one that holds
   it ends the wait.

AW-INF-021 then depends on it, and its AC-7 is observed through it.

*(Revised 2026-10-01 on Codex's review of #289. "Had content" now means a swap with Zones has
applied, not that one is in the log. And the first content must hold the spawn Room, enforced at
activation.)*
