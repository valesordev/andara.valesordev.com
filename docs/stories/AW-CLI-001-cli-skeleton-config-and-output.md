---
id: AW-CLI-001
title: andara-cli skeleton — command tree, configuration precedence, and output contract
epic: EPIC-06
component: cli
type: feature
status: ready
size: S
depends_on: [AW-INF-001]
blocks: [AW-CLI-002, AW-CLI-003, AW-CLI-004]
assignee: cursor
risk: low
---

## Context

`andara-cli` is the Operator's and Builder's only tool, and it is now also the *player's* tool:
ADR-0003 chose gRPC, and a human cannot telnet into a gRPC server, so the Phase 1 Text Interface is
`andara-cli play` (`AW-CLI-004`). ADR-0004 put content in the data layer, so it is the Builder's tool
too. That makes the skeleton load-bearing for three audiences rather than one.

Building the skeleton first is cheap and prevents the usual outcome, where the fifteenth command
invents its own flag conventions, its own output format, and its own error handling because the
first fourteen never agreed on any.

## User story

As an operator, I want every `andara-cli` command to take the same global flags, read configuration
from the same precedence chain, and emit machine-readable output on request, so that I can script
against it without special-casing each subcommand.

## Scope

### In scope
- Command tree skeleton with `version`, `config`, and `completion`.
- Configuration precedence: flag > environment variable > config file > default.
- Output contract: human-readable by default, `--output json` for machines, on every command.
- Exit-code taxonomy shared by all commands.
- Error presentation: one actionable line on stderr, structured detail under `--output json`.
- Client-side telemetry: command name, duration, outcome.
- TLS trust configuration: where the CA comes from, and the deliberate absence of an escape hatch.
- Credential *storage*: file location, permissions, and how it interacts with config precedence.

### Out of scope
- The Protocol client itself and any command that connects — `AW-CLI-004` and `AW-SRV-005`, which own
  the generated gRPC/Connect client and Session establishment.
- Content commands — `AW-CLI-002` and `AW-CLI-003`.
- The auth *model* — token format, issuance, refresh, and lifetimes are `AW-SRV-008`'s. This story
  owns only where a credential is stored and how it is protected, because that is a config-precedence
  question and the precedence chain is built here.

## Acceptance criteria

1. **Given** no arguments **when** `andara-cli` runs **then** it prints the command tree with a
   one-line description per command and exits 0.
2. **Given** `andara-cli version` **when** it runs **then** it prints version, commit, and build date,
   and with `--output json` emits an object with keys `version`, `commit`, `built_at`.
3. **Given** a config file setting `server.address`, an environment variable
   `ANDARA_SERVER_ADDRESS`, and a `--server-address` flag, all with different values **when**
   `andara-cli config show` runs **then** the flag value wins, and the output names the source of
   every effective setting.
4. **Given** an unknown flag **when** any command runs **then** it exits 2, prints one line naming
   the unknown flag on stderr, and prints nothing on stdout.
5. **Given** `--output json` on any command **when** it succeeds **then** stdout contains only valid
   JSON and nothing else; all human-facing chatter goes to stderr.
6. **Given** `--output json` on any command **when** it fails **then** stdout contains a JSON object
   with keys `error.code`, `error.message`, `error.detail`, and the process exits non-zero.
7. **Given** a config file with an unknown key **when** any command runs **then** it exits 2 naming
   the key and the file. Silent ignoring of unknown configuration is a defect.
8. **Given** `andara-cli completion zsh` **when** it runs **then** it emits a valid completion script
   and exits 0.
9. **Given** any command **when** it runs **then** it never reads a datastore, a Kubernetes secret,
   or any resource other than its config file, its credential file, its environment, and (later) the
   server Protocol.
10. **Given** `--insecure`, `--tls-skip-verify`, or any equivalent **when** any command runs **then**
    it exits 2 naming the flag as unsupported and stating that the CA is configured with `--tls-ca`.
    This is an acceptance criterion rather than a convention because a prohibition that lives only in
    a comment acquires an exception by the fifth pull request, and that exception ships.
11. **Given** a credential file whose mode is more permissive than `0600` **when** any command runs
    **then** it exits 2 naming the file and its mode, and reads nothing from it.
12. **Given** a stored credential **when** `andara-cli config show` runs, with or without
    `--output json` **then** the credential's value appears nowhere in the output; the file path and
    whether a credential is present may appear.

## Interface contract

### Global flags (every command)

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `--config` | `ANDARA_CONFIG` | `$XDG_CONFIG_HOME/andara/cli.yaml` | config file path |
| `--server-address` | `ANDARA_SERVER_ADDRESS` | `localhost:8443` | the server endpoint — gRPC/Connect over TLS |
| `--tls-ca` | `ANDARA_TLS_CA_FILE` | empty | CA bundle to trust; empty means the system trust store |
| `--output` / `-o` | `ANDARA_OUTPUT` | `human` | `human` or `json` |
| `--log-level` | `ANDARA_LOG_LEVEL` | `warn` | CLI's own diagnostics, to stderr |
| `--timeout` | `ANDARA_TIMEOUT` | `30s` | per-command deadline |
| `--no-color` | `NO_COLOR` | unset | disable ANSI output |
| `--credentials` | `ANDARA_CREDENTIALS` | `$XDG_CONFIG_HOME/andara/credentials.yaml` | credential file path |

### TLS, and the flag that does not exist

