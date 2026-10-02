# andara-cli

Operator, builder, and player tooling. Reaches the server over the same versioned
Protocol as every other client — no privileged back door, no direct datastore
access (`CLAUDE.md` §10).

This package is the command tree and shared chassis (`AW-CLI-001`), the account
and auth commands (`AW-SRV-008`), the in-process `sim repl` harness, and `play`,
the Text Interface (`AW-CLI-004`), and `content`, the Content Language compiler (`AW-CLI-006`).

```
make build
export ANDARA_CONFIG=/path/to/repo/.local/cli.yaml   # written by make up
./bin/andara-cli
./bin/andara-cli version -o json
./bin/andara-cli config show
```

## Commands

| Command | Purpose |
|---------|---------|
| `andara-cli` | print the command tree |
| `andara-cli version` | version, commit, and build date |
| `andara-cli config show` | effective settings and the source of each |
| `andara-cli completion zsh` | shell completion script (`bash`/`fish`/`powershell` also) |
| `andara-cli auth login` / `logout` / `refresh` / `whoami` | the stored credential (`AW-SRV-008`) |
| `andara-cli account …` / `invite …` / `registration …` | account administration (operator) |
| `andara-cli account set-packs <id> --pack ID … \| --clear` | replace the Content Packs a Builder may publish to (`AW-SRV-035`, operator) |
| `andara-cli sim repl` | drive the Command Pipeline in-process against content on disk (developer) |
| `andara-cli snapshot list` | list a Zone's snapshot objects in the configured store (`AW-SRV-006`, operator) |
| `andara-cli character create <name>` / `list` | make a Character; list yours with where each is (`AW-CLI-007`) |
| `andara-cli play` | enter the world: the Text Interface over the Protocol (`AW-CLI-004`, `--character` from `AW-CLI-007`) |
| `andara-cli content compile` | compile a Content Language pack to canonical blobs, offline (`AW-CLI-006`, builder) |
| `andara-cli content validate` | compile a pack and run the server's validator over it, offline; or validate a published version (`AW-CLI-002`, builder) |
| `andara-cli content inspect` | print a resolved Zone, Room, or Template, with each field's provenance (`AW-CLI-002`, builder) |
| `andara-cli content fmt` | rewrite `.aw` sources to the canonical form; `--check` changes nothing (`AW-CLI-006`, builder) |
| `andara-cli content decompile` | reconstruct `.aw` source from a pack on disk (`AW-CLI-006`, builder) |
| `andara-cli content publish` / `approve` / `activate` / `rollback` | the publish path: a new version, a second approval, the Active Pointer moved or moved back (`AW-CLI-003`, builder) |
| `andara-cli content history` / `diff` / `fetch` | a pack's versions, what changed between two, and a version's source (`AW-CLI-003`, builder) |
| `andara-cli server info` | the server's build, protocol range, and content in effect (`AW-CLI-003`) |
| `andara-cli content fetch-core` | cache an `andara.core` this binary does not embed, from a pack directory (`AW-CLI-006`, builder) |

## Global flags

Every command accepts these. Precedence is **flag > environment variable > config file > default**.

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `--config` | `ANDARA_CONFIG` | `$XDG_CONFIG_HOME/andara/cli.yaml` | config file path |
| `--server-address` | `ANDARA_SERVER_ADDRESS` | `localhost:8443` | gRPC/Connect endpoint over TLS |
| `--tls-ca` | `ANDARA_TLS_CA_FILE` | empty (system trust store) | CA bundle to trust |
| `--tls-server-name` | `ANDARA_TLS_SERVER_NAME` | empty (the host in `server.address`) | the name the server's certificate is verified against (`AW-SRV-042`) |
| `--output` / `-o` | `ANDARA_OUTPUT` | `human` | `human` or `json` |
| `--log-level` | `ANDARA_LOG_LEVEL` | `warn` | CLI diagnostics, to stderr |
| `--timeout` | `ANDARA_TIMEOUT` | `30s` | per-command deadline |
| `--no-color` | `NO_COLOR` | unset | disable ANSI output |
| `--credentials` | `ANDARA_CREDENTIALS` | `$XDG_CONFIG_HOME/andara/credentials.yaml` | credential file path |

