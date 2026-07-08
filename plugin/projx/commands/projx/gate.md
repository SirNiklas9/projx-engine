---
description: List the ProjX off-limits gate rules, or check a path against them
allowed-tools: Bash(projx-engine:*)
---
ProjX gate rules (paths the agent must not read/edit/run against):

!`projx-engine --root . gate list`

If I named a path in "$ARGUMENTS", also run `projx-engine --root . gate check "<that path>"` and tell me whether it is allowed or off-limits. To ADD a rule, run `projx-engine --root . gate add "<glob>"`.
