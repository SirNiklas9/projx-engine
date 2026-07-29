package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SirNiklas9/projx-engine/internal/routing"
)

func TestMain(m *testing.M) {
	if os.Getenv("PROJX_TEST_DISPATCH_HELPER") == "1" {
		args := os.Args[1:]
		for i := 0; i < len(args); i++ {
			if args[i] == "__dispatch-run" && i+1 < len(args) {
				root := "."
				for j := 0; j+1 < len(args); j++ {
					if args[j] == "--root" {
						root = args[j+1]
						break
					}
				}
				runDispatchSupervise(root, args[i+1:])
				os.Exit(0)
			}
			if args[i] == "agent" || args[i] == "verify" {
				os.Exit(0)
			}
		}
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestMCPDispatchRunLaunchesAndReturnsManagedManifest(t *testing.T) {
	root := t.TempDir()
	original := launchMCPDispatch
	t.Cleanup(func() { launchMCPDispatch = original })
	launchMCPDispatch = func(gotRoot, task string) ([]byte, error) {
		m := &dispatchManifest{
			ID: "acceptance-run", Message: task, State: "running",
			Started: time.Now(), PID: 4242, ParentPID: os.Getpid(),
			Steps: []dispatchStepStat{{
				Task: task, Tier: "cheap-fast", Kind: "agent", State: "running",
				Provider: "codex", ProviderModel: "gpt-test", ProviderEffort: "medium",
				PID: 4243, ParentPID: 4242,
			}},
		}
		return nil, writeDispatchManifest(gotRoot, m)
	}
	params, _ := json.Marshal(map[string]any{
		"name":      "dispatch_run",
		"arguments": map[string]any{"root": root, "task": "fix the typo"},
	})
	resp := mcpToolCall(mcpReq{ID: json.RawMessage("1"), Params: params}, root)
	result, ok := resp.Result.(map[string]any)
	if !ok || result["isError"] != false {
		t.Fatalf("dispatch_run response = %#v", resp)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["run_id"] != "acceptance-run" || structured["pid"] != 4242 {
		t.Fatalf("dispatch_run structured result = %#v", result["structuredContent"])
	}
	m, err := readDispatchManifest(root, "acceptance-run")
	if err != nil || len(m.Steps) != 1 || m.Steps[0].Provider != "codex" {
		t.Fatalf("managed manifest = %+v, err=%v", m, err)
	}
}

func TestMCPAdvertisesRouteAsPreviewAndDispatchAsExecution(t *testing.T) {
	tools := mcpTools()
	found := map[string]bool{}
	for _, tool := range tools {
		if name, _ := tool["name"].(string); name == "route" || name == "dispatch_run" {
			found[name] = true
		}
	}
	if !found["route"] || !found["dispatch_run"] {
		t.Fatalf("MCP tools missing route/dispatch_run: %#v", found)
	}
}

func TestLiveManagedDispatchRecordsExactRouteAndProcessTree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_TEST_DISPATCH_HELPER", "1")
	task := "implement the acceptance change"
	steps := []dispatchStep{{Task: task, Decision: routing.Decision{
		Kind: "agent", Class: "default", Provider: "codex", Model: "gpt-test",
		NativeEffort: "medium", Reason: "acceptance route", Selection: "exact catalog profile",
	}}}
	startDetachedDispatch(root, steps, task)

	var m *dispatchManifest
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		runs := listDispatchManifests(root)
		if len(runs) == 1 {
			m = runs[0]
			if m.State == "done" || m.State == "failed" {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if m == nil || m.State != "done" {
		log := ""
		if m != nil {
			data, _ := os.ReadFile(dispatchLogPath(root, m.ID))
			log = string(data)
		}
		t.Fatalf("live dispatch did not finish: manifest=%+v log=%s", m, strings.TrimSpace(log))
	}
	if m.PID == 0 || m.ParentPID == 0 || len(m.Steps) != 1 || m.Steps[0].PID == 0 {
		t.Fatalf("managed process tree was not fully recorded: %+v", m)
	}
	step := m.Steps[0]
	if step.Provider != "codex" || step.ProviderModel != "gpt-test" || step.ProviderEffort != "medium" {
		t.Fatalf("manifest route differs from selected invocation: %+v", step)
	}
	if m.Verify != "passed" {
		t.Fatalf("verification gate = %q, want passed", m.Verify)
	}
}

func TestFastSupervisorCompletionCannotBeOverwrittenToRunning(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_TEST_DISPATCH_HELPER", "1")
	task := "implement the fast acceptance change"
	steps := []dispatchStep{{Task: task, Decision: routing.Decision{
		Kind: "agent", Class: "default", Provider: "codex", Model: "gpt-test",
		NativeEffort: "medium", Reason: "acceptance route",
	}}}
	startDetachedDispatch(root, steps, task)
	var runs []*dispatchManifest
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs = listDispatchManifests(root)
		if len(runs) == 1 && runs[0].State != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(runs) != 1 || runs[0].State != "done" || runs[0].Verify != "passed" {
		t.Fatalf("fast terminal manifest was lost: %+v", runs)
	}
}
