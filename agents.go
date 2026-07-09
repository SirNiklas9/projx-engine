package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AgentTemplate declares how to invoke an agent non-interactively. Argv is a
// template; placeholders are substituted at launch:
//
//	{{task}}             -> the task / prompt (required)
//	{{model}}            -> tier-resolved model id (the arg is DROPPED if empty)
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
}

type renderOpts struct {
	Model            string
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
		case "{{systemPromptFile}}":
			return o.SystemPromptFile, o.SystemPromptFile != ""
		default:
			return strings.NewReplacer(
				"{{task}}", task,
				"{{model}}", o.Model,
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
	if data, err := os.ReadFile(filepath.Join(absRoot, ".projx", "agents.json")); err == nil {
		var list []AgentTemplate
		if json.Unmarshal(data, &list) == nil {
			for _, a := range list {
				if a.Name != "" && len(a.Argv) > 0 {
					out[a.Name] = a
				}
			}
		}
	}
	return out
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
	name := os.Getenv("PROJX_AGENT")
	if name == "" {
		name = "claude"
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
func prepareAgentContext(absRoot, task string) (ctxFile string, env map[string]string) {
	st := openStore(absRoot)
	var preamble string
	if strings.TrimSpace(task) != "" {
		preamble = compileStorePreambleForTask(st, task)
	} else {
		preamble = compileStorePreamble(st)
	}
	st.Close()
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

// agentLaunch resolves the agent command AND prepares the store context in one
// step, returning argv + the env that delivers the contract + model. Every launch
// path (uncaged headless, caged spec, serve) uses it, so the engine's work is
// applied uniformly — cage or no cage.
func agentLaunch(absRoot, task string) (name string, argv []string, env map[string]string) {
	ctxFile, env := prepareAgentContext(absRoot, task)
	name, argv = resolveAgentArgv(absRoot, task, renderOpts{
		Model:            os.Getenv("PROJX_AGENT_MODEL"),
		SystemPromptFile: ctxFile,
	})
	return name, argv, env
}

// workerSafeTools is the curated floor of shell commands a dispatched worker may run
// WITHOUT prompting — the "basic permissions" a worker needs to actually build: VCS,
// the common toolchains, its own engine (read ops), and read-only shell utilities.
// Anything NOT on this list still prompts, which is the "reach and ask for more"
// escalation (the human grants the rest). Named here so a project can widen it later
// from data; the ProjX gate independently blocks secrets/off-limits regardless.
var workerSafeTools = []string{
	"git", "go", "gofmt", "goimports", "make",
	"npm", "npx", "pnpm", "yarn", "node",
	"projx-engine",
	"cat", "ls", "grep", "rg", "find", "sed", "awk", "head", "tail", "wc",
}

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
	args = append(args, "Read", "Grep", "Glob") // read-only tools are always safe
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
