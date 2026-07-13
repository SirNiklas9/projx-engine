---
description: Pin which running ProjX agent the status line renders in full (fat) — or clear the pin
allowed-tools: Bash(projx-engine:*)
---
Set the ProjX status-line focus (which background agent renders with full detail):

!`projx-engine --root . focus "$ARGUMENTS"`

The status line shows one line per running background agent across all projects. By
default the agent in the current ProjX scope renders fat (task, role, elapsed, branch);
the rest render lean. `/projx:focus <selector>` overrides that to pin a specific agent —
match by dispatch id, project name, or role. Run `/projx:focus --clear` to return to the
default (fat follows the current scope), or `/projx:focus` with no argument to see the pin.
