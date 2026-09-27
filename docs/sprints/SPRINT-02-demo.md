# SPRINT-02 demo — a dropped connection is survivable

**Goal (from `SPRINT-02.md`):** two players stand in `town/plaza` on a fresh local stack. The first
one's connection drops without a quit. The second reads `<name> goes linkdead.`, and `look` shows
`<name> (linkdead)` under `Here:`. The first player runs `andara-cli play` again within the 180 s
grace and is back in the same Room, with a body that never left. The bystander reads
`<name> reconnects.`, and a clean quit then reads `<name> leaves the world.` This is the **linkdead
slice of the M2 gate**.

**Result:** passed, with no §9 defects. Run by PM on 2026-09-27 against a fresh clone of
`origin/main` at `478d547`.

## Preconditions

- Docker with Compose v2, Go, and Python 3 on the machine.
- A fresh clone of `origin/main`. Every command below runs from its root.
- No other `andara` compose project you want to keep. The project name is fixed, so a stack started
  from another clone is the same stack, and step 2 wipes it.
- Ports 8080, 8443, 9092, 8081, 5432, 6379, 3000, 9090, 3100 and 4317 free.

## Steps

Each step lists the command, what you should see, and what the 2026-09-27 run saw.

1. **`make bootstrap`**
   - **Expect:** a toolchain table ending `bootstrap: ok`.
   - **Run:** as expected, in 22 s.

2. **`make down VOLUMES=1`, then `make up`**
   - **Expect:** `down: stack stopped, volumes … removed`, then `up: the stack is ready` with the
     endpoint list. `andara-server revision` is this clone's `HEAD`.
   - **Run:** as expected. The revision was `478d54741c19…`, so `make up` built the server from this
     tree. SPRINT-01's step 2a (`§9 defect → AW-INF-016`) is gone.

3. **`make build`**
   - **Expect:** `build: bin/andara-cli bin/andara-server bin/andara-projector`.
   - **Run:** as expected.

4. **`make stack-linkdead`**
   - **What it does:** logs in as the operator, puts a watcher and a second player in
     `town/plaza`, sends `SIGKILL` to the second player's `play` (a drop, not a quit), has it
     `play` again, then quits it cleanly. It then reads the server's `/metrics`.
   - **Expect:** the watcher's transcript, then the final line:
     ```
     <dropped> arrives.
     <dropped> goes linkdead.
     Market Plaza
     A dusty square of packed earth.
     Exits: east, north, south
     Here: <dropped> (linkdead)
     <dropped> reconnects.
     <dropped> leaves the world.
     stack-linkdead: linkdead gate — dropped, marked, reconnected, quit — passes
     ```
     Also `stack-linkdead: <dropped> reconnected into the plaza; the body never left`.
   - **Run:** as expected, in 10 s, with `Droppedemmjrhcp` watched by `Watcherfhqrdodt`.

5. **`make stack-play`** (the M1 gate, as a regression check)
   - **Expect:** `stack-play: M1 gate — both halves, a bound Character walking in play's own
     transcript — passes`.
   - **Run:** as expected, in 12 s.

## What this demo doesn't show

- **Grace expiry and the combat extension.** The despawn at 180 s is asserted by `AW-SRV-015`'s
  integration tests on Ticks, not live.
- **The server-kill half of M2.** `kill -9` of the server and recovery within 120 s is SPRINT-04's
  slice.
