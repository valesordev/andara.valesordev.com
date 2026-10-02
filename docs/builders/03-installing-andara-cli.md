# 3. Installing andara-cli

[← The Builder's Guide](README.md)

`andara-cli` is one self-contained binary. You need no Go and no clone of the code.

## Download

Builds for every merge are published as the `cli-dev` release:

```
https://github.com/valesordev/andara.valesordev.com/releases/download/cli-dev/SHA256SUMS
```

`SHA256SUMS` lists the current archives. The file names carry the build's commit, so they change
with every build. Pick the one for your platform:

| Platform | Archive |
|----------|---------|
| Linux, x86-64 | `andara-cli_<commit>_linux_amd64.tar.gz` |
| Linux, ARM | `andara-cli_<commit>_linux_arm64.tar.gz` |
| macOS, Intel | `andara-cli_<commit>_darwin_amd64.tar.gz` |
| macOS, Apple silicon | `andara-cli_<commit>_darwin_arm64.tar.gz` |
| Windows | `andara-cli_<commit>_windows_amd64.zip` |

Download it from the same release (`…/releases/download/cli-dev/<archive>`). The download is
anonymous.

## Check it

In the directory holding both files, on Linux:

```
sha256sum --ignore-missing -c SHA256SUMS
```

```
andara-cli_<commit>_linux_amd64.tar.gz: OK
```

On macOS, use `shasum -a 256 --ignore-missing -c SHA256SUMS`. On Windows, compare the output of
`Get-FileHash <archive>` with the archive's line in `SHA256SUMS`.

Unpack it. The archive holds `andara-cli` (`andara-cli.exe` on Windows), `LICENSE` and `NOTICE`. Put
`andara-cli` somewhere on your `PATH`.

**macOS:** the binary isn't signed. If you downloaded it with a browser, macOS blocks the first run.
Clear the quarantine with `xattr -d com.apple.quarantine andara-cli`, or open it once from Finder
with right-click, Open.

## First run

```
andara-cli version
```

```
version:  <commit>
commit:   <commit>
built_at: <time>
core:     andara.core@1
```

The `core:` line is the version of `andara.core` this binary carries. Your packs name it in their
`requires` line ([section 4](04-your-first-zone.md)). After a core change, see
[section 9](09-when-something-fails.md#core_version_mismatch).

## Point it at `dev`

Create `~/.config/andara/cli.yaml` (on Windows, `%USERPROFILE%\.config\andara\cli.yaml`):

```yaml
server:
  address: andara-dev.solo7.valesordev.com:443
```

Write the address exactly like that, with `:443`. Your login is stored under the address as
written, and a different spelling is a different login. `dev`'s certificate is a public one, so you
need no CA file, and **you must not have one set**. A CA file replaces the system's trust store
instead of adding to it, so a CA left over from a local stack makes every call to `dev` fail with
`x509: certificate signed by unknown authority`.

Check what `andara-cli` will use:

```
andara-cli config show
```

Two lines matter:
- `server.address` should read `andara-dev.solo7.valesordev.com:443`, with source `file`.
- `server.tls_ca` should be **empty**. If it isn't, its source column says where it's set: remove
  `tls_ca` from that config file, unset `ANDARA_TLS_CA_FILE`, or drop `--tls-ca`. If the `config`
  line names a file other than `~/.config/andara/cli.yaml`, `ANDARA_CONFIG` or `--config` points
  elsewhere, so unset it for `dev`.

## Log in to `dev`

```
andara-cli auth login --username <you>
```

It asks `Password: ` and prints:

```
logged in to andara-dev.solo7.valesordev.com:443 as <you>; session valid until <time>, stored in <path>
```

Check it any time with `andara-cli auth whoami`.

A login lasts an hour, and the content commands don't renew it. When one stops with
`unauthenticated` (exit 3), run `andara-cli auth refresh`, which works for 30 days after you log
in, or log in again.
