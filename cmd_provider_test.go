package main

import (
	"path/filepath"
	"testing"

	store "github.com/SirNiklas9/projx-store"
)

func TestProviderDisableWritesGlobalHardGate(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PROJX_YOURS_DIR", filepath.Join(t.TempDir(), "global"))
	runProviderCmd(root, []string{"disable", "claude"})

	st := openStore(root)
	defer st.Close()
	rec, ok := st.physicalFor(store.ScopeGlobal).Get(providerEnabledSetting + "claude")
	if !ok || rec.Scope != store.ScopeGlobal || rec.Body != "false" {
		t.Fatalf("global provider gate = %+v, found=%t", rec, ok)
	}
}
