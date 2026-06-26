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
func resolveAgentArgv(absRoot, task string) (string, []string) {
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
	return tmpl.render(task, renderOpts{
		Model:            os.Getenv("PROJX_AGENT_MODEL"),
		SystemPromptFile: os.Getenv("PROJX_STORE_CONTEXT_FILE"),
	})
}
