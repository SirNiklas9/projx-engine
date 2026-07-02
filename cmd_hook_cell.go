package main

// cmd_hook_cell.go — drive a DEPLOYED WASM cell over HTTP from the hook command.
//
// `projx-engine hook` normally computes the lifecycle locally against the project's
// .projx store. When PROJX_CELL_URL is set it instead PROXIES each event to the cell's
// HTTP API (floor / delta / gate-check / reset / suggest) and emits the SAME stdout /
// stderr / exit-code contract. So one connector (settings.json → `projx-engine hook`)
// drives either the local store or the deployed cell — switch with one env var. The
// cell owns the store + checkpoints; the engine here is a thin, stateless HTTP shim.
//
// Best-effort: any HTTP error degrades to "inject nothing / allow", never a blocked
// session — same posture as the local path.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	store "github.com/SirNiklas9/projx-store"
)

var cellHTTP = &http.Client{Timeout: 15 * time.Second}

// handleHookViaCell maps a hook event to the cell's HTTP endpoints. Mirrors handleHook's
// contract (stdout injected, exit 2 blocks).
func handleHookViaCell(base string, input []byte) (stdout, stderr string, code int) {
	var ev hookEvent
	_ = json.Unmarshal(input, &ev)
	sid := ev.SessionID
	if sid == "" {
		sid = "default"
	}
	base = strings.TrimRight(base, "/")

	// A spawned worker (PROJX_ROLE=worker) gets the executor directive prepended so it
	// does the task directly instead of obeying the trunk's "dispatch, don't mutate" law.
	// The directive is the SAME editable store record the native path reads — fetched
	// from the cell's /api/context/worker-directive; degrades to the built-in default
	// text if the cell is briefly unreachable (never leaves a worker with no reframing).
	frame := func(ctx string) string {
		if ctx != "" && os.Getenv("PROJX_ROLE") == "worker" {
			wd := store.DefaultWorkerDirective
			if m, ok := cellReq("GET", base+"/api/context/worker-directive"); ok {
				if t, _ := m["text"].(string); t != "" {
					wd = t
				}
			}
			return wd + ctx
		}
		return ctx
	}

	switch ev.Event {
	case "SessionStart":
		if m, ok := cellReq("GET", base+"/api/context/floor?session="+url.QueryEscape(sid)); ok {
			if floor, _ := m["floor"].(string); floor != "" {
				return wrapProjectContext(frame(floor)), "", 0
			}
		}

	case "UserPromptSubmit":
		u := base + "/api/context/delta?session=" + url.QueryEscape(sid) + "&task=" + url.QueryEscape(ev.Prompt)
		if m, ok := cellReq("GET", u); ok {
			if ctx, _ := m["context"].(string); ctx != "" {
				return wrapProjectContext(frame(ctx)), "", 0
			}
		}

	case "PreToolUse":
		// Trunk-dispatch gate (same as the native path): deny file-mutating tools in the
		// TRUNK when dispatcher-mode is on; a projx-spawned worker (PROJX_ROLE=worker) is
		// exempt. Role is read locally; the cell owns the on/off setting.
		if store.IsMutatingTool(ev.ToolName) && os.Getenv("PROJX_ROLE") != "worker" {
			if m, ok := cellReq("GET", base+"/api/gate/dispatcher"); ok {
				if on, _ := m["on"].(bool); on {
					return "", "ProjX dispatcher-mode: the trunk dispatches, it does not edit. Route this to a tier-agent — `projx-engine dispatch --run \"<task>\"` — or turn it off with `projx-engine store commit --kind gate-rule --key setting/dispatcher-mode --body off`.", 2
				}
			}
		}
		p := ev.ToolInput.FilePath
		if p == "" {
			return "", "", 0
		}
		if m, ok := cellReq("GET", base+"/api/gate/check?path="+url.QueryEscape(p)); ok {
			if denied, _ := m["denied"].(bool); denied {
				pat, _ := m["pattern"].(string)
				return "", fmt.Sprintf("ProjX gate: %q is off-limits by gate rule %q.", p, pat), 2
			}
		}

	case "PreCompact":
		cellReq("POST", base+"/api/context/reset?session="+url.QueryEscape(sid))

	case "Stop":
		if m, ok := cellReq("POST", base+"/api/context/suggest?session="+url.QueryEscape(sid)); ok {
			if block, _ := m["block"].(bool); block {
				msg, _ := m["suggest"].(string)
				return "", msg, 2
			}
		}
	}
	return "", "", 0
}

// cellReq performs an HTTP request to the cell and decodes a JSON object. ok=false on any
// transport / non-200 / decode failure (caller degrades gracefully).
func cellReq(method, u string) (map[string]any, bool) {
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, false
	}
	resp, err := cellHTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	return m, true
}
