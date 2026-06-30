package main

// context.go — the smart-context lifecycle primitives exposed over the cell's HTTP
// API, so a harness connector can drive a deployed WASM cell the same way the native
// `projx-engine hook` drives the CLI. All pure store logic (shared projx-store), no
// capabilities required:
//
//   GET /api/context/floor          -> the LEAN session-start floor (protocol + law)
//   GET /api/context/slice?task=... -> the task-sliced contract (law + relevant records)
//
// The per-message DELTA (suppress already-seen) needs per-session checkpoint state and
// the v2 semantic selector needs an outbound-HTTP capability — both are follow-ups; the
// floor + deterministic slice are the parity baseline.

import (
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// handleContextFloor — GET /api/context/floor -> {"floor": "..."}. The lean floor the
// connector injects at SessionStart: the protocol + the binding law (gates +
// conventions) in full, and nothing else (reference knowledge loads per-task).
func handleContextFloor(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	c.JSON(200, pulpgin.H{"floor": store.AgentContextFloor(s)})
}

// handleContextSlice — GET /api/context/slice?task=... -> {"context": "..."}. The
// per-message task-sliced contract: the law in full plus only the reference records
// relevant to the task (deterministic v1 selection). An empty task yields the full
// preamble.
func handleContextSlice(c *pulpgin.Context) {
	task := c.Query("task")
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	c.JSON(200, pulpgin.H{"task": task, "context": store.AgentContextForTask(s, task)})
}
