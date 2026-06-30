package main

import (
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// handleRoute — GET /api/route?task=... -> {class, cmd, source, reason}. The auto
// model-tier DECISION via the shared decider (store.RouteDecide): the precedence
// ladder — per-message @-override > standing pin/floor > keyword classifier >
// (cheap triage) > default — then resolve the launch command from the store's KRoute
// records. The policy is DECLARED in the store, not hardcoded. triage is nil here:
// the cell has no outbound-HTTP capability yet, so the ambiguous middle routes
// deterministically (a cell-side triage via a transport.http.outbound capability is
// a follow-up). Same store.RouteDecide the native engine uses.
func handleRoute(c *pulpgin.Context) {
	task := c.Query("task")
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	d := store.RouteDecide(s, task, cellTriageFunc())
	c.JSON(200, pulpgin.H{"task": task, "class": d.Class, "cmd": d.Cmd, "source": d.Source, "reason": d.Reason})
}
