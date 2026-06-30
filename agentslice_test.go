package main

import (
	"strings"
	"testing"
)

// TestPrepareAgentContextSlices proves a launch with a task gets the task-sliced
// contract (law + relevant doc, unrelated doc excluded), while no task gets the full
// dump — so an agent launch no longer pays for the whole store/code-map every time.
func TestPrepareAgentContextSlices(t *testing.T) {
	root := t.TempDir()
	seedSessionStore(t, root) // gate secret/**, convention, doc minecraft/login, doc billing

	_, env := prepareAgentContext(root, "work on the minecraft login backend")
	sliced := env["PROJX_STORE_CONTEXT"]
	if !strings.Contains(sliced, "secret/**") {
		t.Error("sliced launch context dropped the law")
	}
	if !strings.Contains(sliced, "minecraft/login/backend") {
		t.Error("sliced launch context missing the relevant doc")
	}
	if strings.Contains(sliced, "billing/checkout") {
		t.Error("sliced launch context leaked the unrelated billing doc")
	}

	_, envFull := prepareAgentContext(root, "")
	full := envFull["PROJX_STORE_CONTEXT"]
	if !strings.Contains(full, "billing/checkout") {
		t.Error("no-task launch should get the FULL store (billing doc present)")
	}
	if len(sliced) >= len(full) {
		t.Errorf("sliced (%d) should be smaller than full (%d)", len(sliced), len(full))
	}
}
