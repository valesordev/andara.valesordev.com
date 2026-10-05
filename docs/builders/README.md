# The Builder's Guide

From no access to your own Zone live on `dev`, and back.

This guide is for **Builders**: people who write the World in the Content Language and publish it.
You don't need this repository, Go, or any access to the code. You need `andara-cli`, the
**Content Repository** (`valesordev/andara.solo7.media`), and an Account on `dev`.

The guide explains and links. The rules themselves live in the
[Content Language specification](../specs/content-language/v1/README.md), and where the guide
names a rule, it links there instead of restating it.

## Sections

1. [What a Builder does here](01-what-a-builder-does.md): packs, versions, approval, and what
   content can express today.
2. [Getting access](02-getting-access.md): what to ask an Operator for, and the commands the
   Operator runs.
3. [Installing andara-cli](03-installing-andara-cli.md): the download, the checksum, and your first
   login to `dev`.
4. [Your first Zone](04-your-first-zone.md): a tutorial that takes you from an empty pack to a
   Zone you walk on `dev`, then change, then roll back.
5. [The everyday loop](05-the-everyday-loop.md): `history`, `diff`, `fetch`, rollback, and sharing a
   pack.
6. [The language, by example](06-the-language-by-example.md): a guided tour, with links into the
   specification.
7. [Reference](reference.md): Directions, Component types, `andara.core` Templates, and
   diagnostic codes, generated from `andara-cli` itself so it can't drift.
8. [Building on `dev`](08-building-on-dev.md): the town, Purgatory, and `goto`.
9. [When something fails](09-when-something-fails.md): exit codes, the errors you'll meet, and how
   to ask for a server change.

If you've never built here, read 1, then do 2, 3 and 4 in order. After that, 5 and 9 are the ones
you'll come back to.

## Words

The guide uses the project's [glossary](../glossary.md). The terms you'll meet most are Content
Pack, Content Version, Active Pointer, Approval, Zone, Room, Exit, Direction, Component and
Template. Each is defined there.
