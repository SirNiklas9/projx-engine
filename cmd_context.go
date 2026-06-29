package main

import (
	"fmt"
)

// runContextCmd implements `projx-engine [--root <dir>] context`.
//
// It compiles the project store into the SAME ambient preamble that is injected
// at agent launch (compileStorePreamble) and prints it to stdout. This is the
// stdout-callable form of the launch context: the surface a per-harness connector
// (e.g. a Claude Code SessionStart hook) calls to inject project knowledge.
//
// Read-only with respect to the store (List only; compileStorePreamble never
// mutates) and it does NOT launch an agent or write the agent-context.md file —
// unlike `agent run`, which writes that file as a side effect. Exit 0 on success.
func runContextCmd(absRoot string, _ []string) {
	st := openStore(absRoot)
	defer st.Close()
	fmt.Print(compileStorePreamble(st))
}
