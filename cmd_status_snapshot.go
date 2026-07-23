package main

// Presentation-neutral status model shared by Claude's ANSI statusline, the
// CLI/TUI, and MCP clients. Unexported fields carry renderer-only state and are
// intentionally excluded from JSON.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	store "github.com/SirNiklas9/projx-store"
)

type StatusModes struct {
	Dispatcher        bool `json:"dispatcher"`
	Cage              bool `json:"cage"`
	OverrideAuthority bool `json:"override_authority"`
}

type StatusHealth struct {
	Store          bool   `json:"store"`
	MCP            bool   `json:"mcp"`
	MCPCurrent     bool   `json:"mcp_current"`
	Hooks          bool   `json:"hooks"`
	HooksCurrent   bool   `json:"hooks_current"`
	Binary         bool   `json:"binary"`
	BinaryStale    bool   `json:"binary_stale"`
	BinaryPath     string `json:"binary_path,omitempty"`
	BinaryRevision string `json:"binary_revision,omitempty"`
	SourceRevision string `json:"source_revision,omitempty"`
	SourceDirty    bool   `json:"source_dirty,omitempty"`
}

type StatusAgent struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Root      string `json:"root"`
	State     string `json:"state"`
	Operation string `json:"operation"`
	Role      string `json:"role,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Verify    string `json:"verify,omitempty"`
	Step      int    `json:"step"`
	Total     int    `json:"total"`
}

type StatusSnapshot struct {
	GeneratedAt     time.Time     `json:"generated_at"`
	ActiveRoot      string        `json:"active_root,omitempty"`
	ProjectName     string        `json:"project_name,omitempty"`
	PrimaryScope    string        `json:"primary_scope"`
	ActiveScopes    []string      `json:"active_scopes"`
	WorkspaceRoot   string        `json:"workspace_root,omitempty"`
	Project         bool          `json:"project"`
	RecordCount     int           `json:"record_count"`
	GlobalRecords   int           `json:"global_records"`
	WorkspaceRecords int          `json:"workspace_records"`
	ProjectRecords  int           `json:"project_records"`
	CandidateCount  int           `json:"candidate_count"`
	ReviewDueCount  int           `json:"review_due_count"`
	SupersededCount int           `json:"superseded_count"`
	RejectedCount   int           `json:"rejected_count"`
	GateCount       int           `json:"gate_count"`
	ADRCount        int           `json:"adr_count"`
	NewestADR       int64         `json:"newest_adr,omitempty"`
	ADRFresh        bool          `json:"adr_fresh"`
	ADRAgeDays      int           `json:"adr_age_days,omitempty"`
	Verification    string        `json:"verification"`
	Modes           StatusModes   `json:"modes"`
	Health          StatusHealth  `json:"health"`
	LastAction      string        `json:"last_action,omitempty"`
	ContextBytes    int           `json:"context_bytes,omitempty"`
	Agents          []StatusAgent `json:"agents"`

	home      string
	crumb     statusCrumb
	haveCrumb bool
	storeErr  string
}

func buildStatusSnapshot(cwd, sid string) StatusSnapshot {
	s := StatusSnapshot{
		GeneratedAt:  time.Now(),
		PrimaryScope: "global",
		ActiveScopes: []string{"global"},
		Agents:       []StatusAgent{},
		Verification: "not-run",
	}
	if p, err := os.Executable(); err == nil {
		s.Health.BinaryPath = filepath.Clean(p)
		_, err = os.Stat(p)
		s.Health.Binary = err == nil
	}
	if rev, _, _ := vcsInfo(); rev != "" {
		s.Health.BinaryRevision = rev
	}
	if s.Health.BinaryPath == "" {
		if p, err := exec.LookPath("projx-engine"); err == nil {
			s.Health.BinaryPath = filepath.Clean(p)
		}
	}
	if s.Health.BinaryPath != "" && !s.Health.Binary {
		if _, err := os.Stat(s.Health.BinaryPath); err == nil {
			s.Health.Binary = true
		}
	}
	if home, err := claudeHomeDir(); err == nil {
		hookCommands := configuredProjxHookCommands(home)
		s.Health.Hooks = len(hookCommands) > 0
		s.Health.HooksCurrent = commandsUseBinary(hookCommands, s.Health.BinaryPath)
	}
	s.home = nearestProjxDir(cwd)
	if s.home == "" {
		if wp := workspaceStorePath(cwd); wp != "" {
			s.ActiveRoot = filepath.Dir(filepath.Dir(wp))
			s.PrimaryScope = "workspace"
			s.ActiveScopes = []string{"global", "workspace"}
			s.WorkspaceRoot = s.ActiveRoot
		}
	}
	if sid != "" && s.home != "" {
		s.crumb, s.haveCrumb = readStatusCrumb(s.home, sid)
	}
	if s.ActiveRoot == "" {
		s.ActiveRoot = s.home
	}
	if s.haveCrumb && s.crumb.R != "" && isProjxDir(s.crumb.R) {
		s.ActiveRoot = s.crumb.R
	}
	s.Project = s.ActiveRoot != "" && isProjxDir(s.ActiveRoot)
	if s.Project {
		s.PrimaryScope = "project"
		if wp := workspaceStorePath(s.ActiveRoot); wp != "" {
			s.WorkspaceRoot = filepath.Dir(filepath.Dir(wp))
			s.ActiveScopes = []string{"global", "workspace", "project"}
		} else {
			s.ActiveScopes = []string{"global", "project"}
		}
	}
	storeRoot := s.ActiveRoot
	if storeRoot == "" {
		storeRoot = cwd
	}
	if s.Project {
		s.ProjectName = filepath.Base(s.ActiveRoot)
	}
	s.LastAction, s.ContextBytes = s.crumb.A, s.crumb.N
	st, err := openStoreExistingSafe(storeRoot)
	if err != nil {
		s.storeErr = err.Error()
		return s
	}
	defer st.Close()
	s.Health.Store = true
	projectFilter := store.InScope(store.ScopeProject)
	projectFilter.IncludeNonAuthoritative = true
	workspaceFilter := store.InScope(store.ScopeWorkspace)
	workspaceFilter.IncludeNonAuthoritative = true
	globalFilter := store.InScope(store.ScopeGlobal)
	globalFilter.IncludeNonAuthoritative = true
	nowMillis := s.GeneratedAt.UnixMilli()
	s.ADRFresh = true
	all := append([]store.Record{}, st.List(globalFilter)...)
	all = append(all, st.List(workspaceFilter)...)
	all = append(all, st.List(projectFilter)...)
	for _, r := range all {
		switch r.LifecycleStatus() {
		case store.StatusCandidate:
			s.CandidateCount++
			continue
		case store.StatusSuperseded:
			s.SupersededCount++
			continue
		case store.StatusRejected:
			s.RejectedCount++
			continue
		}
		if r.ReviewDueAt(nowMillis) {
			s.ReviewDueCount++
		}
		if r.Kind != store.KDeclaredStructure {
			s.RecordCount++
			switch r.Scope {
			case store.ScopeGlobal:
				s.GlobalRecords++
			case store.ScopeWorkspace:
				s.WorkspaceRecords++
			case store.ScopeProject:
				s.ProjectRecords++
			}
		}
		if r.Kind == store.KGateRule {
			s.GateCount++
		}
		if r.Kind == store.KADR {
			s.ADRCount++
			if r.ReviewDueAt(nowMillis) || (r.ReviewAfter == 0 && r.UpdatedAt > 0 && s.GeneratedAt.Sub(time.UnixMilli(r.UpdatedAt)) > 90*24*time.Hour) {
				s.ADRFresh = false
			}
			if r.UpdatedAt > s.NewestADR {
				s.NewestADR = r.UpdatedAt
			}
		}
	}
	s.Modes = StatusModes{store.DispatcherModeOn(st), store.CageModeOn(st), store.OverrideAuthorityOn(st)}
	if s.NewestADR > 0 {
		s.ADRAgeDays = int(time.Since(time.UnixMilli(s.NewestADR)).Hours() / 24)
		if s.ADRAgeDays < 0 {
			s.ADRAgeDays = 0
		}
	}
	mcpCommands := configuredProjxMCPCommands(s.ActiveRoot)
	s.Health.MCP = len(mcpCommands) > 0
	s.Health.MCPCurrent = commandsUseBinary(mcpCommands, s.Health.BinaryPath)
	s.Health.SourceRevision, s.Health.SourceDirty = engineSourceIdentity(s.ActiveRoot)
	s.Health.BinaryStale = binaryIdentityStale(s.Health.BinaryRevision, s.Health.SourceRevision, s.Health.SourceDirty)
	for _, a := range gatherRunningAgents(s.ActiveRoot) {
		sa := StatusAgent{ID: a.m.ID, Project: a.project, Root: a.root, State: a.m.State, Operation: curOpLabel(a), Role: agentRole(a), Branch: branchOf(a.root), Verify: a.m.Verify, Step: a.curIndex, Total: a.total}
		s.Agents = append(s.Agents, sa)
	}
	return s
}

func engineSourceIdentity(root string) (revision string, dirty bool) {
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.Contains(string(mod), "module github.com/SirNiklas9/projx-engine") {
		return "", false
	}
	headCmd := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	headCmd.SysProcAttr = quietSysProcAttr()
	head, err := headCmd.Output()
	if err != nil {
		return "", false
	}
	statusCmd := exec.Command("git", "-C", root, "status", "--porcelain", "--untracked-files=all")
	statusCmd.SysProcAttr = quietSysProcAttr()
	status, err := statusCmd.Output()
	if err != nil {
		return strings.TrimSpace(string(head)), false
	}
	return strings.TrimSpace(string(head)), engineBuildInputsDirty(string(status))
}

func engineBuildInputsDirty(porcelain string) bool {
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 4 {
			continue
		}
		path := strings.Trim(strings.TrimSpace(line[2:]), `"`)
		if i := strings.LastIndex(path, " -> "); i >= 0 {
			path = strings.Trim(strings.TrimSpace(path[i+4:]), `"`)
		}
		path = filepath.ToSlash(path)
		if strings.HasSuffix(path, ".go") || path == "go.mod" || path == "go.sum" ||
			strings.HasPrefix(path, "skill/") || strings.HasPrefix(path, "codex-skill/") ||
			strings.HasPrefix(path, "claude-connector/.claude/") || strings.HasPrefix(path, "status-dashboard/") {
			return true
		}
	}
	return false
}

