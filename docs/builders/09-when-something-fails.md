# 9. When something fails

[← The Builder's Guide](README.md)

## Exit codes

Every `andara-cli` command exits with one of these:

| Exit | Meaning | Usually |
|-----:|---------|---------|
| 0 | done | warnings alone still exit 0 |
| 1 | tried, and was refused | findings in your pack, or the server refused: approval, permission, a stale parent, an activation refusal. You answered no to a prompt |
| 2 | the command was wrong | a bad flag or argument; `--yes` missing with no terminal; not logged in |
| 3 | couldn't reach the server, or it doesn't know you | off the tailnet, a wrong `server.address`, an expired login |
| 4 | the server didn't answer in time | `--timeout`, 30 s by default |

With `--output json`, the reason is `error.code`, and that's the word in the tables below. Findings
are different. `validate`, and `publish` refused by the server, print the findings array on stdout,
with each finding's own code, and the summary's code on stderr ([section 5](05-the-everyday-loop.md#scripting)).

## The errors you'll meet

### Your pack

| Code | What happened | Usual fix |
|------|---------------|-----------|
| `unknown_room` | an Exit points at a Room that isn't there | fix the target's spelling. A bare target means the same Zone, and `zone.room` another Zone of your pack ([errors.md §3.2](../specs/content-language/v1/errors.md#32-raised-by-the-compiler-defined-by-the-loader)) |
| `unknown_zone` | an Exit points at a Zone your pack doesn't have | Exits can't cross packs. Use `goto` to reach other packs ([section 8](08-building-on-dev.md#reaching-your-zone-goto)) |
| `duplicate_direction` | a Room has two Exits the same way | one Exit per Direction per Room |
| `fallback_missing` | a Zone has no `fallback`, or it names a Room that isn't in the Zone | add `fallback <room>` |
| `pack_mismatch` | the `pack` line names a different pack from the one being compiled | in the Content Repository, the `pack` line names the directory it's in |
| `unknown_component_type` | a Component the server doesn't define | [the list](07-reference.md#component-types) |
| `missing_reverse_exit` | warning: an Exit has no Exit back | add the return Exit, or leave it if one-way is what you meant |
| `orphan_room` | warning: nothing in the Zone leads into the Room | add an Exit in, or ignore it if you'll `goto` there |
| `would_reformat` | `fmt --check` found files not in canonical layout | `andara-cli content fmt --path <pack>` |
| `validation_failed` | `validate` or `publish` found findings before sending anything | fix them. Each one is on its own line above the summary |

Every code is in the [error contract](../specs/content-language/v1/errors.md#3-the-codes).

### <a id="core_version_mismatch"></a>`core_version_mismatch`

Your `pack.aw` says `requires andara.core@N`, and your `andara-cli` carries a different core. Compare
the number with the `core:` line of `andara-cli version`:
- **Your `requires` is lower:** `andara.core` has moved on, which is the usual case. Change `pack.aw`
  to the number `andara-cli version` shows, validate, and fix whatever the new core changed.
- **Your `requires` is higher:** your `andara-cli` is old. Download the current one
  ([section 3](03-installing-andara-cli.md#download)), or `make tools` in the Content Repository.

The message's remedy is right in the second case and wrong in the first. That's #308.

### Publishing and activating

| Code | What happened | Usual fix |
|------|---------------|-----------|
| `validation` | the server refused the publish. Its findings print like local ones | most often `duplicate_zone`: your Zone ID is taken on `dev` ([section 8](08-building-on-dev.md#the-town-and-the-zone-ids-it-holds)) |
| `pack_not_held` | you don't hold that pack | ask an Operator for the grant ([section 2](02-getting-access.md#a-pack)) |
| `stale_parent` | someone published while you were uploading | `content history`, then publish again ([section 5](05-the-everyday-loop.md#sharing-a-pack)) |
| `unapproved` | the version has no approval | ask for one ([section 5](05-the-everyday-loop.md#asking-for-an-approval)) |
| `self_approval` | you can't approve a version you published | a second Builder holding the pack approves it |
| `zone_removed` | activating would drop a Zone that's in effect | keep the Zone. Removing a Zone isn't supported |
| `spawn_room_removed` | activating would leave `dev` without its spawn Room | keep `purgatory/start`. This only affects `town` |
| `core_version` | the version was built for a different `andara.core` than `dev` runs | rebuild against `dev`'s core: compare `server info`'s `andara.core` line with yours |
| `not_found` | no such version | `content history` |
| `operator_only` | `--override`, or moving `andara.core` | ask an Operator |
| `declined` | you answered anything but `y` | nothing changed |
| `no_previous_version` | nothing earlier to roll back to | `rollback --to N`, or `activate` |

### Connecting

| Code | What happened | Usual fix |
|------|---------------|-----------|
| `not_logged_in` | no login stored for this `server.address` | `andara-cli auth login` ([section 3](03-installing-andara-cli.md#log-in-to-dev)). Check that the address is spelled as when you logged in |
| `unauthenticated` | your login expired, or `dev` was reset | `andara-cli auth refresh`, or log in again. After a reset, see [section 2](02-getting-access.md#after-dev-is-reset) |
| `connect_failed` | `dev` didn't answer | are you on the tailnet? Is `server.address` `andara-dev.solo7.valesordev.com:443`? |
| `timeout` | `dev` didn't answer in time | try again. If it keeps happening, tell an Operator |

## Asking for a server change

If you need something the language or the server can't do yet (a Component type, a field, a
Direction, a rule), open an issue on this repository:

```
https://github.com/valesordev/andara.valesordev.com/issues/new
```

Say what you're trying to build and what stops you. This repository is public, so describe the
mechanic, not the lore: keep the World's story in the Content Repository, which is private.

Game design, lore and mechanics are Brian's call. An issue gets his decision before anyone builds
it.
