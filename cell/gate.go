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

// handleAgentSpec — GET /api/agent/spec?task=... -> the engine's launch contract
// for the agent: the routed model tier (auto-provisioned by task) + the gate deny
// rules. This is "what the AI goes through" — assembled by the engine from the
// store, handed to whatever actually launches the agent (the Workbench, the CLI,
// or the native caged runner). The steering itself rides in CLAUDE.md, which the
// engine already generates from the same store.
func handleAgentSpec(c *pulpgin.Context) {
	task := c.Query("task")
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	class, cmd := store.Route(s, task)
	c.JSON(200, pulpgin.H{
		"task":  task,
		"class": class,
		"cmd":   cmd,
		"deny":  store.DenyRules(s),
	})
}
