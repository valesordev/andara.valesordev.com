# SPRINT-01 demo — M1 in the operator's hands

**Goal (from `SPRINT-01.md`):** on a fresh local stack, an Operator creates a Character with
`andara-cli`, enters the World, reads a Room, moves `north` through the log, and reads a
different Room. `make stack-play` asserts the same thing in CI, with a second player watching.
This is the **M1 gate**.

**Result:** passed, with one §9 defect at step 2 (`→ AW-INF-016`), run by PM on 2026-09-25 against a
fresh clone of `origin/main` at `343cdbe`.

## Preconditions

- Docker with Compose v2, Go, and Python 3 on the machine.
- A fresh clone of `origin/main`. Every command below runs from its root.
- No other `andara` compose project running. The project name is fixed, so a stack started from
  another clone is the same stack. Stop it from that clone with `make down VOLUMES=1`, or from this
  one, since it's the same project.
- Ports 8080, 8443, 9092, 8081, 5432, 6379, 3000, 9090, 3100 and 4317 free.

## Steps

Each step lists the command, what you should see, and what the 2026-09-25 run saw.

1. **`make bootstrap`**
   - **Expect:** a toolchain table ending `bootstrap: ok`.
   - **Run:** as expected, in 19 s.

2. **`make up`**
   - **Expect:** every service healthy, then a summary listing the endpoints, including
     `andara-server localhost:8443 (gRPC, TLS)` and the bootstrap operator `operator:andara-local`.
   - **Run: ❌ the wrong server.** The summary printed as expected, but the running
     `andara-andara-server` image was built at 2026-09-25T20:47Z. That's before #93–#95 landed
     AW-SRV-012 on `main`. `make up` never builds an image that already exists (#73), so on any
     machine that has run the stack before, this step runs an older server than the tree.

   **2a. `§9 defect → AW-INF-016`.** Rebuild the server from this tree, then bring it up again:
   ```
   docker compose -f deploy/compose/docker-compose.yaml --profile full --profile server build andara-server
   make up
   ```
   - **Expect:** a new image `Created` time, and `curl -sf http://127.0.0.1:8080/readyz` prints `ok`.
   - **Run:** image created 2026-09-25T19:58:55-07:00, `/readyz` returned `ok`. Once AW-INF-016
     lands, `make up` alone does this, and 2a goes away.

3. **`make build`**
   - **Expect:** `build: bin/andara-cli bin/andara-server bin/andara-projector`.
   - **Run:** as expected.

4. **`bin/andara-cli --config .local/cli.yaml auth login --username operator`**
   - Enter `andara-local` at the password prompt. `--config` points at the CLI config `make up`
     wrote. Every command below takes it too; it's omitted from them for readability.
   - **Expect:** `logged in to localhost:8443 as operator; session valid until <time>, stored in <path>`.
   - **Run:** as expected.

5. **`bin/andara-cli character create Aldric`**
   - **Expect:** `Aldric (1 of 5)`, exit 0.
   - **Run:** as expected.

6. **`bin/andara-cli character create Aldric`** again
   - **Expect:** `that name is taken`, exit 1. Names are reserved; nothing else is attempted.
   - **Run:** as expected.

7. **`bin/andara-cli character list`**
   - **Expect:** `Aldric  dormant  town/plaza`.
   - **Run:** as expected.

8. **`bin/andara-cli play --character Aldric`**, then type `north`, then `look`, then Ctrl-D
   - **Expect:**
     ```
     -- Connected to localhost:8443 as operator, playing Aldric (session <32 hex>, protocol 1).
     Aldric arrives.
     Market Plaza
     A dusty square of packed earth.
     Exits: east, north, south
     Aldric leaves north.
     Aldric arrives from the south.
     Town Hall
     Stone walls and faded banners.
     Exits: south
     ```
     and exit 0.
   - **Run:** exactly this.
   - A move doesn't describe the destination. The new Room appears on the `look` (SPRINT-02
     game-design question 1).
   - The player's own `arrives` can print before the first Room, because it shares the automatic
     `look`'s tick. That's recorded against AW-CLI-007 AC-4 in
     `docs/feedback/AW-CLI-007-character-commands.md` §2.

9. **`bin/andara-cli play --character Aldric --show-protocol`**, then type `look`, then Ctrl-D
   - **Expect:** the Command acknowledged by the log before its Event arrives:
     ```
       » Submit session_id=… client_ref=…-2 raw="look"
       « SubmitResponse session_id=… client_ref=…-2 partition=5 accepted_offset=<n>
       « RoomDescribed session_id=… event_id=<n> tick=<n> client_ref=…-2
     ```
   - **Run:** as expected (`partition=5 accepted_offset=11`).
   - Wait a second after step 8 before relaunching. A relaunch within about 300 ms of quitting a
     session that moved is refused `already_live` (exit 1). See
     `docs/feedback/AW-SRV-014-character-roster.md` §3.

10. **`make stack-play`**
    - **Expect:** it ends
      `stack-play: M1 gate — both halves, a bound Character walking in play's own transcript — passes`.
      On the way, it shows two players entering, one walking from the plaza to the hall while the
      other watches, JSON output that's only JSON, and a server restart the client recovers from.
    - **Run:** as expected, in 11 s. `TestLive_M1Gate` passed in 1.26 s.

11. **`make down VOLUMES=1`**
    - **Expect:** `down: stack stopped, volumes and <repo>/.local/data removed; …`.
    - **Run:** as expected.

## What this demo is not

- **Not M2:** a restart still replays the whole log, and a dropped connection ends the Session.
  There's no linkdead grace yet (SPRINT-02).
- **Not on the box:** it runs on the compose stack. `dev` in the cluster waits on Brian's box session
  (AW-INF-013, AW-INF-014, AW-INF-008).
