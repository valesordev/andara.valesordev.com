# AW-SRV-049: how the roster learns of a rejected teardown, and a bound that #114 refused

Raised by PM, 2026-10-05, writing the roster and Gateway story architecture requested on `AW-SRV-028`
(`docs/feedback/AW-SRV-028-handoff-contract.md`, "For Brian, PM and SRE"). Each heading names the role that
should answer.

## For architecture

1. **Where does the roster observe a rejected teardown?** A teardown rejected `in_transit` is rejected
   post-log, after the roster's produce returned nil. The post-log `CommandRejected{in_transit}` Event, keyed
   by the Character, may be enough for the roster to retry (scope (c)). If the roster needs a synchronous
   answer from the Log instead, `AW-SRV-014`'s produce seam changes and the story is M with a seam change.
   The story stays `draft` until you say which.
1b. **A wall-clock bound against #114.** Scope (b), taken from your request, frees the linkdead hold unless
   `LinkdeadEntered` is observed within `ingress.produce_deadline`. `holdLinkdead` in `server/roster/roster.go`
   says the opposite: "No wall-clock bound: the sim's deadline starts when the mark applies and runs in Ticks,
   and a timer here could free the Account while the body is still in the World (review of #114)." A hold
   freed on a timer lets the Account select a second Character, and the first Character's mark, retried
   after the crossing (AC-4), can then land: two live Characters for one Account, against `AW-SRV-014`'s
   `already_live` rule. The story adds AC-8 for that case and leaves both answers open: the roster re-holds
   when a late `LinkdeadEntered` arrives, or the second selection is refused. Please rule, or amend the
   request.
2. **A new reason.** `SelectCharacter` past `ingress.transit_hold` fails `UNAVAILABLE`, ErrorInfo
   `{domain: andara.character, reason: in_transit}`. `AW-SRV-014`'s table has no `in_transit` (it exists
   only on the ingress side). Confirm the reason, or name another.
