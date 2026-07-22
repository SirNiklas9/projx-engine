package main

// cmd_mcp.go — `projx-engine mcp`: an agent-AGNOSTIC MCP (Model Context Protocol)
// stdio server. Exposes the ProjX store as tools — store_query, route, gate_check,
// store_commit — over the open standard, so ANY MCP client (Claude Code, Cursor,
// Codex, Cline, Windsurf) can PULL project knowledge, not just Claude Code.
//
// This is the PULL surface only. PUSH (per-turn context injection) and ENFORCE (gate
// deny) stay per-harness hooks — MCP cannot inject-every-turn or block a tool call, so
// it can't replace the connector; it complements it. Build-on-Pulp: the stdio transport
// is thin "hands"; the brain is the shared projx-store (the SAME logic serve + the cell
// run). Floats: each tool takes an optional "root" (else --root / the launch dir).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	store "github.com/SirNiklas9/projx-store"
)

const mcpProtocolVersion = "2024-11-05"

type mcpReq struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type mcpResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpErr         `json:"error,omitempty"`
}

type mcpErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func runMCPCmd(absRoot string, _ []string) {
	// MCP is harness-neutral and long-lived, making it the reliable owner for
	// the loopback dashboard (especially under Windows job containment).
	_ = startStatusServerInProcess(absRoot)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 16<<20) // allow large messages
	out := bufio.NewWriter(os.Stdout)

	send := func(r mcpResp) {
		r.JSONRPC = "2.0"
		b, _ := json.Marshal(r)
		out.Write(b)
		out.WriteByte('\n')
		out.Flush()
	}

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req mcpReq
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		isNotification := len(req.ID) == 0
		switch req.Method {
		case "initialize":
			send(mcpResp{ID: req.ID, Result: map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "projx-engine", "version": "0.1.0"},
			}})
		case "notifications/initialized", "notifications/cancelled":
			// notifications carry no id and get no response
		case "ping":
			if !isNotification {
				send(mcpResp{ID: req.ID, Result: map[string]any{}})
			}
		case "tools/list":
			send(mcpResp{ID: req.ID, Result: map[string]any{"tools": mcpTools()}})
		case "tools/call":
			send(mcpToolCall(req, absRoot))
		default:
			if !isNotification {
				send(mcpResp{ID: req.ID, Error: &mcpErr{Code: -32601, Message: "method not found: " + req.Method}})
			}
		}
	}
}

func mcpStr(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func mcpTools() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	return []map[string]any{
		{
			"name":        "status_snapshot",
			"description": "Return a presentation-neutral ProjX status snapshot: active scope, modes, health, gates, ADR freshness, and running agents.",
			"inputSchema": obj(map[string]any{
				"root":    mcpStr("optional repo root"),
				"session": mcpStr("optional harness session id for floating scope"),
			}),
		},
		{
			"name":        "store_query",
			"description": "Search this project's ProjX knowledge store — conventions, decisions, docs, and the code-map of symbols with file:line anchors. Returns matching records so you can jump to a symbol or read a declared rule instead of grepping.",
			"inputSchema": obj(map[string]any{
				"query": mcpStr("what to find (a concept, symbol name, or phrase)"),
				"root":  mcpStr("optional: absolute path of the repo to query (defaults to the launch dir)"),
			}, "query"),
		},
		{
			"name":        "route",
			"description": "Ask ProjX which model tier a task deserves (cheap-fast / default / deep-reasoning / elevate) plus the launch command.",
			"inputSchema": obj(map[string]any{
				"task": mcpStr("the task description"),
				"root": mcpStr("optional repo root"),
			}, "task"),
		},
		{
			"name":        "gate_check",
			"description": "Check whether a file path is off-limits by this project's ProjX gate rules (secrets, keys, dotenv, ssh). Returns whether it is denied and which rule.",
			"inputSchema": obj(map[string]any{
				"path": mcpStr("the file path to check"),
				"root": mcpStr("optional repo root"),
			}, "path"),
		},
		{
			"name":        "impact",
			"description": "Blast radius: who calls this symbol, transitively (up to a few hops). Deterministic, name-matched from the code-map — approximate (no type resolution), but fast and self-contained. Use before changing a widely-used symbol.",
			"inputSchema": obj(map[string]any{
				"symbol": mcpStr("the symbol/function name to check (bare name, e.g. \"Add\")"),
				"depth":  mcpStr("optional: max hops to walk transitively (default 3)"),
				"root":   mcpStr("optional repo root"),
			}, "symbol"),
		},
		{
			"name":        "store_commit",
			"description": "Stage a durable AI discovery in this project's ProjX store as a non-authoritative candidate (NOT a markdown file). Verification or human review promotes it.",
			"inputSchema": obj(map[string]any{
				"kind":        mcpStr("doc | convention | adr"),
				"key":         mcpStr("a short key/title"),
				"body":        mcpStr("the fact to remember"),
				"scope":       mcpStr("optional: project (default) | workspace | global"),
				"supersedes":  mcpStr("optional record id this candidate may replace"),
				"claim_class": mcpStr("optional stable|volatile or domain-specific class"),
				"evidence":    mcpStr("optional live-source, test, or file reference"),
				"root":        mcpStr("optional repo root"),
			}, "kind", "key", "body"),
		},
	}
}