`--tls-server-name` is for a port-forward: the CLI dials `localhost` while the
pod's certificate names its Service (`andara-0.andara.<ns>.svc`). It sets only the
name TLS verifies against. The dial target is still `server.address`, and so is the
key the credential is stored under. Without it, such a dial fails verification and
exits 3.

There is no `--insecure` and no `--tls-skip-verify`. Passing either exits 2 and
points at `--tls-ca`. TLS is unconditional (ADR-0003).

`--config` and `--credentials` are paths, not keys inside `cli.yaml`. A missing
default-path config file is not an error; an explicit `--config` / `ANDARA_CONFIG`
path that does not exist is a usage error.

## Config file

YAML. Unknown keys are a usage error — they are never silently ignored.

```yaml
server:
  address: localhost:8443
  tls_ca: /path/to/ca.pem
  tls_server_name: andara-0.andara.dev.svc   # optional; see --tls-server-name
output: human
log_level: warn
timeout: 30s
no_color: false
credentials: /path/to/credentials.yaml
```

`make up` writes `.local/cli.yaml` with `server.address` and `server.tls_ca` for
the local stack. Activate it with:

```
export ANDARA_CONFIG=$PWD/.local/cli.yaml
```

## Credentials

Stored in their own file, never in `cli.yaml`. The file is a YAML mapping keyed
by `server.address`; values are opaque (`AW-SRV-008` defines what a credential
is). Mode must be `0600` or tighter — anything looser exits 2 and the file is
not read. `config show` prints the path and whether a credential is present for
the current server; it never prints the value.

## Output and exit codes

`--output json`: stdout is a single JSON value and nothing else. Human-facing
chatter and diagnostics go to stderr. On failure, stdout is:

```json
{"error":{"code":"unknown_flag","message":"...","detail":{}}}
```

| Code | Meaning |
|------|---------|
| `0` | success |
| `1` | the operation was attempted and failed |
| `2` | usage error — bad flag, bad config, missing argument |
| `3` | connection or authentication failure |
| `4` | timeout |

`error.code` values are additive-only. Codes this skeleton emits:
`unknown_flag`, `unknown_config_key`, `unsupported_flag`, `invalid_value`,
`config_not_found`, `invalid_config`, `credential_file_mode`.

Codes `play` adds: `not_logged_in`, `protocol_version`, `disconnected`, and
the connected commands' `connect_failed`, `unauthenticated`,
`permission_denied`, `server_error`, `timeout`.

Codes the roster adds (`AW-CLI-007`): `no_character` and `character_required`
(exit 2), and the server's `ErrorInfo` reasons passed through as they are —
`roster_full`, `name_taken`, `name_invalid`, `already_live`,
`no_such_character` (exit 1).

Codes `account set-packs` adds (`AW-SRV-035`): the server's `ErrorInfo` reasons
(domain `andara.accounts`) passed through as they are, all exit 1:
`operator_only`, `account_not_found`, `invalid_pack_id`, `core_not_grantable`,
`record_version`. Neither `--pack` nor `--clear`, or both, is exit 2.

Codes `content` adds (`AW-CLI-006`, `AW-CLI-002`): `compile_failed`,
`would_reformat`, `validation_failed`, `blob_hash_mismatch`, `unsafe_source_path`, `not_found` and
`core_version_mismatch` (exit 1), and `core_fetch_unavailable` (exit 3).

Codes the publish path adds (`AW-CLI-003`): the server's `ErrorInfo` reasons (domain
`andara.content`) passed through as they are, all exit 1: `validation`, `pack_not_held`,
`self_approval`, `core_published_at_boot`, `operator_only`, `unapproved`, `stale_parent`,
`blob_too_large`, `pack_too_large`, `not_found`, and an activation refusal's own reason
(`zone_removed`, `spawn_room_removed`, `core_version`) with `error.detail.subjects`. The CLI's
own: `declined` and `no_previous_version` (exit 1), `no_sources` (exit 1), and `yes_required`
(exit 2).

