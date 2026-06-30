package main

import (
	"fmt"
	"strings"
)

// runContextCmd implements `projx-engine [--root <dir>] context [--task "<prompt>"]`.
//
// It compiles the project store into the ambient preamble and prints it to stdout —
// the stdout-callable form of the launch context a per-harness connector injects.
// WITHOUT --task (e.g. a SessionStart hook) it prints the FULL floor. WITH --task
// (e.g. a UserPromptSubmit hook) it prints the TASK-SLICED contract: law in full +
// only the reference records relevant to the prompt ("query, don't dump").
//
// Read-only with respect to the store (List only) and it does NOT launch an agent
// or write agent-context.md — unlike `agent run`. Exit 0 on success.
func runContextCmd(absRoot string, args []string) {
	task := parseTaskFlag(args)
	st := openStore(absRoot)
	defer st.Close()
	if task != "" {
		fmt.Print(compileStorePreambleForTask(st, task))
		return
	}
	fmt.Print(compileStorePreamble(st))
}

// parseTaskFlag extracts `--task <value>` or `--task=<value>` from args; "" if absent.
func parseTaskFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--task" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(args[i], "--task="); ok {
			return v
		}
	}
	return ""
}
