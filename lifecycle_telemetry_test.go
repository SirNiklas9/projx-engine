package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRecordLifecycleTelemetryCapturesLifecycleContract(t *testing.T) {
	root := t.TempDir()
	event := lifecycleEvent{SessionID: "session-1", Event: "SessionStart"}
	recordLifecycleTelemetry(root, event, "started", 17)
	data, err := os.ReadFile(lifecycleTelemetryPath(root))
	if err != nil {
		t.Fatal(err)
	}
	var got lifecycleTelemetry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "session-1" || got.Event != "SessionStart" || got.MapRefreshDecision != "started" {
		t.Fatalf("unexpected telemetry: %+v", got)
	}
	if got.PID == 0 || got.InjectedBytes != 17 || got.EstimatedTokens != 5 {
		t.Fatalf("missing process/token telemetry: %+v", got)
	}
}