Help text is golden-tested, as is `play`'s rendering over a recorded Event
stream (`admin/cli/testdata/play/`). Regenerate both with `make goldens`.

## snapshot — reading the store

`snapshot list --zone <zone>` prints a Zone's snapshot objects, newest offset first: the
tick each was taken at, its `state_version`, the Partition offset that keys it, its size,
and the first twelve hex characters of its State Hash. `--output json` carries the full
hash and every field.

It reads the store **directly**, the way `sim repl` reads content on disk. Nothing here
talks to a server, so `--server-address` and the stored credential are read and unused,
and the command works when no server is running — which is when an operator most wants it
(`docs/runbooks/snapshot-stale.md`). Point it at the store the server is configured with:

```
andara-cli snapshot list --zone town --fs-path /var/lib/andara/snapshots
andara-cli snapshot list --zone town --store s3 --s3-bucket andara-prod --s3-endpoint minio:9000
```

An object that cannot be read or decoded is still a row, with its error in place of its
fields. That includes an object written by a newer binary than this one, which is the case
a rollback most needs to see; dropping it would make a corrupt or unreadable round look
like a missing one.

`snapshot verify`, and a `snapshot list` that asks the running server what *it* can see
over `Admin`, are `AW-SRV-007`'s. The two are not redundant: that one answers what the
server sees, this one answers what is actually in the bucket, and a runbook wants the
second when the first disagrees with it.

## content — the Content Language compiler

```
andara-cli content validate --path mypack
andara-cli content inspect template mypack.Merchant --path mypack
andara-cli content compile --path mypack --out build
andara-cli content fmt --path mypack --check
andara-cli content decompile --path build --out src
andara-cli content validate --pack town --version 8
```

Only `--pack` talks to a server. `compile` is pure: it reads a pack directory and
the `andara.core` the pack pins, and nothing else, so a Builder works offline and
the server's publish gate can call the same function.

