package main

import (
	"testing"
	"time"
)

func TestMarkDispatchCancelledMarksUnfinishedStepsAndReason(t *testing.T) {
	m := &dispatchManifest{State: "running", Steps: []dispatchStepStat{{State: "done"}, {State: "running"}, {State: "pending"}}}
	markDispatchCancelled(m)
	if m.State != "cancelled" || m.FailureReason != "cancelled by user" || m.Finished.IsZero() {
		t.Fatalf("manifest cancellation = %+v", m)
	}
	if m.Steps[0].State != "done" || m.Steps[1].State != "cancelled" || m.Steps[2].State != "cancelled" {
		t.Fatalf("step states after cancellation = %+v", m.Steps)
	}
	if time.Since(m.Steps[1].Finished) > time.Minute || m.Steps[2].Finished.IsZero() {
		t.Fatalf("cancelled steps missing finish time: %+v", m.Steps)
	}
}
