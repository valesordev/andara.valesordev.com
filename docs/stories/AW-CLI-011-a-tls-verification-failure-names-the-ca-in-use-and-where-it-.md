---
id: AW-CLI-011
title: A TLS verification failure names the CA in use and where it came from
epic: EPIC-06
component: cli
type: feature
status: draft
size: S
depends_on: [AW-CLI-001, AW-SRV-042]
blocks: []
lane: implementation
risk: low
---

## Context

In SPRINT-03's M3 walk-through, the Operator's first `andara-cli auth login` to `dev` failed with
`tls: failed to verify certificate: x509: certificate signed by unknown authority`. `dev`'s edge
serves a public Let's Encrypt certificate. The CLI had a private CA pinned, left over from local
stack work (`ANDARA_CONFIG=.local/cli.yaml`, written by `make up`). A pinned CA replaces the system
trust store, so the public certificate failed. Nothing in the error said a CA was pinned, or where
the pin came from. Finding it took `config show`, a reproduction, and a read of the scripts.

The Builder's Guide now says how to find and remove a pin (#330, section 3). Architecture asked for
the CLI half (2026-10-02): when verification fails, the error itself names the CA in use and its
source, so the next person reads the cause instead of searching for it. It isn't on any milestone
path.

## User story

As a builder, I want a certificate error from `andara-cli` to tell me which CA it trusted and where
that setting came from, so that I can fix a stale CA pin without a support conversation.

## Scope

### In scope
- On a TLS certificate-verification failure while connecting, the connect error's message and its
  JSON `detail` name the CA in use and its source.

### Out of scope
- Other connect failures (refused, DNS, timeout), which keep their current messages.
- Changing how a CA is resolved, or which source takes precedence (`AW-CLI-001`).
- Warning when a pinned CA is merely present but unused. Only a failure reports it.

## Acceptance criteria

1. **Given** `server.tls_ca` set from a config file, and a server whose certificate that CA didn't
   sign **when** any command connects **then** it exits `3`, and the message ends with
   `; trusted only the CA in <path> (server.tls_ca, from config file <config path>)`.
2. **Given** the CA from `ANDARA_TLS_CA_FILE` **then** the source reads `from ANDARA_TLS_CA_FILE`.
   **Given** it from `--tls-ca` **then** the source reads `from --tls-ca`.
3. **Given** the config file was chosen by `ANDARA_CONFIG` or by `--config` **then** AC-1's
   `<config path>` is that file, followed by `(ANDARA_CONFIG)` or `(--config)`. The default path has
   no suffix.
4. **Given** no CA set, and a certificate the system trust store rejects **then** the message ends
   with `; trusted the system trust store`.
5. **Given** a failure that isn't certificate verification (refused, DNS, timeout) **then** the message
   is unchanged.
6. **Given** `--output json` **then** the error envelope's `detail` carries `tls_ca` (path or empty),
   `tls_ca_source` (`flag`, `env`, `file` or `default`), `config_path`, and `config_path_source`
   (`flag` for `--config`, `env` for `ANDARA_CONFIG`, `default`), for ACs 1–4. A JSON consumer can
   tell everything the human message says, including AC-3's distinction.
7. **Given** a `--tls-server-name` mismatch (the certificate is valid but for another name) **then** the
   message names the expected name and its source in the same form. That's the port-forward case
   (`AW-SRV-042`).

## Interface contract

- **Exit:** `3`, code `connect_failed` (`CodeConnect` in `admin/cli/client.go`), as today
  (`AW-CLI-001`). The code doesn't change. Only the message and `detail` gain the suffix and fields,
  so scripts that branch on `connect_failed` keep working.
- **Message suffix forms:**
  - `; trusted only the CA in <path> (server.tls_ca, from <source>)`
  - `; trusted the system trust store`
  - `; expected the name <name> (server.tls_server_name, from <source>)` (AC-7)
- **`<source>`:** `--tls-ca`, `ANDARA_TLS_CA_FILE`, or `config file <path>`, plus ` (ANDARA_CONFIG)` or
  ` (--config)` when one of those chose the file. These are the sources `config show` already
  reports.
- **JSON `detail` keys:** `tls_ca`, `tls_ca_source`, `config_path`, `config_path_source` and, for AC-7,
  `tls_server_name` and `tls_server_name_source`. Source values are `config show`'s: `flag`, `env`,
  `file`, `default`.
- **Classifying the failure:** the error unwraps to `x509.UnknownAuthorityError`,
  `x509.CertificateInvalidError` or `x509.HostnameError`. Nothing else gets the suffix.

## Data / state impact

None.

## Observability requirements

None new. It's a local CLI message. `AW-CLI-001`'s `cli.command` span already records the failed
connect. The new `detail` fields hold a file path and a source name, never a certificate or a
credential.

## Test plan

- **Unit:** each source for ACs 1–4 and 7, against an `httptest` TLS server with its own CA. The
  unchanged messages for AC-5. The JSON keys for AC-6.
- **Integration:** none beyond unit. The walk-through reproduction is a unit case: a CA from
  `ANDARA_CONFIG` against a server signed by another CA.
- **Manual/operator:**
  ```
  ANDARA_TLS_CA_FILE=/some/other/ca.pem andara-cli server info
  # expect: exit 3, "...; trusted only the CA in /some/other/ca.pem (server.tls_ca, from ANDARA_TLS_CA_FILE)"
  ```

## Definition of done

CLAUDE.md §8.

## Open questions

`depends_on` includes `AW-SRV-042` (`done`), which introduced `server.tls_server_name` for AC-7.
Revised after review of #331: the error code is `connect_failed`, the JSON names the config file's
selector, and `--config` is a selector too.

Nothing else is open. Architecture agreed the scope on 2026-10-02.
