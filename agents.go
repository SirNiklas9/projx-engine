package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProviderCapabilities makes the provider contract inspectable.  A route is
// provider/model/effort data; adapters, not routing configuration strings,
// decide how that data becomes a provider-specific invocation.
type ProviderCapabilities struct {
	NonInteractive   bool
	ModelSelection   bool
	ReasoningEffort  bool
	SystemPromptFile bool
	WorkspaceSandbox bool
	NetworkHosts     []string
}

// ProviderInvocation is the provider-neutral launch request. Profile remains
// routing metadata: it is deliberately not passed to Codex's --profile flag,
// which selects a local Codex configuration file and means something different.
type ProviderInvocation struct {
	Task             string
	Model            string
	NativeEffort     string
	SystemPromptFile string
	Sandbox          string
}

type providerAdapter interface {
	Name() string
	Capabilities() ProviderCapabilities
	Build(ProviderInvocation) (string, []string)
}

type claudeProviderAdapter struct{}

func (claudeProviderAdapter) Name() string { return "claude" }
func (claudeProviderAdapter) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{NonInteractive: true, ModelSelection: true, ReasoningEffort: true, SystemPromptFile: true, NetworkHosts: []string{"api.anthropic.com"}}
}
func (claudeProviderAdapter) Build(in ProviderInvocation) (string, []string) {
	args := []string{"-p", "--no-session-persistence", in.Task}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.NativeEffort != "" {
		args = append(args, "--effort", in.NativeEffort)
	}
	if in.SystemPromptFile != "" {
		args = append(args, "--append-system-prompt-file", in.SystemPromptFile)
	}
	return "claude", args
}

type codexProviderAdapter struct{}

func (codexProviderAdapter) Name() string { return "codex" }
func (codexProviderAdapter) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{NonInteractive: true, ModelSelection: true, ReasoningEffort: true, WorkspaceSandbox: true, NetworkHosts: []string{"api.openai.com"}}
}
func (codexProviderAdapter) Build(in ProviderInvocation) (string, []string) {
	sandbox := in.Sandbox
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	// Codex has no CLI system-prompt-file flag. Deliver the durable context as
	// an explicit preamble pointing at the project-scoped file it can read.
	task := in.Task
	if in.SystemPromptFile != "" {
		task = fmt.Sprintf("Before acting, read and follow the ProjX execution context at %q.\n\nTask:\n%s", in.SystemPromptFile, task)
	}
	// Codex's native sandbox denies egress by default. Enable only the API
	// endpoint required for the inference stream; this is enforced by Codex's
	// network proxy, rather than relying on ProjX's cooperative host metadata.
	// These are global Codex options and must precede the exec subcommand.
	args := []string{
		"--config", "sandbox_workspace_write.network_access=true",
		"--config", "features.network_proxy.enabled=true",
		"--config", `features.network_proxy.domains={ "api.openai.com" = "allow" }`,
		"exec",
	}
	if in.Model != "" {
		args = append(args, "--model", in.Model)
	}
	if in.NativeEffort != "" {
		args = append(args, "--config", fmt.Sprintf("model_reasoning_effort=%q", in.NativeEffort))
	}
	args = append(args, "--sandbox", sandbox, task)
	return "codex", args
}

// providerNetworkHosts returns only the endpoint required to run the selected
// provider. Optional plugin and catalog traffic stays blocked.
func providerNetworkHosts(name string) []string {
	adapter, ok := builtinProviderAdapter(name)
	if !ok {
		return nil
	}
	return append([]string(nil), adapter.Capabilities().NetworkHosts...)
}

func builtinProviderAdapter(name string) (providerAdapter, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude":
		return claudeProviderAdapter{}, true
	case "codex":
		return codexProviderAdapter{}, true
	default:
		return nil, false
	}
}

// AgentTemplate declares how to invoke an agent non-interactively. Argv is a
// template; placeholders are substituted at launch:
//
//	{{task}}             -> the task / prompt (required)
//	{{model}}            -> tier-resolved model id (the arg is DROPPED if empty)
//	{{profile}}          -> provider profile (the arg is DROPPED if empty)
//	{{systemPromptFile}} -> steering/context file path (DROPPED if empty)
//
// An arg that resolves to an empty optional is dropped together with an
// immediately-preceding flag arg (one starting with "-"). This is the WHOLE
// per-agent surface: a new agent (Codex, a GPT CLI, …) is a new template — data,
// not engine code — which is what keeps the orchestration agent-agnostic.
type AgentTemplate struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

// builtinAgents ships only the reference agent. Others are declared by the user
// in .projx/agents.json (seeded or hand-written).
var builtinAgents = map[string]AgentTemplate{
	"claude": {Name: "claude", Argv: []string{
		"claude", "-p", "{{task}}",
		"--model", "{{model}}",
		"--append-system-prompt-file", "{{systemPromptFile}}",
	}},
	"codex": {Name: "codex", Argv: []string{
		"codex", "exec", "--model", "{{model}}",
		"--sandbox", "workspace-write", "{{task}}",
	}},
}

