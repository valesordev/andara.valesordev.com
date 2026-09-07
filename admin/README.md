# andara-cli

Operator and builder tooling. Go, Cobra-style command tree.

Not yet implemented. First story: `AW-CLI-001` (command tree, configuration precedence, output
contract).

`andara-cli` reaches the server over the same versioned Protocol as every other client. There is no
privileged back door into the process and no direct datastore write for anything a command can do
(`CLAUDE.md` §10). If an operational procedure cannot be expressed as a CLI command, that is a gap
in the CLI, not a reason to open a database client.

## Configuration

Precedence: flag > environment variable > config file > default. Default config path is
`$XDG_CONFIG_HOME/andara/cli.yaml`. Every command accepts `--output json` and emits nothing but JSON
on stdout when it does. Exit-code taxonomy is in `AW-CLI-001`.