`--server-address` names a TLS endpoint. There is **no `--insecure` and no `--tls-skip-verify`**, and
AC-10 asserts their absence rather than leaving it to reviewer discipline. ADR-0003 puts TLS on the
wire unconditionally; a skip-verify flag is how that becomes optional in practice, first in a shell
alias, then in a script, then in production.

`--tls-ca` defaults to empty, meaning the system trust store — so a deployed environment with a
publicly-issued certificate needs no flag at all. The local stack is the case that needs one, and
`make up` (`AW-INF-002`) writes a ready-to-use config naming both the endpoint and the generated CA,
so the local path is one `ANDARA_CONFIG` export rather than two flags on every invocation.

Port 8080 is **not** the endpoint. `AW-INF-002` binds it to loopback, in plaintext, for health and
metrics — an operator surface, not a protocol one. An earlier draft of this table defaulted
`--server-address` to `localhost:8080`; a client built against that would have spoken gRPC at a
Prometheus endpoint over cleartext.

### Credential storage

Credentials live in their own file, never in `cli.yaml`:

| Property | Rule |
|----------|------|
| Path | `$XDG_CONFIG_HOME/andara/credentials.yaml`, overridable with `--credentials` |
| Mode | `0600`. Anything looser is a usage error, not a warning (AC-11). |
| Precedence | Credentials do **not** participate in the flag > env > file > default chain for other settings; they are looked up by `server.address` so one file serves several environments. |
| Output | Never printed by `config show`, never logged, never included in `--output json` (AC-12). |
| Content | Opaque to this story. `AW-SRV-008` defines what a credential *is*; this story defines only where it lives and who may read it. |

A stored credential is what makes `--as` auditable rather than anonymous: acting as another identity
requires knowing who is really acting, so the server must always be able to name the real caller. See
`AW-CLI-004` and `AW-SRV-008`.

### Exit codes

| Code | Meaning |
|------|---------|
| `0` | success |
| `1` | the operation was attempted and failed (server error, validation failure) |
| `2` | usage error — bad flag, bad config, missing argument; nothing was attempted |
| `3` | connection or authentication failure — the server was not reached |
| `4` | timeout |

The distinction between 1, 2, and 3 is load-bearing for scripting: a 2 means fix the invocation, a
3 means fix the environment, a 1 means look at what the server said.

### Error taxonomy

```
error.code   — stable, machine-readable, snake_case, never localized
error.message — one line, actionable, human-facing
error.detail — optional structured object with the specifics
```

`error.code` values are additive-only; removing or repurposing one is a breaking change to any script
that branches on it.

## Data / state impact

Reads a config file and, when present, a credential file. Writes nothing except where a future
command is explicitly a write command.

The credential file's *contents* arrive with `AW-SRV-008` and must not be improvised here. Its
location, its `0600` requirement, and its exclusion from every output path are settled here, because
they are properties of the configuration system and retrofitting them means a period during which
credentials leak into `config show` output and shell history.

## Observability requirements

A CLI's observability is its output and its exit code; it is not a service and does not scrape.

### Metrics
None. Explicitly none — do not add a metrics exporter to a short-lived CLI process.

### Logs
- CLI diagnostics go to stderr, structured when `--output json`, human when not.
- Required fields when structured: `ts`, `level`, `msg`, `command`, `trace_id`.
- Never log the contents of the config file, and never log the credential file at any level, for any
  reason, including at `debug`.

### Traces
- `cli.command` — root span per invocation, attributes `command`, `outcome`, `exit_code`. Propagated
  to the server as the parent of `command.execute` once the Protocol client exists, so an operator
  action is traceable end to end. This is why the trace ID exists in this story rather than later.

### Alerts
None.

## Test plan

- **Unit:** precedence resolution across all four sources; unknown-flag and unknown-config-key
  handling; exit-code mapping for each error class; JSON output validity including the failure case;
  stdout/stderr separation asserted byte-for-byte.
- **Integration:** golden-file tests over `--help` output for the whole tree, so an accidental change
  to the command surface is visible in a diff.
- **Manual/operator:**
  ```
  andara-cli
  andara-cli version -o json | jq .
  andara-cli config show -o json | jq '.settings[] | {key, value, source}'
  andara-cli --bogus ; echo $?    # expect 2
  ```

## Definition of done

CLAUDE.md §8, plus:
- `--help` golden files exist and are regenerated by a make target, not by hand.
- Every command in the tree accepts every global flag; a test asserts this over the whole tree
  rather than per command.

## Open questions

- `[ASSUMPTION]` Cobra-style command tree, per CLAUDE.md §1.
- `[ASSUMPTION]` Config file format is YAML, matching the server's. If the server config ends up
  elsewhere, they should agree.
- Per ADR-0003 there is **one endpoint** serving both `andara.game.v1.Game` and
  `andara.admin.v1.Admin`, so `--server-address` is a single value rather than a game/admin pair. It
  is `localhost:8443` locally, matching `AW-SRV-005`'s `grpc.listen` default.
- **Resolved 2026-09-07 (Brian):** stored credentials are supported, and acting as another identity
  still records who was really acting. The storage contract is above; the token model is
  `AW-SRV-008`'s.
- `[ASSUMPTION]` The client uses Connect's Go implementation, which speaks gRPC, gRPC-Web, and Connect
  from one generated client — matching what the server serves (ADR-0003).
