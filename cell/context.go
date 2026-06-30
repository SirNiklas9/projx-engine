package main

// context.go — the smart-context lifecycle over the cell's HTTP API. The lifecycle
// LOGIC (floor / delta / refill / suggest) is the shared projx-store definition
// (store.SessionContext / store.SessionSuggest); this binds it to the cell's hands —
// per-session checkpoints via storage.fs (pulp.FS) — exactly as the native face binds
// it to .projx files. This is what lets MANY concurrent agents share one cell + one
// store while each keeps its own delta cursor.
//
//   GET  /api/context/floor   ?session=     -> lean SessionStart floor (+ fresh checkpoint)
//   GET  /api/context/slice   ?task=        -> stateless task slice (preview, no session)
//   GET  /api/context/delta   ?session=&task= -> per-message delta (law + new/changed)
//   POST /api/context/reset   ?session=     -> PreCompact: mark floor lost
//   POST /api/context/suggest ?session=     -> Stop: @remember nudge {suggest, block}

import (
	"encoding/json"
	"strings"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

// pulpCheckpoints is the cell's store.CheckpointStore: one JSON file per session under
// .projx, via the host's storage.fs capability. Best-effort — a missing/corrupt file is
// a fresh session; a failed write only costs a little redundant context.
type pulpCheckpoints struct{}

func cellCheckpointPath(session string) string {
	return ".projx/agent-seen-" + sanitizeCellSession(session) + ".json"
}

func (pulpCheckpoints) Load(session string) store.Checkpoint {
	cp := store.Checkpoint{Seen: map[string]int64{}}
	if data, err := pulp.FS.Read(cellCheckpointPath(session)); err == nil {
		_ = json.Unmarshal(data, &cp)
		if cp.Seen == nil {
			cp.Seen = map[string]int64{}
		}
	}
	return cp
}

func (pulpCheckpoints) Save(session string, cp store.Checkpoint) {
	if data, err := json.Marshal(cp); err == nil {
		_ = pulp.FS.Write(cellCheckpointPath(session), data)
	}
}

// sanitizeCellSession maps a session id to a safe filename component (mirrors the native
// sanitizeSession); empty/"-" → "default".
func sanitizeCellSession(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return "default"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// handleContextFloor — SessionStart. With ?session= it writes a fresh checkpoint and
// returns the lean floor; without a session it is a stateless floor preview.
func handleContextFloor(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	if session := c.Query("session"); session != "" {
		c.JSON(200, pulpgin.H{"floor": store.SessionContext(s, pulpCheckpoints{}, session, "", false, nil)})
		return
	}
	c.JSON(200, pulpgin.H{"floor": store.AgentContextFloor(s)})
}

// handleContextSlice — stateless task-sliced preview (no session, no checkpoint).
func handleContextSlice(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	task := c.Query("task")
	c.JSON(200, pulpgin.H{"task": task, "context": store.AgentContextForTask(s, task)})
}

// handleContextDelta — UserPromptSubmit. Per-session delta: law re-asserted + only the
// new/changed task-relevant records (or a full refill right after a reset).
func handleContextDelta(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	session := c.Query("session")
	task := c.Query("task")
	c.JSON(200, pulpgin.H{"context": store.SessionContext(s, pulpCheckpoints{}, session, task, false, nil)})
}

// handleContextReset — PreCompact. Mark the floor lost so the next turn refills.
func handleContextReset(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	store.SessionContext(s, pulpCheckpoints{}, c.Query("session"), "", true, nil)
	c.JSON(200, pulpgin.H{"reset": true})
}

// handleContextSuggest — Stop. SUGGEST-ONLY @remember nudge: {suggest, block}.
func handleContextSuggest(c *pulpgin.Context) {
	s, err := openStore()
	if err != nil {
		c.JSON(503, pulpgin.H{"error": "store unavailable: " + err.Error()})
		return
	}
	msg, block := store.SessionSuggest(s, pulpCheckpoints{}, c.Query("session"))
	c.JSON(200, pulpgin.H{"suggest": msg, "block": block})
}
