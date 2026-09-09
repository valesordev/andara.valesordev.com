# andara-cli

Operator, builder, and player tooling. Reaches the server over the same versioned
Protocol as every other client — no privileged back door, no direct datastore
access (`CLAUDE.md` §10).

This package is the command tree and shared chassis (`AW-CLI-001`). Protocol
commands arrive in later stories (`play` in `AW-CLI-004`, `content` in `AW-CLI-002`).

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

Help text is golden-tested. Regenerate with `make goldens`.
