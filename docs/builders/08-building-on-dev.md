# 8. Building on `dev`

[← The Builder's Guide](README.md)

`dev` is where you test. It's one server, reachable only from inside the tailnet, and it changes when
anyone activates a version.

## The town, and the Zone IDs it holds

`dev` always has pack `town`. Its Zones, `town`, `docks`, `wilds` and `purgatory`, are a small test
world: a market plaza, a town hall, a pier, a warehouse, a trail and a copse. It's the
[Fixture Pack](../glossary.md#content-pipeline), its source is `content/fixtures/town/` in this
repository, and it's there so `dev` is playable before anyone has built anything.

Zone IDs are shared by every pack in the World. So your Zones can't be named `town`, `docks`,
`wilds` or `purgatory`, or anything another active pack uses. `validate` checks one pack alone and
can't see a clash. `publish` does, and refuses it with `duplicate_zone`.

`content history town` shows who activated `town` last.

## Purgatory

Every new Character starts in **Purgatory**: one Room, `purgatory/start`, with one Exit, `out`, into
the town's plaza. `out` is one-way, so you can't walk back.

Purgatory's description is a placeholder for now.

## Reaching your Zone: `goto`

Exits can't cross packs, so nothing in the town can lead into your Zone, and nothing in your Zone can
lead out to the town. To reach it, a Builder jumps:

```
> goto glade/clearing
```

- `goto <zone>/<room>` jumps to any Room in the World, yours or anyone's.
- `goto <room>` jumps to a Room in the Zone you're in.
- The separator is `/`, not the `.` content uses (`exit south -> docks.pier`).
- IDs are lowercase, as written in the source.
- There's no abbreviation for `goto`.

It needs the `builder` role on the Account you're playing. Without it, `goto` says
`you may not goto`, and that includes Operators who aren't also Builders.

| You see | It means |
|---------|----------|
| `there is no room glade/clearing` | there's no such Room in the World. Check the IDs. If you've just activated, the World hasn't loaded the version yet, so try again in a few seconds |
| `usage: goto <zone>/<room>` | the target is missing or malformed |
| `you may not goto` | your Account doesn't have `builder` |

Your Zone appears on `dev` the moment your version is active. It disappears only if you activate a
version without it, and removing a Zone that's in effect is refused (`zone_removed`). Plan your Zone
IDs before you publish them.

## What everyone sees

`dev` is shared. Activating changes the World for everyone on it. Players in a Room your new version
removes are moved to your Zone's `fallback`. Use `history` before you activate, so you know whose
version you're replacing, and roll back when you've finished showing something off.
