package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestUpsertProjXManagedBlockPreservesUserBytes(t *testing.T) {
	user := []byte{0xef, 0xbb, 0xbf}
	user = append(user, []byte("# Team rules\r\n\r\nKeep this exact.\r\n")...)
	out, changed, err := UpsertProjXManagedBlock(user, AgentInstructionsBlock())
	if err != nil || !changed {
		t.Fatalf("upsert: changed=%t err=%v", changed, err)
	}
	if !bytes.HasPrefix(out, user) {
		t.Fatalf("user bytes changed:\nwant prefix %q\ngot %q", user, out)
	}
	if bytes.Contains(out, []byte("\n")) && !bytes.Contains(out, []byte("\r\n")) {
		t.Fatal("managed block did not adopt CRLF")
	}
}

func TestUpsertProjXManagedBlockIsIdempotentAndScoped(t *testing.T) {
	original := []byte("before\n\n" + ProjXManagedBeginV1 + "\nold\n" + ProjXManagedEnd + "\n\nafter\n")
	first, changed, err := UpsertProjXManagedBlock(original, AgentInstructionsBlock())
	if err != nil || !changed {
		t.Fatalf("refresh: changed=%t err=%v", changed, err)
	}
	if !bytes.HasPrefix(first, []byte("before\n\n")) || !bytes.HasSuffix(first, []byte("\n\nafter\n")) {
		t.Fatalf("content outside markers changed: %q", first)
	}
	second, changed, err := UpsertProjXManagedBlock(first, AgentInstructionsBlock())
	if err != nil || changed {
		t.Fatalf("second upsert: changed=%t err=%v", changed, err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("idempotent upsert changed bytes")
	}
}

func TestUpsertProjXManagedBlockRefusesMalformedOrDuplicateMarkers(t *testing.T) {
	cases := [][]byte{
		[]byte(ProjXManagedBeginV1 + "\nmissing end"),
		[]byte(ProjXManagedEnd),
		[]byte(ProjXManagedBeginV1 + "\na\n" + ProjXManagedEnd + "\n" + ProjXManagedBeginV1 + "\nb\n" + ProjXManagedEnd),
	}
	for _, input := range cases {
		out, changed, err := UpsertProjXManagedBlock(input, AgentInstructionsBlock())
		if !errors.Is(err, ErrManagedBlockMalformed) || changed || out != nil {
			t.Fatalf("input %q: out=%q changed=%t err=%v", input, out, changed, err)
		}
	}
}

func TestRemoveProjXManagedBlockRemovesOnlyManagedBytes(t *testing.T) {
	input := []byte("before\n" + ProjXManagedBeginV1 + "\nmanaged\n" + ProjXManagedEnd + "\nafter")
	out, changed, err := RemoveProjXManagedBlock(input)
	if err != nil || !changed {
		t.Fatalf("remove: changed=%t err=%v", changed, err)
	}
	if string(out) != "before\n\nafter" {
		t.Fatalf("remove touched non-managed bytes: %q", out)
	}
}

func TestMigrateClaudeToAgentsImportPreservesUserContent(t *testing.T) {
	input := []byte("user before\n\n" + ManagedBlock(NewMem()) + "\n\nuser after\n")
	out, changed, err := MigrateClaudeToAgentsImport(input)
	if err != nil || !changed {
		t.Fatalf("migrate: changed=%t err=%v", changed, err)
	}
	if !bytes.Contains(out, []byte("user before")) || !bytes.Contains(out, []byte("user after")) {
		t.Fatalf("migration lost user content: %q", out)
	}
	if bytes.Contains(out, []byte(ClaudeBegin)) || !bytes.Contains(out, []byte("@AGENTS.md")) {
		t.Fatalf("migration did not replace legacy block: %q", out)
	}
}

func TestMigrateClaudeLeavesExistingUserAgentsImportUntouched(t *testing.T) {
	input := []byte("# Claude rules\n\n@AGENTS.md\n\nUser content.\n")
	out, changed, err := MigrateClaudeToAgentsImport(input)
	if err != nil || changed {
		t.Fatalf("migrate: changed=%t err=%v", changed, err)
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("existing import changed: %q", out)
	}
}