func binaryIdentityStale(binaryRevision, sourceRevision string, sourceDirty bool) bool {
	if binaryRevision == "" || sourceRevision == "" {
		return false
	}
	return sourceDirty || !strings.EqualFold(binaryRevision, sourceRevision)
}

func mcpConfigured(root string) bool {
	return len(configuredProjxMCPCommands(root)) > 0
}

func configuredProjxMCPCommands(root string) []string {
	var commands []string
	b, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	if err == nil {
		var v struct {
			MCPServers map[string]struct {
				Command string `json:"command"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal(b, &v) == nil {
			if server, ok := v.MCPServers["projx"]; ok {
				commands = append(commands, server.Command)
			}
		}
	}
	b, err = os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	if err != nil {
		return commands
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string `toml:"command"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(b), &cfg); err != nil {
		return commands
	}
	if server, ok := cfg.MCPServers["projx"]; ok {
		commands = append(commands, server.Command)
	}
	return commands
}

func configuredProjxHookCommands(home string) []string {
	var commands []string
	for _, path := range []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".codex", "hooks.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var root any
		if json.Unmarshal(data, &root) != nil {
			continue
		}
		walkJSONStrings(root, func(key, value string) {
			if key == "command" && isProjxHookCmd(value) {
				commands = append(commands, value)
			}
		})
	}
	return commands
}

func walkJSONStrings(value any, visit func(key, value string)) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if s, ok := child.(string); ok {
				visit(key, s)
				continue
			}
			walkJSONStrings(child, visit)
		}
	case []any:
		for _, child := range v {
			walkJSONStrings(child, visit)
		}
	}
}

