package main

import (
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// handleRoute — GET /api/route?task=... -> {"class":...,"cmd":...}. The auto
// model-tier decision: classify the task (deterministic, no LLM) and resolve the
// launch command from the store's KRoute records. The policy is DECLARED in the
// store, not hardcoded — and it is the same store.Route the native engine uses.
func handleRoute(c *pulpgin.Context) {
	task := c.Query("task")
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	class, cmd := store.Route(s, task)
	c.JSON(200, pulpgin.H{"task": task, "class": class, "cmd": cmd})
}
