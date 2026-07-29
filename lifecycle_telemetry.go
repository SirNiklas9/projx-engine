package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// lifecycleTelemetry is an append-only operational record. It is deliberately
// separate from the knowledge store: hook diagnostics must not become injected
// project knowledge or make a lifecycle failure block a user session.
type lifecycleTelemetry struct {
	At                 time.Time `json:"at"`
	SessionID          string    `json:"session_id,omitempty"`
	Event              string    `json:"event,omitempty"`
	PID                int       `json:"pid"`
	ParentPID          int       `json:"parent_pid"`
	MapRefreshDecision string    `json:"map_refresh_lock_decision,omitempty"`
	InjectedBytes      int       `json:"injected_bytes"`
	EstimatedTokens    int       `json:"estimated_tokens"`
	DurationMS         int64     `json:"duration_ms"`
	Outcome            string    `json:"outcome"`
	ExitCode           int       `json:"exit_code"`
	Root               string    `json:"root,omitempty"`
	Harness            string    `json:"harness,omitempty"`
}

type lifecycleTelemetryContext struct {
	Started  time.Time
	ExitCode int
	Harness  string
}

func lifecycleTelemetryPath(root string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".projx", "lifecycle.jsonl")
}

func recordLifecycleTelemetry(root string, event lifecycleEvent, mapRefreshDecision string, injectedBytes int, context ...lifecycleTelemetryContext) {
	path := lifecycleTelemetryPath(root)
	if path == "" {
		return
	}
	ctx := lifecycleTelemetryContext{Started: time.Now()}
	if len(context) > 0 {
		ctx = context[0]
	}
	record := lifecycleTelemetry{
		At: time.Now().UTC(), SessionID: event.SessionID, Event: event.Event,
		PID: os.Getpid(), ParentPID: os.Getppid(), MapRefreshDecision: mapRefreshDecision,
		InjectedBytes: injectedBytes, EstimatedTokens: (injectedBytes + 3) / 4,
		DurationMS: time.Since(ctx.Started).Milliseconds(), ExitCode: ctx.ExitCode,
		Root: filepath.Clean(root), Harness: ctx.Harness,
	}
	if ctx.ExitCode == 0 {
		record.Outcome = "allowed"
	} else if ctx.ExitCode == 2 {
		record.Outcome = "blocked"
	} else {
		record.Outcome = "failed"
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}
