package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBudgetAutomaticHookContextLeavesSmallContextAlone(t *testing.T) {
	const ctx = "small relevant ProjX context"
	if got := budgetAutomaticHookContext(ctx); got != ctx {
		t.Fatalf("small context changed: %q", got)
	}
}

func TestBudgetAutomaticHookContextCapsAtLineBoundary(t *testing.T) {
	ctx := "safety floor\n" + strings.Repeat("unrelated record\n", 600) + "relevant task record\n"
	got := budgetAutomaticHookContext(ctx)
	if len(got) > automaticHookContextMaxBytes {
		t.Fatalf("context length = %d, want at most %d", len(got), automaticHookContextMaxBytes)
	}
	if !strings.Contains(got, "capped to protect token budget") {
		t.Fatal("capped context missing retrieval notice")
	}
	if !strings.HasSuffix(got, automaticHookContextTruncated) {
		if !strings.HasSuffix(got, "relevant task record\n") {
			t.Fatal("capped context did not retain the task-relevant tail")
		}
	}
	if !strings.Contains(got, "safety floor") {
		t.Fatal("capped context did not retain the safety floor")
	}
	if !strings.Contains(got, "relevant task record") {
		t.Fatal("capped context did not retain the task-relevant tail")
	}
}

func TestBudgetAutomaticHookContextPreservesUTF8(t *testing.T) {
	ctx := strings.Repeat("knowledge ✅\n", 600)
	if got := budgetAutomaticHookContext(ctx); !utf8.ValidString(got) {
		t.Fatal("budget split a UTF-8 rune")
	}
}
