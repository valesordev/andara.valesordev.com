# Game theory for Andara: a working primer

Each concept below is something to use *on a specific mechanic*, not to recite. Use it only when it changes a decision. Explain it in terms of the mechanic at hand.

| Concept | What it is | Ask of a mechanic | Andara-flavored example |
|---|---|---|---|
| **Dominant strategy** | A choice that is best whatever others do | Is there an option no one would ever skip? Then the others are dead content | "Always rest after a fight" beats every pacing decision if rest is free |
| **Nash equilibrium** | A set of choices where no one gains by changing alone | Where does play settle if everyone is rational? Do you like that place? | Two factions each guard a rift toll; both stay hostile because defecting loses ground |
| **Incentive compatibility** | The rewarded behavior is the intended behavior | Does the rule pay players for what you *meant*? | A bounty that pays per kill pays for farming, not for clearing the road |
| **Risk/reward curve** | How payoff scales with risk taken | Does extra risk buy proportionally more? Is the tail survivable? | Strain: each extra cast risks more; flat risk makes the cap meaningless |
| **Sinks and faucets** | Where a resource enters and leaves the world | Does inflow exceed outflow forever? | Coin from creatures with no repair or travel costs inflates |
| **Positive feedback loop** | Success makes more success (snowball) | Can a lead become unbeatable? | Reputation unlocks better jobs that raise reputation faster |
| **Negative feedback loop** | Success dampens itself (catch-up) | What keeps the leader honest and the laggard alive? | Strain: more power draws more risk |
| **Information asymmetry** | One side knows what the other can't | Who knows what, and does hiding it make play or just frustration? | Hidden enemy strain makes bluffing meaningful; hidden loot tables don't |
| **Tragedy of the commons** | Shared resource overused because each gains alone | Does the shared thing (a room, a spawn, a market) get exhausted? | A single harvest node in the commons |
| **Opportunity cost** | What you give up by choosing | Is every meaningful choice a real trade? | Carry weight forces gear trade-offs |
| **Pareto dominance** | Option A is at least as good as B in every way | Are two items really different? | A sword strictly better and no heavier than another |
| **Coordination vs. conflict games** | Aligned vs. opposed interests | Do players want to cooperate, and what stops betrayal? | Parties splitting loot |
| **Variance and skill** | Randomness narrows skill gaps | How much should luck matter at this layer? | Hit rolls vs. positioning |

## Habits to build
1. **Find the degenerate strategy first.** For any rule, ask what a player who only wants the number to go up does, forever.
2. **Predict, then check.** Before analysing, guess how play will settle. Compare.
3. **One lever, one purpose.** If a number serves two purposes, you cannot tune it.
4. **Make costs visible or deliberately hidden.** Hidden is a design choice; accidental is a bug.
5. **Prefer tunables over constants.** A number you can't change after play-testing is a commitment; list it.
6. **Check multi-actor cases.** Single-player analysis misses most of the interesting breakage.
