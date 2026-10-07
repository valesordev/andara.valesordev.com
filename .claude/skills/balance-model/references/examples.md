# Examples

## Triggers
**Prompt:** "Strain adds 2 per cast, decays 1 per minute, cap 10. Can someone chain-cast forever?"
**Behavior:** Writes a small script, runs greedy, cautious, and burst-then-rest strategies over 1000 simulated fights, finds the burst-and-rest loop wins every time, shows sensitivity to decay rate (decay ≥ 3 removes the cap's meaning), proposes a `[PROPOSED]` change and names the concept (*dominant strategy*).

## Non-triggers
- "What should strain even be?" → mechanic-brief.
- "Write requirements for strain" → mechanic-spec.
