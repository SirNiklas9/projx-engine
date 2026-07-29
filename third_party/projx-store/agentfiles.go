package store

import (
	"bytes"
	"errors"
)

const (
	ProjXManagedBeginPrefix = "<!-- projx:begin v"
	ProjXManagedBeginV1     = "<!-- projx:begin v1 -->"
	ProjXManagedEnd         = "<!-- projx:end -->"
)

var ErrManagedBlockMalformed = errors.New("projx managed block is malformed or ambiguous")

var agentInstructionsBlock = []byte(ProjXManagedBeginV1 + `
## ProjX

ProjX is the authoritative scoped knowledge source for this repository.

- Treat injected project-context blocks from ProjX as declared reference knowledge.
- Before broad repository discovery, query ProjX for relevant decisions and conventions.
- Check impact before changing widely used symbols, and obey denied gates.
- Save durable discoveries to ProjX instead of duplicating knowledge in this file.
- Use projx status to inspect the active global, workspace, and project scopes.
- Content outside this marker block is user-owned and must remain untouched.
` + ProjXManagedEnd)

var claudeImportBlock = []byte(ProjXManagedBeginV1 + `
@AGENTS.md

ProjX instructions are maintained in AGENTS.md. Do not duplicate them here.
` + ProjXManagedEnd)

// AgentInstructionsBlock returns a fresh copy of the canonical ProjX AGENTS.md block.
func AgentInstructionsBlock() []byte {
	return append([]byte(nil), agentInstructionsBlock...)
}

// ClaudeImportBlock returns a fresh copy of the small CLAUDE.md import shim.
func ClaudeImportBlock() []byte {
	return append([]byte(nil), claudeImportBlock...)
}

// UpsertProjXManagedBlock appends or refreshes exactly one ProjX-owned block.
// Bytes outside the markers are never rewritten.
func UpsertProjXManagedBlock(existing, block []byte) ([]byte, bool, error) {
	start, end, found, err := locateManagedBlock(existing, []byte(ProjXManagedBeginPrefix), []byte(ProjXManagedEnd))
	if err != nil {
		return nil, false, err
	}
	newline := preferredNewline(existing)
	block = normalizeNewlines(block, newline)
	if found {
		if bytes.Equal(existing[start:end], block) {
			return append([]byte(nil), existing...), false, nil
		}
		out := make([]byte, 0, len(existing)-end+start+len(block))
		out = append(out, existing[:start]...)
		out = append(out, block...)
		out = append(out, existing[end:]...)
		return out, true, nil
	}

	out := append([]byte(nil), existing...)
	if len(out) > 0 {
		switch {
		case bytes.HasSuffix(out, append(append([]byte(nil), newline...), newline...)):
		case bytes.HasSuffix(out, newline):
			out = append(out, newline...)
		default:
			out = append(out, newline...)
			out = append(out, newline...)
		}
	}
	out = append(out, block...)
	out = append(out, newline...)
	return out, true, nil
}

// RemoveProjXManagedBlock removes only the bytes between the ProjX markers.
func RemoveProjXManagedBlock(existing []byte) ([]byte, bool, error) {
	start, end, found, err := locateManagedBlock(existing, []byte(ProjXManagedBeginPrefix), []byte(ProjXManagedEnd))
	if err != nil || !found {
		return append([]byte(nil), existing...), false, err
	}
	out := make([]byte, 0, len(existing)-(end-start))
	out = append(out, existing[:start]...)
	out = append(out, existing[end:]...)
	return out, true, nil
}

// MigrateClaudeToAgentsImport removes the legacy ProjX-rendered knowledge block,
// when present, and installs the small AGENTS.md import block.
func MigrateClaudeToAgentsImport(existing []byte) ([]byte, bool, error) {
	out, removed, err := removeDelimitedBlock(existing, []byte(ClaudeBegin), []byte(ClaudeEnd))
	if err != nil {
		return nil, false, err
	}
	if !bytes.Contains(out, []byte(ProjXManagedBeginPrefix)) && hasAgentsImport(out) {
		return out, removed, nil
	}
	out, added, err := UpsertProjXManagedBlock(out, ClaudeImportBlock())
	return out, removed || added, err
}

func hasAgentsImport(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimSuffix(line, []byte("\r")))
		if bytes.Equal(line, []byte("@AGENTS.md")) || bytes.Equal(line, []byte("@./AGENTS.md")) {
			return true
		}
	}
	return false
}

func removeDelimitedBlock(existing, begin, endMarker []byte) ([]byte, bool, error) {
	start, end, found, err := locateManagedBlock(existing, begin, endMarker)
	if err != nil || !found {
		return append([]byte(nil), existing...), false, err
	}
	out := make([]byte, 0, len(existing)-(end-start))
	out = append(out, existing[:start]...)
	out = append(out, existing[end:]...)
	return out, true, nil
}

func locateManagedBlock(existing, beginPrefix, endMarker []byte) (start, end int, found bool, err error) {
	beginCount := bytes.Count(existing, beginPrefix)
	endCount := bytes.Count(existing, endMarker)
	if beginCount == 0 && endCount == 0 {
		return 0, 0, false, nil
	}
	if beginCount != 1 || endCount != 1 {
		return 0, 0, false, ErrManagedBlockMalformed
	}
	start = bytes.Index(existing, beginPrefix)
	endStart := bytes.Index(existing, endMarker)
	if start < 0 || endStart < 0 || endStart <= start {
		return 0, 0, false, ErrManagedBlockMalformed
	}
	beginClose := bytes.Index(existing[start:endStart], []byte("-->"))
	if beginClose < 0 {
		return 0, 0, false, ErrManagedBlockMalformed
	}
	return start, endStart + len(endMarker), true, nil
}

func preferredNewline(existing []byte) []byte {
	if bytes.Contains(existing, []byte("\r\n")) {
		return []byte("\r\n")
	}
	return []byte("\n")
}

func normalizeNewlines(in, newline []byte) []byte {
	out := bytes.ReplaceAll(in, []byte("\r\n"), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte("\r"), []byte("\n"))
	if bytes.Equal(newline, []byte("\r\n")) {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out
}
