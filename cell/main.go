// Command projxenginecell is the ProjX ENGINE as a Pulp cell — the control-plane
// BRAIN. It runs as WASM on the Pulp host: the declared-knowledge store and its
// API served over transport.http.inbound, persistence via storage.sqlite, files
// (CLAUDE.md + the history journal) via storage.fs.
//
// The CAGE (Landlock/AppContainer/netns) is NOT compiled into this cell — it is
// irreducibly native "hands". The cell reaches it as a Pulp CAPABILITY
// (spawn.confine): POST /api/agent/run assembles the contract and calls
// pulp.Confine.RunCaged, and the host performs the confined launch. This cell is
// pure logic; it never touches the OS directly. Build-on-Pulp law: brain = cell,
// hands = Pulp capabilities (no native executor in the path).
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o cell.wasm .
package main

import (
	"fmt"

	"github.com/BananaLabs-OSS/Fiber/pulp"
	pulpgin "github.com/BananaLabs-OSS/Fiber/pulp/gin"
	store "github.com/SirNiklas9/projx-store"
)

func main() {}

func init() {
	pulp.OnInit(func(_ []byte) error {
		// Auto-seed the floor contract into a fresh project store so the engine
		// boots with knowledge — the SAME floor definition the native engine uses
		// (store.SeedFloor), no duplication.
		if s, err := openStore(); err == nil {
			if len(s.List(store.InScope(store.ScopeProject))) == 0 {
				store.SeedFloor(s)
				syncClaudeMD(s)
			}
		}
		r := pulpgin.New()
		r.GET("/api/store", handleStoreList)
		r.POST("/api/store", handleStorePut)
		r.DELETE("/api/store", handleStoreDelete)
		r.GET("/api/store/history", handleStoreHistory)
		r.POST("/api/store/undo", handleStoreUndo)
		r.GET("/api/route", handleRoute)
		r.GET("/api/gate", handleGate)
		r.GET("/api/gate/check", handleGateCheck)
		r.GET("/api/context/floor", handleContextFloor)
		r.GET("/api/context/slice", handleContextSlice)
		r.GET("/api/context/delta", handleContextDelta)
		r.POST("/api/context/reset", handleContextReset)
		r.POST("/api/context/suggest", handleContextSuggest)
		r.GET("/api/agent/spec", handleAgentSpec)
		r.POST("/api/agent/run", handleAgentRun)
		r.GET("/api/agent/run/status", handleAgentStatus)
		if err := r.RegisterRoutes(); err != nil {
			fmt.Println("[projx-engine cell] route registration:", err)
		}
		pulp.OnStep(func(ev pulp.StepEvent) error { return r.Dispatch(ev) })
		fmt.Println("[projx-engine cell] control plane ready (store over transport.http)")
		return nil
	})
}

// openStore opens the project store via the host's storage.sqlite capability.
// (The two-file Workspace + repo-path landing is a later brick; brick 1 proves
// the engine runs as a cell and serves the store.)
func openStore() (*store.SQLite, error) { return store.Open("store.db") }
