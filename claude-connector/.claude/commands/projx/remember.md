---
description: Save a fact to the ProjX knowledge store (well-formed, with a code anchor if relevant)
allowed-tools: Bash(projx-engine:*)
---
Commit the following to the ProjX project store with `projx-engine store commit`. Choose a
sensible `--kind` (`doc`, `adr`, `convention`, or `history`), a lowercase slash `--key` path
(e.g. `area/feature/subsystem`), and — if it points at code — include `{"anchor":"file.go:42"}`
in the `--body`. One fact = one commit. Do NOT write it to a markdown file; the store is the
source of truth.

Fact to remember: $ARGUMENTS
