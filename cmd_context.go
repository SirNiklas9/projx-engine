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
	// SESSION-AWARE delta path (step 5): when a --session id is given, the engine
	// keeps a per-session checkpoint and sends only the delta each turn. Without
	// --session it stays stateless (full floor or task slice), as before.
	if session, ok := parseStrFlagOK(args, "--session"); ok {
		runSessionContext(absRoot, session, task, parseBoolFlag(args, "--reset"))
		return
	}
	st := openStore(absRoot)
	defer st.Close()
	if task != "" {
		fmt.Print(compileStorePreambleForTask(st, task))
		return
	}
	fmt.Print(compileStorePreamble(st))
}

// parseStrFlagOK extracts `--flag <value>` or `--flag=<value>`; ok=false if absent.
func parseStrFlagOK(args []string, flag string) (string, bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if v, found := strings.CutPrefix(args[i], flag+"="); found {
			return v, true
		}
	}
	return "", false
}

// parseStrFlag is parseStrFlagOK discarding the presence bool ("" if absent).
func parseStrFlag(args []string, flag string) string {
	v, _ := parseStrFlagOK(args, flag)
	return v
}

// parseBoolFlag reports whether a bare `--flag` is present in args.
func parseBoolFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
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
