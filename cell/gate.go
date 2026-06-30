package main

import (
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// handleGate — GET /api/gate -> {"deny":[...]}. The off-limits paths rendered as
// agent file-tool deny rules, from the store's gate rules.
func handleGate(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	c.JSON(200, pulpgin.H{"deny": store.DenyRules(s)})
}

// handleAgentSpec — GET /api/agent/spec?task=... -> the engine's FULL launch
// contract for the agent, assembled by the cell (the brain) from the store:
//   - class/cmd: the routed model tier (auto-provisioned by task)
//   - deny:      the gate off-limits rules as agent file-tool deny rules
//   - preamble:  the ambient CONTRACT (read-before-act, knowledge-in/out=store,
//     gate-is-law) + the live store contents, tiered — the positive knowledge the
//     agent is bound by, rendered from the SAME shared store.AgentPreamble the
//     native path uses, so cell-assembled and native-assembled contracts are
//     identical by construction.
//
// This is the complete "what the AI goes through", handed to whatever actually
// launches the agent (the Workbench, or the Pulp host's caged runner).
func handleAgentSpec(c *pulpgin.Context) {
	task := c.Query("task")
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	d := store.RouteDecide(s, task, cellTriageFunc()) // the decider (pin/floor/@-override/keyword); triage nil in-cell
	c.JSON(200, pulpgin.H{
		"task":     task,
		"class":    d.Class,
		"cmd":      d.Cmd,
		"source":   d.Source,
		"deny":     store.DenyRules(s),
		"preamble": store.AgentContextForTask(s, task), // task-sliced contract (full floor when task is empty)
	})
}
