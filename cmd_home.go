package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func runHomeCmd(absRoot string, args []string) {
	s := buildStatusSnapshot(absRoot, "")
	for _, arg := range args {
		if arg == "--json" {
			out, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(out))
			return
		}
	}

	if !s.Health.Store {
		fmt.Println("ProjX needs attention")
		fmt.Println("  knowledge store: unavailable")
		fmt.Println("  run: projx init --global")
		return
	}

	fmt.Println("ProjX ready")
	fmt.Printf("  scope: %s\n", homeScopeLabel(s))
	fmt.Printf("  knowledge: %d records, %d ADRs, %d gates\n", s.RecordCount, s.ADRCount, s.GateCount)
	fmt.Printf("  hooks: %s\n", homeHealthLabel(s.Health.Hooks, s.Health.HooksCurrent))
	if s.Project {
		fmt.Printf("  MCP: %s\n", homeHealthLabel(s.Health.MCP, s.Health.MCPCurrent))
	}
	if s.MapRefreshing {
		fmt.Println("  map: refreshing in the background; current verified map remains available")
	}
	if s.Project {
		fmt.Println("\nUseful commands:")
		fmt.Println("  projx store query <terms>   search scoped knowledge")
		fmt.Println("  projx impact <symbol>       inspect change impact")
		fmt.Println("  projx --dashboard           open the web dashboard")
		return
	}
	if s.PrimaryScope == "workspace" {
		fmt.Printf("\nProjX workspace active at %s\n", s.WorkspaceRoot)
		fmt.Println("  enter a member repository to add or use project-scoped knowledge")
		fmt.Println("  workspace knowledge remains available throughout this tree")
		return
	}
	fmt.Printf("\nNo ProjX project is active at %s\n", absRoot)
	fmt.Println("  run: projx --root . init")
}

func homeScopeLabel(s StatusSnapshot) string {
	parts := []string{"global"}
	if s.WorkspaceRoot != "" {
		parts = append(parts, filepath.Base(s.WorkspaceRoot))
	}
	if s.ProjectName != "" {
		parts = append(parts, s.ProjectName)
	}
	return strings.Join(parts, " > ")
}

func homeHealthLabel(present, current bool) string {
	switch {
	case current:
		return "healthy"
	case present:
		return "installed but stale"
	default:
		return "not installed"
	}
}