type renderOpts struct {
	Model            string
	Profile          string
	NativeEffort     string
	SystemPromptFile string
}

// render substitutes placeholders, dropping unset optional args (and a paired
// preceding flag). Returns the command name + args.
func (t AgentTemplate) render(task string, o renderOpts) (string, []string) {
	keep := func(s string) (string, bool) {
		switch s {
		case "{{task}}":
			return task, true
		case "{{model}}":
			return o.Model, o.Model != ""
		case "{{profile}}":
			return o.Profile, o.Profile != ""
		case "{{systemPromptFile}}":
			return o.SystemPromptFile, o.SystemPromptFile != ""
		default:
			return strings.NewReplacer(
				"{{task}}", task,
				"{{model}}", o.Model,
				"{{profile}}", o.Profile,
				"{{systemPromptFile}}", o.SystemPromptFile,
			).Replace(s), true
		}
	}
	var out []string
	for _, a := range t.Argv {
		v, ok := keep(a)
		if !ok {
			if n := len(out); n > 0 && strings.HasPrefix(out[n-1], "-") {
				out = out[:n-1] // drop the now-orphaned flag
			}
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return "", nil
	}
	return out[0], out[1:]
}

// loadAgents returns the builtin templates overlaid with any in
// .projx/agents.json (a JSON array of {name, argv}).
func loadAgents(absRoot string) map[string]AgentTemplate {
	out := make(map[string]AgentTemplate, len(builtinAgents)+2)
	for k, v := range builtinAgents {
		out[k] = v
	}
	if list, ok := customAgentTemplates(absRoot); ok {
		for _, a := range list {
			out[a.Name] = a
		}
	}
	return out
}

// customAgentTemplates is an intentional local escape hatch for a provider
// outside ProjX's built-in adapter set. It may override a builtin, but routing
// configuration itself still carries only provider/model/effort data.
func customAgentTemplates(absRoot string) ([]AgentTemplate, bool) {
	data, err := os.ReadFile(filepath.Join(absRoot, ".projx", "agents.json"))
	if err != nil {
		return nil, false
	}
	var list []AgentTemplate
	if json.Unmarshal(data, &list) != nil {
		return nil, false
	}
	valid := make([]AgentTemplate, 0, len(list))
	for _, a := range list {
		if a.Name != "" && len(a.Argv) > 0 {
			valid = append(valid, a)
		}
	}
	return valid, true
}

// resolveAgentArgv builds the non-interactive command for the configured agent.
// PROJX_AGENT_CMD (an explicit command, e.g. from routing per task class) wins;
// otherwise the PROJX_AGENT-named template is rendered with the tier model
// (PROJX_AGENT_MODEL) and the steering file (PROJX_STORE_CONTEXT_FILE). An
// unknown agent name is treated as a bare command. Agent-agnostic by data.
func resolveAgentArgv(absRoot, task string, opts renderOpts) (string, []string) {
	if cmd := strings.TrimSpace(os.Getenv("PROJX_AGENT_CMD")); cmd != "" {
		f := strings.Fields(cmd)
		return f[0], append(f[1:], task)
	}
	name := strings.TrimSpace(os.Getenv("PROJX_AGENT"))
	if name == "" {
		name = "claude"
	}
	if custom, ok := customAgentTemplates(absRoot); ok {
		for _, tmpl := range custom {
			if strings.EqualFold(tmpl.Name, name) {
				return tmpl.render(task, opts)
			}
		}
	}
	if adapter, ok := builtinProviderAdapter(name); ok {
		return adapter.Build(ProviderInvocation{
			Task: task, Model: opts.Model, NativeEffort: opts.NativeEffort,
			SystemPromptFile: opts.SystemPromptFile, Sandbox: "workspace-write",
		})
	}
	tmpl, ok := loadAgents(absRoot)[name]
	if !ok {
		return name, []string{task}
	}
	return tmpl.render(task, opts)
}

// prepareAgentContext compiles the store preamble, writes .projx/agent-context.md,
// and returns the context file + the env that delivers it. This is "the rest of
// the engine work for the AI": even UNCAGED, the agent gets the steering/contract
// + gates-as-context + model — not a bare CLI. When task is non-empty the preamble
// is TASK-SLICED (law + only the records relevant to the task) instead of the full
// store dump, so a launch costs the least, most-relevant context.
func buildAgentPolicyBlock() string {
	class := strings.TrimSpace(os.Getenv("PROJX_POLICY_CLASS"))
	provider := strings.TrimSpace(os.Getenv("PROJX_POLICY_PROVIDER"))
	profile := strings.TrimSpace(os.Getenv("PROJX_POLICY_PROFILE"))
	model := strings.TrimSpace(os.Getenv("PROJX_POLICY_MODEL"))
	fallback := strings.TrimSpace(os.Getenv("PROJX_POLICY_FALLBACK"))
	if class == "" && provider == "" && profile == "" && model == "" && fallback == "" {
		return ""
	}
	if fallback == "" {
		fallback = "ambient-allowed"
	}
	return fmt.Sprintf(`## Active execution policy
{
  "task_class": %q,
  "preferred_provider": %q,
  "preferred_profile": %q,
  "preferred_model": %q,
  "fallback": %q,
  "deterministic_first": true
}

Follow this policy unless a deterministic gate or explicit higher-priority rule forbids it.

`, class, provider, profile, model, fallback)
}

func prepareAgentContext(absRoot, task string) (ctxFile string, env map[string]string) {
	st := openStore(absRoot)
	var preamble string
	if strings.TrimSpace(task) != "" {
		preamble = compileStorePreambleForTask(st, task)
	} else {
		preamble = compileStorePreamble(st)
	}
	st.Close()
	preamble = buildAgentPolicyBlock() + applyWorkerRole(preamble, workerRoleLabel()) // policy + per-worker role scope
	ctxFile, _ = writeAgentContextText(absRoot, preamble)
	env = map[string]string{
		"PROJX_STORE_CONTEXT": preamble,
		"PROJX_AGENT_CONTEXT": "1",
		"PROJX_ROLE":          "worker", // exempt spawned workers from the trunk-dispatch gate
	}
	if ctxFile != "" {
		env["PROJX_STORE_CONTEXT_FILE"] = ctxFile
	}
	return ctxFile, env
}

// workerRoleLabel returns the descriptive role a launched worker plays, read from
// PROJX_WORKER_ROLE (set per dispatched step by the supervisor). Defaults to the
// generic "worker". This is an OBSERVABILITY/scoping label only — the gate-exemption
// signal stays PROJX_ROLE=worker regardless.
func workerRoleLabel() string {
	if r := strings.TrimSpace(os.Getenv("PROJX_WORKER_ROLE")); r != "" {
		return r
	}
	return "worker"
}

// applyWorkerRole prepends a one-line role banner to a compiled preamble when the
// role is a SPECIFIC dispatched-step role (not the generic "worker"), so the worker's
// injected context visibly states the narrow scope it was spawned for. Combined with
// the task-slice (compileStorePreambleForTask), this is the per-worker ProjX scope:
// role + step-relevant knowledge, not the whole trunk context.
func applyWorkerRole(preamble, role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "worker"
	}
	return "# Dispatched-worker execution contract\n\n" +
		"You are a managed ProjX worker (`PROJX_ROLE=worker`), not the coordinator. " +
		"Implement and verify THIS assigned task directly. Do not call `route`, " +
		"`dispatch_run`, `projx dispatch`, or create another worker.\n\n" +
		"Your scoped role: " + role + ".\n\n" + preamble
}

