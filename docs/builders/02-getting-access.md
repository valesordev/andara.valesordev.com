# 2. Getting access

[← The Builder's Guide](README.md)

Ask an Operator for four things. Each has an Operator's side, given here as the commands the
Operator runs.

| You need | Why |
|----------|-----|
| An Account with the `builder` role | to publish, and to `goto` any Room |
| A pack grant | you can publish only to packs you hold |
| The Content Repository | where your sources live and get checked |
| A place on the tailnet | `dev` is reachable only from inside it |

## An Account

There's no self-registration. The Operator creates your Account, sets your first password, and
tells you your username and that password:

```
andara-cli account create --username <you> --role builder --role player
```

It asks `Password for <you>: ` and prints:

```
created account <account-id> (<you>)
```

The Operator keeps `<account-id>`, because the commands below take it.

Usernames are 3 to 32 characters of `a-z`, `0-9`, `_` and `-`. Passwords are at least 8
characters. You can't change your own password. Ask an Operator, who runs
`andara-cli account reset-password <account-id>`.

## The `builder` role, on an Account that exists

If you already have an Account without `builder`, the Operator adds it:

```
andara-cli account set-roles <account-id> --role builder --role player
```

```
roles of <account-id> set to builder,player (record version <n>)
```

This replaces your whole set of roles, so the Operator names every role you should keep.

## A pack

Pick a pack ID with the Operator: lowercase, like a Zone ID (`glade`, `brian`, `northmarch`). The
Operator grants it:

```
andara-cli account set-packs <account-id> --pack glade
```

```
<account-id>: builder packs glade
```

`set-packs` replaces your whole set of packs, so to hold two, the Operator names both
(`--pack glade --pack caves`). The pack doesn't need to exist yet: granting a new pack ID is how you
get a new pack. `andara.core` can't be granted.

If the line ends `(inactive: no builder role)`, you have the grant but not the role. Ask for
`set-roles`.

## The Content Repository

Ask for access to `valesordev/andara.solo7.media`. It's private. That's where your sources live, and
its pull-request check validates them before you publish ([section 4](04-your-first-zone.md)).

## The tailnet

`dev` is at `andara-dev.solo7.valesordev.com`, and that name resolves only inside Brian's tailnet.
Ask Brian to add you. Without it, you can still write, format and validate everything offline, but
you can't publish, play, or see `dev`.

## After `dev` is reset

An Operator sometimes resets `dev`'s World with `make world-reset`. Content survives a reset:
every pack's versions and Active Pointer stay. Accounts and Characters don't. After a reset:
1. the Operator re-creates your Account and grant with `account create` and `account set-packs`, as
   above (you get a new `<account-id>`);
2. you log in again ([section 3](03-installing-andara-cli.md#log-in-to-dev));
3. you create a Character again.

Your packs and their history are untouched.
