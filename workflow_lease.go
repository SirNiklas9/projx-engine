package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const parallelWorkerEnv = "PROJX_WORKER_PARALLEL"

// enforceParallelWorkerLease is the mechanical half of workflow write sets.
// Scheduling prevents declared overlap; this hook prevents a worker from writing
// outside what it declared. It fails closed because an absent/invalid lease must
// never silently turn a parallel worker into an unrestricted worker.
func enforceParallelWorkerLease(absRoot string, ev lifecycleEvent, targets []string) error {
	if os.Getenv(parallelWorkerEnv) != "1" || !isMutatingHookTool(ev.ToolName) {
		return nil
	}
	raw := strings.TrimSpace(os.Getenv("PROJX_WORKER_WRITES"))
	if raw == "" {
		return fmt.Errorf("ProjX parallel worker has no write lease")
	}
	patterns := filepath.SplitList(raw)
	for _, p := range patterns {
		if err := validateWorkflowWrite(p); err != nil {
			return fmt.Errorf("ProjX parallel worker has invalid write lease %q: %w", p, err)
		}
	}
	workdir := strings.TrimSpace(ev.ToolInput.Workdir)
	if workdir == "" {
		workdir = absRoot
	}
	pseudo := filepath.Clean(filepath.Join(workdir, "_"))
	var concrete []string
	for _, target := range targets {
		abs := target
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workdir, abs)
		}
		abs = filepath.Clean(abs)
		if abs == pseudo {
			continue
		} // scope breadcrumb, not a tool-supplied target
		concrete = append(concrete, abs)
	}
	if len(concrete) == 0 {
		return fmt.Errorf("ProjX parallel worker mutation has no parseable target; action blocked")
	}
	for _, abs := range concrete {
		rel, err := filepath.Rel(absRoot, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("ProjX parallel worker target %q escapes its repository write lease", abs)
		}
		rel = filepath.ToSlash(rel)
		allowed := false
		for _, pattern := range patterns {
			ok, matchErr := doublestar.Match(filepath.ToSlash(strings.TrimSpace(pattern)), rel)
			if matchErr != nil {
				return fmt.Errorf("ProjX invalid write-lease glob %q: %w", pattern, matchErr)
			}
			if ok {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("ProjX parallel worker target %q is outside its declared write lease", rel)
		}
	}
	return nil
}
