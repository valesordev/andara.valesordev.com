---
name: balance-model
description: Sanity-check an Andara's World mechanic's numbers by simulation or a small model: find dominant strategies, economy inflation, snowballs, and degenerate loops, and report which tunables the result is sensitive to. Trigger on "is 5 strain per cast too high", "simulate the coin economy", "does reputation snowball", "model this combat round", "which tunables matter". Works in either repo; models are scratch, not source of truth. Not for exploring design options (mechanic-brief), writing the spec (mechanic-spec), or tuning from live play data (sre/pm).
---

# Balance model

A model answers "does this behave the way the intent says?", not "is the number right". Starting values stay `[PROPOSED]` until played.

## Steps
1. **Take the rules and tunables** from the spec or brief. State the design intent being tested as a falsifiable claim ("a character that rests every third fight never reaches the cap").
2. **Pick the smallest model** that can fail that claim: a table for one-step math; a Python script for loops, many actors, or randomness. Put it in the scratchpad (or attach it to the brief); never commit it as a rule source.
3. **Run strategies, not averages.** Include at least: the greedy strategy (maximize the number), the cautious one, and the one an exploit-minded player would find. For multi-actor rules, run pairs.
4. **Sweep the tunables** one at a time across their ranges; report which change the outcome and which don't.
5. **Report:** the claim, the result, the strategy that breaks it (if any), sensitivity, and the proposed adjustment as a `[PROPOSED]` change to a tunable or rule. Say what the model leaves out.
6. **Teach:** name the concept the result illustrates (a snowball is a positive feedback loop; a free faucet is inflation) and what it changes about the decision.

## Output
Chat summary with the table of strategies and outcomes, plus the model file path. One next action: usually a decision for Brian or a tunable to record in the spec.
