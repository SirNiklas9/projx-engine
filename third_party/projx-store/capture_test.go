package store

import (
	"strings"
	"testing"
)

// TestCaptureHint covers detection (command + natural language) and that the hint
// flows through AgentContextForTask only when capture is signalled.
func TestCaptureHint(t *testing.T) {
	// Non-capture tasks yield no hint.
	for _, task := range []string{"fix the login bug", "what does the billing flow do?", ""} {
		if got := CaptureHint(task); got != "" {
			t.Errorf("non-capture task %q yielded a hint", task)
		}
	}
	// Capture tasks yield the commit instructions.
	for _, task := range []string{
		"@remember the login flow uses JWT",
		"please remember this: billing retries 3x",
		"add that to the knowledge base",
		"can you document this decision",
	} {
		got := CaptureHint(task)
		if !strings.Contains(got, "store commit") || !strings.Contains(got, "CAPTURE REQUESTED") {
			t.Errorf("capture task %q did not yield the commit hint", task)
		}
		if !strings.Contains(got, "--claim-class") || !strings.Contains(got, "--evidence") {
			t.Errorf("capture task %q omitted durable candidate metadata", task)
		}
		if strings.Contains(got, "`history`") {
			t.Errorf("capture task %q still recommends transient history records", task)
		}
	}

	m := NewMem()
	if err := m.Put(Record{ID: "conv/x", Kind: KConvention, Scope: ScopeProject, Key: "x", Body: "y"}); err != nil {
		t.Fatal(err)
	}
	// Triggered: hint present.
	if !strings.Contains(AgentContextForTask(m, "@remember login uses JWT"), "CAPTURE REQUESTED") {
		t.Error("AgentContextForTask should append the capture hint when triggered")
	}
	// Not triggered: no hint.
	if strings.Contains(AgentContextForTask(m, "look at the login flow"), "CAPTURE REQUESTED") {
		t.Error("AgentContextForTask should NOT append the hint for a normal task")
	}
}
