package main

import (
	"testing"
	"time"
)

func TestDispatchStepStartedRecordsManagedChildInManifest(t *testing.T) {
	root := t.TempDir()
	m := &dispatchManifest{
		ID:    "managed-child",
		Steps: []dispatchStepStat{{Task: "verify", State: "running"}},
	}
	started := dispatchStepStarted(root, m, &m.Steps[0])
	started(4242)

	got, err := readDispatchManifest(root, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	step := got.Steps[0]
	if step.PID != 4242 || step.ParentPID == 0 || step.Started.IsZero() {
		t.Fatalf("managed child missing from manifest: %+v", step)
	}
	if time.Since(step.Started) > time.Minute {
		t.Fatalf("unexpected child start time: %s", step.Started)
	}
}
