package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// validateWorkflowWrite keeps parallel write authority inside the repository.
// Absolute paths and parent traversal fail closed; slash-normalized relative globs
// are accepted so the same manifest works on every harness and OS.
func validateWorkflowWrite(p string) error {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if p == "" || filepath.IsAbs(p) || p == "." || p == "**" || p == "**/*" {
		return fmt.Errorf("must be a scoped repo-relative path or glob")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "" {
			return fmt.Errorf("must not escape the repository")
		}
	}
	return nil
}

func writeStem(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		p = p[:i]
	}
	return strings.TrimSuffix(p, "/")
}

// workflowWritesOverlap is deliberately conservative: concurrency is allowed only
// when ProjX can prove two declarations are disjoint. Ambiguous glob relationships
// serialize instead of relying on an agent's promise.
func workflowWritesOverlap(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			xs, ys := writeStem(x), writeStem(y)
			if xs == "" || ys == "" || xs == ys || strings.HasPrefix(xs, ys+"/") || strings.HasPrefix(ys, xs+"/") {
				return true
			}
		}
	}
	return false
}

// workflowBatches produces deterministic authored-order waves. Dependencies must
// have completed in an earlier wave; ready steps share a wave only when every
// declared write set is provably disjoint.
func workflowBatches(m *WorkflowManifest) ([][]int, error) {
	if !m.Parallel {
		out := make([][]int, len(m.Steps))
		for i := range m.Steps {
			out[i] = []int{i}
		}
		return out, nil
	}
	done := map[string]bool{}
	remaining := make([]bool, len(m.Steps))
	for i := range remaining {
		remaining[i] = true
	}
	var out [][]int
	for len(done) < len(m.Steps) {
		var batch []int
		for i, s := range m.Steps {
			if !remaining[i] {
				continue
			}
			ready := true
			for _, d := range s.Deps {
				if !done[d] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			compatible := true
			for _, j := range batch {
				if workflowWritesOverlap(s.Writes, m.Steps[j].Writes) {
					compatible = false
					break
				}
			}
			if compatible {
				batch = append(batch, i)
			}
		}
		if len(batch) == 0 {
			return nil, fmt.Errorf("workflow: dependency deadlock")
		}
		for _, i := range batch {
			remaining[i] = false
			done[m.Steps[i].ID] = true
		}
		out = append(out, batch)
	}
	return out, nil
}

// workflowChangedPathsAllowed is the batch-level fail-closed backstop used after
// parallel workers finish: every observed mutation must be covered by a declaration.
func workflowChangedPathsAllowed(paths []string, steps []WorkflowStep) error {
	for _, changed := range paths {
		changed = filepath.ToSlash(strings.TrimSpace(changed))
		allowed := false
		for _, s := range steps {
			for _, pat := range s.Writes {
				if ok, _ := filepath.Match(filepath.FromSlash(pat), filepath.FromSlash(changed)); ok || writeStem(pat) == changed || strings.HasPrefix(changed, writeStem(pat)+"/") {
					allowed = true
					break
				}
			}
			if allowed {
				break
			}
		}
		if !allowed {
			return fmt.Errorf("undeclared workflow mutation: %s", changed)
		}
	}
	return nil
}
