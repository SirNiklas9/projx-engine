package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

// TestPrepareAgentContext proves "the rest of the engine work" reaches the agent
// even uncaged: the seeded contract is compiled into a context file, the env
// carries it, and it's threaded into the agent invocation.
func TestPrepareAgentContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(st, root, nil); err != nil { // floor: conventions + gates
		t.Fatal(err)
	}
	st.Close()

	ctxFile, env := prepareAgentContext(root, "") // no task → full preamble
	if ctxFile == "" {
		t.Fatal("no context file produced")
	}
	if _, err := os.Stat(ctxFile); err != nil {
		t.Fatalf("context file missing on disk: %v", err)
	}
	if !strings.Contains(env["PROJX_STORE_CONTEXT"], "dispatch don't mutate") {
		t.Error("seeded project law not present in compiled context")
	}
	if env["PROJX_AGENT_CONTEXT"] != "1" {
		t.Error("PROJX_AGENT_CONTEXT flag not set")
	}
	if env["PROJX_ROLE"] != "worker" {
		t.Error("ProjX-launched agent is not marked as an independently governed worker")
	}
	for _, required := range []string{
		"YOUR CONTRACT",
		"READ BEFORE ACTING",
		"KNOWLEDGE OUT = store.commit",
		"OFF-LIMITS IS LAW",
	} {
		if !strings.Contains(env["PROJX_STORE_CONTEXT"], required) {
			t.Errorf("worker lifecycle context missing required policy %q", required)
		}
	}

	// The context file is threaded into the agent's invocation (uncaged path too).
	name, argv := resolveAgentArgv(root, "do x", renderOpts{SystemPromptFile: ctxFile})
	if joined := name + " " + strings.Join(argv, " "); !strings.Contains(joined, "--append-system-prompt-file "+ctxFile) {
		t.Errorf("context file not threaded into agent argv: %s", joined)
	}
}

func TestAgentTemplateRender(t *testing.T) {
	cl := builtinAgents["claude"]

	// Full: model + steering file present.
	name, args := cl.render("do x", renderOpts{Model: "claude-opus-4-8", SystemPromptFile: "/ctx.md"})
	got := name + " " + strings.Join(args, " ")
	for _, want := range []string{"claude", "-p", "do x", "--model claude-opus-4-8", "--append-system-prompt-file /ctx.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q in %q", want, got)
		}
	}

	// No model → --model and its value are both dropped; file stays.
	_, args = cl.render("t", renderOpts{SystemPromptFile: "/ctx.md"})
	j := strings.Join(args, " ")
	if strings.Contains(j, "--model") {
		t.Errorf("empty model should drop the flag: %s", j)
	}
	if !strings.Contains(j, "--append-system-prompt-file /ctx.md") {
		t.Errorf("steering file should remain: %s", j)
	}

	// No optionals → bare "-p <task>".
	name, args = cl.render("hi", renderOpts{})
	if name != "claude" || strings.Join(args, " ") != "-p hi" {
		t.Errorf("bare render wrong: %s %v", name, args)
	}
}

func TestResolveAgentFromFileAndOverride(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".projx", "agents.json"),
		[]byte(`[{"name":"codex","argv":["codex","exec","{{task}}","--model","{{model}}"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	// A non-claude agent works purely from the declared template (no engine code).
	t.Setenv("PROJX_AGENT_CMD", "")
	t.Setenv("PROJX_AGENT", "codex")
	t.Setenv("PROJX_AGENT_MODEL", "gpt-x")
	name, args := resolveAgentArgv(root, "fix bug", renderOpts{Model: "gpt-x"})
	got := name + " " + strings.Join(args, " ")
	if !strings.Contains(got, "codex exec fix bug") || !strings.Contains(got, "--model gpt-x") {
		t.Errorf("codex template not resolved: %s", got)
	}

	// An explicit PROJX_AGENT_CMD (routing) overrides the template path.
	t.Setenv("PROJX_AGENT_CMD", "myagent --flag")
	name, args = resolveAgentArgv(root, "task", renderOpts{})
	if name != "myagent" || strings.Join(args, " ") != "--flag task" {
		t.Errorf("PROJX_AGENT_CMD override wrong: %s %v", name, args)
	}
}
