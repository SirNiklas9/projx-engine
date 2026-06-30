package main

// run.go — agent launch from the cell (brain) through Pulp (hands), no native
// executor in the path. The cell assembles the contract and drives the launch.
//
//   POST /api/agent/run         {task, caged, allowHosts} -> {jobID, mode, class}
//   GET  /api/agent/run/status  ?id=N&caged=bool          -> {done, exitCode, ...}
//
// Caging is OPT-IN (req.caged). Launches are ASYNC: the cell SUBMITS the job and
// returns a jobID immediately so the cell step is never blocked by a long agent
// run; the caller polls status. Uncaged → spawn.process; caged → spawn.confine
// (Landlock on Linux / AppContainer on Windows; egress netns/gVisor on Linux).
//
// CONTRACT DELIVERY: the store PREAMBLE is written to .projx/agent-context.md
// (claude: --append-system-prompt-file; + PROJX_STORE_CONTEXT env for neutral
// harnesses); the gate DENY rules to .projx/agent-settings.json (claude:
// --settings). projx-engine is on the agent's jail PATH so it can `store commit`
// its knowledge from inside the cage.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// Contract artifacts written into the project (.projx) before launch. Paths are
// relative to the storage.fs root (= the project root the agent runs in).
const (
	agentContextFile  = ".projx/agent-context.md"
	agentSettingsFile = ".projx/agent-settings.json"
)