func mcpToolCall(req mcpReq, defaultRoot string) mcpResp {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &p)
	arg := func(k string) string {
		if s, ok := p.Arguments[k].(string); ok {
			return strings.TrimSpace(s)
		}
		return ""
	}
	root := defaultRoot
	if r := arg("root"); r != "" {
		if abs, err := filepath.Abs(r); err == nil {
			root = abs
		}
	}
	text := func(s string, isErr bool) mcpResp {
		return mcpResp{ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": s}}, "isError": isErr,
		}}
	}

	switch p.Name {
	case "status_snapshot":
		snapshot := buildStatusSnapshot(root, arg("session"))
		b, err := json.MarshalIndent(snapshot, "", "  ")
		if err != nil {
			return text("snapshot failed: "+err.Error(), true)
		}
		return mcpResp{ID: req.ID, Result: map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(b)}},
			"structuredContent": snapshot,
			"isError":           false,
		}}
	case "store_query":
		st := openStore(root)
		defer st.Close()
		recs := mcpQuery(st, arg("query"))
		if len(recs) == 0 {
			return text("no matching records for: "+arg("query"), false)
		}
		var b strings.Builder
		for i, r := range recs {
			if i >= 30 {
				fmt.Fprintf(&b, "… (%d more)\n", len(recs)-30)
				break
			}
			fmt.Fprintf(&b, "- [%s] %s — %s\n", r.ID, r.Key, mcpTrunc(r.Body, 240))
		}
		return text(b.String(), false)
	case "route":
		st := openStore(root)
		defer st.Close()
		d := store.RouteDecide(st, arg("task"), nil)
		return text(fmt.Sprintf("tier: %s\ncmd: %s\nreason: %s", d.Class, d.Cmd, d.Reason), false)
	case "impact":
		st := openStore(root)
		defer st.Close()
		depth := 0
		if d := arg("depth"); d != "" {
			fmt.Sscanf(d, "%d", &depth)
		}
		hits, truncated := computeImpact(st, arg("symbol"), depth)
		if len(hits) == 0 {
			return text("no callers found for "+arg("symbol")+" in the indexed code-map", false)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d symbol(s) reach %s:\n", len(hits), arg("symbol"))
		for _, h := range hits {
			fmt.Fprintf(&b, "  [depth %d] %s  %s\n", h.Depth, h.Name, h.Anchor)
		}
		if truncated {
			b.WriteString("(truncated — very wide blast radius)\n")
		}
		return text(b.String(), false)
	case "gate_check":
		st := openStore(root)
		defer st.Close()
		pat, denied := store.GateDenied(st, arg("path"))
		if denied {
			return text(fmt.Sprintf("DENIED — %q is off-limits by gate rule %q", arg("path"), pat), false)
		}
		return text(fmt.Sprintf("allowed — %q is not off-limits", arg("path")), false)
	case "store_commit":
		st := openStore(root)
		defer st.Close()
		kind, ok := mcpAgentKind(arg("kind"))
		if !ok {
			return text("kind must be one of: doc, convention, adr", true)
		}
		rec := store.Record{Kind: kind, Scope: mcpScope(arg("scope")), Key: arg("key"), Body: arg("body"),
			Status: store.StatusCandidate, Provenance: store.ProvenanceAgent, Supersedes: arg("supersedes"),
			ClaimClass: arg("claim_class"), Evidence: arg("evidence"), Model: os.Getenv("PROJX_MODEL")}
		rec.ID = kind.String() + "/" + slug(rec.Key)
		if before, exists := st.Get(rec.ID); exists && before.Authoritative() {
			return text("commit refused: agent cannot overwrite authoritative "+rec.ID+"; use a distinct key and set supersedes", true)
		}
		if err := st.Put(rec); err != nil {
			return text("commit failed: "+err.Error(), true)
		}
		syncProjectClaudeMD(root, st)
		return text("committed "+rec.ID, false)
	default:
		return mcpResp{ID: req.ID, Error: &mcpErr{Code: -32602, Message: "unknown tool: " + p.Name}}
	}
}

// mcpQuery is a lean AND-term match over the store's records (keys/bodies/ids),
// including the code-map anchors — self-contained, no ranking model.
func mcpQuery(st store.Store, query string) []store.Record {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	var out []store.Record
	for _, r := range st.List(store.Filter{}) {
		hay := strings.ToLower(r.ID + " " + r.Key + " " + r.Body)
		ok := true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, r)
		}
	}
	return out
}

// mcpAgentKind restricts store_commit to agent-writable kinds (never gate/route/etc).
func mcpAgentKind(s string) (store.Kind, bool) {
	switch strings.ToLower(s) {
	case "doc":
		return store.KDoc, true
	case "convention":
		return store.KConvention, true
	case "adr":
		return store.KADR, true
	}
	return 0, false
}

func mcpScope(s string) store.Scope {
	switch strings.ToLower(s) {
	case "global":
		return store.ScopeGlobal
	case "workspace":
		return store.ScopeWorkspace
	default:
		return store.ScopeProject
	}
}

func mcpTrunc(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
