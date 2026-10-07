# Examples

**Prompt:** "/role arch"
**Behavior:** The agent runs `.claude/bin/role set arch <session>`, and the alias resolves to `architecture`. It treats the printed charter as binding and runs the repo's session-start steps. It replies with the role, the branch prefix `arch/`, its skills, and what the active sprint has for architecture next, such as the three `draft` stories waiting on contract review. Then it waits.

**Prompt:** "/role impl pick up AW-SRV-041"
**Behavior:** The agent runs `.claude/bin/role set impl <session>` with only `impl`, never the task words. It gives the short role block, then starts the task: `story show AW-SRV-041`, `story start AW-SRV-041`, and a branch `impl/aw-srv-041-…`.

**Prompt:** "/role pm fix the projector race"
**Behavior:** The role is set to pm. The task is implementation work in `internal/`, which pm doesn't own. The agent says so and suggests `/role impl fix the projector race` or a comment to implementation. It doesn't start.

**Prompt:** "/role"
**Behavior:** The agent shows the current role (`unset` on a fresh session) and the roles this repo defines, then stops.

**Non-trigger:** The agent decides on its own, mid-task, that it should be architecture → it never invokes `/role`. Only Brian does.
