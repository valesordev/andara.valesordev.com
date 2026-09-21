# andara-cli

Operator, builder, and player tooling. Reaches the server over the same versioned
Protocol as every other client — no privileged back door, no direct datastore
access (`CLAUDE.md` §10).

This package is the command tree and shared chassis (`AW-CLI-001`), the account
and auth commands (`AW-SRV-008`), the in-process `sim repl` harness, and `play`,
the Text Interface (`AW-CLI-004`). `content` arrives with `AW-CLI-002`.

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
| `andara-cli sim repl` | drive the Command Pipeline in-process against content on disk (developer) |
| `andara-cli play` | enter the world: the Text Interface over the Protocol (`AW-CLI-004`) |

## Global flags

Every command accepts these. Precedence is **flag > environment variable > config file > default**.

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `--config` | `ANDARA_CONFIG` | `$XDG_CONFIG_HOME/andara/cli.yaml` | config file path |
| `--server-address` | `ANDARA_SERVER_ADDRESS` | `localhost:8443` | gRPC/Connect endpoint over TLS |
| `--tls-ca` | `ANDARA_TLS_CA_FILE` | empty (system trust store) | CA bundle to trust |
| `--output` / `-o` | `ANDARA_OUTPUT` | `human` | `human` or `json` |
| `--log-level` | `ANDARA_LOG_LEVEL` | `warn` | CLI diagnostics, to stderr |
| `--timeout` | `ANDARA_TIMEOUT` | `30s` | per-command deadline |
| `--no-color` | `NO_COLOR` | unset | disable ANSI output |
| `--credentials` | `ANDARA_CREDENTIALS` | `$XDG_CONFIG_HOME/andara/credentials.yaml` | credential file path |

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

Help text is golden-tested, as is `play`'s rendering over a recorded Event
stream (`admin/cli/testdata/play/`). Regenerate both with `make goldens`.

## play — the Text Interface

```
andara-cli auth login --username <you>
andara-cli play [--as <account-id>] [--world] [--show-protocol] [--no-history] [--reconnect=false] [--client-timeout 10s]
```

`play` opens a Session with the stored credential, subscribes to its Events,
asks the world for the Room, and then reads lines. What you type is sent as
typed — the server parses it (`AW-SRV-003`); this client decides nothing — and
what the world says arrives as prose. The renderer is a pure function from
`EventEnvelope` to lines (`admin/cli/render.go`); a Phase 2 client substitutes
another against the same recorded stream.

| Event | Rendered as |
|-------|-------------|
| `RoomDescribed` | title, description, `Exits: …`, `Here: …` |
| `CharacterArrived` / `CharacterLeft` | one line naming the Character and the direction |
| `CommandRejected` | the message, verbatim — the same voice as a refusal returned on `Submit` |
| `Heartbeat` | nothing |
| `Resync` | "You may have missed some events; the world continues from here." and a fresh `look` |
| `ZoneFaulted`, `SubscriberDropped`, `SimulationStopped` | one system-voice line each |
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
| `DEADLINE_EXCEEDED` `produce_deadline`, or the client's own `--client-timeout` | retry with the **same** `client_ref` up to 4 attempts — the server answers with the original outcome |
| `DEADLINE_EXCEEDED` `outcome_unknown` | stop; "the world may or may not have taken it. Use `look`" |
| `UNAUTHENTICATED` on `Submit` | the Session is gone: the line is dropped and the stream loop reopens a Session |
| anything else typed | the message, once |

A dropped connection is announced (`-- Connection lost; reconnecting.`), retried
with backoff (1 s doubling to 15 s, jittered), and the stream resumed from the last
`event_id` seen; a resume the server cannot honor arrives as a `Resync`, which is
announced and followed by a `look`. `--reconnect=false` makes a drop exit 3
(`disconnected`) instead, for scripts. A server whose Protocol range excludes
this client's version (1) exits 3 with `protocol_version`, naming both ranges.

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
```

History lives at `$XDG_STATE_HOME/andara/history` (`~/.local/state/andara/history`),
last 1000 lines, opt out with `--no-history`. `--timeout` bounds opening the
Session; `--client-timeout` (default: `--timeout`) bounds one `Submit`; the play
session itself has no deadline. `--as` opens the Session acting as another
account (operator or game master; every audit record names both). `--world`
asks for World visibility on the stream (`AW-SRV-011`); the role alone never
implies it.

`make stack-play` runs the M1 gate against the local stack (`scripts/stack_play.sh`).