// handleAgentRun assembles the contract and SUBMITS an agent launch (async),
// optionally caged, returning a jobID to poll.
func handleAgentRun(c *pulpgin.Context) {
	var req struct {
		Task       string   `json:"task"`
		Caged      bool     `json:"caged"`
		AllowHosts []string `json:"allowHosts,omitempty"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(400, pulpgin.H{"error": "bad request"})
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		c.JSON(400, pulpgin.H{"error": "task is required"})
		return
	}

	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}

	// Assemble the contract from the store — the same pieces /api/agent/spec
	// returns, now delivered + executed. RouteDecide = the decider (pin/floor/
	// @-override/keyword); triage nil in-cell (no outbound-HTTP capability yet).
	d := store.RouteDecide(s, req.Task, nil)
	class, cmd := d.Class, d.Cmd
	preamble := store.AgentContextForTask(s, req.Task) // task-sliced: law + only task-relevant records
	deny := store.DenyRules(s)

	// Deliver the contract as on-disk artifacts the agent consumes (best-effort:
	// the env var still carries the preamble even if a file write fails).
	_ = pulp.FS.Write(agentContextFile, []byte(preamble))
	if b, mErr := json.Marshal(map[string]any{"permissions": map[string]any{"deny": deny}}); mErr == nil {
		_ = pulp.FS.Write(agentSettingsFile, b)
	}

	root := agentRoot()
	argv := agentArgv(cmd, req.Task)
	env := map[string]string{
		"PROJX_AGENT_CONTEXT":   "1", // restricted store-contract mode in projx-engine
		"PROJX_STORE_CONTEXT":   preamble,
		"PROJX_CONFINE_HEADLESS": "1", // one-shot subagent: no console (Windows headless cage)
	}

	if req.Caged {
		id, err := pulp.Confine.SubmitCaged(pulp.CagedPolicy{
			Argv:     argv,
			Root:     root,
			Dir:      root,
			NetAllow: agentNetAllow(req.AllowHosts),
			JailBins: agentJailBins(argv),
			Env:      env,
		})
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "submit_caged: " + err.Error()})
			return
		}
		c.JSON(200, pulpgin.H{"jobID": id, "mode": "caged", "class": class, "caged": true})
		return
	}

	id, err := pulp.Process.Submit(pulp.RunRequest{Argv: argv, Dir: root, Env: env})
	if err != nil {
		c.JSON(500, pulpgin.H{"error": "submit: " + err.Error()})
		return
	}
	c.JSON(200, pulpgin.H{"jobID": id, "mode": "uncaged", "class": class, "caged": false})
}

// handleAgentStatus polls a submitted job. ?id=N&caged=bool — done=false while
// the agent is still running; on completion it carries the result.
func handleAgentStatus(c *pulpgin.Context) {
	idStr := c.Query("id")
	id64, err := strconv.ParseUint(strings.TrimSpace(idStr), 10, 32)
	if err != nil {
		c.JSON(400, pulpgin.H{"error": "id is required (uint)"})
		return
	}
	id := uint32(id64)

	if c.Query("caged") == "true" {
		res, ok, err := pulp.Confine.PollCaged(id)
		if err != nil {
			c.JSON(500, pulpgin.H{"error": "poll_caged: " + err.Error()})
			return
		}
		if !ok {
			c.JSON(200, pulpgin.H{"done": false})
			return
		}
		c.JSON(200, pulpgin.H{"done": true, "exitCode": res.ExitCode, "error": res.Error, "audit": res.AuditEvents})
		return
	}

	res, ok, err := pulp.Process.Poll(id)
	if err != nil {
		c.JSON(500, pulpgin.H{"error": "poll: " + err.Error()})
		return
	}
	if !ok {
		c.JSON(200, pulpgin.H{"done": false})
		return
	}
	c.JSON(200, pulpgin.H{"done": true, "exitCode": res.ExitCode, "stdout": string(res.Stdout), "stderr": string(res.Stderr), "error": res.Error})
}

// agentRoot is the project root the launched agent operates in: PROJX_ROOT (set
// by the host from -project), else PROJX_PROJECT_ROOT.
func agentRoot() string {
	if r := strings.TrimSpace(os.Getenv("PROJX_ROOT")); r != "" {
		return r
	}
	return strings.TrimSpace(os.Getenv("PROJX_PROJECT_ROOT"))
}

// agentArgv resolves the agent command line and appends contract-delivery flags.
// PROJX_AGENT overrides the binary (full path or bare basename; tests point it at
// a fake agent); otherwise the routed cmd from the store is used. For claude the
// task rides as the print-mode (`-p`) prompt and the contract files are passed via
// --append-system-prompt-file / --settings; a non-claude/fake agent just receives
// the task as an argument.
func agentArgv(routedCmd, task string) []string {
	var base []string
	if a := strings.TrimSpace(os.Getenv("PROJX_AGENT")); a != "" {
		base = []string{a}
	} else {
		base = strings.Fields(routedCmd)
	}
	if len(base) == 0 {
		base = []string{defaultAgentBin()}
	}
	if strings.Contains(strings.ToLower(agentBasename(base[0])), "claude") {
		return append(base,
			"-p", task,
			"--append-system-prompt-file", agentContextFile,
			"--settings", agentSettingsFile,
		)
	}
	return append(base, task)
}

// agentNetAllow is the egress allowlist: anthropic.com by default plus any
// caller-supplied hosts (e.g. a relay).
func agentNetAllow(extra []string) []string {
	out := []string{"anthropic.com", ".anthropic.com"}
	for _, h := range extra {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// agentJailBins is the restricted PATH for the caged agent: the agent itself,
// projx-engine (so it can read/commit the store from inside the cage), git, and a
// shell appropriate to the host OS.
func agentJailBins(argv []string) []string {
	bins := []string{"projx-engine", "git"}
	if len(argv) > 0 {
		bins = append([]string{agentBasename(argv[0])}, bins...)
	}
	if hostIsWindows() {
		bins = append(bins, "cmd")
	} else {
		bins = append(bins, "sh", "bash")
	}
	return bins
}

func hostIsWindows() bool { return os.Getenv("PROJX_HOST_OS") == "windows" }

func defaultAgentBin() string {
	if hostIsWindows() {
		return "claude.exe"
	}
	return "claude"
}

func agentBasename(agent string) string {
	base := filepath.Base(filepath.ToSlash(agent))
	if base == "" || base == "." {
		return defaultAgentBin()
	}
	return base
}
