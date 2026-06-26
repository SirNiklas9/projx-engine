package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestVerifyViolationsCleanEmpty proves the programmatic checker returns no
// violations (and no error) for a project with no declared-structure rules.
func TestVerifyViolationsCleanEmpty(t *testing.T) {
	vs, err := verifyViolations(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("empty project should have no violations, got %v", vs)
	}
}

func TestVerifyLoop(t *testing.T) {
	// 1. Clean on the first try → 1 iteration.
	res, err := VerifyLoop("task", 3,
		func(string) error { return nil },
		func() ([]string, error) { return nil, nil })
	if err != nil || !res.Clean || res.Iterations != 1 {
		t.Fatalf("clean-first: %+v err=%v", res, err)
	}

	// 2. Violations clear on the 2nd check → fixed after 2 iterations.
	checkN := 0
	res, err = VerifyLoop("task", 5,
		func(string) error { return nil },
		func() ([]string, error) {
			checkN++
			if checkN < 2 {
				return []string{"a -> b"}, nil
			}
			return nil, nil
		})
	if err != nil || !res.Clean || res.Iterations != 2 {
		t.Fatalf("fixed-after-2: %+v err=%v", res, err)
	}

	// 3. The violation is fed back into the agent's task on the retry.
	var lastTask string
	checkN = 0
	if _, err := VerifyLoop("base task", 3,
		func(tf string) error { lastTask = tf; return nil },
		func() ([]string, error) {
			checkN++
			if checkN < 2 {
				return []string{"x -> y"}, nil
			}
			return nil, nil
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lastTask, "x -> y") {
		t.Errorf("violation not fed back to agent: %q", lastTask)
	}

	// 4. Persistent violations → give up at maxIters, report remaining.
	res, _ = VerifyLoop("task", 2,
		func(string) error { return nil },
		func() ([]string, error) { return []string{"persistent"}, nil })
	if res.Clean || res.Iterations != 2 || len(res.Remaining) == 0 {
		t.Fatalf("give-up: %+v", res)
	}

	// 5. An agent error aborts the loop.
	if _, err := VerifyLoop("task", 3,
		func(string) error { return fmt.Errorf("boom") },
		func() ([]string, error) { return nil, nil }); err == nil {
		t.Error("expected agent error to propagate")
	}
}