func commandsUseBinary(commands []string, binaryPath string) bool {
	if len(commands) == 0 || binaryPath == "" {
		return false
	}
	want := strings.ToLower(filepath.ToSlash(filepath.Clean(binaryPath)))
	wants := []string{want}
	if runtime.GOOS == "windows" {
		dir := filepath.Dir(binaryPath)
		switch strings.ToLower(filepath.Base(binaryPath)) {
		case "projx-engine.exe":
			wants = append(wants, strings.ToLower(filepath.ToSlash(filepath.Join(dir, "projx-engine-headless.exe"))))
		case "projx-engine-headless.exe":
			wants = append(wants, strings.ToLower(filepath.ToSlash(filepath.Join(dir, "projx-engine.exe"))))
		}
	}
	for _, command := range commands {
		got := strings.ToLower(filepath.ToSlash(strings.Trim(strings.TrimSpace(command), `"`)))
		current := false
		for _, candidate := range wants {
			if strings.Contains(got, candidate) {
				current = true
				break
			}
		}
		if current {
			continue
		}
		fields := strings.Fields(got)
		if len(fields) > 0 && strings.Trim(fields[0], `"`) == "projx-engine" {
			resolved, err := exec.LookPath("projx-engine")
			if err == nil && strings.EqualFold(filepath.Clean(resolved), filepath.Clean(binaryPath)) {
				continue
			}
		}
		return false
	}
	return true
}

func renderStatusCompact(s StatusSnapshot) string {
	if !s.Project {
		return "projx global floor"
	}
	scope := strings.Join(s.ActiveScopes, " -> ")
	parts := []string{"projx", s.ProjectName, scope, fmt.Sprintf("%d rec", s.RecordCount), fmt.Sprintf("%d gates", s.GateCount), fmt.Sprintf("%d agents", len(s.Agents))}
	if s.CandidateCount > 0 {
		parts = append(parts, fmt.Sprintf("%d candidate", s.CandidateCount))
	}
	if s.ReviewDueCount > 0 {
		parts = append(parts, fmt.Sprintf("%d review-due", s.ReviewDueCount))
	}
	if s.Modes.Dispatcher {
		parts = append(parts, "dispatcher")
	}
	if s.Modes.Cage {
		parts = append(parts, "cage")
	}
	return strings.Join(parts, " · ")
}
