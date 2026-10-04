# 4. Your first Zone

[← The Builder's Guide](README.md)

This tutorial makes a pack `glade` with one Zone of two Rooms. You check it, publish it, approve
it, activate it, walk it on `dev`, change it, and roll it back. Use your own pack ID wherever this
says `glade`: the one an Operator granted you ([section 2](02-getting-access.md#a-pack)).

Before you start, you need:
- `andara-cli` installed and logged in to `dev` ([section 3](03-installing-andara-cli.md));
- access to the Content Repository;
- a pack grant, and the `builder` role.

Every step is an `andara-cli` command, a `git` command on the Content Repository, or a `make` target,
with what it prints. `andara-cli` means the one you installed in [section 3](03-installing-andara-cli.md),
on your `PATH`. Inside the Content Repository, `make tools` also puts the version the repository is
pinned to at `.tools/bin/andara-cli`, and `./.tools/bin/andara-cli` works for every step too.

## Get the Content Repository

```
git clone git@github.com:valesordev/andara.solo7.media.git
cd andara.solo7.media
make tools
make check
```

`make tools` installs the `andara-cli` release the repository is pinned to, into `.tools/bin/`, and
`make check` runs the same check the pull requests run. For the starter pack, `content/example`, its
`validate` line reads:

```
1 zones, 2 rooms, 0 templates, core andara.core@1
```

`content/example` is the shape to copy: a directory named for the pack, a `pack.aw`, and a file per
Zone.

## Sign your commits

The Content Repository's `main` accepts only signed commits, so set signing up once, before your
first commit. These steps use an SSH key: the one you clone with is fine.

1. On GitHub, under **Settings → SSH and GPG keys → New SSH key**, add your public key again with
   **Key type: Signing Key**. A key added for authentication doesn't sign.
2. In the clone, tell `git` who you are and how to sign. The email must be one GitHub has verified
   for your account, or GitHub shows the commit as unverified:

   ```
   git config user.name "<your name>"
   git config user.email "<a verified GitHub email>"
   git config gpg.format ssh
   git config user.signingkey ~/.ssh/id_ed25519.pub
   git config commit.gpgsign true
   ```

   Use your own public key's path.
3. To check signatures locally, tell `git` which keys to trust. Create a file listing yours, then:

   ```
   git config gpg.ssh.allowedSignersFile ~/.config/git/allowed_signers
   ```

   The file has one line per key: `<your email> <the contents of your .pub file>`.

Check a commit with `git log --show-signature -1`. It reads `Good "git" signature for <your email>`.

If your key has a passphrase, `git` asks for it on every commit. Add the key to your agent once
per session with `ssh-add`, naming the private key, not the `.pub`.

If you committed before setting this up, re-sign the branch's commits with
`git rebase --exec 'git commit --amend --no-edit -S' main`, then `git push --force-with-lease`.
If the commits also carry the wrong author email, use `--reset-author` in that command, so they take
the email you just set.

## Write the pack

Make a branch:

```
git switch -c glade-first-zone
```

Create `content/glade/pack.aw`. The directory name and the pack ID must match:

```
pack glade requires andara.core@1
```

The number after `@` is the `core:` line from `andara-cli version`.

Create `content/glade/glade.aw`:

```
zone glade "The Glade" {
  fallback clearing

  room clearing "A Clearing" {
    desc "Tall grass, and a ring of birches."
    exit north -> stream
  }

  room stream "By the Stream" {
    desc "Cold water runs over flat stones."
    exit south -> clearing
  }
}
```

That's a Zone `glade` with two Rooms, joined both ways: north from the clearing, and south back from
the stream. `fallback` names the Room anyone is moved to if a later version removes the Room they
stand in. Every Zone needs one. [Section 6](06-the-language-by-example.md) walks through each line.

**Your Zone IDs must be new on `dev`.** `town`, `docks`, `wilds` and `purgatory` belong to `dev`'s
town ([section 8](08-building-on-dev.md)), and so does any Zone another active pack holds. Checking
your pack alone can't see those. Publishing can, and refuses a clash with `duplicate_zone`.

## Format and validate

```
andara-cli content fmt --path content/glade
```

```
2 files, 0 rewritten
```

`fmt` puts files in the one canonical layout, and the pull-request check refuses files that aren't
in it. If it rewrote anything, look at what changed: that's the house style.

```
andara-cli content validate --path content/glade
```

```
1 zones, 2 rooms, 0 templates, core andara.core@1
```

`validate` needs no network. It checks your pack against the `andara.core` built into
`andara-cli`.

Here's what a mistake looks like. Misspell the north Exit's target as `streem`, and `validate` says:

```
content/glade/glade.aw:6:19: unknown_room Room "clearing" exits north to streem, and Zone "glade" has no Room "streem"
  glade
  clearing
  north
content/glade: 1 finding(s) refuse the pack
```

It exits 1. The first line is `file:line:column`, the code, and what's wrong. The indented lines are
the path to it: Zone `glade`, Room `clearing`, Exit `north`.
[Section 9](09-when-something-fails.md#the-errors-youll-meet) lists the codes you'll meet. Put the
`stream` back.

## Open a pull request

```
git add content/glade
git commit -m "glade: a first Zone"
git push -u origin glade-first-zone
```

Open the pull request from the link `git push` prints. The repository's `check` runs `fmt --check`
and `validate` on the packs you changed, and shows any finding on the line it's about. A pull request
that changes anything outside `content/` checks every pack. When it's
green, merge it, then bring your clone up to date:

```
git switch main
git pull
```

The merge publishes nothing. Publishing is the next step, and it's yours.

## Publish

```
andara-cli content publish --path content/glade
```

```
glade@1 published (first version), awaiting approval
```

Your sources go up with it, and the server checks them against everything else on `dev`. Nothing
in the World changes yet.

Publish prints what is in your pack: its findings and its warnings, placed on your source. It doesn't
print another pack's existing warnings: the town's one-way Exit out of Purgatory is the town's own and
shows when the town publishes. A Zone ID clash is one `duplicate_zone` on your Zone, naming both
packs. If publish ever refuses on a finding printed under another pack's name (`town/…`), it isn't one
of your files and isn't yours to fix: tell an Operator.

## Approve it

A second Builder who holds `glade`, or an Operator, approves the version:

```
andara-cli content approve glade 1
```

```
glade@1 approved by <them>
```

**While Brian is `dev`'s only Builder**, he approves his own versions as an Operator. That's
temporary, and it's how `dev` works for one person, not the process for a team. The command asks
first:

```
You published glade@1. Approve it yourself as <you>? [y/N] y
glade@1 approved by <you> (self-approval: you published it)
```

If you're a Builder without the Operator role, you can't approve your own version. The server
refuses it with `self_approval`.

**Two Accounts, one person.** If your Operator and Builder are separate Accounts (publish as one,
approve as the other), note that `andara-cli` keeps one login per server address. `auth login
--username <other>` switches, and replaces the stored login. To keep both, give the second its own
credentials file and name it on each command:

```
andara-cli --credentials ~/.config/andara/operator.yaml auth login --username <operator>
andara-cli --credentials ~/.config/andara/operator.yaml content approve glade 1
```

Without `--credentials`, commands use your usual login.

## Activate it

```
andara-cli content activate glade 1
```

It shows what will change and asks before it does:

```
activate glade@1, replacing nothing active
  glade@1 published by <you> at <time>, approved by <them> at <time>
Proceed? [y/N] y
glade@1 active (nothing was active)
```

The World loads it within a few seconds. Check what's in effect:

```
andara-cli server info
```

```
version <v>
commit <sha>
environment dev
protocol 1-1
content andara.core@1
content glade@1
content town@<n>
content_digest <hex>
```

## Walk it

Make a Character, once:

```
andara-cli character create Wren
```

```
Wren (1 of 5)
```

Then play:

```
andara-cli play --character Wren
```

```
-- Connected to andara-dev.solo7.valesordev.com:443 as <you>, playing Wren (session …, protocol 1).
```

New Characters start in Purgatory. Nothing links to your Zone yet, and Exits can't cross packs, so
jump there with `goto`, using `/` between the Zone and the Room:

```
> goto glade/clearing
```

You arrive in **A Clearing** and see its description. `north` takes you to the stream, and `south`
brings you back. `/quit` ends the session.

If `goto` says `there is no room glade/clearing` right after you activated, the World hasn't loaded
the version yet. Try again in a few seconds.

## Change it

Edit the stream's description in `content/glade/glade.aw`, then repeat the loop:

```
git switch -c glade-stream
andara-cli content fmt --path content/glade
andara-cli content validate --path content/glade
git commit -am "glade: the stream"
git push -u origin glade-stream
```

After the pull request merges, and you've run `git switch main` and `git pull`:

```
andara-cli content publish --path content/glade
```

```
glade@2 published (parent 1), awaiting approval
```

Approve and activate `glade 2` as before. Activation now reads
`glade@2 active (was glade@1)`. See what changed between the two:

```
andara-cli content diff glade 1 2
```

Each line is `+` added, `-` removed, or `~` changed, with the file and line in version 2's source.

## Roll it back

```
andara-cli content rollback glade
```

It asks, as `activate` does, then:

```
glade@1 active (rolled back from glade@2)
```

Version 1 was approved, so rolling back needs no new approval. `goto glade/stream` shows the old
description. Version 2 still exists, and `andara-cli content activate glade 2` brings it back.

That's the whole loop. [Section 5](05-the-everyday-loop.md) covers the commands around it.