**`validate`** compiles the pack, then runs the server's own validator
(`content.Validate`, the Loader's) over the compiled blobs, decoded by the server's
decoder, against the core. It has no validation logic of its own, so a pack that
passes here passes the publish gate on the same core. Findings from both stages
come out in the compiler's one format, each validator finding placed back on its
`.aw` line by the compiler's source map. A finding both stages raise is listed
once. It prints `N zones, M rooms, T templates, core andara.core@V`, or exits 1.
Under `--output json`, stdout is the array of findings and nothing else, and
stderr carries the one-line summary. `--pack ID --version N` fetches a published
version over `Admin.GetVersion` and `Admin.GetBlob`, checks every blob against its
manifest hash, compiles the sources it was published with, and validates the
compiled blobs the server holds. The blobs decide the result: sources that don't
compile are reported as warnings beside them. A source path that would leave the
pack is refused (`unsafe_source_path`) before anything is written. An unreachable
server is exit 3.

**`inspect zone|room|template <ref>`** reads the same validated pack. `zone <zone>`
prints the fallback and the Rooms. `room <zone>/<room>` prints the Components, and
the Exits in Direction order (compass, then up, down, in, out), each marked `back:
<direction>` or `one-way`. `template <pack>.<Name>` prints the flattened
Components, one field per line as `Behavior.name = "town.merchant"  (town.Merchant)`,
naming the ancestor that set each value and the one that added each marker
Component. Core Templates can be inspected too.

| Command | Flag | Default | Purpose |
|---------|------|---------|---------|
| `compile` | `--path` | `.` | pack directory to compile |
| | `--out` | empty (write nothing) | write the compiled blobs here: Zones at the root, Templates under `templates/`, sources under `src/`. Blobs a previous compile wrote that this one does not are removed; anything else in the directory is left alone. An `--out` inside `--path` is not read back as pack input |
| | `--cache` | see below | core pack cache |
| `fmt` | `--path` | `.` | directory whose `*.aw` files are rewritten |
| | `--check` | `false` | rewrite nothing; list the files that would change and exit 1 (`would_reformat`) |
| `decompile` | `--path` | `.` | the pack directory to decompile. It is compiled first, and its findings are reported like `compile`'s |
| | `--out` | empty (list only) | write the reconstructed `.aw` files here |
| | `--cache` | see below | core pack cache, needed to un-flatten inherited Components |
| `validate`, `inspect …` | `--path` | `.` | pack directory of `.aw` source |
| | `--pack`, `--version` | empty | a published version to fetch over Admin instead; exclusive with `--path` |
| | `--cache` | see below | core pack cache |
| `fetch-core` | `--from` | empty | a pack directory on disk to cache, such as `content/core` or a checkout |
| | `--version` | `1` | the `andara.core` version to cache it as |
| | `--cache` | see below | core pack cache |

**Finding the core.** A pack's `requires andara.core@M` is looked up in order:
1. the core this binary embeds, `content/core/` under its `VERSION`, if `M` is that
   version. `andara-cli version` names it. It's the same bytes every server embeds and
   publishes at boot, and a test holds its digest to `content/core/VERSIONS`;
2. the cache: `--cache`, then `ANDARA_CONTENT_CACHE`, then `~/.cache/andara/packs`.
   `fetch-core --from` writes it.

Otherwise the compile fails with `core_version_mismatch`, naming both versions and
the remedy for the direction of the skew. If `M` is above the embedded core, it names the
`andara-cli` release that embeds `andara.core@M`. If `M` is below it (the usual case after a core
bump), it says to change `requires` in `pack.aw` to the embedded version. `fetch-core`
never fetches over Admin: without `--from` it exits 3 with `core_fetch_unavailable`,
naming the embedded core. `decompile` has no `--pack`/`--version`: a published
version's source is `content fetch`'s.

Findings print one per line on stderr as `file:line:col: CODE message`, with the
declaration chain indented beneath, and warnings print even when the compile
succeeds. Under `--output json`, `compile`'s findings ride in the result's
`diagnostics` array, or in `error.detail.diagnostics` when the compile is refused,
so stdout stays one JSON value. `validate`'s stdout is the array itself. The codes and positions are `docs/specs/content-language/errors.md`'s.

### The publish path (`AW-CLI-003`)

```
A$ andara-cli content publish --path ./town      # town@8 published (parent 7), awaiting approval
B$ andara-cli content approve town 8             # town@8 approved by B
A$ andara-cli content activate town 8            # shows what changes; y to proceed
A$ andara-cli server info                        # content town@8
A$ andara-cli content rollback town              # back to the version active before
```

Each command is one of `AW-SRV-013`'s Admin RPCs. The server is the security boundary; the
commands show approval state before a change and render the server's refusal.

- **`publish [--path DIR] [--pack ID]`** validates locally as `content validate` does, and a
  local finding stops it there. It then asks `HasBlobs` which blobs the server already has and
  uploads only the rest, one `PublishBlob` stream each, and publishes with the pack's newest
  version as parent. A stale parent (someone published meanwhile) is explained, not retried.
  The server's findings print in the same format as local ones, placed on the source.
- **`approve <pack> <version> [--yes]`**: approving a version you published (an Operator's
  self-approval) asks first; with no terminal it needs `--yes`.
- **`activate <pack> <version> [--yes] [--override --reason TEXT]`** and **`rollback <pack>
  [--to N] [--yes]`** print the pack, the version in effect and the one replacing it, who
  published it and who approved it, and wait for `y`. With no terminal they need `--yes`. The
  text is logged at `info` with the trace ID the RPCs carry. `rollback` defaults to the version
  active before the current one.
- **`history <pack> [--limit N]`**: every version, newest first, `*` on the active one, with
  author, approval and every interval it was active.
- **`diff <pack> <N> <M>`**: Zones, Rooms, Exits, Templates and Component fields added (`+`),
  removed (`-`) or changed (`~`), each pointing into M's source, or N's for a removal.
- **`fetch <pack> <version> [--out DIR]`** writes the source the version was published with.
  A compiled Template records its source as `<pack directory>/<file>`, so `--out` defaults to
  the directory name it was published from, where it compiles back to the same blobs.
- **`server info`** prints `version`, `commit`, `environment`, `protocol <min>-<max>`, one
  `content <pack>@<version>` per pack, and `content_digest <hex>`.

Accounts are named by ID, except your own, which is your username: no RPC reads another
Account's name. Every `--output json` result and error carries `trace_id`.

## character — the roster

```
andara-cli character create <name>
andara-cli character list
```

Creating and choosing a Character are roster RPCs on the Game service
(`AW-SRV-014`), not world Commands: nothing typed into the world means create
or select. Each command opens a Session of its own with the stored credential,
calls the RPC, and closes it, so every roster action is a Session-correlated
audit line on the server.

`create` prints `Aldric (1 of 5)`: the name as the server stored it, and the
roster's size against the server's cap. `--output json` is
`{"character": <CharacterSummary>, "count": 1, "max_per_account": 5}`. The count
is the roster listed just before the create plus one, because
`CreateCharacterResponse` carries the cap but not the count. A refused name
prints the server's message and exits 1 with its reason as `error.code`.

`list` prints one line per Character, sorted by name: the name, `live` (bound
to a Session now) or `dormant`, and `zone/room`, where the server last knew it to
be. `--output json` is `{"characters": [<CharacterSummary>…], "max_per_account": 5}`.
The server is asked every time; nothing is cached.

## play — the Text Interface

```
andara-cli auth login --username <you>
andara-cli character create <name>
andara-cli play [--character <name>] [--as <account-id>] [--world] [--show-protocol] [--no-history] [--reconnect=false] [--client-timeout 10s]
```

Before it subscribes, `play` enters the World as a Character: the one
`--character` names (case-insensitively, resolved through `ListCharacters`; a
`character_id` is never typed), or with no flag the Account's only one. With no
Characters it exits 2 `no_character`, and with several and no flag it exits 2
`character_required` and lists them. Either way nothing is subscribed. The order
is `OpenSession` → `SelectCharacter` → `Subscribe` → `look`. The selection's
answer is an ack, like `Submit`'s: it appears only under protocol visibility
(`» SelectCharacter session_id=… character_id=…`), and the arrival comes on the
stream like any other Event. The connection notice names the Character:
`-- Connected to <server> as <you>, playing Aldric (session …, protocol 1).`
If the Character is live on another Session at launch, `play` prints the server's
message and exits 1 `already_live`.

A reconnect selects the same Character again before it resumes. The old
Session's teardown is what frees the Character, and the new Session can get
there first, so during a reconnect `already_live` is retried on the reconnect
backoff and announced once (`-- Waiting for your previous session to end.`). It
is never fatal there. Any other refusal of the selection ends `play` with that
reason.

`play` opens a Session with the stored credential, subscribes to its Events,
asks the world for the Room, and then reads lines. What you type is sent as
typed — the server parses it (`AW-SRV-003`); this client decides nothing — and
what the world says arrives as prose. The renderer is a pure function from
`EventEnvelope` to lines (`admin/cli/render.go`); a Phase 2 client substitutes
another against the same recorded stream.

| Event | Rendered as |
|-------|-------------|
| `RoomDescribed` | title, description, `Exits: …`, `Here: …`, each linkdead occupant marked `(linkdead)` |
| `CharacterArrived` / `CharacterLeft` | one line naming the Character and the direction |
| `CharacterLinkdead` | "`<name>` goes linkdead." |
| `CharacterReconnected` | "`<name>` reconnects." |
| `CharacterDespawned` | "`<name>` fades from the world." for `linkdead` and `linkdead_ceiling`; "`<name>` leaves the world." for `quit`, `switch`, and any other reason. The wording is a placeholder, Brian's to change |
| `CommandRejected` | the message, verbatim — the same voice as a refusal returned on `Submit` |
| `Heartbeat` | nothing |
| `Resync` | "You may have missed some events; the world continues from here." and a fresh `look` |
| `ZoneFaulted`, `SubscriberDropped`, `SimulationStopped` | one system-voice line each (`SubscriberDropped`'s `buffer_full` and `revoked` as prose; the token stays under `/protocol`) |
| anything newer than this client | "Something happened here that this client cannot describe (event N)." |

Lines that start with `/` are for the client, never sent:

| Line | Effect |
|------|--------|
| `/protocol [on\|off]` | protocol visibility: every Intent sent and Event received, with message name, session ID, `client_ref`, `event_id`, and tick, indented and marked `»` (sent) / `«` (received). `--show-protocol` starts with it on. |
| `/help` | the list |
| `/quit` | leave, as do Ctrl-C and Ctrl-D at an empty line: the Session is closed and the exit code is 0 |

Refusals print the server's message as given, wherever in the pipeline they
happened; the `ErrorInfo.reason` steers only behavior (`AW-SRV-010`,
`AW-SRV-031`):

| Server answer | Client behavior |
|---------------|-----------------|
| `UNAVAILABLE` `world_read_only` / `in_transit` | hold the prompt for `RetryInfo` (500 ms without one), retry the same line up to 3 times, then print the message |
| `DEADLINE_EXCEEDED` `produce_deadline`, or the client's own `--client-timeout` | retry with the **same** `client_ref` up to 4 attempts, on the **same Session** — the server answers with the original outcome. If the Session changed under the retry (a reconnect), the outcome is unknown and the player is told to `look`; a retry is never sent on a Session the line did not start in |
| `DEADLINE_EXCEEDED` `outcome_unknown` | stop; "the world may or may not have taken it. Use `look`" |
| `UNAUTHENTICATED` on `Submit` | the Session is gone: the line is dropped and the stream loop reopens a Session |
| anything else typed | the message, once |

A dropped connection is announced (`-- Connection lost; reconnecting.`), retried
with backoff (1 s doubling to 15 s, jittered), and the stream resumed from the last
`event_id` seen; a resume the server cannot honor arrives as a `Resync`, which is
announced and followed by a `look`. A connection that died without saying so is
noticed by the transport, not by luck: the stream connection is health-checked with
an HTTP/2 ping after 30 s of silence — above the server's 20 s heartbeat, so a live
stream never pings — and errors within ~45 s of dying. If the stored session token
has expired by the time of a reconnect (`auth.session_ttl` counts from login), the
refresh token is exchanged once and the credential file updated, as `auth refresh`
does. `--reconnect=false` makes a drop exit 3 (`disconnected`) instead, for scripts.
A server whose Protocol range excludes this client's version (1) exits 3 with
`protocol_version`, naming both ranges. SIGINT and SIGTERM end play at once, even
mid-retry, with the Session closed.

`--output json` writes the raw stream — every envelope, heartbeats included —
to stdout as one `protojson` object per line, and everything else (notices,
refusals, protocol lines) to stderr, notices and refusals as the structured log
lines of `AW-CLI-001`. On a pipe rather than a terminal, `play` shows no prompt,
holds each line until the previous one's Events have arrived (up to 3 s), and
lingers the same at end of input, so a scripted transcript reads in the order
it was typed:

```
printf 'look\nnorth\n' | andara-cli play
printf 'look\n' | andara-cli play --output json | jq .
printf 'look\n' | andara-cli play --output json | jq 'select(.heartbeat == null)'   # Events only
```

Heartbeats are in the JSON stream on purpose: the proto calls them stream
frames, and a heartbeat's `tick` is the only liveness a script can see. Filter
them out with the `jq` above rather than expecting the client to.

History lives at `$XDG_STATE_HOME/andara/history` (`~/.local/state/andara/history`),
last 1000 lines, opt out with `--no-history`. `--timeout` bounds opening the
Session; `--client-timeout` (default: `--timeout`) bounds one `Submit`; the play
session itself has no deadline. `--as` opens the Session acting as another
account (operator or game master; every audit record names both). `--world`
asks for World visibility on the stream (`AW-SRV-011`); the role alone never
implies it.

`make stack-play` runs the M1 gate against the local stack (`scripts/stack_play.sh`).
