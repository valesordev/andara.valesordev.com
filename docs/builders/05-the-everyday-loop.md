# 5. The everyday loop

[← The Builder's Guide](README.md)

Once your first Zone is live ([section 4](04-your-first-zone.md)), most days go: edit, `fmt`,
`validate`, pull request, `publish`, approve, `activate`. These commands sit around that loop.

## What's been published: `history`

```
andara-cli content history glade
```

```
  glade@3  published by <you> at <time>, not approved; never active
  glade@2  published by <you> at <time>, approved by <them> at <time>; active <from> to <to>
* glade@1  published by <you> at <time>, approved by <them> at <time>; active <from> to <to>, <from> to now
```

Newest first. `*` marks the version that's active now. `--limit N` shows fewer. Accounts appear by
their ID, except your own, which shows your username.

## What changed: `diff`

```
andara-cli content diff glade 1 2
```

```
~ room glade/stream: title "By the Stream" -> "The Stream"  (glade.aw:9)
+ room glade/pool  (glade.aw:14)
+ exit glade/stream west -> pool  (glade.aw:12)
```

`+` is added, `-` removed, `~` changed. Each line points into version 2's source, or into version
1's for a removal. Diff before you approve someone else's version: it's what you're signing.

## Get a version's source back: `fetch`

```
andara-cli content fetch glade 2
```

It writes version 2's `.aw` files into a directory with the name of the one it was published from,
and lists each file. `--out <dir>` writes somewhere else. The files compile back to exactly what
was published. Use it to recover work, or to see what a co-Builder published before their pull
request merged.

## Look inside without playing: `inspect`

```
andara-cli content inspect zone glade --path content/glade
andara-cli content inspect room glade/clearing --path content/glade
andara-cli content inspect template glade.Hermit --path content/glade
```

`inspect room` lists the Room's Exits, each marked `back:` with the Direction home, or
`one-way: no Exit back`. `inspect template` shows every Component field with the Template it came
from. With `--pack glade --version 2` in place of `--path`, it reads a published version instead.

## Roll back, or forward

`andara-cli content rollback glade` goes back to the version active before this one.
`--to N` names one. Any version that was approved can be activated again, with no new approval. A
version activated only with `--override` was never approved, so going back to it needs `--override`
again.

## Sharing a pack

Two Builders can hold the same pack. There's no merging: **the last pointer move wins.** Each
publish is built on the pack's newest version, and the World runs whichever version was activated
last. Work it out in the Content Repository's pull requests, as you would any shared code.

If someone publishes while you're uploading, your publish stops:

```
glade: someone published after version 2 while this was uploading (…); run `content history glade` and publish again
```

It exits 1 with `stale_parent`, and nothing of yours was written. Pull, look at `history` and
`diff`, and publish again.

`activate` always shows what it replaces, so you can see whose version you're moving the pointer
off.

## Asking for an approval

Publish, then tell your co-Builder the version: `glade@4`. They run `content diff glade 3 4`, play it
if they want to, and `content approve glade 4`. Either of you can then activate it.

## Scripting

Every command takes `--output json`, which prints one JSON value on stdout, with a `trace_id`. The
commands that change the live World (`activate` and `rollback`) ask first. With no terminal to ask
at, they need `--yes`.
