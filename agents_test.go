package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

func TestWorkerContextForbidsRecursiveDispatch(t *testing.T) {
	got := applyWorkerRole("project context", "default worker")
	for _, want := range []string{"PROJX_ROLE=worker", "Implement and verify THIS assigned task directly", "Do not call `route`", "default worker"} {
		if !strings.Contains(got, want) {
			t.Fatalf("worker context missing %q:\n%s", want, got)
		}
	}
}

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

func TestBuiltinCodexTemplateRender(t *testing.T) {
	adapter, ok := builtinProviderAdapter("codex")
	if !ok {
		t.Fatal("Codex adapter is not registered")
	}
	name, args := adapter.Build(ProviderInvocation{
		Task: "fix the dispatcher", Model: "gpt-5.6-sol", NativeEffort: "ultra", SystemPromptFile: "/ctx.md",
	})
	got := name + " " + strings.Join(args, " ")
	for _, want := range []string{"codex", "exec", "--model gpt-5.6-sol", `--config model_reasoning_effort="ultra"`, "sandbox_workspace_write.network_access=true", "features.network_proxy.enabled=true", `features.network_proxy.domains={ "api.openai.com" = "allow" }`, "--sandbox workspace-write", "Before acting, read and follow the ProjX execution context", "/ctx.md", "fix the dispatcher"} {
		if !strings.Contains(got, want) {
			t.Errorf("Codex invocation missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "--profile") {
		t.Errorf("routing profile leaked into Codex config-file profile flag: %s", got)
	}
}

func TestBuiltinClaudeAdapterRendersEffortAndEphemeralOutput(t *testing.T) {
	adapter, ok := builtinProviderAdapter("claude")
	if !ok {
		t.Fatal("Claude adapter is not registered")
	}
	if !adapter.Capabilities().ReasoningEffort {
		t.Fatal("Claude adapter does not declare reasoning effort")
	}
	name, args := adapter.Build(ProviderInvocation{
		Task: "fix the dispatcher", Model: "sonnet", NativeEffort: "xhigh", SystemPromptFile: "/ctx.md",
	})
	got := name + " " + strings.Join(args, " ")
	for _, want := range []string{"claude", "-p", "--no-session-persistence", "--model sonnet", "--effort xhigh", "--append-system-prompt-file /ctx.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("Claude invocation missing %q in %q", want, got)
		}
	}
}

func TestProviderNetworkHostsAreProviderSpecific(t *testing.T) {
	if got := providerNetworkHosts("codex"); len(got) != 1 || got[0] != "api.openai.com" {
		t.Fatalf("codex network hosts = %q", got)
	}
	if got := providerNetworkHosts("claude"); len(got) != 1 || got[0] != "api.anthropic.com" {
		t.Fatalf("claude network hosts = %q", got)
	}
	if got := providerNetworkHosts("unknown"); len(got) != 0 {
		t.Fatalf("unknown network hosts = %q", got)
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

func TestPrepareAgentContextInjectsPolicyBlock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".projx"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(root, ".projx", "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(st, root, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	t.Setenv("PROJX_POLICY_CLASS", "deep-reasoning")
	t.Setenv("PROJX_POLICY_PROVIDER", "codex")
	t.Setenv("PROJX_POLICY_PROFILE", "deep-reasoning")
	t.Setenv("PROJX_POLICY_MODEL", "gpt-5-codex")
	t.Setenv("PROJX_POLICY_FALLBACK", "explicit-only")
	_, env := prepareAgentContext(root, "redesign auth")
	ctx := env["PROJX_STORE_CONTEXT"]
	for _, want := range []string{"## Active execution policy", `"task_class": "deep-reasoning"`, `"preferred_provider": "codex"`, `"preferred_model": "gpt-5-codex"`, `"fallback": "explicit-only"`} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("policy block missing %q in context:\n%s", want, ctx)
		}
	}
}