// agentLaunch resolves the agent command AND prepares the store context in one
// step, returning argv + the env that delivers the contract + model. Every launch
// path (uncaged headless, caged spec, serve) uses it, so the engine's work is
// applied uniformly — cage or no cage.
func agentLaunch(absRoot, task string) (name string, argv []string, env map[string]string) {
	ctxFile, env := prepareAgentContext(absRoot, task)
	name, argv = resolveAgentArgv(absRoot, task, renderOpts{
		Model:            os.Getenv("PROJX_AGENT_MODEL"),
		Profile:          os.Getenv("PROJX_AGENT_PROFILE"),
		NativeEffort:     os.Getenv("PROJX_AGENT_EFFORT"),
		SystemPromptFile: ctxFile,
	})
	return name, argv, env
}

// The worker permission floor is NOT defined here — it lives in the store as editable
// data (store.SettingWorkerAllow / store.WorkerAllowBins), so any rule can change
// without a recompile. The engine only RENDERS whatever the store declares (below).

// claudeAllowedToolsArgs renders a safe-list into the agent CLI's --allowedTools flag:
// each shell command as Bash(<cmd>:*), plus the always-safe read-only tools. Pure, so
// it is unit-tested directly. Returns nil for an empty list (no flag → everything
// prompts, i.e. the old behavior).
func claudeAllowedToolsArgs(bins []string) []string {
	if len(bins) == 0 {
		return nil
	}
	args := []string{"--allowedTools"}
	for _, b := range bins {
		args = append(args, "Bash("+b+":*)")
	}
	// A worker's core job is editing files, so the file tools are basic permissions too;
	// the ProjX gate still blocks off-limits paths on every Read/Edit/Write regardless.
	args = append(args, "Read", "Write", "Edit", "MultiEdit", "Grep", "Glob")
	return args
}

// isClaudeAgent reports whether the resolved agent binary is a Claude Code CLI — the
// launcher whose allow-list flag (--allowedTools) we know how to render. Other
// providers keep their own permission config; the flag is not injected for them, so
// the safe-list stays agnostic (Claude gets it as data; a future provider supplies its
// own renderer via the integration seam).
func isClaudeAgent(agentPath string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(agentPath)), "claude")
}

// kvSlice turns an env map into "k=v" entries for exec.Cmd.Env.
func kvSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
